-- deploy-v190-multitenant.sql — one cloud, many businesses.
--
-- WHY. The cloud was built for ONE business. A fresh install anywhere
-- auto-joined the only store (sync_register: "exactly one active store →
-- join it"), so every new LedgerPOS customer queued under the founding
-- business's store; approving one merged two businesses' products, stock
-- and sales. Store lists, the pending-device pool and store creation were
-- cloud-wide, the store registry was world-readable, and any approved till
-- (a cashier's included) could overwrite its team's owner login.
--
-- WHAT. Additive and backwards-compatible for installed tills:
--   1. businesses: the tenant above stores. Every existing store becomes
--      business #1 (the founding business, is_platform = true).
--   2. store_payment_profiles: per-store M-Pesa routing (see buni-stkpush).
--      Founding-business stores keep the shared KCB till ('shared');
--      everyone else defaults to 'manual' (customers pay the shop's own
--      till number) until their own STK route is configured and verified.
--   3. sync_register_business: a fresh install registering a NEW business
--      gets its own business + store + approved owner device, atomically.
--   4. sync_register no longer auto-joins anyone: unknown devices register
--      pending (team null) and wait for an owner (assign / join link /
--      owner sign-in). Existing devices are untouched.
--   5. Business scoping: sync_list_stores, sync_create_store, and the
--      pending pool (sync_list_pending / sync_assign_device of an
--      unassigned till) only ever see the caller's own business. The
--      legacy pending pool is the founding business's alone.
--   6. portal_set_credentials: once an owner login exists, replacing it
--      needs a valid owner capability (a cashier's till can no longer take
--      over the owner portal). Owner usernames are unique cloud-wide (they
--      are the sign-in key).
--   7. The store registry (sync_stores) is no longer world-readable.
--
-- Rollback: each CREATE OR REPLACE below has its previous body in git
-- history (db/ and supabase migrations); the new tables/columns are inert
-- if the old functions are restored. Verified with the checks at the end.

-- ---------------------------------------------------------------- tables

create table if not exists businesses (
  id          bigserial primary key,
  name        text not null check (length(btrim(name)) between 2 and 80),
  is_platform boolean not null default false,
  created_by_device text,
  created_at  timestamptz not null default now()
);
alter table businesses enable row level security;
revoke all on businesses from anon, authenticated;

insert into businesses (id, name, is_platform)
  values (1, 'Founding business', true)
  on conflict (id) do nothing;
select setval(pg_get_serial_sequence('businesses', 'id'),
              greatest((select max(id) from businesses), 1));

alter table sync_stores add column if not exists business_id bigint references businesses(id);
update sync_stores set business_id = 1 where business_id is null;
alter table sync_stores alter column business_id set not null;
create index if not exists sync_stores_business_idx on sync_stores(business_id);

create table if not exists store_payment_profiles (
  team_code      text primary key references sync_stores(team_code) on delete cascade,
  -- shared = the platform's KCB till (founding business only)
  -- own    = STK straight to the shop's own short code (needs own_verified)
  -- manual = customers pay the shop's till/paybill; cashier confirms code
  stk_mode       text not null default 'manual' check (stk_mode in ('shared','own','manual')),
  till_number    text not null default '' check (till_number ~ '^[0-9]{0,10}$'),
  paybill_number text not null default '' check (paybill_number ~ '^[0-9]{0,10}$'),
  display_name   text not null default '' check (length(display_name) <= 40),
  org_shortcode  text not null default '' check (org_shortcode ~ '^[0-9]{0,10}$'),
  org_passkey    text not null default '',          -- secret: never returned
  own_verified   boolean not null default false,    -- set by the platform after a live test
  updated_at     timestamptz not null default now()
);
alter table store_payment_profiles enable row level security;
revoke all on store_payment_profiles from anon, authenticated;

insert into store_payment_profiles (team_code, stk_mode, display_name)
  select s.team_code, 'shared', left(s.name, 40) from sync_stores s
   where s.business_id = 1
  on conflict (team_code) do nothing;

-- 7. registry no longer public (tills fall back to the bootstrap row, and
-- the cloud — not the till — decides team membership in sync_register)
drop policy if exists stores_public_read on sync_stores;
revoke select on sync_stores from anon;

-- ---------------------------------------------------------------- helpers

-- business of an approved, non-revoked device (null if none)
create or replace function public._device_business(p_device_id text, p_secret_hash text)
returns bigint language sql stable security definer set search_path to 'public' as $$
  select s.business_id
    from sync_devices d join sync_stores s on s.team_code = d.team_code
   where d.device_id = p_device_id and d.secret_hash = p_secret_hash
     and d.approved and not d.revoked
   limit 1
$$;
revoke all on function public._device_business(text, text) from public, anon, authenticated;

create or replace function public._mint_team_code(p_seed text)
returns text language plpgsql volatile security definer set search_path to 'public' as $$
declare c text; tries int := 0;
begin
  loop
    c := 'T' || upper(substr(md5(random()::text || clock_timestamp()::text || coalesce(p_seed,'')), 1, 4))
         || '-' || upper(substr(md5(clock_timestamp()::text || random()::text), 1, 4));
    exit when not exists (select 1 from sync_stores where team_code = c);
    tries := tries + 1;
    if tries > 20 then raise exception 'could not mint a unique team code'; end if;
  end loop;
  return c;
end $$;
revoke all on function public._mint_team_code(text) from public, anon, authenticated;

create or replace function public._unique_slug(p_name text)
returns text language plpgsql volatile security definer set search_path to 'public' as $$
declare base text; s text; i int := 1;
begin
  base := btrim(lower(regexp_replace(coalesce(p_name, ''), '[^a-zA-Z0-9]+', '-', 'g')), '-');
  if length(base) < 2 then base := 'shop'; end if;
  base := left(base, 24);
  s := base;
  while exists (select 1 from sync_stores where slug = s) loop
    i := i + 1;
    s := left(base, 24) || '-' || i;
  end loop;
  return s;
end $$;
revoke all on function public._unique_slug(text) from public, anon, authenticated;

-- ---------------------------------------------------------------- 3. new business

create or replace function public.sync_register_business(
  p_device_id text, p_secret_hash text, p_device_name text, p_app_version text,
  p_business_name text, p_business_tz text default 'Africa/Nairobi')
returns json language plpgsql security definer set search_path to 'public', 'extensions' as $$
declare cur record; biz_id bigint; code text; slug text; tz text; nm text; next_id int;
begin
  if p_device_id is null or length(p_device_id) < 8
     or p_secret_hash is null or length(p_secret_hash) < 32 then
    return json_build_object('ok', false, 'error', 'invalid identity');
  end if;
  nm := btrim(coalesce(p_business_name, ''));
  if length(nm) < 2 or length(nm) > 60 then
    return json_build_object('ok', false, 'error', 'business name must be 2-60 characters');
  end if;
  -- abuse dams: per device and cloud-wide (counted on success below)
  if not public.signin_dam_allow('bizreg', p_device_id, 'device', 3, 1440)
     or not public.signin_dam_global_ok('bizreg', 200, 60) then
    return json_build_object('ok', false, 'error',
      'too many new businesses registered right now, try again later');
  end if;

  select * into cur from sync_devices where device_id = p_device_id;
  if cur.device_id is not null then
    if cur.revoked then
      return json_build_object('ok', false, 'error', 'this device was removed from LedgerPOS cloud');
    end if;
    if cur.secret_hash <> '' and cur.secret_hash <> p_secret_hash then
      return json_build_object('ok', false, 'error', 'identity mismatch');
    end if;
    if cur.team_code is not null and cur.approved then
      return json_build_object('ok', false, 'error',
        'this till already belongs to a business, so it cannot register another one');
    end if;
  end if;

  tz := coalesce(nullif(btrim(p_business_tz), ''), 'Africa/Nairobi');
  begin
    perform now() at time zone tz;
  exception when others then
    tz := 'Africa/Nairobi';
  end;

  insert into businesses (name, created_by_device) values (nm, p_device_id) returning id into biz_id;
  code := public._mint_team_code(nm);
  slug := public._unique_slug(nm);
  select coalesce(max(id), 0) + 1 into next_id from sync_stores;
  insert into sync_stores (id, slug, name, team_code, business_tz, auto_approve, active, business_id)
    values (next_id, slug, nm, code, tz, false, true, biz_id);
  insert into store_payment_profiles (team_code, stk_mode, display_name)
    values (code, 'manual', left(nm, 40));

  if cur.device_id is null then
    insert into sync_devices (device_id, team_code, device_name, app_version,
        secret_hash, approved, last_seen)
      values (p_device_id, code, coalesce(nullif(btrim(p_device_name), ''), 'owner till'),
              coalesce(p_app_version, ''), p_secret_hash, true, now());
  else
    update sync_devices set team_code = code, approved = true, secret_hash = p_secret_hash,
        device_name = coalesce(nullif(btrim(p_device_name), ''), device_name),
        app_version = coalesce(nullif(btrim(p_app_version), ''), app_version),
        last_seen = now()
      where device_id = p_device_id;
  end if;
  perform public.signin_dam_fail('bizreg', p_device_id, 'device', 1440);

  return json_build_object('ok', true, 'team_code', code, 'store_name', nm,
                           'business_id', biz_id, 'slug', slug);
end $$;
revoke all on function public.sync_register_business(text,text,text,text,text,text) from public;
grant execute on function public.sync_register_business(text,text,text,text,text,text) to anon, authenticated;

-- ---------------------------------------------------------------- 4. no auto-join

create or replace function public.sync_register(p_device_id text, p_secret_hash text,
  p_device_name text, p_app_version text, p_lan_addr text default ''::text)
returns json language plpgsql security definer set search_path to 'public' as $$
declare cur record; v_lan text;
begin
  if p_device_id is null or length(p_device_id) < 8
     or p_secret_hash is null or length(p_secret_hash) < 32 then
    return json_build_object('ok', false, 'error', 'invalid identity');
  end if;
  v_lan := coalesce(nullif(btrim(coalesce(p_lan_addr, '')), ''), null);
  select * into cur from sync_devices where device_id = p_device_id;
  if cur.device_id is not null then
    if cur.revoked then
      return json_build_object('ok', false, 'error', 'device revoked');
    end if;
    if cur.secret_hash <> '' and cur.secret_hash <> p_secret_hash then
      return json_build_object('ok', false, 'error', 'identity mismatch');
    end if;
    update sync_devices set device_name = p_device_name, app_version = p_app_version,
        secret_hash = p_secret_hash, lan_addr = coalesce(v_lan, cur.lan_addr),
        last_seen = now()
      where device_id = p_device_id;
    return json_build_object('ok', true, 'approved', cur.approved,
        'team_code', cur.team_code, 'pending', cur.team_code is null);
  end if;
  -- A device the cloud has never seen joins NOBODY by itself (v1.9): it is
  -- either a new business (sync_register_business), a worker (join link),
  -- an owner (owner_device_signin), or waits for an owner to assign it.
  insert into sync_devices (device_id, team_code, device_name, app_version,
      secret_hash, approved, lan_addr, last_seen)
    values (p_device_id, null, coalesce(p_device_name, 'till'),
            coalesce(p_app_version, ''), p_secret_hash, false,
            coalesce(v_lan, ''), now());
  return json_build_object('ok', true, 'approved', false, 'pending', true);
end $$;

-- 4-arg legacy overload: same behaviour
create or replace function public.sync_register(p_device_id text, p_secret_hash text,
  p_device_name text, p_app_version text)
returns json language sql security definer set search_path to 'public' as $$
  select public.sync_register(p_device_id, p_secret_hash, p_device_name, p_app_version, ''::text)
$$;

-- ---------------------------------------------------------------- 5. business scoping

create or replace function public.sync_list_stores(p_device_id text, p_secret_hash text)
returns json language plpgsql security definer set search_path to 'public' as $$
declare biz bigint; out_rows jsonb;
begin
  biz := public._device_business(p_device_id, p_secret_hash);
  if biz is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  select coalesce(jsonb_agg(jsonb_build_object(
      'slug', s.slug, 'name', s.name, 'team_code', s.team_code,
      'devices', (select count(*) from sync_devices d
                  where d.team_code = s.team_code and not d.revoked),
      'created_at', s.created_at) order by s.id), '[]'::jsonb) into out_rows
    from sync_stores s where s.active and s.business_id = biz;
  return json_build_object('ok', true, 'stores', out_rows);
end $$;

create or replace function public.sync_create_store(p_device_id text, p_secret_hash text,
  p_name text, p_slug text, p_business_tz text default 'Africa/Nairobi'::text,
  p_owner_capability text default null::text)
returns json language plpgsql security definer set search_path to 'public', 'extensions' as $$
declare biz bigint; plat boolean; new_code text; next_id int; tz text;
begin
  biz := public._device_business(p_device_id, p_secret_hash);
  if biz is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  p_name := btrim(coalesce(p_name, ''));
  p_slug := lower(btrim(coalesce(p_slug, '')));
  if length(p_name) < 2 or length(p_name) > 60 then
    return json_build_object('ok', false, 'error', 'store name must be 2-60 characters');
  end if;
  if p_slug <> '' and p_slug !~ '^[a-z0-9][a-z0-9-]{1,29}$' then
    return json_build_object('ok', false, 'error',
        'store slug must be 2-30 chars: letters, numbers, dashes');
  end if;
  if p_slug = '' or exists (select 1 from sync_stores where slug = p_slug) then
    p_slug := public._unique_slug(p_name);  -- slugs are cloud-wide ids
  end if;
  -- names only need to be unique inside ONE business
  if exists (select 1 from sync_stores where business_id = biz and name = p_name) then
    return json_build_object('ok', false, 'error', 'you already have a store with that name');
  end if;
  tz := coalesce(nullif(btrim(p_business_tz), ''), 'Africa/Nairobi');
  begin
    perform now() at time zone tz;
  exception when others then
    tz := 'Africa/Nairobi';
  end;
  new_code := public._mint_team_code(p_slug);
  select coalesce(max(id), 0) + 1 into next_id from sync_stores;
  insert into sync_stores (id, slug, name, team_code, business_tz, auto_approve, active, business_id)
    values (next_id, p_slug, p_name, new_code, tz, false, true, biz);
  select is_platform into plat from businesses where id = biz;
  insert into store_payment_profiles (team_code, stk_mode, display_name)
    values (new_code, case when plat then 'shared' else 'manual' end, left(p_name, 40));
  return json_build_object('ok', true, 'slug', p_slug, 'name', p_name,
      'team_code', new_code, 'business_tz', tz,
      'note', 'Add tills to this store with a join link or by assigning them.');
end $$;

-- The legacy unassigned-device pool belongs to the founding business only:
-- a device in it has no business, so no other tenant may see or claim it.
create or replace function public.sync_list_pending(p_device_id text, p_secret_hash text)
returns json language plpgsql security definer set search_path to 'public' as $$
declare biz bigint; plat boolean; out_rows jsonb;
begin
  biz := public._device_business(p_device_id, p_secret_hash);
  if biz is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  select is_platform into plat from businesses where id = biz;
  if not coalesce(plat, false) then
    return json_build_object('ok', true, 'devices', '[]'::jsonb);
  end if;
  select coalesce(jsonb_agg(jsonb_build_object(
      'device_id', d.device_id, 'device_name', d.device_name,
      'app_version', d.app_version, 'last_seen', d.last_seen)
      order by d.last_seen desc), '[]'::jsonb) into out_rows
    from sync_devices d where d.team_code is null and not d.revoked;
  return json_build_object('ok', true, 'devices', out_rows);
end $$;

create or replace function public.sync_assign_device(p_device_id text, p_secret_hash text,
  p_target_device_id text, p_team_code text, p_owner_capability text default null::text)
returns json language plpgsql security definer set search_path to 'public', 'extensions' as $$
declare dev record; store record; tgt record; biz bigint; plat boolean;
begin
  select * into dev from sync_devices
    where device_id = p_device_id and secret_hash = p_secret_hash
      and approved and not revoked;
  if dev.device_id is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  biz := public._device_business(p_device_id, p_secret_hash);
  select * into store from sync_stores where team_code = p_team_code and active;
  if store.team_code is null or store.business_id is distinct from biz then
    return json_build_object('ok', false, 'error', 'unknown store');
  end if;
  if store.team_code <> dev.team_code then
    return json_build_object('ok', false, 'error', 'you can only assign tills to your own store');
  end if;
  select * into tgt from sync_devices where device_id = p_target_device_id;
  if tgt.device_id is null then
    return json_build_object('ok', false, 'error', 'device not registered');
  end if;
  if tgt.revoked then
    return json_build_object('ok', false, 'error', 'device is revoked');
  end if;
  if tgt.team_code is not null and tgt.team_code <> dev.team_code then
    return json_build_object('ok', false, 'error', 'that till belongs to another store');
  end if;
  if tgt.team_code is null then
    select is_platform into plat from businesses where id = biz;
    if not coalesce(plat, false) then
      return json_build_object('ok', false, 'error',
        'add tills with a join link (Settings → Team → Invite a worker)');
    end if;
  end if;
  update sync_devices set team_code = store.team_code, approved = true, last_seen = now()
    where device_id = tgt.device_id;
  return json_build_object('ok', true, 'device_id', tgt.device_id,
      'team_code', store.team_code);
end $$;

-- ---------------------------------------------------------------- 6. owner login

create or replace function public.portal_set_credentials(p_device_id text, p_secret_hash text,
  p_username text, p_password_hash text, p_owner_capability text default null::text)
returns json language plpgsql security definer set search_path to 'public', 'extensions' as $$
declare dev record; existing record;
begin
  select * into dev from sync_devices
    where device_id = p_device_id and secret_hash = p_secret_hash
      and approved and not revoked;
  if dev.device_id is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  if dev.team_code is null then
    return json_build_object('ok', false, 'error', 'device has no store yet');
  end if;
  p_username := btrim(coalesce(p_username, ''));
  p_password_hash := btrim(coalesce(p_password_hash, ''));
  if p_username = '' or length(p_username) > 60 then
    return json_build_object('ok', false, 'error', 'invalid username');
  end if;
  if p_password_hash !~ '^\$2[aby]\$' then
    return json_build_object('ok', false, 'error', 'password hash must be bcrypt');
  end if;
  -- the username is the cloud sign-in key: unique across businesses
  if exists (select 1 from portal_accounts
              where lower(username) = lower(p_username) and team_code <> dev.team_code) then
    return json_build_object('ok', false, 'error',
      'that owner username is taken, pick another one');
  end if;
  -- replacing an existing owner login needs the owner, not just a till
  select * into existing from portal_accounts where team_code = dev.team_code;
  if existing.team_code is not null
     and public.owner_capability_check(p_owner_capability, 'portal_set_credentials', dev.team_code) is null then
    return json_build_object('ok', false, 'error',
      'confirm with the current owner password to change the owner login');
  end if;
  insert into portal_accounts (team_code, username, password_hash, updated_at)
    values (dev.team_code, p_username, p_password_hash, now())
    on conflict (team_code) do update
      set username = excluded.username,
          password_hash = excluded.password_hash,
          updated_at = now();
  return json_build_object('ok', true);
end $$;

-- ---------------------------------------------------------------- payment profile RPCs

-- What the till needs to know (never the passkey).
create or replace function public.payment_profile_get(p_device_id text, p_secret_hash text)
returns json language plpgsql security definer set search_path to 'public' as $$
declare dev record; pr record;
begin
  select * into dev from sync_devices
    where device_id = p_device_id and secret_hash = p_secret_hash
      and approved and not revoked;
  if dev.device_id is null or dev.team_code is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  select * into pr from store_payment_profiles where team_code = dev.team_code;
  return json_build_object('ok', true,
    'stk_mode', coalesce(pr.stk_mode, 'manual'),
    'stk_available', coalesce(pr.stk_mode = 'shared' or (pr.stk_mode = 'own' and pr.own_verified), false),
    'till_number', coalesce(pr.till_number, ''),
    'paybill_number', coalesce(pr.paybill_number, ''),
    'display_name', coalesce(pr.display_name, ''),
    'org_shortcode', coalesce(pr.org_shortcode, ''),
    'own_requested', coalesce(pr.stk_mode = 'own', false),
    'own_verified', coalesce(pr.own_verified, false),
    'has_passkey', coalesce(pr.org_passkey <> '', false));
end $$;

-- Owner sets where customers pay. Requesting 'own' stores the short code +
-- passkey but leaves own_verified false: the platform turns it on after a
-- live test with KCB, so a typo can never send a customer's money astray.
-- A shop can never select 'shared' (the platform's till) for itself.
create or replace function public.payment_profile_set(p_device_id text, p_secret_hash text,
  p_owner_capability text, p_stk_mode text, p_till_number text, p_paybill_number text,
  p_display_name text, p_org_shortcode text default '', p_org_passkey text default null)
returns json language plpgsql security definer set search_path to 'public', 'extensions' as $$
declare dev record; plat boolean; mode text;
begin
  select * into dev from sync_devices
    where device_id = p_device_id and secret_hash = p_secret_hash
      and approved and not revoked;
  if dev.device_id is null or dev.team_code is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  -- Same rule as portal_set_credentials: until the shop has a cloud owner
  -- login, its approved tills (admin-gated locally) may set this; after
  -- that, only a fresh owner capability can change where money goes.
  if exists (select 1 from portal_accounts where team_code = dev.team_code)
     and public.owner_capability_check(p_owner_capability, 'payment_profile_set', dev.team_code) is null then
    return json_build_object('ok', false, 'error',
      'confirm with the owner username and password to change where payments go');
  end if;
  select b.is_platform into plat from sync_stores s join businesses b on b.id = s.business_id
   where s.team_code = dev.team_code;
  mode := coalesce(nullif(btrim(p_stk_mode), ''), 'manual');
  if mode not in ('shared','own','manual') then
    return json_build_object('ok', false, 'error', 'unknown payment mode');
  end if;
  if mode = 'shared' and not coalesce(plat, false) then
    return json_build_object('ok', false, 'error', 'that payment route is not available for this shop');
  end if;
  p_till_number := regexp_replace(coalesce(p_till_number, ''), '\s', '', 'g');
  p_paybill_number := regexp_replace(coalesce(p_paybill_number, ''), '\s', '', 'g');
  p_org_shortcode := regexp_replace(coalesce(p_org_shortcode, ''), '\s', '', 'g');
  if p_till_number !~ '^[0-9]{0,10}$' or p_paybill_number !~ '^[0-9]{0,10}$'
     or p_org_shortcode !~ '^[0-9]{0,10}$' then
    return json_build_object('ok', false, 'error', 'till, paybill and short code are digits only');
  end if;
  if mode = 'own' and p_org_shortcode = '' then
    return json_build_object('ok', false, 'error', 'enter the short code M-Pesa prompts should pay');
  end if;
  insert into store_payment_profiles (team_code, stk_mode, till_number, paybill_number,
      display_name, org_shortcode, org_passkey, own_verified, updated_at)
    values (dev.team_code, mode, p_till_number, p_paybill_number,
            left(btrim(coalesce(p_display_name, '')), 40), p_org_shortcode,
            coalesce(p_org_passkey, ''), false, now())
    on conflict (team_code) do update set
      stk_mode = excluded.stk_mode,
      till_number = excluded.till_number,
      paybill_number = excluded.paybill_number,
      display_name = excluded.display_name,
      org_shortcode = excluded.org_shortcode,
      -- keep the stored passkey unless a new one is sent
      org_passkey = case when p_org_passkey is null then store_payment_profiles.org_passkey
                         else excluded.org_passkey end,
      -- any change to the destination needs re-verification
      own_verified = case when excluded.stk_mode = 'own'
                           and store_payment_profiles.stk_mode = 'own'
                           and store_payment_profiles.org_shortcode = excluded.org_shortcode
                           and p_org_passkey is null
                          then store_payment_profiles.own_verified else false end,
      updated_at = now();
  return public.payment_profile_get(p_device_id, p_secret_hash);
end $$;

revoke all on function public.payment_profile_get(text,text) from public;
grant execute on function public.payment_profile_get(text,text) to anon, authenticated;
revoke all on function public.payment_profile_set(text,text,text,text,text,text,text,text,text) from public;
grant execute on function public.payment_profile_set(text,text,text,text,text,text,text,text,text) to anon, authenticated;

-- Server-side lookup for the buni-stkpush Edge Function (service role only).
create or replace function public.stk_route_for_team(p_team text)
returns json language sql stable security definer set search_path to 'public' as $$
  select json_build_object(
    'stk_mode', coalesce(pr.stk_mode, 'manual'),
    'own_verified', coalesce(pr.own_verified, false),
    'org_shortcode', coalesce(pr.org_shortcode, ''),
    'org_passkey', coalesce(pr.org_passkey, ''),
    'display_name', coalesce(nullif(pr.display_name, ''), s.name, ''),
    'is_platform', coalesce(b.is_platform, false))
  from sync_stores s
  join businesses b on b.id = s.business_id
  left join store_payment_profiles pr on pr.team_code = s.team_code
  where s.team_code = p_team and s.active
$$;
revoke all on function public.stk_route_for_team(text) from public, anon, authenticated;
grant execute on function public.stk_route_for_team(text) to service_role;

-- ---------------------------------------------------------------- verification
-- select id, name, is_platform from businesses;                 -- 1 founding row
-- select team_code, business_id from sync_stores;               -- all = 1
-- select team_code, stk_mode from store_payment_profiles;       -- founding = shared
-- select has_table_privilege('anon','public.sync_stores','select'); -- false
-- select sync_register('probe-device-xyz', repeat('a',64), 'probe', 't'); -- pending, no team
