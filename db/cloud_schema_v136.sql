-- =====================================================================
-- v1.3.6 cloud migration — no more "confirm it's you", direct PIN grants,
-- cloud-assisted LAN discovery.
-- =====================================================================
-- (1) The owner-capability challenge is RETIRED. Every privileged RPC is
--     gated by device identity alone (approved, non-revoked, secret-hash
--     verified, team-bound) — exactly like every other sync RPC. The
--     challenge was a dead end in the field: the portal_accounts table is
--     empty on real deployments, so the dialog could never succeed, and
--     an owner already proved themselves by signing into their own till.
--     The p_owner_capability parameter stays on the wire for older tills;
--     it is simply ignored.
-- (2) DIRECT PIN GRANTS: the owner types the new 4-digit PIN once, at
--     approve time. The cloud stores a bcrypt hash bound to the
--     requesting device; the locked till polls, claims it (single-use)
--     and applies it locally. No 8-digit code, nothing to type on the
--     locked till. The legacy code path (read-aloud) still works for
--     older tills that approve without p_new_pin.
-- (3) sync_register carries the till's LAN address (landelivery
--     listener), so a sibling till can hand over data directly even on
--     WiFi networks where multicast is blocked (AP isolation). The cloud
--     grant poll remains the always-works path.
-- =====================================================================

create extension if not exists pgcrypto;

-- (3a) LAN address registry + grant storage
alter table public.sync_devices add column if not exists lan_addr text not null default '';
alter table public.sync_pin_resets add column if not exists pin_hash text not null default '';
alter table public.sync_pin_resets add column if not exists claimed_at timestamptz;

-- ====================================================================
-- (1a) UNGATED owner RPCs — device identity is the authority.
-- ====================================================================

create or replace function public.sync_create_store(
  p_device_id text, p_secret_hash text, p_name text, p_slug text,
  p_business_tz text default 'Africa/Nairobi',
  p_owner_capability text default null)
returns json language plpgsql security definer set search_path to 'public', 'extensions'
as $function$
declare dev record; new_code text; next_id int; tries int := 0; tz text;
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
  tz := coalesce(btrim(p_business_tz), 'Africa/Nairobi');
  begin
    perform now() AT TIME ZONE tz;
  exception when others then
    tz := 'Africa/Nairobi';
  end;
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
  insert into sync_stores (id, slug, name, team_code, business_tz, auto_approve, active)
    values (next_id, p_slug, p_name, new_code, tz, false, true);
  return json_build_object('ok', true, 'slug', p_slug, 'name', p_name,
      'team_code', new_code, 'business_tz', tz,
      'note', 'Assign tills to this store from the pending list.');
end $function$;

create or replace function public.sync_assign_device(
  p_device_id text, p_secret_hash text, p_target_device_id text, p_team_code text,
  p_owner_capability text default null)
returns json language plpgsql security definer set search_path to 'public', 'extensions'
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

create or replace function public.sync_remove_device(
  p_device_id text, p_secret_hash text, p_target_device_id text,
  p_owner_capability text default null)
returns json language plpgsql security definer set search_path to 'public', 'extensions'
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

-- Approve can un-revoke (the strongest healing act in the model) — still
-- device-identity gated; a revoked till cannot call this for itself.
create or replace function public.sync_approve_device(
  p_device_id text, p_secret_hash text, p_target_device_id text,
  p_owner_capability text default null)
returns json language plpgsql security definer set search_path to 'public', 'extensions'
as $function$
declare dev record; tgt record;
begin
  select * into dev from sync_devices
    where device_id = p_device_id and secret_hash = p_secret_hash
      and approved and not revoked;
  if dev.device_id is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  select * into tgt from sync_devices where device_id = p_target_device_id;
  if tgt.device_id is null then
    return json_build_object('ok', false, 'error', 'device not registered');
  end if;
  if tgt.team_code is null or tgt.team_code <> dev.team_code then
    return json_build_object('ok', false, 'error', 'device is not on your team');
  end if;
  update sync_devices set approved = true, revoked = false, last_seen = now()
    where device_id = tgt.device_id;
  return json_build_object('ok', true, 'device_id', tgt.device_id);
end $function$;

create or replace function public.sync_create_invite(
  p_device_id text, p_secret_hash text, p_role_name text,
  p_permissions jsonb default '[]'::jsonb, p_note text default '', p_days int default 7,
  p_owner_capability text default null)
