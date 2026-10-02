-- LedgerPOS cloud schema (Supabase, project ixxiqrobcwkvyjtxdkvh).
-- Canonical reference for the multi-store sync cloud. Applied live via the
-- Management API; keep this file in sync with any future changes.
--
-- Model:
--   sync_stores    one row per store an owner runs. slug is stable, team_code
--                  is the sync partition (minted by the DB, never guessed).
--   sync_devices   one row per till. team_code = the store it serves; NULL
--                  + approved=false = registered but waiting for the owner
--                  to assign it to a store (only happens when >1 store exists).
--   sync_events    change log partitioned by team_code. A device can only
--                  ever read/write its own team's rows (RPC-verified identity).
--   sync_bootstrap legacy single-store bootstrap (row 1 mirrors store 1) so
--                  v1.1.0 tills keep auto-joining the default store.
--
-- Security: RLS on every table. Anon can read sync_bootstrap row 1 and the
-- active sync_stores rows only; sync_devices/sync_events have no anon
-- policies at all. Every mutation goes through SECURITY DEFINER RPCs that
-- verify (device_id, secret_hash) and stamp team_code server-side.

-- ---------------------------------------------------------------- stores --
create table if not exists sync_stores (
  id          int primary key,
  slug        text unique not null,
  name        text not null,
  team_code   text unique not null,
  auto_approve boolean not null default true,
  active      boolean not null default true,
  created_at  timestamptz not null default now()
);

insert into sync_stores (id, slug, name, team_code, auto_approve, active)
select 1, 'main', 'Main Store', b.team_code, b.auto_approve, true
from sync_bootstrap b where b.id = 1
on conflict (id) do nothing;

alter table sync_stores enable row level security;
drop policy if exists stores_public_read on sync_stores;
create policy stores_public_read on sync_stores for select to anon
  using (active);

-- -------------------------------------------------------------- devices --
-- Pending (store-unassigned) devices carry a NULL team_code.
alter table sync_devices alter column team_code drop not null;

-- ---------------------------------------------------------- register RPC --
-- Multi-store aware: with exactly one active store the till auto-joins it
-- (zero-config, unchanged behaviour). With several stores the new till is
-- registered as pending; the owner assigns it from Settings → Team.

create or replace function public.sync_register(
  p_device_id text, p_secret_hash text, p_device_name text, p_app_version text)
returns json language plpgsql security definer set search_path to 'public'
as $function$
declare cur record; store record; store_count int;
begin
  if p_device_id is null or length(p_device_id) < 8
     or p_secret_hash is null or length(p_secret_hash) < 32 then
    return json_build_object('ok', false, 'error', 'invalid identity');
  end if;
  select * into cur from sync_devices where device_id = p_device_id;
  if cur.device_id is not null then
    if cur.revoked then
      return json_build_object('ok', false, 'error', 'device revoked');
    end if;
    if cur.secret_hash <> '' and cur.secret_hash <> p_secret_hash then
      return json_build_object('ok', false, 'error', 'identity mismatch');
    end if;
    update sync_devices set device_name = p_device_name, app_version = p_app_version,
        secret_hash = p_secret_hash, last_seen = now()
      where device_id = p_device_id;
    return json_build_object('ok', true, 'approved', cur.approved,
        'team_code', cur.team_code, 'pending', cur.team_code is null);
  end if;
  select count(*) into store_count from sync_stores where active;
  if store_count = 0 then
    return json_build_object('ok', false,
        'error', 'no store is registered in the cloud yet');
  end if;
  if store_count = 1 then
    select * into store from sync_stores where active limit 1;
    insert into sync_devices (device_id, team_code, device_name, app_version,
        secret_hash, approved, last_seen)
      values (p_device_id, store.team_code, coalesce(p_device_name, 'till'),
              coalesce(p_app_version, ''), p_secret_hash, store.auto_approve, now());
    return json_build_object('ok', true, 'approved', store.auto_approve,
        'team_code', store.team_code, 'pending', false);
  end if;
  -- Several stores exist: never guess. Register as pending; the owner
  -- assigns this device to a store from an approved till.
  insert into sync_devices (device_id, team_code, device_name, app_version,
      secret_hash, approved, last_seen)
    values (p_device_id, null, coalesce(p_device_name, 'till'),
            coalesce(p_app_version, ''), p_secret_hash, false, now());
  return json_build_object('ok', true, 'approved', false, 'team_code', null,
      'pending', true,
      'stores', (select coalesce(jsonb_agg(jsonb_build_object(
                    'slug', s.slug, 'name', s.name, 'team_code', s.team_code)
                    order by s.id), '[]'::jsonb)
                 from sync_stores s where s.active));
