-- LedgerPOS cloud schema v1.3.2 (applied live via the Management API;
-- keep in sync with db/cloud_schema.sql — canonical bodies appended there).
-- Project: ixxiqrobcwkvyjtxdkvh. Every statement is idempotent.
--
-- Closes four review findings, per the owner's execution directive:
--
--   (1) 1.1 — OWNER CAPABILITY RPCs. Cloud owner actions previously trusted
--       a DEVICE identity ("which till is calling"), never an owner-level
--       HUMAN ("which human authorized this"). A stolen approved till could
--       create stores, assign/revoke devices, mint invites, replace portal
--       credentials and approve PIN resets. Now every privileged RPC also
--       requires a short-lived signed owner capability minted ONLY when the
--       caller proves the owner's portal credential (bcrypt) on top of its
--       device identity. Till UI prompts "Confirm it's you" once per 15 min.
--
--   (2) 8.6 — PORTAL SQL RATE LIMITER. portal_login had NO attempt limiter
--       (owner_device_signin's dam was per-username only, sprayable).
--       New shared SQL dam: normalized+hashed buckets, atomic upsert,
--       per-identity window, plus a GLOBAL failure budget across buckets
--       (catches distributed spray), plus a lazy 1-day sweep so the table
--       stays bounded without a cron. Multi-store identity: credentials
--       matching several teams now return needsStoreChoice + team list
--       instead of silently picking the first row.
--
--   (3) 8.9 — TIMEZONE-AWARE PORTAL DAYS. "Sales today" followed the
--       database session timezone (UTC → "today" flipped at 03:00 EAT).
--       sync_stores.business_tz (IANA, default Africa/Nairobi) now anchors
--       the day boundary; [start, start+1d) half-open interval.
--
--   (4) 8.2/5 — PAYSTACK WEBHOOK RELAY STORAGE. The Paystack secret key is
--       moving to an Edge Function secret (dual-read window: Deno.env first,
--       app_config vault row as fallback). The public webhook receiver is an
--       Edge Function that HMAC-verifies with Deno.env and stores verified
--       events here; tills claim the events for their own pending references
--       through a device-gated RPC and run them through the exact-once
--       completion path. app_config row deletion happens AFTER one full
--       verified payment cycle through the new path (never before).
--
-- Backward compatibility: every new RPC parameter has a DEFAULT, so older
-- tills can still CALL these functions — privileged ones now answer
-- 'owner approval required …' until the till presents a capability. The
-- OTA manifest sweeps tills to v1.3.2 which prompts for it.

create extension if not exists pgcrypto;

-- ====================================================================
-- (1) OWNER CAPABILITY — short-lived signed authority for owner RPCs
-- ====================================================================

-- HMAC signing key for capability tokens. No grants: reachable only from
-- SECURITY DEFINER functions (service_role/postgres for rotation).
create table if not exists owner_capability_keys (
  id         int primary key default 1 check (id = 1),
  key_hex    text not null,
  created_at timestamptz not null default now()
);
revoke all on table public.owner_capability_keys from anon, authenticated;

-- Who did what, when, with which capability (target enrichments ride the
-- RPC bodies; this table is the who/when spine).
create table if not exists owner_action_audit (
  id            bigserial primary key,
  action        text not null,
  team_code     text not null default '',
  actor_owner   text not null default '',
  capability_id text not null default '',
  created_at    timestamptz not null default now()
);
revoke all on table public.owner_action_audit from anon, authenticated;

-- Owner proves HUMAN knowledge (their portal credential — the SAME bcrypt
-- hash an approved till published via portal_set_credentials) on top of an
-- approved, non-revoked device identity. Returns a signed, team-scoped,
-- short-lived capability token: base64(payload) + '.' + hmac_sha256_hex.
-- The token never grants anything by itself — only privileged RPCs consume
-- it, and only for the team it was minted for.
create or replace function public.owner_mint_capability(
  p_device_id text, p_secret_hash text, p_username text, p_password text,
  p_ttl_minutes int default 15)
returns json language plpgsql security definer
set search_path to 'public', 'extensions'
as $function$
declare dev record; acct record; matched boolean := false;
        team text; owner_name text; v_key text;
        payload text; sig text; exp bigint; jti text; ttl int;
begin
  select * into dev from sync_devices
    where device_id = p_device_id and secret_hash = p_secret_hash
      and approved and not revoked;
  if dev.device_id is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  if dev.team_code is null or dev.team_code = '' then
    return json_build_object('ok', false, 'error', 'device has no store yet');
  end if;
  p_username := btrim(coalesce(p_username, ''));
  if p_username = '' or coalesce(p_password, '') = '' then
    return json_build_object('ok', false, 'error',
      'enter your owner username and password to confirm');
  end if;
  ttl := coalesce(p_ttl_minutes, 15);
  if ttl < 1 then ttl := 1; end if;
  if ttl > 60 then ttl := 60; end if;

  -- brute-force dam per (username, device) — a stolen till cannot spray
  if not public.signin_dam_allow('capmint', lower(p_username), dev.device_id, 10, 15) then
    return json_build_object('ok', false, 'error',
      'too many attempts, try again in 15 minutes');
  end if;

  for acct in select * from portal_accounts
              where lower(username) = lower(p_username) loop
    matched := true;
    if crypt(p_password, acct.password_hash) = acct.password_hash then
      team := acct.team_code;
      owner_name := acct.username;
      exit;
    end if;
  end loop;

  if not matched then
    perform crypt(p_password, '$2a$10$C6UzMDM.H6dfI/f/IKcEeO7VTgxjrpU8k95Lxvtqk1PGCvXnLBDF6');
  end if;

  if team is null or team <> dev.team_code then
    perform public.signin_dam_fail('capmint', lower(p_username), dev.device_id, 15);
    return json_build_object('ok', false, 'error',
      'those owner credentials were not recognized for this store');
  end if;

  -- signing key: minted once, on first use
  insert into owner_capability_keys (id, key_hex)
    values (1, encode(gen_random_bytes(32), 'hex'))
    on conflict (id) do nothing;
  select k.key_hex into v_key from owner_capability_keys k where k.id = 1;

  jti := encode(gen_random_bytes(16), 'hex');
  exp := extract(epoch from now() + make_interval(mins => ttl))::bigint;
  payload := json_build_object(
      'v', 1, 'jti', jti, 'team', team, 'owner', owner_name, 'exp', exp)::text;
  sig := encode(hmac(payload, v_key, 'sha256'), 'hex');
  return json_build_object('ok', true,
      'capability', payload || '.' || sig,
      'teamCode', team, 'owner', owner_name,
      'expiresAt', to_timestamp(exp));
end $function$;

-- INTERNAL verifier used by every privileged RPC. Returns the capability
-- payload when the token is valid, unexpired, correctly signed and scoped
-- to the caller's team; NULL otherwise. Also writes the action audit row.
create or replace function public.owner_capability_check(
  p_capability text, p_action text, p_team text)
returns jsonb language plpgsql security definer
set search_path to 'public', 'extensions'
as $function$
declare parts text[]; payload text; sig text; v_key text; cap jsonb; exp bigint;
begin
  if p_capability is null or btrim(p_capability) = '' or p_team is null then
    return null;
  end if;
  parts := string_to_array(p_capability, '.');
  if array_length(parts, 1) <> 2 then
    return null;
  end if;
  payload := parts[1];
  sig := parts[2];
  select k.key_hex into v_key from owner_capability_keys k where k.id = 1;
  if v_key is null then
    return null;
  end if;
  -- HMAC compare inside the DB: no remote timing oracle at PostgREST
  -- granularity (network jitter dominates); equality is accepted here.
  if encode(hmac(payload, v_key, 'sha256'), 'hex') <> sig then
    return null;
  end if;
  begin
    cap := payload::jsonb;
  exception when others then
    return null;
  end;
  exp := nullif(cap->>'exp', '')::bigint;
  if exp is null or to_timestamp(exp) < now() then
    return null;
  end if;
  if coalesce(cap->>'team', '') <> p_team then
    return null;
  end if;
  begin
    insert into owner_action_audit (action, team_code, actor_owner, capability_id)
      values (p_action, p_team, coalesce(cap->>'owner', ''), coalesce(cap->>'jti', ''));
  exception when others then
    null; -- audit is best-effort; never block the authorized action
  end;
  return cap;
end $function$;

-- ====================================================================
-- (2) SHARED SQL SIGN-IN DAM — atomic, bounded, spray-resistant
-- ====================================================================

-- One row per (kind, identity-hash, source-hash) window. Keys are SHA-256
-- digests: bounded width, no plaintext usernames/ips at rest.
create table if not exists rpc_attempt_buckets (
  kind         text not null,
  ident_hash   text not null,
  source_hash  text not null,
  attempts     int not null default 0,
  window_start timestamptz not null default now(),
  primary key (kind, ident_hash, source_hash)
);
create index if not exists rpc_attempt_buckets_window
  on rpc_attempt_buckets (kind, window_start);
revoke all on table public.rpc_attempt_buckets from anon, authenticated;

create or replace function public.signin_dam_allow(
  p_kind text, p_ident text, p_source text,
  p_max_attempts int default 10, p_window_minutes int default 15)
returns boolean language plpgsql security definer
set search_path to 'public', 'extensions'
as $function$
declare att int; ws timestamptz;
begin
  -- lazy sweep: ~1/20 calls prune day-old rows (no cron dependency)
  if random() < 0.05 then
    delete from rpc_attempt_buckets where window_start < now() - interval '1 day';
  end if;
  select attempts, window_start into att, ws
    from rpc_attempt_buckets
    where kind = p_kind
      and ident_hash = encode(digest(p_ident, 'sha256'), 'hex')
      and source_hash = encode(digest(coalesce(p_source, ''), 'sha256'), 'hex');
  if att is null then
    return true;
  end if;
  if ws < now() - make_interval(mins => greatest(p_window_minutes, 1)) then
    return true;
  end if;
  return att < greatest(p_max_attempts, 1);
end $function$;

create or replace function public.signin_dam_fail(
  p_kind text, p_ident text, p_source text, p_window_minutes int default 15)
returns void language plpgsql security definer
set search_path to 'public', 'extensions'
as $function$
declare w int := greatest(p_window_minutes, 1);
begin
  insert into rpc_attempt_buckets (kind, ident_hash, source_hash, attempts, window_start)
    values (p_kind,
            encode(digest(p_ident, 'sha256'), 'hex'),
            encode(digest(coalesce(p_source, ''), 'sha256'), 'hex'),
            1, now())
    on conflict (kind, ident_hash, source_hash) do update
      set attempts = case when rpc_attempt_buckets.window_start > now() - make_interval(mins => w)
                          then rpc_attempt_buckets.attempts + 1 else 1 end,
          window_start = case when rpc_attempt_buckets.window_start > now() - make_interval(mins => w)
                              then rpc_attempt_buckets.window_start else now() end;
end $function$;

create or replace function public.signin_dam_reset(
  p_kind text, p_ident text, p_source text)
returns void language plpgsql security definer
set search_path to 'public', 'extensions'
as $function$
begin
  delete from rpc_attempt_buckets
    where kind = p_kind
      and ident_hash = encode(digest(p_ident, 'sha256'), 'hex')
      and source_hash = encode(digest(coalesce(p_source, ''), 'sha256'), 'hex');
end $function$;

-- GLOBAL budget across all buckets of a kind within the window: catches
-- distributed sprays that keep each single identity under its own dam.
create or replace function public.signin_dam_global_ok(
  p_kind text, p_budget int default 300, p_window_minutes int default 15)
returns boolean language plpgsql security definer
set search_path to 'public'
as $function$
declare total bigint;
begin
  select coalesce(sum(attempts), 0) into total
    from rpc_attempt_buckets
    where kind = p_kind
      and window_start > now() - make_interval(mins => greatest(p_window_minutes, 1));
  return total < greatest(p_budget, 1);
end $function$;

-- ====================================================================
-- (3) PORTAL LOGIN v2 — dam + multi-store choice + business timezone
-- ====================================================================

-- Per-store IANA business timezone ("sales today" boundary). Africa/Nairobi
-- matches the fleet today; owners change it with one UPDATE when they expand.
alter table sync_stores add column if not exists business_tz text not null default 'Africa/Nairobi';

create or replace function public.portal_login(
  p_username text, p_password text, p_team_code text default null)
returns json language plpgsql security definer
set search_path to 'public', 'extensions'
as $function$
declare acct record; ok_teams text[] := '{}'; team text;
        perf jsonb; cur text; cfg text; tz text; day_start timestamptz;
        store_names jsonb; t text;
begin
  p_username := btrim(coalesce(p_username, ''));
  if p_username = '' or p_password is null or p_password = '' then
    return json_build_object('ok', false, 'error', 'username and password are required');
  end if;

  -- brute-force dam: per-identity window + global spray budget
  if not public.signin_dam_global_ok('portal', 300, 15)
     or not public.signin_dam_allow('portal', lower(p_username), 'web', 10, 15) then
    return json_build_object('ok', false, 'error',
      'too many attempts, try again in 15 minutes');
  end if;

  -- collect EVERY team whose published credential matches (multi-store
  -- owners may legitimately hold the same username on several teams)
  for acct in select * from portal_accounts
              where lower(username) = lower(p_username) loop
    if crypt(p_password, acct.password_hash) = acct.password_hash then
      ok_teams := ok_teams || acct.team_code;
    end if;
  end loop;

  if array_length(ok_teams, 1) is null then
    -- unknown username: burn a dummy bcrypt compare for timing parity
    perform crypt(p_password, '$2a$10$C6UzMDM.H6dfI/f/IKcEeO7VTgxjrpU8k95Lxvtqk1PGCvXnLBDF6');
    perform public.signin_dam_fail('portal', lower(p_username), 'web', 15);
    return json_build_object('ok', false, 'error',
      'wrong username or password, or the portal is not activated yet for your shop');
  end if;

  -- ambiguous identity: never guess. Ask the portal page which store.
  if array_length(ok_teams, 1) > 1 and coalesce(p_team_code, '') = '' then
    select coalesce(jsonb_agg(jsonb_build_object(
               'teamCode', s.team_code, 'storeName', s.name) order by s.id), '[]'::jsonb)
      into store_names
      from sync_stores s where s.team_code = any(ok_teams) and s.active;
    if store_names is null or store_names = '[]'::jsonb then
      perform public.signin_dam_fail('portal', lower(p_username), 'web', 15);
      return json_build_object('ok', false, 'error',
        'wrong username or password, or the portal is not activated yet for your shop');
    end if;
    return json_build_object('ok', false, 'needsStoreChoice', true,
        'teams', store_names);
  end if;

  if coalesce(p_team_code, '') <> '' then
    if not (p_team_code = any(ok_teams)) then
      perform public.signin_dam_fail('portal', lower(p_username), 'web', 15);
      return json_build_object('ok', false, 'error',
        'those credentials do not match that store');
    end if;
    team := p_team_code;
  else
    team := ok_teams[1];
  end if;

  perform public.signin_dam_reset('portal', lower(p_username), 'web');

  -- store identity + currency + business timezone from synced config
  select payload->'values'->>'store_name' into cfg from sync_events
    where team_code = team and entity = 'config' and payload ? 'values'
      and payload->'values' ? 'store_name'
    order by id desc limit 1;
  select payload->'values'->>'currency_code' into cur from sync_events
    where team_code = team and entity = 'config' and payload ? 'values'
      and payload->'values' ? 'currency_code'
    order by id desc limit 1;

  -- business-day boundary in the STORE's timezone (not the DB session's).
  -- Invalid stored tz → fall back to Africa/Nairobi (never error the login).
  select coalesce(s.business_tz, 'Africa/Nairobi') into tz
    from sync_stores s where s.team_code = team;
  begin
    perform now() AT TIME ZONE tz;
  exception when others then
    tz := 'Africa/Nairobi';
  end;
  day_start := date_trunc('day', now() AT TIME ZONE tz) AT TIME ZONE tz;

  -- dedupe the order stream: newest event per client_uuid; skip voided
  with orders as (
    select distinct on (payload->>'clientUuid') payload
    from sync_events
    where team_code = team and entity = 'order' and op = 'upsert'
    order by payload->>'clientUuid', id desc
  ), live as (
    select o.payload from orders o
    where coalesce(o.payload->>'paidAt', '') <> ''
      and not exists (
        select 1 from sync_events v
        where v.team_code = team and v.entity = 'void'
          and v.payload->>'orderClientUuid' = o.payload->>'clientUuid')
  )
  select jsonb_build_object(
    'storeName', coalesce(cfg, ''),
    'currency', coalesce(cur, 'KES'),
    'businessTz', tz,
    'revenueToday', coalesce((select sum((payload->>'totalCents')::bigint) from live
        where (payload->>'paidAt')::timestamptz >= day_start
          and (payload->>'paidAt')::timestamptz < day_start + interval '1 day'), 0),
    'ordersToday', coalesce((select count(*) from live
        where (payload->>'paidAt')::timestamptz >= day_start
          and (payload->>'paidAt')::timestamptz < day_start + interval '1 day'), 0),
    'revenue7d', coalesce((select sum((payload->>'totalCents')::bigint) from live
        where (payload->>'paidAt')::timestamptz >= now() - interval '7 days'), 0),
    'orders7d', coalesce((select count(*) from live
        where (payload->>'paidAt')::timestamptz >= now() - interval '7 days'), 0),
    'revenue30d', coalesce((select sum((payload->>'totalCents')::bigint) from live
        where (payload->>'paidAt')::timestamptz >= now() - interval '30 days'), 0),
    'orders30d', coalesce((select count(*) from live
        where (payload->>'paidAt')::timestamptz >= now() - interval '30 days'), 0),
    'voids30d', (select count(*) from sync_events
        where team_code = team and entity = 'void'
          and created_at >= now() - interval '30 days'),
    'payments', coalesce((
      select jsonb_object_agg(p.method, p.total) from (
        select lower(coalesce(x.payment->>'method', 'other')) as method,
               sum((x.payment->>'amountCents')::bigint) as total
        from live l, jsonb_array_elements(l.payload->'payments') x(payment)
        where (l.payload->>'paidAt')::timestamptz >= now() - interval '30 days'
          and coalesce(x.payment->>'completedAt', '') <> ''
        group by 1) p), '{}'::jsonb),
    'topProducts', coalesce((
      select jsonb_agg(jsonb_build_object('name', t.name, 'qty', t.qty)
             order by t.qty desc) from (
        select i.item->>'name' as name, sum((i.item->>'qty')::int) as qty
        from live l, jsonb_array_elements(l.payload->'items') i(item)
        where (l.payload->>'paidAt')::timestamptz >= now() - interval '30 days'
          and coalesce(i.item->>'name', '') <> ''
        group by 1 order by 2 desc limit 8) t), '[]'::jsonb),
    'cashiers', coalesce((
      select jsonb_agg(jsonb_build_object('name', coalesce(c.cashierName, '?'),
                 'orders', c.cnt, 'revenue', c.rev) order by c.rev desc) from (
        select payload->>'cashierName' as cashierName, count(*) as cnt,
               sum((payload->>'totalCents')::bigint) as rev
        from live
        where (payload->>'paidAt')::timestamptz >= now() - interval '30 days'
        group by 1) c), '[]'::jsonb),
    'devices', (select count(*) from sync_devices
        where team_code = team and not revoked),
    'lastSeen', (select max(last_seen) from sync_devices
        where team_code = team and not revoked),
    'generatedAt', now()
  ) into perf;
  return json_build_object('ok', true, 'teamCode', team, 'performance', perf);