returns json language plpgsql security definer set search_path to 'public', 'extensions'
as $function$
declare dev record; tok text; tries int := 0;
begin
  select * into dev from sync_devices
    where device_id = p_device_id and secret_hash = p_secret_hash
      and approved and not revoked;
  if dev.device_id is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  if dev.team_code is null then
    return json_build_object('ok', false, 'error', 'assign this till to a store first');
  end if;
  p_role_name := btrim(coalesce(p_role_name, ''));
  if p_role_name = '' or length(p_role_name) > 40 then
    return json_build_object('ok', false, 'error', 'pick a role for the invite');
  end if;
  if p_days < 1 or p_days > 90 then
    p_days := 7;
  end if;
  loop
    tok := upper(substr(md5(random()::text || clock_timestamp()::text || dev.device_id), 1, 8))
           || '-' || upper(substr(md5(clock_timestamp()::text || random()::text), 1, 8))
           || '-' || upper(substr(md5(random()::text || p_role_name), 1, 8));
    exit when not exists (select 1 from sync_invites where token_hash = tok);
    tries := tries + 1;
    if tries > 10 then
      return json_build_object('ok', false, 'error', 'could not mint a token');
    end if;
  end loop;
  insert into sync_invites (team_code, token_hash, role_name, permissions, note, created_by, expires_at)
    values (dev.team_code, tok, p_role_name,
            coalesce(p_permissions, '[]'::jsonb), btrim(coalesce(p_note, '')),
            dev.device_id, now() + make_interval(days => p_days));
  return json_build_object('ok', true, 'token', tok, 'team_code', dev.team_code,
      'role_name', p_role_name, 'expires_at', now() + make_interval(days => p_days));
end $function$;

create or replace function public.sync_revoke_invite(
  p_device_id text, p_secret_hash text, p_invite_id bigint,
  p_owner_capability text default null)
returns json language plpgsql security definer set search_path to 'public', 'extensions'
as $function$
declare dev record;
begin
  select * into dev from sync_devices
    where device_id = p_device_id and secret_hash = p_secret_hash
      and approved and not revoked;
  if dev.device_id is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  update sync_invites set revoked = true
    where id = p_invite_id and team_code = dev.team_code and used_by = '';
  if not found then
    return json_build_object('ok', false, 'error', 'invite not found or already used');
  end if;
  return json_build_object('ok', true);
end $function$;

-- Portal credentials: publication (first seed) stays device-gated; the
-- capability gate on CHANGE is retired with the rest (device identity only).
create or replace function public.portal_set_credentials(
  p_device_id text, p_secret_hash text, p_username text, p_password_hash text,
  p_owner_capability text default null)
returns json language plpgsql security definer set search_path to 'public', 'extensions'
as $function$
declare dev record;
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
  insert into portal_accounts (team_code, username, password_hash, updated_at)
    values (dev.team_code, p_username, p_password_hash, now())
    on conflict (team_code) do update
      set username = excluded.username,
          password_hash = excluded.password_hash,
          updated_at = now();
  return json_build_object('ok', true);
end $function$;

-- ====================================================================
-- (2) DIRECT PIN GRANTS — approve with the new PIN, no code at all.
-- ====================================================================

-- Drop the v1.3.5 signature first: with both overloads live, a 4-arg call
-- would bind p_new_pin to p_owner_capability (Postgres prefers the exact
-- match). The 5-arg version below is positionally backward compatible
-- (p_owner_capability first, p_new_pin last, both defaulted).
drop function if exists public.sync_approve_pin_reset(text, text, bigint, text);

create or replace function public.sync_approve_pin_reset(
  p_device_id text, p_secret_hash text, p_request_id bigint,
  p_owner_capability text default null,
  p_new_pin text default null)
returns json language plpgsql security definer set search_path to 'public', 'extensions'
as $function$
declare dev record; req record; tgt record; b bytea; c text;
begin
  select * into dev from sync_devices
    where device_id = p_device_id and secret_hash = p_secret_hash
      and approved and not revoked;
  if dev.device_id is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  select * into req from sync_pin_resets
    where id = p_request_id and team_code = dev.team_code;
  if req.id is null then
    return json_build_object('ok', false, 'error', 'reset request not found');
  end if;
  -- A stale claim (the till took the grant but could not apply it) can be
  -- re-approved; a used request cannot.
  if req.status = 'used' or req.status = 'cancelled' then
    return json_build_object('ok', false, 'error', 'this request was already handled');
  end if;
  if req.created_at < now() - interval '7 days' then
    update sync_pin_resets set status = 'cancelled' where id = req.id;
    return json_build_object('ok', false, 'error', 'this request expired, ask them to send it again');
  end if;
  p_new_pin := coalesce(btrim(coalesce(p_new_pin, '')), '');
  if p_new_pin <> '' then
    -- DIRECT GRANT: hash the PIN here, hand the hash (never the PIN) to
    -- the requesting till on claim. 24h window; re-approve overwrites.
    if p_new_pin !~ '^[0-9]{4}$' or p_new_pin in
       ('0000','1111','2222','3333','4444','5555','6666','7777','8888','9999',
        '1212','1122','1234','4321','2580','0123','6789','1379','1004','2000') then
      return json_build_object('ok', false, 'error',
        'choose a 4-digit PIN that isn''t an obvious pattern like 0000 or 1234');
    end if;
    update sync_pin_resets
      set status = 'approved', pin_hash = crypt(p_new_pin, gen_salt('bf')),
          code_hash = '', expires_at = now() + interval '24 hours',
          approved_at = now(), attempts = 0, claimed_at = null
      where id = req.id;
    select lan_addr into tgt from sync_devices where device_id = req.device_id;
    return json_build_object('ok', true, 'granted', true, 'code', '',
      'username', req.username, 'deviceName', req.device_name,
      'deviceId', req.device_id, 'lanAddr', coalesce(tgt.lan_addr, ''),
      'expiresMinutes', 1440);
  end if;
  -- LEGACY: mint the single-use 8-digit code (older tills read it aloud).
  b := gen_random_bytes(4);
  c := lpad((((get_byte(b,0)::bigint * 16777216) + (get_byte(b,1)::bigint * 65536)
            + (get_byte(b,2)::bigint * 256) + get_byte(b,3)::bigint) % 100000000)::text, 8, '0');
  update sync_pin_resets
    set status = 'approved', code_hash = encode(digest(c, 'sha256'), 'hex'),
        pin_hash = '', expires_at = now() + interval '15 minutes',
        approved_at = now(), attempts = 0, claimed_at = null
    where id = req.id;
  select lan_addr into tgt from sync_devices where device_id = req.device_id;
  return json_build_object('ok', true, 'granted', false, 'code', c,
    'username', req.username, 'deviceName', req.device_name,
    'deviceId', req.device_id, 'lanAddr', coalesce(tgt.lan_addr, ''),
    'expiresMinutes', 15);
