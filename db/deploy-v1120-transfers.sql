-- v1.12.0 — stock transfers between the shops of one business.
--
-- The cloud is the single record of a transfer; each till only applies
-- its own side once. Flow:
--   1. The sending till takes the stock off its shelf (locally, one tx) and
--      queues the transfer; transfer_send records it here (idempotent per
--      sending shop + client_ref).
--   2. A till of the receiving shop claims it with transfer_receive
--      (row-locked: exactly one claim wins, and the same till asking again
--      gets the same answer), then adds the stock locally.
--   3. While still 'sent', a till of the sending shop may cancel; the stock
--      goes back on its shelf. A cancel that arrives BEFORE the send (the
--      send's answer was lost) leaves a tombstone, so a late send can never
--      revive it.
-- Only shops of the same business can trade. Tills call with their device
-- identity (device_id + secret hash), like every sync_* RPC.

create table if not exists public.stock_transfers (
  id               uuid primary key default gen_random_uuid(),
  business_id      bigint not null references public.businesses(id),
  from_team        text not null references public.sync_stores(team_code),
  to_team          text not null references public.sync_stores(team_code),
  client_ref       text not null check (length(client_ref) between 8 and 80),
  lines            jsonb not null default '[]'::jsonb,
  note             text not null default '' check (length(note) <= 300),
  status           text not null default 'sent' check (status in ('sent', 'received', 'cancelled')),
  sent_by          text not null default '',
  sent_device      text not null default '',
  sent_at          timestamptz not null default now(),
  received_by      text not null default '',
  received_device  text not null default '',
  received_lines   jsonb,
  received_at      timestamptz,
  cancelled_by     text not null default '',
  cancelled_device text not null default '',
  cancelled_at     timestamptz,
  unique (from_team, client_ref)
);
create index if not exists stock_transfers_to_idx on public.stock_transfers (to_team, status, sent_at desc);
create index if not exists stock_transfers_from_idx on public.stock_transfers (from_team, sent_at desc);
alter table public.stock_transfers enable row level security;
revoke all on public.stock_transfers from anon, authenticated;

-- team of an approved, non-revoked device ('' if none)
create or replace function public._device_team(p_device_id text, p_secret_hash text)
returns text language sql stable security definer set search_path to 'public' as $$
  select coalesce((select d.team_code from sync_devices d
    where d.device_id = p_device_id and d.secret_hash = p_secret_hash
      and d.approved and not d.revoked and d.team_code is not null limit 1), '')
$$;
revoke all on function public._device_team(text, text) from public, anon, authenticated;

create or replace function public._transfer_json(t public.stock_transfers)
returns jsonb language sql stable security definer set search_path to 'public' as $$
  select jsonb_build_object(
    'id', t.id, 'from_team', t.from_team, 'to_team', t.to_team,
    'from_name', (select name from sync_stores where team_code = t.from_team),
    'to_name', (select name from sync_stores where team_code = t.to_team),
    'client_ref', t.client_ref, 'lines', t.lines, 'note', t.note, 'status', t.status,
    'sent_by', t.sent_by, 'sent_device', t.sent_device, 'sent_at', t.sent_at,
    'received_by', t.received_by, 'received_device', t.received_device,
    'received_lines', t.received_lines, 'received_at', t.received_at,
    'cancelled_by', t.cancelled_by, 'cancelled_device', t.cancelled_device, 'cancelled_at', t.cancelled_at)
$$;
revoke all on function public._transfer_json(public.stock_transfers) from public, anon, authenticated;

-- lines: [{sku, name, qtyMilli, ...}] 1..500 lines, each sku non-empty and
-- quantity 1..1e12 milli-units
create or replace function public._transfer_lines_ok(p_lines jsonb)
returns boolean language sql immutable as $$
  select jsonb_typeof(p_lines) = 'array'
     and jsonb_array_length(p_lines) between 1 and 500
     and pg_column_size(p_lines) < 512000
     and not exists (
       select 1 from jsonb_array_elements(p_lines) l
        where jsonb_typeof(l) <> 'object'
           or length(btrim(coalesce(l->>'sku', ''))) not between 1 and 64
           or coalesce(l->>'qtyMilli', '') !~ '^[0-9]{1,13}$'
           or (l->>'qtyMilli')::bigint < 1)
$$;

create or replace function public.transfer_send(p_device_id text, p_secret_hash text,
  p_client_ref text, p_to_team text, p_lines jsonb, p_note text default '', p_user text default '')
returns json language plpgsql security definer set search_path to 'public' as $$
declare me text; biz bigint; t stock_transfers;
begin
  me := public._device_team(p_device_id, p_secret_hash);
  if me = '' then return json_build_object('ok', false, 'error', 'device not recognized'); end if;
  select * into t from stock_transfers where from_team = me and client_ref = p_client_ref;
  if found then
    return json_build_object('ok', true, 'existing', true, 'transfer', public._transfer_json(t));
  end if;
  select business_id into biz from sync_stores where team_code = me;
  if not exists (select 1 from sync_stores where team_code = p_to_team and business_id = biz and active) then
    return json_build_object('ok', false, 'final', true, 'error', 'that shop is not part of your business');
  end if;
  if p_to_team = me then
    return json_build_object('ok', false, 'final', true, 'error', 'a shop cannot send stock to itself');
  end if;
  if length(coalesce(p_client_ref, '')) not between 8 and 80 then
    return json_build_object('ok', false, 'final', true, 'error', 'bad transfer reference');
  end if;
  if not public._transfer_lines_ok(p_lines) then
    return json_build_object('ok', false, 'final', true, 'error', 'bad transfer lines');
  end if;
  insert into stock_transfers (business_id, from_team, to_team, client_ref, lines, note, sent_by, sent_device)
  values (biz, me, p_to_team, p_client_ref, p_lines, left(coalesce(p_note, ''), 300), left(coalesce(p_user, ''), 60), p_device_id)
  on conflict (from_team, client_ref) do nothing;
  select * into t from stock_transfers where from_team = me and client_ref = p_client_ref;
  return json_build_object('ok', true, 'existing', false, 'transfer', public._transfer_json(t));
end $$;

create or replace function public.transfer_list(p_device_id text, p_secret_hash text, p_days integer default 90)
returns json language plpgsql security definer set search_path to 'public' as $$
declare me text; out_rows jsonb;
begin
  me := public._device_team(p_device_id, p_secret_hash);
  if me = '' then return json_build_object('ok', false, 'error', 'device not recognized'); end if;
  select coalesce(jsonb_agg(public._transfer_json(t) order by t.sent_at desc), '[]'::jsonb) into out_rows
    from (select * from stock_transfers
           where (from_team = me or to_team = me)
             and jsonb_array_length(lines) > 0 -- cancel tombstones stay hidden
             and (status = 'sent' or sent_at > now() - make_interval(days => greatest(1, least(coalesce(p_days, 90), 400))))
           order by sent_at desc limit 300) t;
  return json_build_object('ok', true, 'team', me, 'transfers', out_rows);
end $$;

create or replace function public.transfer_receive(p_device_id text, p_secret_hash text,
  p_id uuid, p_received jsonb, p_user text default '')
returns json language plpgsql security definer set search_path to 'public' as $$
declare me text; t stock_transfers;
begin
  me := public._device_team(p_device_id, p_secret_hash);
  if me = '' then return json_build_object('ok', false, 'error', 'device not recognized'); end if;
  select * into t from stock_transfers where id = p_id for update;
  if not found or t.to_team <> me then
    return json_build_object('ok', false, 'final', true, 'error', 'transfer not found');
  end if;
  if t.status = 'received' then
    if t.received_device = p_device_id then
      return json_build_object('ok', true, 'existing', true, 'transfer', public._transfer_json(t));
    end if;
    return json_build_object('ok', false, 'final', true, 'error', 'already received on another till');
  end if;
  if t.status = 'cancelled' then
    return json_build_object('ok', false, 'final', true, 'error', 'the sending shop cancelled this transfer');
  end if;
  -- what arrived: one entry per sent line (same sku), 0..sent quantity
  if jsonb_typeof(p_received) <> 'array' or jsonb_array_length(p_received) <> jsonb_array_length(t.lines)
     or exists (
       select 1 from jsonb_array_elements(p_received) with ordinality r(v, i)
         join jsonb_array_elements(t.lines) with ordinality l(v, i) using (i)
        where r.v->>'sku' is distinct from l.v->>'sku'
           or coalesce(r.v->>'qtyMilli', '') !~ '^[0-9]{1,13}$'
           or (r.v->>'qtyMilli')::bigint > (l.v->>'qtyMilli')::bigint) then
    return json_build_object('ok', false, 'final', true, 'error', 'received quantities do not match the transfer');
  end if;
  update stock_transfers set status = 'received', received_by = left(coalesce(p_user, ''), 60),
         received_device = p_device_id, received_lines = p_received, received_at = now()
   where id = p_id
  returning * into t;
  return json_build_object('ok', true, 'existing', false, 'transfer', public._transfer_json(t));
end $$;

-- Cancel by the sender's client_ref (works even when the send's answer
-- never came back: no row yet = leave a cancelled tombstone).
create or replace function public.transfer_cancel(p_device_id text, p_secret_hash text,
  p_client_ref text, p_to_team text, p_user text default '')