end $function$;

-- ====================================================================
-- (4) OWNER-CAPABILITY GATES on the privileged RPCs
--     (new trailing params carry DEFAULTs — old callers still parse,
--      they just get the 'owner approval required' answer)
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
  if public.owner_capability_check(p_owner_capability, 'store.create', dev.team_code) is null then
    return json_build_object('ok', false, 'error',
      'owner approval required, confirm with your owner password (Settings → Team)');
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
  if public.owner_capability_check(p_owner_capability, 'device.assign', dev.team_code) is null then
    return json_build_object('ok', false, 'error',
      'owner approval required, confirm with your owner password (Settings → Team)');
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
  if public.owner_capability_check(p_owner_capability, 'device.remove', dev.team_code) is null then
    return json_build_object('ok', false, 'error',
      'owner approval required, confirm with your owner password (Settings → Team)');
  end if;
  if p_target_device_id = dev.device_id then
    return json_build_object('ok', false, 'error', 'cannot remove this device while using it');
  end if;
  update sync_devices set revoked = true, approved = false, last_seen = now()
    where device_id = p_target_device_id and not revoked;
  return json_build_object('ok', true, 'device_id', p_target_device_id);
end $function$;

-- v1.3.2: approve ALSO requires owner authority (it can un-revoke —
-- the strongest healing act in the model).
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
  if public.owner_capability_check(p_owner_capability, 'device.approve', dev.team_code) is null then
    return json_build_object('ok', false, 'error',
      'owner approval required, confirm with your owner password (Settings → Team)');
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
  if public.owner_capability_check(p_owner_capability, 'invite.create', dev.team_code) is null then
    return json_build_object('ok', false, 'error',
      'owner approval required, confirm with your owner password (Settings → Team)');
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
  if public.owner_capability_check(p_owner_capability, 'invite.revoke', dev.team_code) is null then
    return json_build_object('ok', false, 'error',
      'owner approval required, confirm with your owner password (Settings → Team)');
  end if;
  update sync_invites set revoked = true
    where id = p_invite_id and team_code = dev.team_code and used_by = '';
  if not found then
    return json_build_object('ok', false, 'error', 'invite not found or already used');
  end if;
  return json_build_object('ok', true);