end $function$;

-- ------------------------------------------------------- owner RPCs ------
-- Every owner RPC requires an approved, non-revoked device identity.

-- Add a store under this owner's team umbrella. The team code is minted
-- here (never in the client) and is guaranteed unique.
create or replace function public.sync_create_store(
  p_device_id text, p_secret_hash text, p_name text, p_slug text)
returns json language plpgsql security definer set search_path to 'public'
as $function$
declare dev record; new_code text; next_id int; tries int := 0;
begin
  select * into dev from sync_devices
    where device_id = p_device_id and secret_hash = p_secret_hash
      and approved and not revoked;
  if dev.device_id is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  p_name  := btrim(coalesce(p_name, ''));
  p_slug  := lower(btrim(coalesce(p_slug, '')));
  if length(p_name) < 2 or length(p_name) > 60 then
    return json_build_object('ok', false, 'error', 'store name must be 2-60 characters');
  end if;
  if p_slug = '' then
    p_slug := lower(regexp_replace(p_name, '[^a-zA-Z0-9]+', '-', 'g'));
    p_slug := btrim(p_slug, '-');
  end if;
  if p_slug !~ '^[a-z0-9][a-z0-9-]{1,29}$' then
    return json_build_object('ok', false, 'error',
        'store slug must be 2-30 chars: letters, numbers, dashes');
  end if;
  if exists (select 1 from sync_stores where slug = p_slug) then
    return json_build_object('ok', false, 'error', 'a store with that slug already exists');
  end if;
  if exists (select 1 from sync_stores where name = p_name) then
    return json_build_object('ok', false, 'error', 'a store with that name already exists');
  end if;
  select coalesce(max(id), 0) + 1 into next_id from sync_stores;
  loop
    new_code := 'T' || upper(substr(md5(random()::text || clock_timestamp()::text || p_slug), 1, 4))
                || '-' || upper(substr(md5(clock_timestamp()::text || random()::text), 1, 4));
    exit when not exists (select 1 from sync_stores where team_code = new_code);
    tries := tries + 1;
    if tries > 20 then
      return json_build_object('ok', false, 'error', 'could not mint a unique team code');
    end if;
  end loop;
  insert into sync_stores (id, slug, name, team_code, auto_approve, active)
    values (next_id, p_slug, p_name, new_code, false, true);
  return json_build_object('ok', true, 'slug', p_slug, 'name', p_name,
      'team_code', new_code,
      'note', 'Assign tills to this store from the pending list.');
end $function$;

-- Stores overview for the owner (device counts per store).
create or replace function public.sync_list_stores(
  p_device_id text, p_secret_hash text)
returns json language plpgsql security definer set search_path to 'public'
as $function$
declare dev record; out_rows jsonb;
begin
  select * into dev from sync_devices
    where device_id = p_device_id and secret_hash = p_secret_hash
      and approved and not revoked;
  if dev.device_id is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  select coalesce(jsonb_agg(jsonb_build_object(
      'slug', s.slug, 'name', s.name, 'team_code', s.team_code,
      'devices', (select count(*) from sync_devices d
                  where d.team_code = s.team_code and not d.revoked),
      'created_at', s.created_at) order by s.id), '[]'::jsonb) into out_rows
    from sync_stores s where s.active;
  return json_build_object('ok', true, 'stores', out_rows);
end $function$;

-- Devices registered but not yet assigned to a store (team_code is null).
create or replace function public.sync_list_pending(
  p_device_id text, p_secret_hash text)
returns json language plpgsql security definer set search_path to 'public'
as $function$
declare dev record; out_rows jsonb;
begin
  select * into dev from sync_devices
    where device_id = p_device_id and secret_hash = p_secret_hash
      and approved and not revoked;
  if dev.device_id is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  select coalesce(jsonb_agg(jsonb_build_object(
      'device_id', d.device_id, 'device_name', d.device_name,
      'app_version', d.app_version, 'last_seen', d.last_seen)
      order by d.last_seen desc), '[]'::jsonb) into out_rows
    from sync_devices d where d.team_code is null and not d.revoked;
  return json_build_object('ok', true, 'devices', out_rows);
end $function$;

-- Assign (or re-assign) a device to a store team.
create or replace function public.sync_assign_device(
  p_device_id text, p_secret_hash text, p_target_device_id text, p_team_code text)
