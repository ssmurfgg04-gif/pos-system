-- =====================================================================
-- v1.3.6b cloud patch — code-review findings (2nd round)
-- =====================================================================
-- 1. Cross-tenant control: sync_remove_device revoked ANY device id
--    cloud-wide; sync_assign_device could adopt a device that already
--    belongs to another store. Both now require the same team (assign
--    still adopts pending/unassigned devices).
-- 2. Direct grants dead-ended on old tills (v1.2.1 / v1.3.3 / v1.3.5
--    never poll): approve with p_new_pin now falls back to the legacy
--    8-digit code when the requesting till runs anything older than
--    1.3.6, and reports granted=false so the owner reads the code.
-- 3. lan_addr wipe: '' is not NULL, so a heartbeat without a listener
--    erased the registry. nullif() keeps the last published address.
-- 4. A worker typing garbage into the legacy 8-digit box could burn
--    attempts against a grant row and cancel the owner's grant. Redeem
--    now refuses distinctly when a grant is waiting (attempts untouched).
-- 5. Claim is atomic now: conditional UPDATE + found check.
-- =====================================================================

-- (1a) assign: only pending devices or your own team's devices
create or replace function public.sync_assign_device(
  p_device_id text, p_secret_hash text, p_target_device_id text, p_team_code text,
  p_owner_capability text default null)
returns json language plpgsql security definer set search_path to 'public', 'extensions'
as $function$
declare dev record; store record; tgt record;
begin
  select * into dev from sync_devices
    where device_id = p_device_id and secret_hash = p_secret_hash
      and approved and not revoked;
  if dev.device_id is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  select * into store from sync_stores
    where team_code = p_team_code and active;
  if store.team_code is null then
    return json_build_object('ok', false, 'error', 'unknown store');
  end if;
  if store.team_code <> dev.team_code then
    return json_build_object('ok', false, 'error', 'you can only assign tills to your own store');
  end if;
  select * into tgt from sync_devices where device_id = p_target_device_id;
  if tgt.device_id is null then
    return json_build_object('ok', false, 'error', 'device not registered');
  end if;
  if tgt.revoked then
    return json_build_object('ok', false, 'error', 'device is revoked');
  end if;
  if tgt.team_code is not null and tgt.team_code <> dev.team_code then
    return json_build_object('ok', false, 'error', 'that till belongs to another store');
  end if;
  update sync_devices set team_code = store.team_code, approved = true, last_seen = now()
    where device_id = tgt.device_id;
  return json_build_object('ok', true, 'device_id', tgt.device_id,
      'team_code', store.team_code);
end $function$;

-- (1b) remove: only your own team's devices
create or replace function public.sync_remove_device(
  p_device_id text, p_secret_hash text, p_target_device_id text,
  p_owner_capability text default null)
returns json language plpgsql security definer set search_path to 'public', 'extensions'
as $function$
declare dev record; tgt record;
begin
  select * into dev from sync_devices
    where device_id = p_device_id and secret_hash = p_secret_hash
      and approved and not revoked;
  if dev.device_id is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  if p_target_device_id = dev.device_id then
    return json_build_object('ok', false, 'error', 'cannot remove this device while using it');
  end if;
  select * into tgt from sync_devices where device_id = p_target_device_id;
  if tgt.device_id is null then
    return json_build_object('ok', false, 'error', 'device not registered');
  end if;
  if tgt.team_code is null or tgt.team_code <> dev.team_code then
    return json_build_object('ok', false, 'error', 'that till is not on your team');
  end if;
  update sync_devices set revoked = true, approved = false, last_seen = now()
    where device_id = p_target_device_id and not revoked;
  return json_build_object('ok', true, 'device_id', p_target_device_id);
end $function$;

-- (2) approve: old tills get the legacy code, never a silent grant
create or replace function public.sync_approve_pin_reset(
  p_device_id text, p_secret_hash text, p_request_id bigint,
  p_owner_capability text default null,
  p_new_pin text default null)
