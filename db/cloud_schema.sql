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
