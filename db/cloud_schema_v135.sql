-- =====================================================================
-- v1.3.5 cloud migration — owner approval without friction
-- =====================================================================
-- The shop's instruction: tapping Approve on the owner till must hand out
-- the PIN-reset code in ONE tap. The v1.3.2 "confirm it's you" capability
-- challenge turned every approval into a dead end (the portal credential
-- table can be empty on real deployments, and an admin already proved
-- themselves by signing into their own till). The capability parameter
-- stays for wire compatibility with older tills; it is simply no longer
-- required. Authority = device identity (approved, non-revoked, team-bound)
-- exactly like every non-owner sync RPC. The code itself remains
-- single-use, device-bound, 8 digits, max 6 tries.
-- =====================================================================

create or replace function public.sync_approve_pin_reset(
  p_device_id text, p_secret_hash text, p_request_id bigint,
  p_owner_capability text default null)
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
    return json_build_object('ok', false, 'error', 'this request expired, ask them to send it again');
  end if;
  b := gen_random_bytes(4);
  c := lpad((((get_byte(b,0)::bigint * 16777216) + (get_byte(b,1)::bigint * 65536)
            + (get_byte(b,2)::bigint * 256) + get_byte(b,3)::bigint) % 100000000)::text, 8, '0');
  update sync_pin_resets
    set status = 'approved', code_hash = encode(digest(c, 'sha256'), 'hex'),
        expires_at = now() + interval '15 minutes', approved_at = now(), attempts = 0
    where id = req.id;
  -- deviceId rides along so the owner till can hand the code straight to
  -- the locked till over the shop WiFi (till-to-till, no reading aloud).
  return json_build_object('ok', true, 'code', c, 'username', req.username,
    'deviceName', req.device_name, 'deviceId', req.device_id, 'expiresMinutes', 15);
end $function$;
