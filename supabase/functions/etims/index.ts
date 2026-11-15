// etims — Edge Function (Deno). KRA eTIMS OSCU for every LedgerPOS store.
//
// Tills call this with their device identity; the store's OSCU credentials
// (cmcKey) live only in etims_devices and never reach a till. Invoice
// numbers come from etims_reserve_invoice (row lock + bound to the sale),
// so several tills in one shop still send KRA one gapless sequence and a
// retry of the same sale reuses its number.
//
// POST /functions/v1/etims  { p_device_id, p_secret_hash, action, ... }
//   connect  { env, tin, bhfId, dvcSrlNo, lastInvcNo?, ownerCapability? }
//   status   {}
//   classes  { q }                       KRA item classification search
//   defaults { defaultClsCd }
//   sale     { kind: 'S'|'R', clientRef, orgClientRef?, sale: {...} }
//   stock    { clientRef, sarTyCd, occurredAt, line: {...}, remainQtyMilli }
//
// Responses: { ok: true, ... } | { ok: false, error, retry? }
import { buildSale, itemCode, kenyaStamp, qtyUnitFor, receiptQrUrl, TaxType } from "./payload.ts";

const SB_URL = Deno.env.get("SUPABASE_URL") ?? "";
const SB_KEY = Deno.env.get("SUPABASE_SERVICE_ROLE_KEY") ?? "";
const KRA = {
  sandbox: (Deno.env.get("ETIMS_SANDBOX_URL") ?? "https://etims-api-sbx.kra.go.ke/etims-api").replace(/\/$/, ""),
  production: (Deno.env.get("ETIMS_PRODUCTION_URL") ?? "https://etims-api.kra.go.ke/etims-api").replace(/\/$/, ""),
};
type Env = keyof typeof KRA;

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

async function db(path: string, init: RequestInit = {}): Promise<Response> {
  return fetch(`${SB_URL}/rest/v1/${path}`, {
    ...init,
    headers: {
      apikey: SB_KEY,
      Authorization: `Bearer ${SB_KEY}`,
      "Content-Type": "application/json",
      ...(init.headers ?? {}),
    },
  });
}

async function rpc<T = any>(fn: string, args: Record<string, unknown>): Promise<T | null> {
  const r = await db(`rpc/${fn}`, { method: "POST", body: JSON.stringify(args) });
  if (!r.ok) return null;
  return await r.json().catch(() => null);
}

async function rows<T = any>(path: string): Promise<T[]> {
  const r = await db(path);
  if (!r.ok) return [];
  const j = await r.json().catch(() => []);
  return Array.isArray(j) ? j : [];
}

async function deviceTeam(deviceId: string, secretHash: string): Promise<string> {
  if (!deviceId || !secretHash) return "";
  const r = await rows<{ team_code: string }>(
    `sync_devices?device_id=eq.${encodeURIComponent(deviceId)}&secret_hash=eq.${encodeURIComponent(secretHash)}` +
      `&approved=eq.true&revoked=eq.false&select=team_code`,
  );
  return r.length === 1 ? String(r[0].team_code ?? "") : "";
}

type Device = {
  team_code: string; env: Env; tin: string; bhf_id: string; dvc_srl_no: string; cmc_key: string;
  trade_nm: string; bhf_nm: string; sdc_id: string; mrc_no: string; default_cls_cd: string;
  next_invc_no: number; active: boolean;
};

async function deviceFor(team: string): Promise<Device | null> {
  const r = await rows<Device>(`etims_devices?team_code=eq.${encodeURIComponent(team)}&select=*`);
  return r[0] ?? null;
}

function publicDevice(d: Device | null) {
  if (!d) return { connected: false };
  return {
    connected: !!d.cmc_key && d.active, env: d.env, tin: d.tin, bhfId: d.bhf_id, dvcSrlNo: d.dvc_srl_no,
    tradeName: d.trade_nm, branchName: d.bhf_nm, sdcId: d.sdc_id, mrcNo: d.mrc_no,
    defaultClsCd: d.default_cls_cd, nextInvoiceNo: d.next_invc_no,
  };
}