returns json language plpgsql security definer set search_path to 'public', 'extensions'
as $function$
declare dev record; req record; tgt record; b bytea; c text;
        tgt_ver text; v_ok boolean;
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
  if req.status = 'used' or req.status = 'cancelled' then
    return json_build_object('ok', false, 'error', 'this request was already handled');
  end if;
  if req.created_at < now() - interval '7 days' then
    update sync_pin_resets set status = 'cancelled' where id = req.id;
    return json_build_object('ok', false, 'error', 'this request expired, ask them to send it again');
  end if;
  -- Can the REQUESTING till collect a direct grant? Only 1.3.6+ polls.
  select coalesce(nullif(app_version, ''), '0.0.0') into tgt_ver
    from sync_devices where device_id = req.device_id;
  v_ok := false;
  begin
    v_ok := split_part(tgt_ver, '.', 1)::int > 1
         or (split_part(tgt_ver, '.', 1)::int = 1
             and (split_part(tgt_ver, '.', 2)::int > 3
                  or (split_part(tgt_ver, '.', 2)::int = 3
                      and split_part(tgt_ver, '.', 3)::int >= 6)));
  exception when others then
    v_ok := false;
  end;
  p_new_pin := coalesce(btrim(coalesce(p_new_pin, '')), '');
  if p_new_pin <> '' and v_ok then
    if p_new_pin !~ '^[0-9]{4}$' or p_new_pin in
       ('0000','1111','2222','3333','4444','5555','6666','7777','8888','9999',
        '1212','1122','1234','4321','2580','0123','6789','1379','1004','2000') then
      return json_build_object('ok', false, 'error',
        'choose a 4-digit PIN that isn''t an obvious pattern like 0000 or 1234');
    end if;
    update sync_pin_resets
      set status = 'approved', pin_hash = crypt(p_new_pin, gen_salt('bf')),
          code_hash = '', expires_at = now() + interval '24 hours',
          approved_at = now(), attempts = 0, claimed_at = null
      where id = req.id;
    select lan_addr into tgt from sync_devices where device_id = req.device_id;
    return json_build_object('ok', true, 'granted', true, 'code', '',
      'username', req.username, 'deviceName', req.device_name,
      'deviceId', req.device_id, 'lanAddr', coalesce(tgt.lan_addr, ''),
      'expiresMinutes', 1440);
  end if;
  -- LEGACY (old till, or no PIN typed): single-use 8-digit code.
  b := gen_random_bytes(4);
  c := lpad((((get_byte(b,0)::bigint * 16777216) + (get_byte(b,1)::bigint * 65536)
            + (get_byte(b,2)::bigint * 256) + get_byte(b,3)::bigint) % 100000000)::text, 8, '0');
  update sync_pin_resets
    set status = 'approved', code_hash = encode(digest(c, 'sha256'), 'hex'),
        pin_hash = '', expires_at = now() + interval '15 minutes',
        approved_at = now(), attempts = 0, claimed_at = null
    where id = req.id;
  select lan_addr into tgt from sync_devices where device_id = req.device_id;
  return json_build_object('ok', true, 'granted', false, 'code', c,
    'username', req.username, 'deviceName', req.device_name,
    'deviceId', req.device_id, 'lanAddr', coalesce(tgt.lan_addr, ''),
    'expiresMinutes', 15);
end $function$;

-- (4) redeem: a waiting grant must never be damaged by legacy attempts
create or replace function public.sync_redeem_pin_reset(
  p_device_id text, p_secret_hash text, p_request_id bigint, p_code text)