returns json language plpgsql security definer set search_path to 'public'
as $function$
declare dev record; store record; tgt record;
begin
  select * into dev from sync_devices
    where device_id = p_device_id and secret_hash = p_secret_hash
      and approved and not revoked;
  if dev.device_id is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  select * into store from sync_stores
    where team_code = p_team_code and active;
  if store.team_code is null then
    return json_build_object('ok', false, 'error', 'unknown store');
  end if;
  select * into tgt from sync_devices where device_id = p_target_device_id;
  if tgt.device_id is null then
    return json_build_object('ok', false, 'error', 'device not registered');
  end if;
  if tgt.revoked then
    return json_build_object('ok', false, 'error', 'device is revoked');
  end if;
  update sync_devices set team_code = store.team_code, approved = true, last_seen = now()
    where device_id = tgt.device_id;
  return json_build_object('ok', true, 'device_id', tgt.device_id,
      'team_code', store.team_code);
end $function$;

-- Kill switch from the UI (Settings → Team): revoke a device outright.
create or replace function public.sync_remove_device(
  p_device_id text, p_secret_hash text, p_target_device_id text)
returns json language plpgsql security definer set search_path to 'public'
as $function$
declare dev record;
begin
  select * into dev from sync_devices
    where device_id = p_device_id and secret_hash = p_secret_hash
      and approved and not revoked;
  if dev.device_id is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  if p_target_device_id = dev.device_id then
    return json_build_object('ok', false, 'error', 'cannot remove this device while using it');
  end if;
  update sync_devices set revoked = true, approved = false, last_seen = now()
    where device_id = p_target_device_id and not revoked;
  return json_build_object('ok', true, 'device_id', p_target_device_id);
end $function$;


-- ============================================================
-- v1.1.5 additions
-- ============================================================

-- ------------------------------------------------------- device config ----
create or replace function public.sync_device_config(
  p_device_id text, p_secret_hash text)
returns json language plpgsql security definer
set search_path to 'public'
as $function$
declare dev record; rows jsonb;
begin
  select * into dev from sync_devices
    where device_id = p_device_id and secret_hash = p_secret_hash
      and approved and not revoked;
  if dev.device_id is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  update sync_devices set last_seen = now() where device_id = dev.device_id;
  select coalesce(jsonb_object_agg(c.key, c.value), '{}'::jsonb) into rows
    from app_config c;
  return json_build_object('ok', true, 'config', rows);
end $function$;

-- ----------------------------------------------------- grants (defense) ---
-- Nobody but the owner's tooling (service_role / postgres / CI) may write
-- config; reads happen through RLS (public rows) or the RPC (all rows).
revoke insert, update, delete, truncate on table public.app_config from anon, authenticated;

-- ------------------------------------------------- live config (idempotent) -
-- Owner-provided Paystack keys + the storefront callback. The secret key
-- rides the device-gated RPC; the public key / callback / currency are
-- anon-readable so a fresh till configures itself before approval.
-- LIVE KEY INJECTED OUT-OF-BAND (never committed): the secret value is
-- applied by the operator through the Supabase Management API / SQL
-- editor. The insert below carries a PLACEHOLDER so the row + RLS
-- contract is visible in the repo. Rotate with one UPDATE, never
-- paste real keys into version control.
insert into app_config (key, value, is_secret) values
  ('paystack_secret_key', 'sk_live_ROTATED_VIA_MANAGEMENT_API', true)
on conflict (key) do update set value = excluded.value, is_secret = true, updated_at = now();

insert into app_config (key, value, is_secret) values
  ('paystack_public_key', 'pk_live_66f1d939e67c79ef5418a575f84173b29f61b0e9', false)
on conflict (key) do update set value = excluded.value, is_secret = false, updated_at = now();

insert into app_config (key, value, is_secret) values
  ('paystack_callback_url', 'https://awesomeposs.netlify.app/', false)
on conflict (key) do update set value = excluded.value, is_secret = false, updated_at = now();

insert into app_config (key, value, is_secret) values
  ('paystack_currency', 'KES', false)
on conflict (key) do update set value = excluded.value, is_secret = false, updated_at = now();

insert into app_config (key, value, is_secret) values
  ('paystack_mode', 'live', false)
on conflict (key) do update set value = excluded.value, is_secret = false, updated_at = now();

insert into app_config (key, value, is_secret) values
  ('paystack_enabled', 'true', false)