end $function$;

-- First publication stays device-gated (bootstrap: without it nobody could
-- ever mint the first capability). CHANGING an existing portal credential
-- requires owner authority — that closes the compromised-till takeover path.
create or replace function public.portal_set_credentials(
  p_device_id text, p_secret_hash text, p_username text, p_password_hash text,
  p_owner_capability text default null)
returns json language plpgsql security definer set search_path to 'public', 'extensions'
as $function$
declare dev record; have_account int;
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
  select count(*) into have_account from portal_accounts where team_code = dev.team_code;
  if have_account > 0 then
    if public.owner_capability_check(p_owner_capability, 'portal.credentials.set', dev.team_code) is null then
      return json_build_object('ok', false, 'error',
        'owner approval required, confirm with your owner password (Settings → Team)');
    end if;
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

create or replace function public.sync_approve_pin_reset(
  p_device_id text, p_secret_hash text, p_request_id bigint,
  p_owner_capability text default null)
returns json language plpgsql security definer set search_path to 'public', 'extensions'
as $function$
declare dev record; req record; b bytea; c text;
begin
  select * into dev from sync_devices
    where device_id = p_device_id and secret_hash = p_secret_hash
      and approved and not revoked;
  if dev.device_id is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  if public.owner_capability_check(p_owner_capability, 'pinreset.approve', dev.team_code) is null then
    return json_build_object('ok', false, 'error',
      'owner approval required, confirm with your owner password (Settings → Team)');
  end if;
  select * into req from sync_pin_resets
    where id = p_request_id and team_code = dev.team_code;
  if req.id is null then
    return json_build_object('ok', false, 'error', 'reset request not found');
  end if;
  if req.status <> 'pending' and req.status <> 'approved' then
    return json_build_object('ok', false, 'error', 'this request was already handled');
  end if;
  if req.created_at < now() - interval '7 days' then
    update sync_pin_resets set status = 'cancelled' where id = req.id;
    return json_build_object('ok', false, 'error', 'this request expired, ask them to send it again');
  end if;
  b := gen_random_bytes(4);
  c := lpad((((get_byte(b,0)::bigint * 16777216) + (get_byte(b,1)::bigint * 65536)
            + (get_byte(b,2)::bigint * 256) + get_byte(b,3)::bigint) % 100000000)::text, 8, '0');
  update sync_pin_resets
    set status = 'approved', code_hash = encode(digest(c, 'sha256'), 'hex'),
        expires_at = now() + interval '15 minutes', approved_at = now(), attempts = 0
    where id = req.id;
  return json_build_object('ok', true, 'code', c, 'username', req.username,
    'deviceName', req.device_name, 'expiresMinutes', 15);