// One KRA call. Never throws: network trouble comes back as retry=true.
async function kra(env: Env, path: string, body: unknown, d?: Pick<Device, "tin" | "bhf_id" | "cmc_key">) {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (d) {
    headers.tin = d.tin;
    headers.bhfId = d.bhf_id;
    headers.cmcKey = d.cmc_key;
  }
  try {
    const r = await fetch(`${KRA[env]}${path}`, { method: "POST", headers, body: JSON.stringify(body), signal: AbortSignal.timeout(25_000) });
    const j = await r.json().catch(() => null);
    if (!j) return { ok: false, retry: true, code: String(r.status), msg: `KRA answered ${r.status}`, data: null, raw: null };
    const code = String(j.resultCd ?? "");
    // 000 success; 001 "no search result" is a success for lookups.
    const ok = code === "000" || code === "001";
    // 89x client/transport, 999 unknown, 5xx: worth retrying. 9xx validation: not.
    const retry = !ok && (r.status >= 500 || /^89\d$/.test(code) || code === "999" || code === "");
    return { ok, retry, code, msg: String(j.resultMsg ?? ""), data: j.data ?? null, raw: j };
  } catch (e) {
    return { ok: false, retry: true, code: "net", msg: `KRA unreachable: ${String(e).slice(0, 120)}`, data: null, raw: null };
  }
}

async function ownerAllowed(team: string, capability: string): Promise<boolean> {
  const owners = await rows(`portal_accounts?team_code=eq.${encodeURIComponent(team)}&select=team_code&limit=1`);
  if (owners.length === 0) return true; // no cloud owner yet: the till's admin gate decides (same rule as payments)
  if (!capability) return false;
  const ok = await rpc("owner_capability_check", { p_capability: capability, p_action: "etims_connect", p_team: team });
  return ok !== null && ok !== false;
}

// ---- actions ---------------------------------------------------------------

async function connect(team: string, b: any) {
  const env: Env = b.env === "production" ? "production" : "sandbox";
  const tin = String(b.tin ?? "").trim().toUpperCase();
  const bhfId = String(b.bhfId ?? "00").trim() || "00";
  const serial = String(b.dvcSrlNo ?? "").trim();
  if (!/^[A-Z]\d{9}[A-Z]$/.test(tin)) return json({ ok: false, error: "KRA PIN looks wrong (e.g. P051234567X)" }, 400);
  if (!/^\d{2}$/.test(bhfId)) return json({ ok: false, error: "branch ID is two digits, usually 00" }, 400);
  if (!serial) return json({ ok: false, error: "enter the OSCU device serial number from the eTIMS portal" }, 400);
  if (!(await ownerAllowed(team, String(b.ownerCapability ?? "")))) {
    return json({ ok: false, error: "confirm with the owner username and password to connect eTIMS" }, 403);
  }
  const prev = await deviceFor(team);
  const r = await kra(env, "/selectInitOsdcInfo", { tin, bhfId, dvcSrlNo: serial });
  let info = r.data?.info ?? null;
  if (!r.ok || !info?.cmcKey) {
    // 902 "device already installed": keep the key we hold for this exact device.
    const same = prev && prev.env === env && prev.tin === tin && prev.bhf_id === bhfId && prev.dvc_srl_no === serial && prev.cmc_key;
    if (!(r.code === "902" && same)) {
      return json({ ok: false, error: `KRA refused the device: ${r.msg || r.code}`, retry: r.retry }, r.retry ? 503 : 422);
    }
    info = { cmcKey: prev!.cmc_key, tradeNm: prev!.trade_nm, bhfNm: prev!.bhf_nm, sdcId: prev!.sdc_id, mrcNo: prev!.mrc_no };
  }
  const sameDevice = prev && prev.tin === tin && prev.bhf_id === bhfId && prev.env === env;
  const lastInvc = Math.max(0, Math.floor(Number(b.lastInvcNo ?? 0)));
  const next = Math.max(sameDevice ? prev!.next_invc_no : 1, lastInvc + 1);
  const row = {
    team_code: team, env, tin, bhf_id: bhfId, dvc_srl_no: serial, cmc_key: String(info.cmcKey),
    trade_nm: String(info.tradeNm ?? ""), bhf_nm: String(info.bhfNm ?? ""), sdc_id: String(info.sdcId ?? ""),
    mrc_no: String(info.mrcNo ?? ""), next_invc_no: next, active: true, updated_at: new Date().toISOString(),
  };
  const w = await db("etims_devices?on_conflict=team_code", {
    method: "POST", headers: { Prefer: "resolution=merge-duplicates,return=minimal" }, body: JSON.stringify(row),
  });
  if (!w.ok) return json({ ok: false, error: "could not save the eTIMS device" }, 503);
  return json({ ok: true, device: publicDevice({ ...(prev ?? {}), ...row } as Device) });
}

