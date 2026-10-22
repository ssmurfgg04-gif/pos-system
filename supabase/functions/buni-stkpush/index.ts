// buni-stkpush — Edge Function (Deno).
// KCB Buni M-Pesa Express STK push WITHOUT the consumer key/secret ever
// touching a till. The Go backend calls this with its device identity;
// this function performs the KCB OAuth (client_credentials) and the push,
// and stores the push mapping (checkout id -> team) so buni-callback can
// route the result back to the right team of tills.
//
// Buni wire contract (production-verified Oct 2026):
//   token: POST {BUNI_TOKEN_URL:-https://api.buni.kcbgroup.com/token}
//          form grant_type=client_credentials, Basic key:secret (3600s)
//   push:  POST {BUNI_STK_URL:-https://api.buni.kcbgroup.com/mm/api/request/1.0.0/stkpush}
//          headers: operation=STKPush, routeCode={BUNI_ROUTE_CODE:-207},
//          messageId (unique), Authorization Bearer
//          body: phoneNumber, amount (INTEGER STRING), invoiceNumber
//          ("TILL-REF", <=12 chars), sharedShortCode=true,
//          orgShortCode/orgPassKey "" (shared till), callbackUrl,
//          transactionDescription (<=13 chars)
//   response: {"response":{MerchantRequestID, ResponseCode, CustomerMessage,
//          CheckoutRequestID,...},"header":{statusCode,statusDescription}}
//
// POST /functions/v1/buni-stkpush
//   { p_device_id, p_secret_hash, phone, amount (cents), invoice_ref,
//     order_number, client_uuid? }
//   -> { ok, merchant_request_id, checkout_request_id, customer_message }
//      | { ok:false, error }  (401 device, 503 no keys, 400 bad input, 502 kcb)
const SB_URL = Deno.env.get("SUPABASE_URL") ?? "";
const SB_KEY = Deno.env.get("SUPABASE_SERVICE_ROLE_KEY") ?? "";

const TOKEN_URL = (Deno.env.get("BUNI_TOKEN_URL") ?? "https://api.buni.kcbgroup.com/token").trim();
const STK_URL = (Deno.env.get("BUNI_STK_URL") ?? "https://api.buni.kcbgroup.com/mm/api/request/1.0.0/stkpush").trim();
const ROUTE_CODE = (Deno.env.get("BUNI_ROUTE_CODE") ?? "207").trim();
const CALLBACK_URL = (Deno.env.get("BUNI_CALLBACK_URL") ?? "").trim();

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

async function deviceOK(deviceId: string, secretHash: string): Promise<string> {
  // returns the device's team_code on success, "" on failure
  if (!deviceId || !secretHash || !SB_URL || !SB_KEY) return "";
  const url =
    `${SB_URL}/rest/v1/sync_devices?device_id=eq.${encodeURIComponent(deviceId)}` +
    `&secret_hash=eq.${encodeURIComponent(secretHash)}&approved=eq.true&revoked=eq.false` +
    `&select=team_code`;
  const r = await fetch(url, {
    headers: { apikey: SB_KEY, Authorization: `Bearer ${SB_KEY}` },
  });
  if (!r.ok) return "";
  const rows = await r.json();
  if (!Array.isArray(rows) || rows.length !== 1) return "";
  return String(rows[0].team_code ?? "");
}

// Buni OAuth (client_credentials), cached until ~120s before expiry.
let tok = "";
let tokExp = 0;
async function buniToken(): Promise<string> {
  if (tok && Date.now() < tokExp) return tok;
  const key = (Deno.env.get("BUNI_CONSUMER_KEY") ?? "").trim();
  const secret = (Deno.env.get("BUNI_CONSUMER_SECRET") ?? "").trim();
  if (!key || !secret) return "";
  const r = await fetch(TOKEN_URL, {
    method: "POST",
    headers: {
      "Content-Type": "application/x-www-form-urlencoded",
      Authorization: "Basic " + btoa(`${key}:${secret}`),
    },
    body: "grant_type=client_credentials",
  });
  const j = await r.json().catch(() => null);
  const t = String(j?.access_token ?? "");
  if (!r.ok || !t) return "";
  const secs = Number(j?.expires_in ?? 3600);
  tok = t;
  tokExp = Date.now() + Math.max(300, secs - 120) * 1000;
  return tok;
}

