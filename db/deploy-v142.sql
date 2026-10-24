-- LedgerPOS — deploy v1.4.2 backend hardening (Supabase project ixxiqrobcwkvyjtxdkvh).
--
-- Owner directive (Oct 2026): "review whatever else on database needs fixing
-- ... backend is where it matters". Full audit found the v114/v132 design
-- intent ("a leaked anon key can never read keys, forge callbacks, or
-- rewrite payment config") had DRIFTED in the live database. Four public
-- tables shipped with RLS disabled AND full anon/authenticated DML grants
-- (the PostgREST API therefore honored writes from the anon key that ships
-- inside every till binary):
--
--   buni_callback_events  — anon INSERT could FORGE a ResultCode-0 callback
--                           for a checkout id visible in buni_pushes → the
--                           till claim sweep would mark an order PAID with
--                           no money moving. Anon DELETE/TRUNCATE could
--                           vanish real confirmations.
--   buni_pushes           — anon SELECT exposed pending checkout ids; anon
--                           DELETE could break the claim mapping.
--   key_hex               — orphaned single-row table holding a live 64-hex
--                           key; anon SELECT leaked it to any anon key.
--   next_id               — orphaned helper; full anon DML.
--
-- All four are reachable ONLY by the service-role edge functions
-- (buni-stkpush / buni-callback / paystack-webhook write via
-- SUPABASE_SERVICE_ROLE_KEY) and by SECURITY DEFINER RPCs owned by
-- postgres (buni_claim_events etc.). Tills never touch them over PostgREST
-- (verified: zero rest/v1 references in Go/frontend). Enabling RLS with no
-- policies therefore blocks anon/authenticated completely while changing
-- nothing for the legitimate paths.
--
-- Belt-and-braces: revoke the (now RLS-shadowed) DML grants too — TRUNCATE
-- is privilege-gated and NOT RLS-filtered, so the revoke is the real lock
-- for that verb.
--
-- Also restores the v114 intent on upsert_app_config (execute had drifted
-- back to PUBLIC — CREATE OR REPLACE resets function ACLs; a leaked anon
-- key could rewrite payment/OTA config rows) and adds the one missing
-- index the buni-callback lookup wants.
--
-- Everything is idempotent (safe to re-run).

-- ============================================================
-- (1) RLS on the four exposed tables (+ audit/buckets for future-proofing)
-- ============================================================
alter table public.buni_callback_events   enable row level security;
alter table public.buni_pushes            enable row level security;
alter table public.key_hex                enable row level security;
alter table public.next_id                enable row level security;
alter table public.owner_action_audit     enable row level security;
alter table public.owner_capability_keys  enable row level security;
alter table public.paystack_webhook_events enable row level security;
alter table public.rpc_attempt_buckets    enable row level security;

-- No policies are created: RLS with zero policies = deny-all for
-- anon/authenticated. service_role and the table owner (postgres, which
-- the SECURITY DEFINER RPCs run as) are NOT subject to RLS unless FORCE is
-- set — deliberately not set here.

-- ============================================================
-- (2) Revoke the drifted DML grants (TRUNCATE is not RLS-governed)
-- ============================================================
revoke all on table public.buni_callback_events  from anon, authenticated;
revoke all on table public.buni_pushes           from anon, authenticated;
revoke all on table public.key_hex               from anon, authenticated;
revoke all on table public.next_id               from anon, authenticated;
revoke all on table public.owner_action_audit    from anon, authenticated;
revoke all on table public.owner_capability_keys from anon, authenticated;
revoke all on table public.paystack_webhook_events from anon, authenticated;
revoke all on table public.rpc_attempt_buckets   from anon, authenticated;
-- RLS-shielded already, but the grants should not imply access:
revoke all on table public.webhook_log           from anon, authenticated;
revoke all on table public.portal_accounts       from anon, authenticated;
revoke all on table public.portal_signin_fails   from anon, authenticated;

-- ============================================================
-- (3) upsert_app_config — restore owner/CI-only execute (v114 intent)
-- ============================================================
-- No edge function or till client calls this RPC (verified by repo grep);
-- provisioning runs through the management API as postgres, which the
-- revoke does not touch. NOTE: the function ACL carries a PUBLIC grant by
-- default (and every CREATE OR REPLACE resets ACLs to that default), so
-- the revoke must name PUBLIC — revoking only anon/authenticated leaves
-- the =X public entry in place (verified live).
revoke execute on function public.upsert_app_config(text, text, boolean)
  from anon, authenticated, PUBLIC;

-- ============================================================
-- (4) Missing index: buni-callback resolves pushes by checkout id
-- ============================================================
create index if not exists idx_buni_pushes_checkout
  on public.buni_pushes (checkout_request_id);

-- ============================================================
-- (5) sync_* table grants — TRUNCATE is NOT RLS-governed
-- ============================================================
-- The sync tables have RLS on, so anon row DML was already denied; but
-- TRUNCATE is privilege-gated and ignores RLS entirely, so the wide
-- grants let a leaked anon key wipe the fleet registry (sync_devices) or
-- the event log. Tills reach every sync table exclusively through the
-- SECURITY DEFINER RPCs — except two deliberate direct READS:
--   sync_bootstrap (onboarding) and sync_stores (join screen), both via
-- their public SELECT policies. Keep those SELECT grants; drop every
-- write verb. sync_events / sync_devices / sync_invites /
-- sync_pin_resets have no direct client access at all — full revoke.
revoke insert, update, delete, truncate on table public.sync_bootstrap from anon, authenticated;
revoke insert, update, delete, truncate on table public.sync_stores    from anon, authenticated;
revoke all on table public.sync_events      from anon, authenticated;
revoke all on table public.sync_devices     from anon, authenticated;
revoke all on table public.sync_invites     from anon, authenticated;
revoke all on table public.sync_pin_resets  from anon, authenticated;

-- ============================================================
-- Verify (run after deploy):
--   select relname, relrowsecurity from pg_class c
--     join pg_namespace n on n.oid=c.relnamespace
--    where n.nspname='public' and c.relkind='r' order by 1;
--   -- every row must be relrowsecurity = true
--   select table_name, grantee, privilege_type
--     from information_schema.role_table_grants
--    where table_schema='public' and grantee in ('anon','authenticated')
--      and privilege_type in ('INSERT','UPDATE','DELETE','TRUNCATE');
--   -- must return ZERO rows
--   select indexname from pg_indexes
--    where tablename='buni_pushes';
--   -- must list idx_buni_pushes_checkout
-- Smoke: buni_claim_events + device_config still answer a real till; the
-- edge functions keep writing (service role bypasses RLS).
-- =====================================================================