async function classes(team: string, b: any) {
  const d = await deviceFor(team);
  if (!d?.cmc_key) return json({ ok: false, error: "connect eTIMS first" }, 409);
  const have = await rows(`etims_item_classes?env=eq.${d.env}&select=cls_cd&limit=1`);
  if (have.length === 0) {
    const r = await kra(d.env, "/selectItemClsList", { tin: d.tin, bhfId: d.bhf_id, lastReqDt: "20180520000000" }, d);
    const list = Array.isArray(r.data?.itemClsList) ? r.data.itemClsList : [];
    if (!r.ok || list.length === 0) return json({ ok: false, error: `KRA class list unavailable: ${r.msg || r.code}`, retry: true }, 503);
    for (let i = 0; i < list.length; i += 500) {
      const chunk = list.slice(i, i + 500).map((c: any) => ({
        env: d.env, cls_cd: String(c.itemClsCd), cls_nm: String(c.itemClsNm ?? ""),
        cls_lvl: Number(c.itemClsLvl ?? 0), tax_ty_cd: String(c.taxTyCd ?? ""),
      }));
      await db("etims_item_classes?on_conflict=env,cls_cd", {
        method: "POST", headers: { Prefer: "resolution=merge-duplicates,return=minimal" }, body: JSON.stringify(chunk),
      });
    }
  }
  const q = String(b.q ?? "").trim().replace(/[%*,()]/g, " ").slice(0, 60);
  const filter = q
    ? (/^\d+$/.test(q) ? `&cls_cd=like.${encodeURIComponent(q)}*` : `&cls_nm=ilike.*${encodeURIComponent(q)}*`)
    : "";
  const found = await rows(`etims_item_classes?env=eq.${d.env}${filter}&select=cls_cd,cls_nm,cls_lvl,tax_ty_cd&order=cls_lvl.desc,cls_nm&limit=40`);
  return json({ ok: true, classes: found.map((c: any) => ({ code: c.cls_cd, name: c.cls_nm, level: c.cls_lvl, taxType: c.tax_ty_cd })) });
}

async function setDefaults(team: string, b: any) {
  const cls = String(b.defaultClsCd ?? "").trim();
  if (!/^\d{1,10}$/.test(cls)) return json({ ok: false, error: "pick an item class from KRA's list" }, 400);
  const w = await db(`etims_devices?team_code=eq.${encodeURIComponent(team)}`, {
    method: "PATCH", headers: { Prefer: "return=minimal" },
    body: JSON.stringify({ default_cls_cd: cls, updated_at: new Date().toISOString() }),
  });
  if (!w.ok) return json({ ok: false, error: "could not save" }, 503);
  return json({ ok: true, device: publicDevice(await deviceFor(team)) });
}

const TAX: TaxType[] = ["A", "B", "C", "D", "E"];

