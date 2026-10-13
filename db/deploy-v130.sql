-- LedgerPOS — deploy v1.3.0 to the live fleet cloud (Supabase project ixxiqrobcwkvyjtxdkvh).
--
-- Run ONCE in the Supabase SQL editor (or via the Management API) — it
-- (1) applies the v1.3.0 cloud RPCs (db/cloud_schema_v117.sql): the
--     device un-brick (join token / owner credential / owner Approve all
--     heal a revoked device) + the mom-friendly forgot-PIN RPCs, and
-- (2) publishes the v1.3.0 OTA manifest so tills self-update from
--     Settings → System (GitHub Releases stays as the fallback channel).
--
-- Idempotent: every statement is CREATE OR REPLACE / IF NOT EXISTS — safe
-- to re-run. After running, verify with the queries at the bottom.

-- ------------------------------------------------------------ extensions --
create extension if not exists pgcrypto;   -- digest()/gen_random_bytes()

-- ============================================================ UN-BRICK ===

-- Owner approves a pending device from the roster (Settings → Team).
-- v1.3.0: Approve on a REVOKED device un-revokes it — previously this RPC
-- refused revoked targets, which made the "device revoked" state a
-- permanent brick with the UI's own advice pointing here.
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
  -- Revoked is no longer terminal: the owner is the authority, and this
  -- call IS the owner. Un-revoke + approve in one stroke (the roster now
  -- labels this "Approve again" for revoked rows).
  update sync_devices set approved = true, revoked = false, last_seen = now()
    where device_id = tgt.device_id;
  return json_build_object('ok', true, 'device_id', tgt.device_id);
end $function$;

