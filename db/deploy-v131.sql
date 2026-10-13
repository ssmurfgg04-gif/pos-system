-- LedgerPOS — deploy v1.3.1 cloud fixes (Supabase project ixxiqrobcwkvyjtxdkvh).
--
-- Two targeted repairs, both idempotent (safe to re-run):
--
-- (1) sync_register — the canonical cloud_schema.sql accidentally carried a
--     copy-pasted invite-claim block referencing an undeclared variable
--     (inv.id). Any FRESH provisioning or re-apply of the master file would
--     break new-till registration with "column inv.id does not exist". The
--     live database still runs the pre-corruption body, so this deploy makes
--     live match the repaired canonical file (invite claiming stays
--     exclusively in sync_join_team, where the token parameter exists).
--
-- (2) sync_device_config — till-scoped vault. The RPC now returns ONLY the
--     key families a till consumes (paystack_*, update_manifest_*) instead
--     of every app_config row. Device approval must not hand a till secrets
--     it has no job for (off-site backup keys, future infrastructure
--     credentials). Current keys are 100% covered — zero behavior change
--     for the live fleet; this is forward-hardening.
--
-- After running, verify with the queries at the bottom.

-- ============================================================
-- (1) sync_register — repaired canonical body
-- ============================================================
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
  -- NOTE: no invite handling here — register is the zero-config / owner-
  -- assignment path. Invite tokens are claimed exclusively by
  -- sync_join_team (which takes p_token).
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
  return json_build_object('ok', true, 'approved', false, 'pending', true);
end $function$;

-- ============================================================
-- (2) sync_device_config — till-scoped vault
-- ============================================================
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
    from app_config c
    where c.key like 'paystack_%' or c.key like 'update_manifest_%';
  return json_build_object('ok', true, 'config', rows);
end $function$;

-- =====================================================================
-- Verify (run these AFTER the deploy):
--   -- register works for a fresh identity (then delete the row):
--   --   select public.sync_register('dev-verify-register-131',
--   --        encode(digest('verify-secret-131','sha256'),'hex'),
--   --        'Verify Probe', '1.3.1');
--   --   delete from sync_devices where device_id = 'dev-verify-register-131';
--   -- vault scoping: the returned config must contain ONLY paystack_* /
--   -- update_manifest_* keys even though app_config may hold others later.
-- =====================================================================
