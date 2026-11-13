-- LedgerPOS — deploy v1.7.1 internal-helper lockdown
-- (Supabase project ixxiqrobcwkvyjtxdkvh). Applied live 2026-11-14 as
-- migration `v171_lock_internal_helpers`; this file keeps the repo in step
-- so a rebuild from db/ reproduces the same grants.
--
-- Finding: five helper functions were SECURITY DEFINER *and* executable by
-- anon/authenticated through PostgREST (/rest/v1/rpc/...). They are only
-- meant to be called from inside other postgres-owned SECURITY DEFINER
-- functions (portal_login, owner_mint_capability, owner_device_signin and
-- the sync_* owner actions). Exposed directly, the anon key that ships in
-- every till let anyone:
--
--   signin_dam_reset     — wipe their own failed-sign-in counter between
--                          guesses -> unlimited password guessing against
--                          portal_login / owner_mint_capability.
--   signin_dam_fail      — inflate any counter, or the global budget read by
--                          signin_dam_global_ok -> lock real owners out.
--   signin_dam_allow     — probe lockout state for any username.
--   signin_dam_global_ok — probe the global sign-in budget.
--   owner_capability_check — verify capabilities / write audit rows outside
--                          the owner-action RPCs that are meant to gate it.
--
-- Fix: revoke EXECUTE from public, anon, authenticated. The definer callers
-- run as their owner (postgres), which keeps EXECUTE, so nothing in the app
-- changes. Verified after apply: portal_login called as anon still answers
-- normally; a direct anon call to signin_dam_reset is refused (42501).
-- Zero Go/frontend/edge-function code calls these over REST (grep-checked).

revoke execute on function public.signin_dam_allow(text, text, text, integer, integer) from public, anon, authenticated;
revoke execute on function public.signin_dam_fail(text, text, text, integer) from public, anon, authenticated;
revoke execute on function public.signin_dam_global_ok(text, integer, integer) from public, anon, authenticated;
revoke execute on function public.signin_dam_reset(text, text, text) from public, anon, authenticated;
revoke execute on function public.owner_capability_check(text, text, text) from public, anon, authenticated;

-- Verify (run after deploy):
--   begin; set local role anon;
--   select public.signin_dam_reset('portal','x','web');  -- expect 42501
--   rollback;
--   begin; set local role anon;
--   select public.portal_login('nobody','wrong','NOTEAM'); -- expect ok:false json
--   rollback;
