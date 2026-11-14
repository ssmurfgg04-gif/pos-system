-- v1.10.0 — KRA eTIMS (OSCU) through LedgerPOS Cloud.
--
-- One OSCU device per store (team). The `etims` edge function is the only
-- caller: tills never see the cmcKey, and invoice numbers come from ONE
-- place so a shop with several tills still sends KRA a gapless sequence.
-- Every table has RLS on and no anon/authenticated grants; the functions
-- are service_role only.

create table if not exists public.etims_devices (
  team_code      text primary key references public.sync_stores(team_code) on delete cascade,
  env            text not null default 'sandbox' check (env in ('sandbox', 'production')),
  tin            text not null,
  bhf_id         text not null default '00',
  dvc_srl_no     text not null,
  cmc_key        text not null default '',
  trade_nm       text not null default '',
  bhf_nm         text not null default '',
  sdc_id         text not null default '',
  mrc_no         text not null default '',
  default_cls_cd text not null default '',
  next_invc_no   bigint not null default 1 check (next_invc_no >= 1),
  next_item_seq  integer not null default 1,
  active         boolean not null default true,
  created_at     timestamptz not null default now(),
  updated_at     timestamptz not null default now()
);

-- Items registered with KRA (/saveItem) for a store. item_key is the
-- till's stable product identity (SKU, else the lower-cased name).
create table if not exists public.etims_items (
  team_code   text not null references public.sync_stores(team_code) on delete cascade,
  item_key    text not null,
  item_cd     text not null,
  item_cls_cd text not null,
  tax_ty_cd   text not null,
  qty_unit_cd text not null,
  registered_at timestamptz not null default now(),
  primary key (team_code, item_key),
  unique (team_code, item_cd)
);

-- Every invoice / credit note ever sent. client_ref is the till's order
-- client_uuid (sale) or return client_uuid (credit note): the same sale
-- sent twice gets the same number and, once signed, the same stamp back.
create table if not exists public.etims_invoices (
  id            bigserial primary key,
  team_code     text not null references public.sync_stores(team_code) on delete cascade,
  client_ref    text not null,
  kind          text not null check (kind in ('S', 'R')),
  invc_no       bigint not null,
  org_invc_no   bigint not null default 0,
  status        text not null default 'pending' check (status in ('pending', 'signed', 'rejected')),
  request       jsonb,
  response      jsonb,
  cur_rcpt_no   bigint,
  tot_rcpt_no   bigint,
  intrl_data    text,
  rcpt_sign     text,
  sdc_date_time text,
  attempts      integer not null default 0,
  created_at    timestamptz not null default now(),
  updated_at    timestamptz not null default now(),
  unique (team_code, client_ref, kind),
  unique (team_code, invc_no)
);

-- KRA's item classification list, cached once per environment.
create table if not exists public.etims_item_classes (
  env       text not null,
  cls_cd    text not null,
  cls_nm    text not null,
  cls_lvl   integer not null default 0,
  tax_ty_cd text not null default '',
  primary key (env, cls_cd)
);

alter table public.etims_devices enable row level security;
alter table public.etims_items enable row level security;
alter table public.etims_invoices enable row level security;
alter table public.etims_item_classes enable row level security;
revoke all on public.etims_devices, public.etims_items, public.etims_invoices, public.etims_item_classes
  from anon, authenticated;

-- Reserve (or return the already reserved) invoice number for one sale /
-- credit note. Row-locks the device so two tills can never take the same
-- number, and binds the number to client_ref so a retry reuses it (no gap).
create or replace function public.etims_reserve_invoice(p_team text, p_client_ref text, p_kind text, p_org_invc_no bigint default 0)
returns json language plpgsql security definer set search_path to 'public'
as $$
declare inv record; n bigint;
begin
  if p_kind not in ('S', 'R') or coalesce(p_client_ref, '') = '' then
    return json_build_object('ok', false, 'error', 'bad invoice reference');
  end if;
  select * into inv from etims_invoices where team_code = p_team and client_ref = p_client_ref and kind = p_kind;
  if found then
    return json_build_object('ok', true, 'invc_no', inv.invc_no, 'status', inv.status,
      'cur_rcpt_no', inv.cur_rcpt_no, 'tot_rcpt_no', inv.tot_rcpt_no, 'intrl_data', inv.intrl_data,
      'rcpt_sign', inv.rcpt_sign, 'sdc_date_time', inv.sdc_date_time, 'existing', true);
  end if;
  update etims_devices set next_invc_no = next_invc_no + 1, updated_at = now()
   where team_code = p_team and active and cmc_key <> ''
   returning next_invc_no - 1 into n;
  if n is null then
    return json_build_object('ok', false, 'error', 'eTIMS is not connected for this shop');
  end if;
  insert into etims_invoices (team_code, client_ref, kind, invc_no, org_invc_no)
    values (p_team, p_client_ref, p_kind, n, coalesce(p_org_invc_no, 0));
  return json_build_object('ok', true, 'invc_no', n, 'status', 'pending', 'existing', false);
end $$;

-- Next item code sequence for /saveItem (per store).
create or replace function public.etims_next_item_seq(p_team text)
returns integer language sql security definer set search_path to 'public'
as $$
  update etims_devices set next_item_seq = next_item_seq + 1, updated_at = now()
   where team_code = p_team returning next_item_seq - 1
$$;

revoke all on function public.etims_reserve_invoice(text, text, text, bigint) from public, anon, authenticated;
revoke all on function public.etims_next_item_seq(text) from public, anon, authenticated;
grant execute on function public.etims_reserve_invoice(text, text, text, bigint) to service_role;
grant execute on function public.etims_next_item_seq(text) to service_role;

create or replace function public.etims_bump_attempts(p_team text, p_client_ref text, p_kind text)
returns void language sql security definer set search_path to 'public'
as $$
  update etims_invoices set attempts = attempts + 1, updated_at = now()
   where team_code = p_team and client_ref = p_client_ref and kind = p_kind
$$;
revoke all on function public.etims_bump_attempts(text, text, text) from public, anon, authenticated;
grant execute on function public.etims_bump_attempts(text, text, text) to service_role;
