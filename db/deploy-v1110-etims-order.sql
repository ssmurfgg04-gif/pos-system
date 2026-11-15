-- v1.11.0 — KRA eTIMS: strict ordering + stock reporting.
--
-- 1) Invoices reach KRA strictly in number order per store, even with many
--    tills: every send holds a short per-store LEASE; inside it the cloud
--    first finishes any earlier unsent invoice, then numbers the new one.
--    A number KRA REJECTS is handed back (only ever the newest number, the
--    lease guarantees it), so the sequence never has a gap.
-- 2) Stock movements (stored and released, insertStockIO) and the item's
--    remaining stock (saveStockMaster) are reported per store with their
--    own gapless sarNo, after the related invoice.

alter table public.etims_devices add column if not exists lease_holder text not null default '';
alter table public.etims_devices add column if not exists lease_until timestamptz;
alter table public.etims_devices add column if not exists next_sar_no bigint not null default 1;

create table if not exists public.etims_stock (
  id          bigserial primary key,
  team_code   text not null references public.sync_stores(team_code) on delete cascade,
  client_ref  text not null,
  sar_no      bigint not null,
  sar_ty_cd   text not null,
  item_cd     text not null default '',
  status      text not null default 'pending' check (status in ('pending', 'io_done', 'done', 'rejected')),
  request     jsonb,
  response    jsonb,
  attempts    integer not null default 0,
  created_at  timestamptz not null default now(),
  updated_at  timestamptz not null default now(),
  unique (team_code, client_ref),
  unique (team_code, sar_no)
);
alter table public.etims_stock enable row level security;
revoke all on public.etims_stock from anon, authenticated;

-- Take (or renew) the store's send lease. True when this holder has it.
create or replace function public.etims_lease(p_team text, p_holder text, p_seconds integer default 60)
returns boolean language plpgsql security definer set search_path to 'public'
as $$
declare got boolean;
begin
  update etims_devices
     set lease_holder = p_holder, lease_until = now() + make_interval(secs => greatest(5, least(p_seconds, 120)))
   where team_code = p_team
     and (lease_until is null or lease_until < now() or lease_holder = p_holder)
  returning true into got;
  return coalesce(got, false);
end $$;

create or replace function public.etims_unlease(p_team text, p_holder text)
returns void language sql security definer set search_path to 'public'
as $$
  update etims_devices set lease_holder = '', lease_until = null
   where team_code = p_team and lease_holder = p_holder
$$;

-- Hand back the NEWEST invoice number after KRA rejected it (no gap).
-- Refuses unless it really is the newest and not signed.
create or replace function public.etims_release_invoice(p_team text, p_client_ref text, p_kind text)
returns boolean language plpgsql security definer set search_path to 'public'
as $$
declare inv record; nxt bigint;
begin
  select * into inv from etims_invoices where team_code = p_team and client_ref = p_client_ref and kind = p_kind for update;
  if not found or inv.status = 'signed' then return false; end if;
  select next_invc_no into nxt from etims_devices where team_code = p_team for update;
  if nxt is distinct from inv.invc_no + 1 then return false; end if;
  delete from etims_invoices where id = inv.id;
  update etims_devices set next_invc_no = inv.invc_no, updated_at = now() where team_code = p_team;
  return true;
end $$;

-- Reserve (or return) the sarNo of one stock movement.
create or replace function public.etims_reserve_sar(p_team text, p_client_ref text, p_sar_ty text)
returns json language plpgsql security definer set search_path to 'public'
as $$
declare st record; n bigint;
begin
  select * into st from etims_stock where team_code = p_team and client_ref = p_client_ref;
  if found then
    return json_build_object('ok', true, 'sar_no', st.sar_no, 'status', st.status, 'existing', true);
  end if;
  update etims_devices set next_sar_no = next_sar_no + 1, updated_at = now()
   where team_code = p_team and active and cmc_key <> ''
  returning next_sar_no - 1 into n;
  if n is null then return json_build_object('ok', false, 'error', 'eTIMS is not connected for this shop'); end if;
  insert into etims_stock (team_code, client_ref, sar_no, sar_ty_cd) values (p_team, p_client_ref, n, p_sar_ty);
  return json_build_object('ok', true, 'sar_no', n, 'status', 'pending', 'existing', false);
end $$;

create or replace function public.etims_release_sar(p_team text, p_client_ref text)
returns boolean language plpgsql security definer set search_path to 'public'
as $$
declare st record; nxt bigint;
begin
  select * into st from etims_stock where team_code = p_team and client_ref = p_client_ref for update;
  if not found or st.status <> 'pending' then return false; end if;
  select next_sar_no into nxt from etims_devices where team_code = p_team for update;
  if nxt is distinct from st.sar_no + 1 then return false; end if;
  delete from etims_stock where id = st.id;
  update etims_devices set next_sar_no = st.sar_no, updated_at = now() where team_code = p_team;
  return true;
end $$;

revoke all on function public.etims_lease(text, text, integer) from public, anon, authenticated;
revoke all on function public.etims_unlease(text, text) from public, anon, authenticated;
revoke all on function public.etims_release_invoice(text, text, text) from public, anon, authenticated;
revoke all on function public.etims_reserve_sar(text, text, text) from public, anon, authenticated;
revoke all on function public.etims_release_sar(text, text) from public, anon, authenticated;
grant execute on function public.etims_lease(text, text, integer) to service_role;
grant execute on function public.etims_unlease(text, text) to service_role;
grant execute on function public.etims_release_invoice(text, text, text) to service_role;
grant execute on function public.etims_reserve_sar(text, text, text) to service_role;
grant execute on function public.etims_release_sar(text, text) to service_role;