// Make sure every line's product is registered with KRA; returns item codes.
async function ensureItems(d: Device, lines: any[], cashier: { id: string; name: string }) {
  const out: Record<string, { itemCd: string; clsCd: string; taxTy: TaxType; qtyUnitCd: string }> = {};
  for (const l of lines) {
    const key = String(l.itemKey ?? "").slice(0, 120);
    if (!key || out[key]) continue;
    const taxTy = (TAX.includes(l.taxTy) ? l.taxTy : "B") as TaxType;
    const qtyUnitCd = qtyUnitFor(l.unit);
    const clsCd = String(l.clsCd || d.default_cls_cd || "");
    const have = (await rows(`etims_items?team_code=eq.${encodeURIComponent(d.team_code)}&item_key=eq.${encodeURIComponent(key)}&select=*`))[0];
    if (have && have.tax_ty_cd === taxTy && have.item_cls_cd === clsCd) {
      out[key] = { itemCd: have.item_cd, clsCd: have.item_cls_cd, taxTy, qtyUnitCd: have.qty_unit_cd };
      continue;
    }
    if (!clsCd) throw { error: "choose a default KRA item class in Settings → eTIMS", retry: false };
    let itemCd = have?.item_cd as string | undefined;
    if (!itemCd) {
      const seq = await rpc<number>("etims_next_item_seq", { p_team: d.team_code });
      if (!seq) throw { error: "could not number the item", retry: true };
      itemCd = itemCode(seq, { service: !!l.service, qtyUnitCd });
    }
    const r = await kra(d.env, "/saveItem", {
      tin: d.tin, bhfId: d.bhf_id, itemCd, itemClsCd: clsCd, itemTyCd: l.service ? "3" : "2",
      itemNm: String(l.name ?? "Item").slice(0, 200), itemStdNm: null, orgnNatCd: "KE", pkgUnitCd: "NT",
      qtyUnitCd, taxTyCd: taxTy, btchNo: null, bcd: l.barcode ? String(l.barcode).slice(0, 20) : null,
      dftPrc: Math.round(Number(l.unitPriceCents ?? 0)) / 100, grpPrcL1: null, grpPrcL2: null, grpPrcL3: null,
      grpPrcL4: null, grpPrcL5: null, addInfo: null, sftyQty: null, isrcAplcbYn: "N", useYn: "Y",
      regrId: cashier.id, regrNm: cashier.name, modrId: cashier.id, modrNm: cashier.name,
    }, d);
    if (!r.ok) throw { error: `KRA would not register "${l.name}": ${r.msg || r.code}`, retry: r.retry };
    await db("etims_items?on_conflict=team_code,item_key", {
      method: "POST", headers: { Prefer: "resolution=merge-duplicates,return=minimal" },
      body: JSON.stringify({ team_code: d.team_code, item_key: key, item_cd: itemCd, item_cls_cd: clsCd, tax_ty_cd: taxTy, qty_unit_cd: qtyUnitCd }),
    });
    out[key] = { itemCd, clsCd, taxTy, qtyUnitCd };
  }
  return out;
}

function stampOf(d: Device, inv: any) {
  return {
    invcNo: Number(inv.invc_no), curRcptNo: Number(inv.cur_rcpt_no ?? 0), totRcptNo: Number(inv.tot_rcpt_no ?? 0),
    intrlData: String(inv.intrl_data ?? ""), rcptSign: String(inv.rcpt_sign ?? ""), sdcDateTime: String(inv.sdc_date_time ?? ""),
    sdcId: d.sdc_id, mrcNo: d.mrc_no, env: d.env, tin: d.tin, bhfId: d.bhf_id,
    qr: receiptQrUrl(d.env, d.tin, d.bhf_id, String(inv.rcpt_sign ?? "")),
  };
}

// ---- strict order ---------------------------------------------------------
// KRA expects invoice numbers in order. Every send holds the store's lease;
// inside it, an earlier invoice still waiting (KRA was unreachable) goes
// first. While that one can't get through, nothing newer is numbered.

async function withLease<T>(team: string, fn: () => Promise<T>): Promise<T | null> {
  const holder = crypto.randomUUID();
  for (let i = 0; i < 6; i++) {
    if (await rpc<boolean>("etims_lease", { p_team: team, p_holder: holder, p_seconds: 70 })) {
      try {
        return await fn();
      } finally {
        await rpc("etims_unlease", { p_team: team, p_holder: holder });
      }
    }
    await new Promise((r) => setTimeout(r, 400 + Math.random() * 600));
  }
  return null; // another till is sending: the caller retries in a few seconds
}

async function patchInvoice(team: string, clientRef: string, kind: string, patch: Record<string, unknown>) {
  await db(`etims_invoices?team_code=eq.${encodeURIComponent(team)}&client_ref=eq.${encodeURIComponent(clientRef)}&kind=eq.${kind}`, {
    method: "PATCH", headers: { Prefer: "return=minimal" }, body: JSON.stringify({ ...patch, updated_at: new Date().toISOString() }),
  });
  await rpc("etims_bump_attempts", { p_team: team, p_client_ref: clientRef, p_kind: kind });
}

function signedPatch(r: any) {
  return {
    status: "signed", response: r.raw,
    cur_rcpt_no: Number(r.data?.curRcptNo ?? 0), tot_rcpt_no: Number(r.data?.totRcptNo ?? 0),
    intrl_data: String(r.data?.intrlData ?? ""), rcpt_sign: String(r.data?.rcptSign ?? ""),
    sdc_date_time: String(r.data?.sdcDateTime ?? ""),
  };
}

