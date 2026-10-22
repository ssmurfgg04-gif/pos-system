// buni-callback — Edge Function (Deno). PUBLIC endpoint.
// KCB Buni posts the STK result here (callbackUrl sent on every push).
// Buni provides NO signature header on callbacks — the auth model is:
//   1. a secret token embedded in the callback URL (BUNI_CALLBACK_TOKEN,
//      constant-time compared; URL never passes through any till);
//   2. the payload alone can NEVER complete a sale — it is only stored;
//      the till claims it through the device-gated buni_claim_events RPC
//      and runs it through the fail-closed stored-amount completion guard.
// Result shape (production-verified): Daraja envelope —
//   {"Body":{"stkCallback":{"MerchantRequestID","CheckoutRequestID",
//    "ResultCode":0|1032|1037|2001,"ResultDesc",...,
//    "CallbackMetadata":{"Item":[{"Name":"Amount","Value":1},...]}}}}
//
// Buni retries on non-2xx: stored events always answer 200; bad token
// answers 401 (do not retry — someone probing); storage hiccup 503.
const SB_URL = Deno.env.get("SUPABASE_URL") ?? "";
const SB_KEY = Deno.env.get("SUPABASE_SERVICE_ROLE_KEY") ?? "";

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

// constant-time compare (never early-exits on a mismatch)
function safeEqual(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return diff === 0;
}

Deno.serve(async (req) => {
  if (req.method !== "POST") return json({ ok: false, error: "POST only" }, 405);
  const want = (Deno.env.get("BUNI_CALLBACK_TOKEN") ?? "").trim();
  if (!want || !SB_URL || !SB_KEY) return json({ ok: false, error: "not configured" }, 503);
  const u = new URL(req.url);
  const got = (u.searchParams.get("token") ?? req.headers.get("x-buni-callback-token") ?? "").trim();
  if (!got || !safeEqual(got, want)) {
    return json({ ok: false, error: "invalid token" }, 401);
  }

  let raw: Record<string, unknown>;
  try {
    raw = await req.json();
  } catch {
    return json({ ok: false, error: "invalid JSON" }, 400);
  }
  const cb = (raw?.Body as Record<string, unknown> | undefined)?.stkCallback as
    | Record<string, unknown>
    | undefined;
  const checkoutID = String(cb?.CheckoutRequestID ?? "");
  if (!checkoutID) {
    return json({ ok: false, error: "missing CheckoutRequestID" }, 400);
  }
  const resultCode = Number(cb?.ResultCode ?? -1);

  // Resolve the push mapping (team scope + order reference). Unknown
  // checkout ids are still stored (team_code='') for audit — KCB must get
  // a fast 200 either way; claims simply never match them.
  let reference = "";
  let teamCode = "";
  const q =
    `${SB_URL}/rest/v1/buni_pushes?checkout_request_id=eq.${encodeURIComponent(checkoutID)}` +
    `&select=reference,team_code&limit=1`;
  const pr = await fetch(q, {
    headers: { apikey: SB_KEY, Authorization: `Bearer ${SB_KEY}` },
  });
  if (pr.ok) {
    const rows = await pr.json();
    if (Array.isArray(rows) && rows[0]) {
      reference = String(rows[0].reference ?? "");
      teamCode = String(rows[0].team_code ?? "");
    }
  }

  const ins = await fetch(`${SB_URL}/rest/v1/buni_callback_events`, {
    method: "POST",
    headers: {
      apikey: SB_KEY,
      Authorization: `Bearer ${SB_KEY}`,
      "Content-Type": "application/json",
      Prefer: "return=minimal",
    },
    body: JSON.stringify({
      event_type: "stk.result",
      checkout_request_id: checkoutID,
      reference,
      team_code: teamCode,
      result_code: Number.isFinite(resultCode) ? Math.round(resultCode) : -1,
      payload: raw,
    }),
  });
  if (!ins.ok) {
    // Storage hiccup: 503 makes Buni retry; the event is not lost.
    return json({ ok: false, error: "storage unavailable" }, 503);
  }
  return json({ ok: true, received: "stk.result", checkoutRequestId: checkoutID });
});