Deno.serve(async (req) => {
  if (req.method !== "POST") return json({ ok: false, error: "POST only" }, 405);
  let b: Record<string, unknown>;
  try {
    b = await req.json();
  } catch {
    return json({ ok: false, error: "invalid JSON" }, 400);
  }
  const team = await deviceOK(String(b.p_device_id ?? ""), String(b.p_secret_hash ?? ""));
  if (!team) return json({ ok: false, error: "device not recognized" }, 401);

  const key = (Deno.env.get("BUNI_CONSUMER_KEY") ?? "").trim();
  const secret = (Deno.env.get("BUNI_CONSUMER_SECRET") ?? "").trim();
  const till = (Deno.env.get("BUNI_TILL") ?? "").trim();
  if (!key || !secret || !till || !CALLBACK_URL) {
    return json({ ok: false, error: "buni keys not configured" }, 503);
  }

  const phone = String(b.phone ?? "").trim();
  if (!/^254[17]\d{8}$/.test(phone)) {
    return json({ ok: false, error: "invalid phone (expected 2547XXXXXXXX/2541XXXXXXXX)" }, 400);
  }
  const cents = Number(b.amount ?? 0);
  if (!Number.isFinite(cents) || cents <= 0 || cents % 100 !== 0) {
    return json({ ok: false, error: "amount must be a positive whole-shilling value in cents" }, 400);
  }
  const amount = String(Math.round(cents / 100));
  // invoiceNumber: "TILL-REF", hard 12-char production limit; the ref is
  // display-only (the checkout id is the matching key), so truncate freely.
  let ref = String(b.invoice_ref ?? "POS").toUpperCase().replace(/[^A-Z0-9]/g, "").slice(0, 4) || "POS";
  if (till.length + 1 + ref.length > 12) ref = ref.slice(0, Math.max(1, 12 - till.length - 1));
  const invoiceNumber = `${till}-${ref}`;
  const orderNumber = String(b.order_number ?? "").slice(0, 40);
  const clientUUID = String(b.client_uuid ?? "").slice(0, 64);

  const token = await buniToken();
  if (!token) return json({ ok: false, error: "buni token unavailable (check consumer keys)" }, 503);

  const pushRes = await fetch(STK_URL, {
    method: "POST",
    headers: {
      accept: "application/json",
      "Content-Type": "application/json",
      Authorization: `Bearer ${token}`,
      operation: "STKPush",
      routeCode: ROUTE_CODE,
      messageId: `${till}-${Date.now()}-${Math.floor(Math.random() * 1e9)}`,
    },
    body: JSON.stringify({
      phoneNumber: phone,
      amount,
      invoiceNumber,
      sharedShortCode: true,
      orgShortCode: "",
      orgPassKey: "",
      callbackUrl: CALLBACK_URL,
      transactionDescription: "LedgerPOS",
    }),
  });
  const pj = await pushRes.json().catch(() => null);
  if (!pushRes.ok || !pj?.response || String(pj.response?.ResponseCode ?? "") !== "0") {
    const desc = String(pj?.header?.statusDescription ?? pj?.fault?.description ?? `kcb ${pushRes.status}`);
    return json({ ok: false, error: desc }, 502);
  }
  const checkoutID = String(pj.response.CheckoutRequestID ?? "");
  if (!checkoutID) {
    return json({ ok: false, error: "kcb returned no checkout id" }, 502);
  }
  // Push mapping: the callback route back to the pushing team.
  const ins = await fetch(`${SB_URL}/rest/v1/buni_pushes`, {
    method: "POST",
    headers: {
      apikey: SB_KEY,
      Authorization: `Bearer ${SB_KEY}`,
      "Content-Type": "application/json",
      Prefer: "resolution=merge-duplicates,return=minimal",
    },
    body: JSON.stringify({
      checkout_request_id: checkoutID,
      reference: clientUUID,
      order_number: orderNumber,
      team_code: team,
      device_id: String(b.p_device_id ?? ""),
      amount_cents: Math.round(cents),
    }),
  });
  if (!ins.ok) {
    // Without the mapping the event can't be claimed — fail loudly so the
    // till marks the leg FAILED and the cashier re-pushes.
    return json({ ok: false, error: "push mapping unavailable" }, 503);
  }
  return json({
    ok: true,
    merchant_request_id: String(pj.response.MerchantRequestID ?? ""),
    checkout_request_id: checkoutID,
    customer_message: String(pj.response.CustomerMessage ?? "").slice(0, 120),
    invoice_number: invoiceNumber,
  });
});