on conflict (key) do update set value = excluded.value, is_secret = false, updated_at = now();
-- LedgerPOS cloud schema v1.2.0 (applied live; keep in sync with db/cloud_schema.sql).
-- Adds: owner cloud sign-in — an owner reinstalling the app (or setting up
-- a new till) signs in with their normal username + password instead of
-- being forced through "create your store". The cloud verifies the SAME
-- bcrypt credential the owner web portal uses (portal_accounts), approves
-- the device into the team server-side, and the shop syncs down.
--
-- SECURITY MODEL
--   * Credentials verified with pgcrypto crypt() against portal_accounts —
--     plaintext never stored, never returned.
--   * Device approval is stamped server-side (the RPC owns it), mirroring
--     sync_join_team.
--   * Brute-force dam: portal_signin_fails counts failures per username in
--     a 15-minute window (10 tries max). The table has no anon access.
--   * Unknown usernames burn a dummy bcrypt compare so timing does not
--     enumerate accounts.
--   * Revoked devices stay revoked — the RPC refuses them like every other
--     sync RPC.

-- -------------------------------------------------- sign-in attempt dam --
create table if not exists portal_signin_fails (
  username     text primary key,
  attempts     int not null default 0,
  window_start timestamptz not null default now()
);
revoke all on table public.portal_signin_fails from anon, authenticated;

create or replace function public.owner_device_signin(
  p_device_id text, p_secret_hash text, p_device_name text, p_app_version text,
  p_username text, p_password text)
returns json language plpgsql security definer set search_path = 'public', 'extensions'
as $function$
declare acct record; dev record; store record; fails int; matched boolean := false;
begin
  -- identity sanity (same gate as sync_register)
  if p_device_id is null or length(p_device_id) < 8
     or p_secret_hash is null or length(p_secret_hash) < 32 then
    return json_build_object('ok', false, 'error', 'invalid identity');
  end if;
  p_username := btrim(coalesce(p_username, ''));
  if p_username = '' or coalesce(p_password, '') = '' then
    return json_build_object('ok', false, 'error', 'username and password are required');
  end if;

  -- brute-force dam: 10 failures per username per 15-minute window
  select attempts into fails from portal_signin_fails
    where username = p_username and window_start > now() - interval '15 minutes';
  if coalesce(fails, 0) >= 10 then
    return json_build_object('ok', false, 'error',
      'too many attempts — try again in 15 minutes');
  end if;

  -- verify against the portal credential store (bcrypt)
  for acct in select * from portal_accounts where username = p_username loop
    matched := true;
    if crypt(p_password, acct.password_hash) = acct.password_hash then
      delete from portal_signin_fails where username = p_username;

      select * into store from sync_stores
        where team_code = acct.team_code and active limit 1;
      if store.id is null then
        return json_build_object('ok', false, 'error',
          'your store is not active in the cloud — contact support');
      end if;

      select * into dev from sync_devices where device_id = p_device_id;
      if dev.device_id is null then
        insert into sync_devices (device_id, team_code, device_name, app_version,
            secret_hash, approved, last_seen)
          values (p_device_id, acct.team_code, coalesce(nullif(btrim(p_device_name), ''), 'owner till'),
                  coalesce(nullif(btrim(p_app_version), ''), ''), p_secret_hash, true, now());
      elsif dev.revoked then
        return json_build_object('ok', false, 'error',
          'this device was revoked — ask the owner to approve it again from Settings → Team');
      else
        update sync_devices
          set team_code = acct.team_code, approved = true, last_seen = now(),
              secret_hash = p_secret_hash,
              device_name = coalesce(nullif(btrim(p_device_name), ''), dev.device_name),
              app_version = coalesce(nullif(btrim(p_app_version), ''), dev.app_version)
          where device_id = p_device_id;
      end if;

      return json_build_object('ok', true,
        'team_code', acct.team_code, 'store_name', store.name);
    end if;
  end loop;

  if not matched then
    -- equalize timing for unknown usernames (dummy bcrypt burn)
    perform crypt(p_password, '$2a$10$C6UzMDM.H6dfI/f/IKcEeO7VTgxjrpU8k95Lxvtqk1PGCvXnLBDF6');
  end if;

  -- count the failure inside the rolling window
  insert into portal_signin_fails (username, attempts, window_start)
    values (p_username, 1, now())
    on conflict (username) do update
      set attempts = case when portal_signin_fails.window_start > now() - interval '15 minutes'
                          then portal_signin_fails.attempts + 1 else 1 end,
          window_start = case when portal_signin_fails.window_start > now() - interval '15 minutes'
                              then portal_signin_fails.window_start else now() end;

  return json_build_object('ok', false, 'error',
    'wrong username or password — or cloud sign-in is not activated for your shop yet (activate it from Settings → Team on an existing till)');
end $function$;