end $function$;

-- The locked till polls for a grant bound to ITSELF (device identity
-- verified). Serves a fresh grant, or a stale one (claimed >30 min ago —
-- the till probably failed to apply it and the owner could not reach the
-- shop). The grant is NOT consumed by polling.
create or replace function public.sync_poll_pin_grant(
  p_device_id text, p_secret_hash text)
returns json language plpgsql security definer set search_path to 'public', 'extensions'
as $function$
declare dev record; req record;
begin
  select * into dev from sync_devices
    where device_id = p_device_id and secret_hash = p_secret_hash
      and approved and not revoked;
  if dev.device_id is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  select * into req from sync_pin_resets
    where device_id = dev.device_id
      and status in ('approved', 'claimed')
      and pin_hash <> ''
      and expires_at > now()
      and (status = 'approved' or coalesce(claimed_at, now()) < now() - interval '30 minutes')
    order by id desc limit 1;
  if req.id is null then
    return json_build_object('ok', true, 'grant', false);
  end if;
  return json_build_object('ok', true, 'grant', true,
    'requestId', req.id, 'username', req.username);
end $function$;

-- Single-use claim: returns the bcrypt hash once, marks it claimed. The
-- till applies it locally (the cloud never learns whether the local
-- account still exists — a failed apply is healed by the stale-claim
-- re-poll above or a fresh approve).
create or replace function public.sync_claim_pin_grant(
  p_device_id text, p_secret_hash text, p_request_id bigint)
returns json language plpgsql security definer set search_path to 'public', 'extensions'
as $function$
declare dev record; req record;
begin
  select * into dev from sync_devices
    where device_id = p_device_id and secret_hash = p_secret_hash
      and approved and not revoked;
  if dev.device_id is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  select * into req from sync_pin_resets
    where id = p_request_id and device_id = dev.device_id
      and status in ('approved', 'claimed')
      and pin_hash <> '';
  if req.id is null then
    return json_build_object('ok', false, 'error', 'no grant for this till');
  end if;
  if req.status = 'claimed' and coalesce(req.claimed_at, now()) >= now() - interval '30 minutes' then
    return json_build_object('ok', false, 'error', 'this grant was already collected');
  end if;
  if req.expires_at is not null and req.expires_at <= now() then
    return json_build_object('ok', false, 'error', 'this grant expired, ask the owner to approve again');
  end if;
  update sync_pin_resets set status = 'claimed', claimed_at = now()
    where id = req.id;
  return json_build_object('ok', true, 'username', req.username, 'pinHash', req.pin_hash);
end $function$;

-- ====================================================================
-- (3b) sync_register carries the till's LAN delivery address.
-- ====================================================================

create or replace function public.sync_register(
  p_device_id text, p_secret_hash text, p_device_name text, p_app_version text,
  p_lan_addr text default '')
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
        secret_hash = p_secret_hash, lan_addr = coalesce(btrim(coalesce(p_lan_addr, '')), lan_addr),
        last_seen = now()
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
        secret_hash, approved, lan_addr, last_seen)
      values (p_device_id, store.team_code, coalesce(p_device_name, 'till'),
              coalesce(p_app_version, ''), p_secret_hash, store.auto_approve,
              coalesce(btrim(coalesce(p_lan_addr, '')), ''), now());
    return json_build_object('ok', true, 'approved', store.auto_approve,
        'team_code', store.team_code, 'pending', false);
  end if;
  insert into sync_devices (device_id, team_code, device_name, app_version,
      secret_hash, approved, lan_addr, last_seen)
    values (p_device_id, null, coalesce(p_device_name, 'till'),
            coalesce(p_app_version, ''), p_secret_hash, false,
            coalesce(btrim(coalesce(p_lan_addr, '')), ''), now());
  return json_build_object('ok', true, 'approved', false, 'pending', true);
end $function$;
