-- LedgerPOS cloud schema v1.1.2 (applied live; keep in sync with db/cloud_schema.sql).
-- Adds: team join links with role (sync_invites), owner portal accounts
-- (portal_accounts), device approval RPC, and the extensions they need.

-- ------------------------------------------------------------ extensions --
create extension if not exists pgcrypto;   -- crypt()/gen_salt('bf') for portal passwords

-- -------------------------------------------------------------- invites --
-- Owner-generated team join links. The LINK carries the role for display;
-- the token is the authority (single-use, expiring, DB-minted). Only the
-- SHA-256 of the token is stored, so the cloud can never mint a link.
create table if not exists sync_invites (
  id          bigserial primary key,
  team_code   text not null,
  token_hash  text unique not null,
  role_name   text not null,
  permissions jsonb not null default '[]'::jsonb,
  note        text not null default '',
  created_by  text not null default '',
  created_at  timestamptz not null default now(),
  expires_at  timestamptz not null,
  used_by     text not null default '',
  used_at     timestamptz,
  revoked     boolean not null default false
);

alter table sync_invites enable row level security;
-- no anon policies: invites are reachable only through owner/device RPCs

-- ---------------------------------------------------------- portal login --
-- One row per team: the credentials an owner-level user last saved on an
-- approved till (bcrypt hash generated on the till — the plaintext never
-- leaves that machine). The public website's owner portal verifies against
-- this row and reads nothing else.
create table if not exists portal_accounts (
  team_code     text unique not null,
  username      text not null,
  password_hash text not null,
  updated_at    timestamptz not null default now()
);

alter table portal_accounts enable row level security;
-- no anon policies: credentials only through identity-checked RPCs

-- ---------------------------------------------------- approve device RPC --
-- Owner approves a pending device from the roster (auto-approve is off).
create or replace function public.sync_approve_device(
  p_device_id text, p_secret_hash text, p_target_device_id text)
returns json language plpgsql security definer set search_path to 'public'
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
  if tgt.revoked then
    return json_build_object('ok', false, 'error', 'device is revoked');
  end if;
  update sync_devices set approved = true, last_seen = now()
    where device_id = tgt.device_id;
  return json_build_object('ok', true, 'device_id', tgt.device_id);
end $function$;

-- ---------------------------------------------------------- invite RPCs --
-- Owner mints a join link: the DB invents the token, stores only its hash.
create or replace function public.sync_create_invite(
  p_device_id text, p_secret_hash text, p_role_name text,
  p_permissions jsonb default '[]'::jsonb, p_note text default '', p_days int default 7)
returns json language plpgsql security definer set search_path to 'public'
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

-- Owner lists the invites for their team.
create or replace function public.sync_list_invites(
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
      'id', i.id, 'role_name', i.role_name, 'note', i.note,
      'created_at', i.created_at, 'expires_at', i.expires_at,
      'used_by', i.used_by, 'used_at', i.used_at, 'revoked', i.revoked)
      order by i.id desc), '[]'::jsonb) into out_rows
    from sync_invites i
    where i.team_code = dev.team_code
      and i.created_at > now() - interval '30 days';
  return json_build_object('ok', true, 'invites', out_rows);
end $function$;

-- Owner kills an unexpired invite.
create or replace function public.sync_revoke_invite(
  p_device_id text, p_secret_hash text, p_invite_id bigint)
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
  update sync_invites set revoked = true
    where id = p_invite_id and team_code = dev.team_code and used_by = '';
  if not found then
    return json_build_object('ok', false, 'error', 'invite not found or already used');
  end if;
  return json_build_object('ok', true);
end $function$;