end $function$;

-- ====================================================================
-- (5) PAYSTACK WEBHOOK RELAY STORAGE (dual-read migration phase A)
--     Edge Function verifies HMAC-SHA512 with Deno.env('PAYSTACK_SECRET_KEY')
--     (fallback: app_config vault row) and stores the verified event here.
--     Tills claim events for THEIR OWN pending references; the app_config
--     row is deleted only after one full verified payment cycle.
-- ====================================================================

create table if not exists paystack_webhook_events (
  id          bigserial primary key,
  event_type  text not null default '',
  reference   text not null default '',
  payload     jsonb not null,
  claimed_by  text[] not null default '{}',
  claim_batch uuid,
  created_at  timestamptz not null default now()
);
alter table paystack_webhook_events add column if not exists claim_batch uuid;
create index if not exists paystack_webhook_events_ref
  on paystack_webhook_events (reference);
revoke all on table public.paystack_webhook_events from anon, authenticated;

-- Device-gated claim: a till sends ITS pending payment references and gets
-- every not-yet-claimed-by-it event whose reference maps back to an order
-- synced by the SAME team (no cross-tenant event leak). Each claim marks
-- the event so one till never receives the same webhook twice.
create or replace function public.paystack_claim_events(
  p_device_id text, p_secret_hash text, p_references text[])
