-- LedgerPOS — deploy v1.2.0 to the live fleet cloud (Supabase project ixxiqrobcwkvyjtxdkvh).
--
-- Run ONCE in the Supabase SQL editor (or via the Management API) — it
-- (1) applies the owner cloud sign-in RPC (db/cloud_schema_v116.sql) and
-- (2) publishes the v1.2.0 OTA manifest so tills self-update from
--     Settings → System (GitHub Releases stays as the fallback channel).
--
-- Idempotent: every statement is CREATE OR REPLACE / ON CONFLICT — safe to
-- re-run. After running, verify with the queries at the bottom.

-- =====================================================================
-- (1) owner_device_signin — owner cloud sign-in on a fresh till
--     (mirrors db/cloud_schema_v116.sql)
-- =====================================================================

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
  if p_device_id is null or length(p_device_id) < 8
     or p_secret_hash is null or length(p_secret_hash) < 32 then
    return json_build_object('ok', false, 'error', 'invalid identity');
  end if;
  p_username := btrim(coalesce(p_username, ''));
  if p_username = '' or coalesce(p_password, '') = '' then
    return json_build_object('ok', false, 'error', 'username and password are required');
  end if;

  select attempts into fails from portal_signin_fails
    where username = p_username and window_start > now() - interval '15 minutes';
  if coalesce(fails, 0) >= 10 then
    return json_build_object('ok', false, 'error',
      'too many attempts — try again in 15 minutes');
  end if;

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
    perform crypt(p_password, '$2a$10$C6UzMDM.H6dfI/f/IKcEeO7VTgxjrpU8k95Lxvtqk1PGCvXnLBDF6');
  end if;

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

-- =====================================================================
-- (2) OTA manifest → v1.2.0 (SHA-256s match the GitHub release assets
--     and downloads/checksums.txt in the repo)
-- =====================================================================

select public.upsert_app_config('update_manifest_tag', 'v1.2.0', false);
select public.upsert_app_config('update_manifest_notes',
  '1.2.0 — owner sign-in on a fresh till (no more forced store creation), X/Z cash reports, purchase-order receiving syncs stock to every till, eTIMS-ready tax invoices (KRA PIN + buyer PIN), multi-store consolidated daily report, compact checkout rail, contrast fixes.', false);
select public.upsert_app_config('update_manifest_assets', '[
  {"name":"ledgerpos-setup-windows-x64.exe","url":"https://github.com/ssmurfgg04-gif/pos-system/releases/download/v1.2.0/ledgerpos-setup-windows-x64.exe","sha256":"59264dad693004766befd30c496ae3526370f9101f5308c10ef0bc4538f956be"},
  {"name":"ledgerpos-macos-apple-silicon.zip","url":"https://github.com/ssmurfgg04-gif/pos-system/releases/download/v1.2.0/ledgerpos-macos-apple-silicon.zip","sha256":"a275fffc034ee68f656659447312370078f0bcb6554f72c9d6df6f18bd60f790"},
  {"name":"ledgerpos-macos-intel.zip","url":"https://github.com/ssmurfgg04-gif/pos-system/releases/download/v1.2.0/ledgerpos-macos-intel.zip","sha256":"94c678f5323355b1ec347cc9e7a5533575e2f9adfcf7a288a0f34cc46f3920c1"},
  {"name":"ledgerpos-linux-x64.tar.xz","url":"https://github.com/ssmurfgg04-gif/pos-system/releases/download/v1.2.0/ledgerpos-linux-x64.tar.xz","sha256":"97ec5b1293ee18341d77bc026b70126fd4f960338b2c911cc5788c5d8b49fd55"}
]'::text, false);

-- =====================================================================
-- Verify:
--   select key, left(value, 60) from app_config
--     where key like 'update_manifest_%';
--   select p_version, ok from (
--     select owner_device_signin('testdevicexx',
--       repeat('a',64), 't', '1.2.0', 'nobody', 'x') as r) t,
--     (select (t.r->>'error') is not null as ok) s;  -- expect error text, ok=true
-- =====================================================================


-- =====================================================================
-- (2) OTA manifest -> v1.2.1 (SHA-256s match the GitHub release assets
--     and downloads/checksums.txt in the repo)
-- =====================================================================

select public.upsert_app_config('update_manifest_tag', 'v1.2.1', false);
select public.upsert_app_config('update_manifest_notes',
  '1.2.1 — product photos and the shop logo now sync to every till (uploaded ones backfill automatically), plus a deep money & sync review: voids refund store credit and kill gift-card codes, tab limits are enforced atomically, checkout money movements follow the team, receipts print every payment leg, offline sales are only discarded on explicit server refusal, and the installer is 8.3 MB again.', false);
select public.upsert_app_config('update_manifest_assets', '[
  {"name":"ledgerpos-setup-windows-x64.exe","url":"https://github.com/ssmurfgg04-gif/pos-system/releases/download/v1.2.1/ledgerpos-setup-windows-x64.exe","sha256":"de6a5efdbdd61d86575261b2e70d739624eea1a8cb7b0e41b3dd1bc4831a8d88"},
  {"name":"ledgerpos-macos-apple-silicon.zip","url":"https://github.com/ssmurfgg04-gif/pos-system/releases/download/v1.2.1/ledgerpos-macos-apple-silicon.zip","sha256":"c439218847950daa9f63a91c478c65c0b163e30c88c545fa101c1835c0b1d8ac"},
  {"name":"ledgerpos-macos-intel.zip","url":"https://github.com/ssmurfgg04-gif/pos-system/releases/download/v1.2.1/ledgerpos-macos-intel.zip","sha256":"6cebff60f738929b0dbf65a876f425dd884123bc0d9bda580b8338cb46035f68"},
  {"name":"ledgerpos-linux-x64.tar.xz","url":"https://github.com/ssmurfgg04-gif/pos-system/releases/download/v1.2.1/ledgerpos-linux-x64.tar.xz","sha256":"950809a709f680f7e8301edf3f8fa327fea196716b254b8cb1ac40684fabcefc"}
]'::text, false);

-- =====================================================================
-- Verify:
--   select key, left(value, 60) from app_config
--     where key like 'update_manifest_%';
-- =====================================================================