// Send the oldest invoice still waiting (other than `except`). Returns
// false when it could not get through (queue blocked: retry later).
async function drainHead(d: Device, except: string): Promise<boolean> {
  const head = (await rows(`etims_invoices?team_code=eq.${encodeURIComponent(d.team_code)}&status=eq.pending&request=not.is.null` +
    `&client_ref=neq.${encodeURIComponent(except)}&order=invc_no.asc&limit=1&select=client_ref,kind,request,invc_no`))[0];
  if (!head) return true;
  const r = await kra(d.env, "/saveTrnsSalesOsdc", head.request, d);
  if (r.ok) {
    await patchInvoice(d.team_code, head.client_ref, head.kind, signedPatch(r));
    return true;
  }
  if (r.retry) {
    await patchInvoice(d.team_code, head.client_ref, head.kind, { response: r.raw });
    return false;
  }
  // KRA refused it for good: it is the newest number (nothing newer can be
  // numbered while it waits), so hand the number back; its till re-sends.
  await patchInvoice(d.team_code, head.client_ref, head.kind, { status: "rejected", response: r.raw });
  await rpc("etims_release_invoice", { p_team: d.team_code, p_client_ref: head.client_ref, p_kind: head.kind });
  return true;
}

async function sale(team: string, b: any) {
  const d = await deviceFor(team);
  if (!d?.cmc_key || !d.active) return json({ ok: false, error: "eTIMS is not connected for this shop", retry: false }, 409);
  const kind = b.kind === "R" ? "R" : "S";
  const clientRef = String(b.clientRef ?? "").slice(0, 80);
  const s = b.sale ?? {};
  const lines = Array.isArray(s.lines) ? s.lines : [];
  if (!clientRef || lines.length === 0) return json({ ok: false, error: "nothing to send", retry: false }, 400);

  // Already signed? Hand the stamp back without touching KRA (idempotent).
  const done = (await rows(`etims_invoices?team_code=eq.${encodeURIComponent(team)}&client_ref=eq.${encodeURIComponent(clientRef)}&kind=eq.${kind}&status=eq.signed&select=*`))[0];
  if (done) return json({ ok: true, stamp: stampOf(d, done) });

  let orgInvcNo = 0;
  if (kind === "R") {
    const org = (await rows(`etims_invoices?team_code=eq.${encodeURIComponent(team)}&client_ref=eq.${encodeURIComponent(String(b.orgClientRef ?? ""))}&kind=eq.S&status=eq.signed&select=invc_no`))[0];
    if (!org) return json({ ok: false, error: "the original sale has no signed eTIMS invoice yet", retry: true }, 409);
    orgInvcNo = Number(org.invc_no);
  }
  const cashier = { id: String(s.cashierId ?? "pos").slice(0, 20), name: String(s.cashierName ?? "POS").slice(0, 60) };

  // Items first (outside the lease): a sale KRA can't accept yet never
  // holds a number.
  let items: Awaited<ReturnType<typeof ensureItems>>;
  try {
    items = await ensureItems(d, lines, cashier);
  } catch (e: any) {
    return json({ ok: false, error: e?.error ?? String(e), retry: !!e?.retry }, e?.retry ? 503 : 422);
  }

  const out = await withLease(team, async () => {
    if (!(await drainHead(d, clientRef))) {
      return { status: 503, body: { ok: false, error: "KRA unreachable: an earlier invoice is waiting, sending in order", retry: true } };
    }
    const res = await rpc<any>("etims_reserve_invoice", { p_team: team, p_client_ref: clientRef, p_kind: kind, p_org_invc_no: orgInvcNo });
    if (!res?.ok) return { status: 503, body: { ok: false, error: res?.error ?? "could not number the invoice", retry: true } };
    if (res.status === "signed") return { status: 200, body: { ok: true, stamp: stampOf(d, res) } };
    const confirmedAt = /^\d{14}$/.test(String(s.confirmedAt ?? "")) ? String(s.confirmedAt) : kenyaStamp(new Date());
    let payload;
    try {
      payload = buildSale({
        kind, invcNo: Number(res.invc_no), orgInvcNo, tin: d.tin, bhfId: d.bhf_id,
        traderInvoiceNo: String(s.traderInvoiceNo ?? clientRef), confirmedAt,
        customerPin: s.customerPin, customerName: s.customerName, customerPhone: s.customerPhone,
        payment: String(s.payment ?? "cash"), discountCents: Math.max(0, Math.round(Number(s.discountCents ?? 0))),
        cashierId: cashier.id, cashierName: cashier.name, tradeName: d.trade_nm, refundReason: s.refundReason,
        lines: lines.map((l: any) => {
          const it = items[String(l.itemKey)];
          return {
            itemCd: it.itemCd, itemClsCd: it.clsCd, name: String(l.name ?? "Item"), barcode: l.barcode,
            qtyUnitCd: it.qtyUnitCd, qtyMilli: Math.max(1, Math.round(Number(l.qtyMilli ?? 0))),
            lineTotalCents: Math.max(0, Math.round(Number(l.lineTotalCents ?? 0))), taxTy: it.taxTy,
            discountCents: l.discountCents === undefined ? undefined : Math.max(0, Math.round(Number(l.discountCents))),
            taxCents: l.taxCents === undefined ? undefined : Math.round(Number(l.taxCents)),
          };
        }),
      });
    } catch (e) {
      await rpc("etims_release_invoice", { p_team: team, p_client_ref: clientRef, p_kind: kind });
      return { status: 400, body: { ok: false, error: String(e), retry: false } };
    }
    // Saved BEFORE sending: if this run dies mid-call, the next send
    // re-sends exactly this invoice first (same number, same content).
    await patchInvoice(team, clientRef, kind, { request: payload, status: "pending" });
    const r = await kra(d.env, "/saveTrnsSalesOsdc", payload, d);
    if (r.ok && r.data) {
      const patch = signedPatch(r);
      await patchInvoice(team, clientRef, kind, patch);
      return { status: 200, body: { ok: true, stamp: stampOf(d, { invc_no: res.invc_no, ...patch }) } };
    }
    if (r.retry) {
      await patchInvoice(team, clientRef, kind, { response: r.raw });
      return { status: 503, body: { ok: false, error: `KRA: ${r.msg || r.code}`, code: r.code, retry: true } };
    }
    await patchInvoice(team, clientRef, kind, { status: "rejected", response: r.raw });
    await rpc("etims_release_invoice", { p_team: team, p_client_ref: clientRef, p_kind: kind });
    return { status: 422, body: { ok: false, error: `KRA: ${r.msg || r.code}`, code: r.code, retry: false } };
  });
  if (!out) return json({ ok: false, error: "another till is sending to KRA, retrying", retry: true }, 503);
  return json(out.body, out.status);
}