returns json language plpgsql security definer set search_path to 'public', 'extensions'
as $function$
declare dev record; req record;
begin
  select * into dev from sync_devices
    where device_id = p_device_id and secret_hash = p_secret_hash
      and approved and not revoked;
  if dev.device_id is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  select * into req from sync_pin_resets
    where id = p_request_id and device_id = dev.device_id;
  if req.id is null then
    return json_build_object('ok', false, 'error', 'reset request not found, send a new one from this screen');
  end if;
  if req.status = 'used' then
    return json_build_object('ok', false, 'error', 'this code was already used');
  end if;
  if req.pin_hash is not null and req.pin_hash <> '' then
    return json_build_object('ok', false, 'error',
      'the owner already set your PIN, go back to the pad and type it');
  end if;
  if req.status <> 'approved' or req.code_hash is null or req.code_hash = '' then
    return json_build_object('ok', false, 'error', 'no approved code yet, the owner still needs to tap Approve');
  end if;
  if req.expires_at is not null and req.expires_at <= now() then
    return json_build_object('ok', false, 'error', 'this code expired, tap Approve on the owner till again');
  end if;
  if req.attempts >= 6 then
    return json_build_object('ok', false, 'error', 'too many wrong tries, send a fresh request');
  end if;
  if encode(digest(coalesce(p_code, ''), 'sha256'), 'hex') <> req.code_hash then
    update sync_pin_resets set attempts = attempts + 1 where id = req.id;
    return json_build_object('ok', false, 'error', 'that code is not right, check the digits and try again');
  end if;
  update sync_pin_resets set status = 'used', used_at = now() where id = req.id;
  return json_build_object('ok', true, 'username', req.username);
end $function$;

-- (3) register: an empty lan_addr keeps the last published one
create or replace function public.sync_register(
  p_device_id text, p_secret_hash text, p_device_name text, p_app_version text,
  p_lan_addr text default '')
returns json language plpgsql security definer set search_path to 'public'
as $function$
declare cur record; store record; store_count int; v_lan text;
begin
  if p_device_id is null or length(p_device_id) < 8
     or p_secret_hash is null or length(p_secret_hash) < 32 then
    return json_build_object('ok', false, 'error', 'invalid identity');
  end if;
  v_lan := coalesce(nullif(btrim(coalesce(p_lan_addr, '')), ''), null);
  select * into cur from sync_devices where device_id = p_device_id;
  if cur.device_id is not null then
    if cur.revoked then
      return json_build_object('ok', false, 'error', 'device revoked');
    end if;
    if cur.secret_hash <> '' and cur.secret_hash <> p_secret_hash then
      return json_build_object('ok', false, 'error', 'identity mismatch');
    end if;
    update sync_devices set device_name = p_device_name, app_version = p_app_version,
        secret_hash = p_secret_hash, lan_addr = coalesce(v_lan, cur.lan_addr),
        last_seen = now()
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
        secret_hash, approved, lan_addr, last_seen)
      values (p_device_id, store.team_code, coalesce(p_device_name, 'till'),
              coalesce(p_app_version, ''), p_secret_hash, store.auto_approve,
              coalesce(v_lan, ''), now());
    return json_build_object('ok', true, 'approved', store.auto_approve,
        'team_code', store.team_code, 'pending', false);
  end if;
  insert into sync_devices (device_id, team_code, device_name, app_version,
      secret_hash, approved, lan_addr, last_seen)
    values (p_device_id, null, coalesce(p_device_name, 'till'),
            coalesce(p_app_version, ''), p_secret_hash, false,
            coalesce(v_lan, ''), now());
  return json_build_object('ok', true, 'approved', false, 'pending', true);
end $function$;

-- (5) claim: atomic single-use
create or replace function public.sync_claim_pin_grant(
  p_device_id text, p_secret_hash text, p_request_id bigint)
returns json language plpgsql security definer set search_path to 'public', 'extensions'
as $function$
declare dev record; req record;
begin
  select * into dev from sync_devices
    where device_id = p_device_id and secret_hash = p_secret_hash
      and approved and not revoked;
  if dev.device_id is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  select * into req from sync_pin_resets
    where id = p_request_id and device_id = dev.device_id
      and status in ('approved', 'claimed')
      and pin_hash <> '';
  if req.id is null then
    return json_build_object('ok', false, 'error', 'no grant for this till');
  end if;
  if req.expires_at is not null and req.expires_at <= now() then
    return json_build_object('ok', false, 'error', 'this grant expired, ask the owner to approve again');
  end if;
  update sync_pin_resets set status = 'claimed', claimed_at = now()
    where id = req.id
      and (status = 'approved' or coalesce(claimed_at, now()) < now() - interval '30 minutes');
  if not found then
    return json_build_object('ok', false, 'error', 'this grant was already collected');
  end if;
  return json_build_object('ok', true, 'username', req.username, 'pinHash', req.pin_hash);
end $function$;