returns json language plpgsql security definer set search_path to 'public' as $$
declare me text; biz bigint; t stock_transfers;
begin
  me := public._device_team(p_device_id, p_secret_hash);
  if me = '' then return json_build_object('ok', false, 'error', 'device not recognized'); end if;
  if length(coalesce(p_client_ref, '')) not between 8 and 80 then
    return json_build_object('ok', false, 'final', true, 'error', 'bad transfer reference');
  end if;
  select * into t from stock_transfers where from_team = me and client_ref = p_client_ref for update;
  if not found then
    select business_id into biz from sync_stores where team_code = me;
    if not exists (select 1 from sync_stores where team_code = p_to_team and business_id = biz) then
      p_to_team := me; -- tombstone only; never visible to anyone else
    end if;
    insert into stock_transfers (business_id, from_team, to_team, client_ref, lines, status, sent_by, sent_device,
                                 cancelled_by, cancelled_device, cancelled_at)
    values (biz, me, p_to_team, p_client_ref, '[]'::jsonb, 'cancelled', '', p_device_id,
            left(coalesce(p_user, ''), 60), p_device_id, now())
    on conflict (from_team, client_ref) do nothing;
    select * into t from stock_transfers where from_team = me and client_ref = p_client_ref for update;
  end if;
  if t.status = 'received' then
    return json_build_object('ok', false, 'final', true, 'error', 'already received, ask the other shop to send it back');
  end if;
  if t.status = 'sent' then
    update stock_transfers set status = 'cancelled', cancelled_by = left(coalesce(p_user, ''), 60),
           cancelled_device = p_device_id, cancelled_at = now()
     where id = t.id returning * into t;
  end if;
  return json_build_object('ok', true, 'transfer', public._transfer_json(t));
end $$;

revoke all on function public.transfer_send(text, text, text, text, jsonb, text, text) from public;
revoke all on function public.transfer_list(text, text, integer) from public;
revoke all on function public.transfer_receive(text, text, uuid, jsonb, text) from public;
revoke all on function public.transfer_cancel(text, text, text, text, text) from public;
grant execute on function public.transfer_send(text, text, text, text, jsonb, text, text) to anon, authenticated, service_role;
grant execute on function public.transfer_list(text, text, integer) to anon, authenticated, service_role;
grant execute on function public.transfer_receive(text, text, uuid, jsonb, text) to anon, authenticated, service_role;
grant execute on function public.transfer_cancel(text, text, text, text, text) to anon, authenticated, service_role;

-- v1.12.1: pin the helper search_path (Supabase advisor 0011)
alter function public._transfer_lines_ok(jsonb) set search_path = public;
