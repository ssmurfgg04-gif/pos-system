-- LedgerPOS cloud schema v1.1.4 (applied live; keep in sync with db/cloud_schema.sql).
-- Adds: the central key vault (app_config) — payment and deployment
-- secrets live in the cloud, every approved device fetches them through
-- the device-gated RPC. Rotation is a SQL UPDATE here, never a site visit.
--
-- SECURITY MODEL
--   * app_config has RLS enabled with NO policies: the anon key cannot
--     select a single row, ever.
--   * Secret rows are stored as pgcrypto PGP ciphertext (enc_value); the
--     decrypt passphrase lives only inside the security-definer function
--     bodies (visible to postgres role / the dashboard, never to PostgREST).
--   * sync_device_config authenticates a device the same way every other
--     sync RPC does: (device_id, secret_hash) must match an approved,
--     non-revoked sync_devices row. Revoking a till instantly cuts it off
--     from future key fetches.
--   * upsert_app_config is owner/CI-only: execute revoked from anon +
--     authenticated so a leaked anon key can never rewrite payment config.

-- ---------------------------------------------------------- key vault ----
create table if not exists app_config (
  key        text primary key,
  value      text,                -- plaintext for public config only
  enc_value  bytea,               -- pgp ciphertext for secret rows
  updated_at timestamptz not null default now()
);

alter table app_config enable row level security;
-- no policies: reachable ONLY through the security-definer RPCs below

-- Owner/CI upsert. p_secret=true stores PGP ciphertext (value ignored).
create or replace function public.upsert_app_config(
  p_key text, p_value text, p_secret boolean default false)
returns json language plpgsql security definer
set search_path to 'public', 'extensions'
as $function$
begin
  if p_key is null or btrim(p_key) = '' then
    return json_build_object('ok', false, 'error', 'key required');
  end if;
  if coalesce(p_secret, false) then
    insert into app_config (key, value, enc_value)
      values (btrim(p_key), null, pgp_sym_encrypt(coalesce(p_value, ''), 'NeGxweTqlv6l4yxQvVv30rH4EFqkLF4q'))
      on conflict (key) do update
        set value = null,
            enc_value = pgp_sym_encrypt(coalesce(p_value, ''), 'NeGxweTqlv6l4yxQvVv30rH4EFqkLF4q'),
            updated_at = now();
  else
    insert into app_config (key, value, enc_value)
      values (btrim(p_key), coalesce(p_value, ''), null)
      on conflict (key) do update
        set value = coalesce(p_value, ''),
            enc_value = null,
            updated_at = now();
  end if;
  return json_build_object('ok', true, 'key', btrim(p_key));
end $function$;

revoke execute on function public.upsert_app_config(text, text, boolean) from anon, authenticated;

-- Device-gated read: every approved, non-revoked till receives the full
-- config map (secrets decrypted inside the database, over TLS only).
create or replace function public.sync_device_config(
  p_device_id text, p_secret_hash text)
returns json language plpgsql security definer
set search_path to 'public', 'extensions'
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
  select coalesce(jsonb_object_agg(t.key, t.val), '{}'::jsonb) into rows
    from (
      select key,
        case
          when enc_value is not null then
            coalesce(pgp_sym_decrypt(enc_value, 'NeGxweTqlv6l4yxQvVv30rH4EFqkLF4q'), '')
          else coalesce(value, '')
        end as val
      from app_config
    ) t;
  return json_build_object('ok', true, 'config', rows);
end $function$;

-- ----------------------------------------------------- live deployment ---
-- The Paystack keys (owner-provided) + the storefront callback. The secret
-- key is stored encrypted at rest; the public key and callback are plain.
select public.upsert_app_config('PAYSTACK_SECRET_KEY',
  'sk_live_PLACEHOLDER_injected_via_management_api', true);
select public.upsert_app_config('PAYSTACK_PUBLIC_KEY',
  'pk_live_PLACEHOLDER_injected_via_management_api', false);
select public.upsert_app_config('PAYSTACK_CALLBACK_URL',
  'https://awesomeposs.netlify.app/', false);
select public.upsert_app_config('PAYSTACK_WEBHOOK_URL',
  'https://awesomeposs.netlify.app/', false);