returns json language plpgsql security definer set search_path to 'public', 'extensions'
as $function$
declare dev record; out_rows json; v_batch uuid;
begin
  select * into dev from sync_devices
    where device_id = p_device_id and secret_hash = p_secret_hash
      and approved and not revoked;
  if dev.device_id is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  if dev.team_code is null or dev.team_code = '' then
    return json_build_object('ok', false, 'error', 'device has no store yet');
  end if;
  if p_references is null or array_length(p_references, 1) is null then
    return json_build_object('ok', true, 'events', '[]'::json);
  end if;
  -- occasional cleanup (7-day retention, no cron)
  if random() < 0.05 then
    delete from paystack_webhook_events where created_at < now() - interval '7 days';
  end if;
  -- mark first (atomic, idempotent per device), then read back ONLY the
  -- rows this exact call newly claimed (claim_batch), so a device receives
  -- each event exactly once even across repeated polls.
  v_batch := gen_random_uuid();
  update paystack_webhook_events w
    set claimed_by = claimed_by || p_device_id, claim_batch = v_batch
    where w.reference = any(p_references)
      and not (w.claimed_by @> array[p_device_id])
      and exists (
        select 1 from sync_events se
        where se.team_code = dev.team_code
          and se.entity = 'order' and se.op = 'upsert'
          and se.payload->>'clientUuid' = w.reference);
  -- read back this call's claims (same-team filter re-applied so a claim
  -- can never leak another team's event, even on reference collision)
  select coalesce(json_agg(json_build_object(
      'id', w.id, 'eventType', w.event_type, 'reference', w.reference,
      'payload', w.payload, 'createdAt', w.created_at) order by w.id), '[]'::json)
    into out_rows
    from paystack_webhook_events w
    where w.claim_batch = v_batch
      and w.claimed_by @> array[p_device_id]
      and exists (
        select 1 from sync_events se
        where se.team_code = dev.team_code
          and se.entity = 'order' and se.op = 'upsert'
          and se.payload->>'clientUuid' = w.reference);
  return json_build_object('ok', true, 'events', out_rows);
end $function$;

-- Cloud-side switch for the payment proxy flip (public: tills read it via
-- the anon config path and start routing init/verify through the Edge
-- Function the moment the owner turns it on — before the vault row retires).
insert into app_config (key, value, is_secret) values
  ('paystack_via_cloud', 'false', false)
on conflict (key) do update set is_secret = false, updated_at = now();
