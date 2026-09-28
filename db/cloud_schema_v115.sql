-- LedgerPOS cloud schema v1.1.5 (applied live; keep in sync with db/cloud_schema.sql).
-- Completes the central key vault: sync_device_config — the device-gated
-- RPC that hands the full app_config map (Paystack secret key included)
-- to APPROVED tills only. Public rows keep flowing through the existing
-- anon-read policy (is_secret = false); secrets flow ONLY through here.
--
-- SECURITY MODEL (app_config)
--   * RLS is enabled with a single policy: anon can SELECT is_secret=false
--     rows (public key, callback URL, currency, update manifest).
--   * Secret rows (is_secret=true) are invisible to anon; the ONLY path is
--     this security-definer RPC, which authenticates the device exactly
--     like every other sync RPC: (device_id, secret_hash) must match an
--     approved, non-revoked sync_devices row. Revoking a till instantly
--     cuts it off from future key fetches.
--   * Write access is revoked from anon/authenticated at the grant level
--     (defense in depth on top of RLS): config is managed by the owner via
--     service_role/dashboard/CI only.

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
