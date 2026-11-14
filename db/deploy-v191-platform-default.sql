-- v1.9.1: a founding-business (is_platform) store with no payment profile
-- row defaults to the shared KCB till. v1.9.0 seeded a row for KIAMBU-MAIN
-- only, so any store the founding business created later would have been
-- treated as 'manual' and refused M-Pesa prompts. Other businesses still
-- default to 'manual'. Applied live 2026-11-14.

create or replace function public.stk_route_for_team(p_team text)
 returns json language sql stable security definer set search_path to 'public'
as $function$
  select json_build_object(
    'stk_mode', coalesce(pr.stk_mode, case when b.is_platform then 'shared' else 'manual' end),
    'own_verified', coalesce(pr.own_verified, false),
    'org_shortcode', coalesce(pr.org_shortcode, ''),
    'org_passkey', coalesce(pr.org_passkey, ''),
    'display_name', coalesce(nullif(pr.display_name, ''), s.name, ''),
    'is_platform', coalesce(b.is_platform, false))
  from sync_stores s
  join businesses b on b.id = s.business_id
  left join store_payment_profiles pr on pr.team_code = s.team_code
  where s.team_code = p_team and s.active
$function$;
revoke all on function public.stk_route_for_team(text) from public, anon, authenticated;
grant execute on function public.stk_route_for_team(text) to service_role;

create or replace function public.payment_profile_get(p_device_id text, p_secret_hash text)
 returns json language plpgsql security definer set search_path to 'public'
as $function$
declare dev record; pr record; plat boolean; mode text;
begin
  select * into dev from sync_devices
    where device_id = p_device_id and secret_hash = p_secret_hash
      and approved and not revoked;
  if dev.device_id is null or dev.team_code is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  select coalesce(b.is_platform, false) into plat from sync_stores s
    join businesses b on b.id = s.business_id where s.team_code = dev.team_code;
  select * into pr from store_payment_profiles where team_code = dev.team_code;
  mode := coalesce(pr.stk_mode, case when coalesce(plat, false) then 'shared' else 'manual' end);
  return json_build_object('ok', true,
    'stk_mode', mode,
    'stk_available', (mode = 'shared' and coalesce(plat, false))
                     or (mode = 'own' and coalesce(pr.own_verified, false)),
    'till_number', coalesce(pr.till_number, ''),
    'paybill_number', coalesce(pr.paybill_number, ''),
    'display_name', coalesce(pr.display_name, ''),
    'org_shortcode', coalesce(pr.org_shortcode, ''),
    'own_requested', mode = 'own',
    'own_verified', coalesce(pr.own_verified, false),
    'has_passkey', coalesce(pr.org_passkey <> '', false));
end $function$;
