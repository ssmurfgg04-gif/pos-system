-- Buni (KCB M-Pesa Express) cloud tables + claim RPC.
-- House pattern mirrors paystack_webhook_events / paystack_claim_events:
--   buni-stkpush (device-authed) writes a push mapping row;
--   buni-callback (public, token-gated) resolves the mapping and stores the
--   VERIFIED-shape event;
--   tills claim events exactly-once per device through buni_claim_events,
--   scoped to the team that made the push (dev.team_code from sync_devices).
-- Access: service role (edge functions) + SECURITY DEFINER RPC only.

CREATE TABLE IF NOT EXISTS public.buni_pushes (
  checkout_request_id text PRIMARY KEY,
  reference           text NOT NULL DEFAULT '',   -- order clientUuid (audit)
  order_number        text NOT NULL DEFAULT '',
  team_code           text NOT NULL DEFAULT '',   -- push team (claim scope)
  device_id           text NOT NULL DEFAULT '',   -- pushing till
  amount_cents        bigint NOT NULL DEFAULT 0,  -- requested amount
  created_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_buni_pushes_team ON public.buni_pushes(team_code);

CREATE TABLE IF NOT EXISTS public.buni_callback_events (
  id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  event_type          text NOT NULL DEFAULT '',   -- 'stk.result'
  checkout_request_id text NOT NULL DEFAULT '',   -- raw id echoed by KCB
  reference           text NOT NULL DEFAULT '',
  team_code           text NOT NULL DEFAULT '',   -- copied from the push mapping
  result_code         integer NOT NULL DEFAULT -1,
  payload             jsonb,                      -- full callback body
  claimed_by          text[] NOT NULL DEFAULT '{}',
  claim_batch         uuid,
  created_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_buni_events_checkout ON public.buni_callback_events(checkout_request_id);
CREATE INDEX IF NOT EXISTS idx_buni_events_team ON public.buni_callback_events(team_code);

-- Exactly-once claim for a device's checkout ids. Mirrors
-- paystack_claim_events: atomic claim-batch update, then read back ONLY the
-- rows this call newly claimed. Team scoping comes from the push mapping
-- (established through device-authenticated pushes), so an event can never
-- leak across teams even on id collision.
CREATE OR REPLACE FUNCTION public.buni_claim_events(
  p_device_id text, p_secret_hash text, p_checkout_ids text[])
RETURNS json
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path TO 'public', 'extensions'
AS $function$
declare dev record; out_rows json; v_batch uuid;
begin
  select * into dev from sync_devices
    where device_id = p_device_id and secret_hash = p_secret_hash
      and approved and not revoked;
  if dev.device_id is null then
    return json_build_object('ok', false, 'error', 'device not recognized');
  end if;
  if dev.team_code is null or dev.team_code = '' then
    return json_build_object('ok', false, 'error', 'device has no store yet');
  end if;
  if p_checkout_ids is null or array_length(p_checkout_ids, 1) is null then
    return json_build_object('ok', true, 'events', '[]'::json);
  end if;
  -- occasional cleanup (7-day retention, no cron)
  if random() < 0.05 then
    delete from buni_callback_events where created_at < now() - interval '7 days';
    delete from buni_pushes where created_at < now() - interval '30 days';
  end if;
  -- mark first (atomic, idempotent per device, team-scoped), then read
  -- back ONLY the rows this exact call newly claimed (claim_batch).
  v_batch := gen_random_uuid();
  update buni_callback_events w
    set claimed_by = claimed_by || p_device_id, claim_batch = v_batch
    where w.checkout_request_id = any(p_checkout_ids)
      and w.team_code = dev.team_code
      and not (w.claimed_by @> array[p_device_id]);
  select coalesce(json_agg(json_build_object(
      'id', w.id, 'reference', w.reference, 'checkoutRequestId',
      w.checkout_request_id, 'teamCode', w.team_code,
      'payload', w.payload, 'createdAt', w.created_at) order by w.id), '[]'::json)
    into out_rows
    from buni_callback_events w
    where w.claim_batch = v_batch
      and w.claimed_by @> array[p_device_id];
  return json_build_object('ok', true, 'events', out_rows);
end $function$;