// ---- stock (stored and released) --------------------------------------------
// One stock movement from the till's ledger → /insertStockIO, then the
// item's remaining quantity → /saveStockMaster. Idempotent per clientRef;
// sarNo gapless per store under the same lease as invoices.
const SAR_TYPES = new Set(["01", "02", "03", "04", "05", "06", "11", "12", "13", "14", "15", "16"]);

async function stock(team: string, b: any) {
  const d = await deviceFor(team);
  if (!d?.cmc_key || !d.active) return json({ ok: false, error: "eTIMS is not connected for this shop", retry: false }, 409);
  const clientRef = String(b.clientRef ?? "").slice(0, 80);
  const sarTy = String(b.sarTyCd ?? "");
  const l = b.line ?? {};
  if (!clientRef || !SAR_TYPES.has(sarTy) || !l.itemKey) return json({ ok: false, error: "bad stock movement", retry: false }, 400);
  const have = (await rows(`etims_stock?team_code=eq.${encodeURIComponent(team)}&client_ref=eq.${encodeURIComponent(clientRef)}&select=status`))[0];
  if (have?.status === "done") return json({ ok: true, status: "done" });
  const cashier = { id: String(b.userId ?? "pos").slice(0, 20), name: String(b.userName ?? "POS").slice(0, 60) };
  let items: Awaited<ReturnType<typeof ensureItems>>;
  try {
    items = await ensureItems(d, [l], cashier);
  } catch (e: any) {
    return json({ ok: false, error: e?.error ?? String(e), retry: !!e?.retry }, e?.retry ? 503 : 422);
  }
  const it = items[String(l.itemKey)];
  const out = await withLease(team, async () => {
    const res = await rpc<any>("etims_reserve_sar", { p_team: team, p_client_ref: clientRef, p_sar_ty: sarTy });
    if (!res?.ok) return { status: 503, body: { ok: false, error: res?.error ?? "could not number the movement", retry: true } };
    const qty = Math.round(Math.abs(Number(l.qtyMilli ?? 0)) / 10) / 100; // KRA: 2 decimals
    const prc = Math.max(0, Math.round(Number(l.unitPriceCents ?? 0))) / 100;
    const amt = Math.round(qty * prc * 100);
    const rate = it.taxTy === "B" ? 16 : it.taxTy === "E" ? 8 : 0;
    const tax = rate > 0 ? Math.floor((2 * amt * rate + 100 + rate) / (2 * (100 + rate))) : 0;
    const patch = (p: Record<string, unknown>) => db(`etims_stock?team_code=eq.${encodeURIComponent(team)}&client_ref=eq.${encodeURIComponent(clientRef)}`, {
      method: "PATCH", headers: { Prefer: "return=minimal" }, body: JSON.stringify({ ...p, item_cd: it.itemCd, updated_at: new Date().toISOString() }),
    });
    if (res.status === "pending") {
      const ocrn = /^\d{8}/.test(String(b.occurredAt ?? "")) ? String(b.occurredAt).slice(0, 8) : kenyaStamp(new Date()).slice(0, 8);
      const io = {
        tin: d.tin, bhfId: d.bhf_id, sarNo: Number(res.sar_no), orgSarNo: 0, regTyCd: "M",
        custTin: null, custNm: null, custBhfId: null, sarTyCd: sarTy, ocrnDt: ocrn, totItemCnt: 1,
        totTaxblAmt: amt / 100, totTaxAmt: tax / 100, totAmt: amt / 100,
        remark: String(b.remark ?? "").slice(0, 400) || null,
        regrId: cashier.id, regrNm: cashier.name, modrId: cashier.id, modrNm: cashier.name,
        itemList: [{
          itemSeq: 1, itemCd: it.itemCd, itemClsCd: it.clsCd, itemNm: String(l.name ?? "Item").slice(0, 200),
          bcd: l.barcode ? String(l.barcode).slice(0, 20) : null, pkgUnitCd: "NT", pkg: qty, qtyUnitCd: it.qtyUnitCd, qty,
          itemExprDt: /^\d{8}$/.test(String(l.expiry ?? "")) ? String(l.expiry) : null,
          prc, splyAmt: amt / 100, totDcAmt: 0, taxblAmt: amt / 100, taxTyCd: it.taxTy, taxAmt: tax / 100, totAmt: amt / 100,
        }],
      };
      await patch({ request: io });
      const r = await kra(d.env, "/insertStockIO", io, d);
      if (!r.ok) {
        if (r.retry) {
          await patch({ response: r.raw });
          return { status: 503, body: { ok: false, error: `KRA: ${r.msg || r.code}`, retry: true } };
        }
        await patch({ status: "rejected", response: r.raw });
        await rpc("etims_release_sar", { p_team: team, p_client_ref: clientRef });
        return { status: 422, body: { ok: false, error: `KRA: ${r.msg || r.code}`, retry: false } };
      }
      await patch({ status: "io_done", response: r.raw });
    }
    const rsd = Math.round(Math.max(0, Number(b.remainQtyMilli ?? 0)) / 10) / 100;
    const m = await kra(d.env, "/saveStockMaster", {
      tin: d.tin, bhfId: d.bhf_id, itemCd: it.itemCd, rsdQty: rsd,
      regrId: cashier.id, regrNm: cashier.name, modrId: cashier.id, modrNm: cashier.name,
    }, d);
    if (!m.ok) return { status: m.retry ? 503 : 422, body: { ok: false, error: `KRA stock level: ${m.msg || m.code}`, retry: true } };
    await patch({ status: "done" });
    return { status: 200, body: { ok: true, status: "done", sarNo: Number(res.sar_no) } };
  });
  if (!out) return json({ ok: false, error: "another till is sending to KRA, retrying", retry: true }, 503);
  return json(out.body, out.status);
}

Deno.serve(async (req) => {
  if (req.method !== "POST") return json({ ok: false, error: "POST only" }, 405);
  let b: any;
  try {
    b = await req.json();
  } catch {
    return json({ ok: false, error: "invalid JSON" }, 400);
  }
  const team = await deviceTeam(String(b.p_device_id ?? ""), String(b.p_secret_hash ?? ""));
  if (!team) return json({ ok: false, error: "device not recognized" }, 401);
  switch (String(b.action ?? "")) {
    case "connect": return await connect(team, b);
    case "status": return json({ ok: true, device: publicDevice(await deviceFor(team)) });
    case "classes": return await classes(team, b);
    case "defaults": return await setDefaults(team, b);
    case "sale": return await sale(team, b);
    case "stock": return await stock(team, b);
    default: return json({ ok: false, error: "unknown action" }, 400);
  }
});