-- A till redeems a join link. The token is the authority: it decides the
-- team AND the role (the link's r= is display-only). The joining device is
-- approved immediately — the owner generated this invite on purpose.
-- v1.3.0: a valid token also RESURRECTS a revoked device (fresh approval +
-- secret rotation). Same authority that approves a brand-new device; the
-- old row stays on the roster with its history, now usable again.
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
  -- Claim the invite atomically FIRST (check-then-act on used_by would let
  -- two concurrent joins share one single-use token under READ COMMITTED).
  update sync_invites set used_by = p_device_id, used_at = now()
    where id = inv.id and used_by = '';
  if not found then
    return json_build_object('ok', false, 'error',
        'this invite is invalid, already used, or expired — ask the owner for a fresh link');
  end if;
  select * into cur from sync_devices where device_id = p_device_id;
  if cur.device_id is not null then
    if cur.revoked then
      -- The token proves owner consent (single-use, expiring, minted on an
      -- approved till, hand-delivered). Heal the device: un-revoke, rotate
      -- the secret to the one just presented, back on the roster.
      update sync_devices
        set team_code = inv.team_code, approved = true, revoked = false,
            secret_hash = p_secret_hash,
            device_name = coalesce(nullif(device_name, ''), 'till'), last_seen = now()
        where device_id = p_device_id;
    elsif cur.secret_hash <> '' and cur.secret_hash <> p_secret_hash then
      return json_build_object('ok', false, 'error', 'identity mismatch');
    else
      update sync_devices set team_code = inv.team_code, approved = true,
          device_name = coalesce(nullif(device_name, ''), 'till'), last_seen = now()
        where device_id = p_device_id;
    end if;
  else
    insert into sync_devices (device_id, team_code, device_name, app_version, secret_hash, approved, last_seen)
      values (p_device_id, inv.team_code, 'till', '', p_secret_hash, true, now());
  end if;
  select name into st from sync_stores where team_code = inv.team_code;
  -- typed locals: a JSONB field read straight off an untyped RECORD breaks
  -- json_build_object ("anonymous composite types" — repro'd live)
  team := inv.team_code; role := inv.role_name; perms := inv.permissions;
  return json_build_object('ok', true, 'team_code', team,
      'store_name', coalesce(st, ''), 'role_name', role,
      'permissions', perms);
end $function$;

-- Owner cloud sign-in (fresh till for an existing business).
-- v1.3.0: valid portal credentials now resurrect a revoked device too —
-- the credential is published by an approved till on every owner password
-- change, so presenting it IS owner consent. (Previously this returned
-- "this device was revoked — ask the owner to approve it again", while
-- the approve RPC itself refused revoked targets: a closed loop.)
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
        -- Verified owner credential heals a revoked device (rotate secret,
        -- back on the roster). Revocation still cuts off every path that
        -- does NOT hold this credential or a fresh owner invite.
        update sync_devices
          set team_code = acct.team_code, approved = true, revoked = false,
              secret_hash = p_secret_hash, last_seen = now(),
              device_name = coalesce(nullif(btrim(p_device_name), ''), dev.device_name),
              app_version = coalesce(nullif(btrim(p_app_version), ''), dev.app_version)
          where device_id = p_device_id;
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

-- ===================================================== FORGOT PIN FLOW ===

-- One row per (device, username) request. The code itself is shown ONCE on
-- the approving owner till; only its SHA-256 lands here.
create table if not exists sync_pin_resets (
  id          bigserial primary key,
  team_code   text not null,
  device_id   text not null,
  device_name text not null default '',
  username    text not null,
  status      text not null default 'pending',  -- pending|approved|used|cancelled
  code_hash   text not null default '',
  expires_at  timestamptz,
  attempts    int not null default 0,
  created_at  timestamptz not null default now(),
  approved_at timestamptz,
  used_at     timestamptz
);
alter table sync_pin_resets enable row level security;
revoke all on table public.sync_pin_resets from anon, authenticated;
-- one ACTIVE request per (device, username)
create unique index if not exists sync_pin_resets_one_active
  on sync_pin_resets (device_id, username) where status = 'pending';

-- A locked till files a reset request for one of its local usernames.
-- Device must be an approved team member (a revoked till's escape hatch is
-- the join link / owner sign-in above, not this).
create or replace function public.sync_request_pin_reset(
  p_device_id text, p_secret_hash text, p_username text, p_device_name text)
returns json language plpgsql security definer set search_path to 'public'
as $function$
declare dev record; rid bigint; uname text;
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
  uname := btrim(coalesce(p_username, ''));
  if uname = '' or length(uname) > 60 then
    return json_build_object('ok', false, 'error', 'enter the username to recover');
  end if;
  -- reuse the active request when one is already open for this user
  select id into rid from sync_pin_resets
    where device_id = p_device_id and username = uname
      and status = 'pending'
      and created_at > now() - interval '7 days'
    limit 1;
  if rid is not null then
    return json_build_object('ok', true, 'request_id', rid, 'reused', true);
  end if;
  insert into sync_pin_resets (team_code, device_id, device_name, username)
    values (dev.team_code, p_device_id, coalesce(nullif(btrim(p_device_name), ''), dev.device_name), uname);
  return json_build_object('ok', true, 'request_id', currval('sync_pin_resets_id_seq'), 'reused', false);
end $function$;

-- Owner till lists its team's open reset requests (Settings → Team).
create or replace function public.sync_list_pin_resets(
  p_device_id text, p_secret_hash text)
returns json language plpgsql security definer set search_path to 'public'
as $function$
declare dev record; out_rows json;
begin
  select * into dev from sync_devices
    where device_id = p_device_id and secret_hash = p_secret_hash
      and approved and not revoked;
  if dev.device_id is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  -- Pending + still-relevant approved requests (an approved code expires in
  -- 15 minutes; the owner must be able to find the row again to re-mint).
  select coalesce(json_agg(json_build_object(
      'id', r.id, 'deviceId', r.device_id, 'deviceName', r.device_name,
      'username', r.username, 'status', r.status,
      'createdAt', r.created_at, 'expiresAt', r.expires_at) order by r.id), '[]'::json)
    into out_rows
    from sync_pin_resets r
    where r.team_code = dev.team_code
      and r.status in ('pending', 'approved')
      and r.created_at > now() - interval '7 days';
  return json_build_object('ok', true, 'requests', out_rows);
end $function$;

-- Owner approves a request: mints a single-use 8-digit code (15 minutes,
-- bound to the requesting device, 6 tries max) and returns it exactly once.
create or replace function public.sync_approve_pin_reset(
  p_device_id text, p_secret_hash text, p_request_id bigint)
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
    return json_build_object('ok', false, 'error', 'this request expired — ask them to send it again');
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

-- The locked till redeems the code the owner read out, then resets the
-- local PIN. Bound to the requesting device + secret, single-use, 15
-- minutes, max 6 wrong tries (then the request cancels itself).
-- v1.3.1: the standard device-identity prologue is ENFORCED here (it was
-- the only RPC without one — plpgsql defaults to PUBLIC EXECUTE, so an
-- anon caller could burn codes or probe request ids); and Approve can
-- re-mint an APPROVED code (expired / read to a wrong number) instead of
-- the UI's "tap Approve again" advice dead-ending on non-pending rows.
create or replace function public.sync_redeem_pin_reset(
  p_device_id text, p_secret_hash text, p_request_id bigint, p_code text)
returns json language plpgsql security definer set search_path to 'public', 'extensions'
as $function$
declare req record; dev record;
begin
  select * into dev from sync_devices
    where device_id = p_device_id and secret_hash = p_secret_hash
      and approved and not revoked;
  if dev.device_id is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  select * into req from sync_pin_resets
    where id = p_request_id and device_id = p_device_id;
  if req.id is null then
    return json_build_object('ok', false, 'error', 'reset request not found — send a new one from this screen');
  end if;
  if req.status = 'used' then
    return json_build_object('ok', false, 'error', 'this code was already used');
  end if;
  if req.status <> 'approved' then
    return json_build_object('ok', false, 'error', 'no approved code yet — the owner still needs to tap Approve');
  end if;
  if req.expires_at is null or req.expires_at < now() then
    return json_build_object('ok', false, 'error', 'this code expired — tap Approve on the owner till again');
  end if;
  if req.attempts >= 6 then
    update sync_pin_resets set status = 'cancelled' where id = req.id;
    return json_build_object('ok', false, 'error', 'too many wrong tries — send a fresh request');
  end if;
  if encode(digest(btrim(coalesce(p_code, '')), 'sha256'), 'hex') <> req.code_hash then
    update sync_pin_resets set attempts = attempts + 1 where id = req.id;
    return json_build_object('ok', false, 'error', 'that code is not right — check the digits and try again');
  end if;
  update sync_pin_resets set status = 'used', used_at = now() where id = req.id;
  return json_build_object('ok', true, 'username', req.username);
end $function$;


-- =====================================================================
-- (2) OTA manifest -> v1.3.0 (SHA-256s match the GitHub release assets
--     and downloads/checksums.txt in the repo)
-- =====================================================================

select public.upsert_app_config('update_manifest_tag', 'v1.3.0', false);
select public.upsert_app_config('update_manifest_notes',
  '1.3.0 — quotations (draft → sent → accepted → move to till, synced across machines, printable A4 offers), locked-out recovery (Forgot PIN? → owner approves → one-time 8-digit code), revoked tills can rejoin with a fresh join link, plus a deep money & sync review: loyalty no longer mints on voided synced sales, cross-shop data can never ride a till that switches teams, photo-sync reliability fix, tax-rate guardrails.', false);
select public.upsert_app_config('update_manifest_assets', '[
  {"name":"ledgerpos-setup-windows-x64.exe","url":"https://github.com/ssmurfgg04-gif/pos-system/releases/download/v1.3.0/ledgerpos-setup-windows-x64.exe","sha256":"8e3dbe28382289a86a58749b15cd36e734a958713ae6c0daa1b2f32250494141"},
  {"name":"ledgerpos-macos-apple-silicon.zip","url":"https://github.com/ssmurfgg04-gif/pos-system/releases/download/v1.3.0/ledgerpos-macos-apple-silicon.zip","sha256":"d5f455d8f02987c4ac18596de8728244ea269ff2704ff6f8883a3d0942d9d9db"},
  {"name":"ledgerpos-macos-intel.zip","url":"https://github.com/ssmurfgg04-gif/pos-system/releases/download/v1.3.0/ledgerpos-macos-intel.zip","sha256":"0e26b7abf301ad6f7d22c6f325357dcbc32d3ac91aabfd0fc3c38957e55b5566"},
  {"name":"ledgerpos-linux-x64.tar.xz","url":"https://github.com/ssmurfgg04-gif/pos-system/releases/download/v1.3.0/ledgerpos-linux-x64.tar.xz","sha256":"31e9b43065df108e40ae7b2e381ebb2033a1a7f328cd41358dc70a6d8d020c18"}
]'::text, false);

-- =====================================================================
-- Verify (run these AFTER the deploy):
--   select key, left(value, 80) from app_config where key like 'update_manifest_%';
--   -- the EXACT read tills perform (anon PostgREST):
--   --   curl "https://ixxiqrobcwkvyjtxdkvh.supabase.co/rest/v1/app_config?select=key,value&key=update_manifest_tag" -H "apikey: <anon>"
--   -- expect: update_manifest_tag = v1.3.0
--   -- paystack secret must stay hidden from the same anon read.
-- =====================================================================
