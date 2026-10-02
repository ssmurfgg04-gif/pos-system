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