-- A new till redeems a join link. The token is the authority: it decides
-- the team AND the role (the link's r= is display-only). The joining device
-- is approved immediately — the owner generated this invite on purpose.
create or replace function public.sync_join_team(
  p_device_id text, p_secret_hash text, p_token text)
returns json language plpgsql security definer set search_path to 'public'
as $function$
declare inv record; cur record; st text; perms jsonb; team text; role text;
begin
  if p_device_id is null or length(p_device_id) < 8
     or p_secret_hash is null or length(p_secret_hash) < 32
     or p_token is null or length(btrim(p_token)) < 12 then
    return json_build_object('ok', false, 'error', 'invalid join request');
  end if;
  select * into inv from sync_invites
    where token_hash = btrim(p_token) and not revoked
      and expires_at > now() and used_by = '';
  if inv.id is null then
    return json_build_object('ok', false, 'error',
        'this invite is invalid, already used, or expired — ask the owner for a fresh link');
  end if;
  select * into cur from sync_devices where device_id = p_device_id;
  if cur.device_id is not null then
    if cur.revoked then
      return json_build_object('ok', false, 'error', 'device revoked');
    end if;
    if cur.secret_hash <> '' and cur.secret_hash <> p_secret_hash then
      return json_build_object('ok', false, 'error', 'identity mismatch');
    end if;
    update sync_devices set team_code = inv.team_code, approved = true,
        device_name = coalesce(nullif(device_name, ''), 'till'), last_seen = now()
      where device_id = p_device_id;
  else
    insert into sync_devices (device_id, team_code, device_name, app_version, secret_hash, approved, last_seen)
      values (p_device_id, inv.team_code, 'till', '', p_secret_hash, true, now());
  end if;
  update sync_invites set used_by = p_device_id, used_at = now() where id = inv.id;
  select name into st from sync_stores where team_code = inv.team_code;
  -- typed locals: a JSONB field read straight off an untyped RECORD breaks
  -- json_build_object ("anonymous composite types" — repro'd live)
  team := inv.team_code; role := inv.role_name; perms := inv.permissions;
  return json_build_object('ok', true, 'team_code', team,
      'store_name', coalesce(st, ''), 'role_name', role,
      'permissions', perms);
end $function$;

-- ----------------------------------------------------------- portal RPCs --
-- An approved till publishes its owner-level credentials (bcrypt hash made
-- locally; plaintext never leaves the machine).
create or replace function public.portal_set_credentials(
  p_device_id text, p_secret_hash text, p_username text, p_password_hash text)
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

-- Public owner login for the website portal: verifies bcrypt and returns
-- this team's shop performance replayed from the synced order stream.
create or replace function public.portal_login(p_username text, p_password text)
returns json language plpgsql security definer set search_path to 'public', 'extensions'
as $function$
declare acct record; ok_team text; perf jsonb; cur text; cfg text;
begin
  p_username := btrim(coalesce(p_username, ''));
  if p_username = '' or p_password is null or p_password = '' then
    return json_build_object('ok', false, 'error', 'username and password are required');
  end if;
  for acct in select * from portal_accounts where username = p_username loop
    if crypt(p_password, acct.password_hash) = acct.password_hash then
      ok_team := acct.team_code;
      exit;
    end if;
  end loop;
  if ok_team is null then
    return json_build_object('ok', false, 'error', 'wrong username or password — or the portal is not activated yet for your shop');
  end if;
  -- shop identity + currency from the latest synced config
  select payload->'values'->>'store_name' into cfg from sync_events
    where team_code = ok_team and entity = 'config' and payload ? 'values'
      and payload->'values' ? 'store_name'
    order by id desc limit 1;
  select payload->'values'->>'currency_code' into cur from sync_events
    where team_code = ok_team and entity = 'config' and payload ? 'values'
      and payload->'values' ? 'currency_code'
    order by id desc limit 1;
  -- dedupe the order stream: newest event per client_uuid; skip voided
  with orders as (
    select distinct on (payload->>'clientUuid') payload
    from sync_events
    where team_code = ok_team and entity = 'order' and op = 'upsert'
    order by payload->>'clientUuid', id desc
  ), live as (
    select o.payload from orders o
    where coalesce(o.payload->>'paidAt', '') <> ''
      and not exists (
        select 1 from sync_events v
        where v.team_code = ok_team and v.entity = 'void'
          and v.payload->>'orderClientUuid' = o.payload->>'clientUuid')
  )
  select jsonb_build_object(
    'storeName', coalesce(cfg, ''),
    'currency', coalesce(cur, 'KES'),
    'revenueToday', coalesce((select sum((payload->>'totalCents')::bigint) from live
        where (payload->>'paidAt')::timestamptz >= date_trunc('day', now())), 0),
    'ordersToday', coalesce((select count(*) from live
        where (payload->>'paidAt')::timestamptz >= date_trunc('day', now())), 0),
    'revenue7d', coalesce((select sum((payload->>'totalCents')::bigint) from live
        where (payload->>'paidAt')::timestamptz >= now() - interval '7 days'), 0),
    'orders7d', coalesce((select count(*) from live
        where (payload->>'paidAt')::timestamptz >= now() - interval '7 days'), 0),
    'revenue30d', coalesce((select sum((payload->>'totalCents')::bigint) from live
        where (payload->>'paidAt')::timestamptz >= now() - interval '30 days'), 0),
    'orders30d', coalesce((select count(*) from live
        where (payload->>'paidAt')::timestamptz >= now() - interval '30 days'), 0),
    'voids30d', (select count(*) from sync_events
        where team_code = ok_team and entity = 'void'
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
        where team_code = ok_team and not revoked),
    'lastSeen', (select max(last_seen) from sync_devices
        where team_code = ok_team and not revoked),
    'generatedAt', now()
  ) into perf;
  return json_build_object('ok', true, 'teamCode', ok_team, 'performance', perf);
end $function$;
