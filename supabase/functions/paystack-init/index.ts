// paystack-init — Edge Function (Deno).
// Creates a Paystack transaction WITHOUT the secret ever touching a till.
// The Go backend calls this when it holds no local key (or the owner flips
// paystack_via_cloud). Auth: till device identity (approved, non-revoked).
// Key resolution (dual-read): Deno.env PAYSTACK_SECRET_KEY first, then the
// app_config vault row (fallback window until the row is retired).
//
// POST /functions/v1/paystack-init
//   { p_device_id, p_secret_hash, email, amount, currency?, reference,
//     callback_url?, metadata? }
//   -> { ok, authorization_url, access_code, reference }
//      | { ok:false, error }  (401 device, 503 no key, 502 paystack)
const SB_URL = Deno.env.get("SUPABASE_URL") ?? "";
const SB_KEY = Deno.env.get("SUPABASE_SERVICE_ROLE_KEY") ?? "";

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

async function deviceOK(deviceId: string, secretHash: string): Promise<boolean> {
  if (!deviceId || !secretHash || !SB_URL || !SB_KEY) return false;
  const url =
    `${SB_URL}/rest/v1/sync_devices?device_id=eq.${encodeURIComponent(deviceId)}` +
    `&secret_hash=eq.${encodeURIComponent(secretHash)}&approved=eq.true&revoked=eq.false` +
    `&select=device_id,team_code`;
  const r = await fetch(url, {
    headers: { apikey: SB_KEY, Authorization: `Bearer ${SB_KEY}` },
  });
  if (!r.ok) return false;
  const rows = await r.json();
  return Array.isArray(rows) && rows.length === 1;
}

// Dual-read: Edge Function secret first, app_config vault row as fallback.
async function paystackKey(): Promise<string> {
  const env = (Deno.env.get("PAYSTACK_SECRET_KEY") ?? "").trim();
  if (env.startsWith("sk_")) return env;
  if (!SB_URL || !SB_KEY) return "";
  const r = await fetch(
    `${SB_URL}/rest/v1/app_config?key=eq.paystack_secret_key&select=value&limit=1`,
    { headers: { apikey: SB_KEY, Authorization: `Bearer ${SB_KEY}` } },
  );
  if (!r.ok) return "";
  const rows = await r.json();
  const v = Array.isArray(rows) && rows[0] ? String(rows[0].value ?? "") : "";
  return v.trim();
}

Deno.serve(async (req) => {
  if (req.method !== "POST") return json({ ok: false, error: "POST only" }, 405);
  let b: Record<string, unknown>;
  try {
    b = await req.json();
  } catch {
    return json({ ok: false, error: "invalid JSON" }, 400);
  }
  const deviceId = String(b.p_device_id ?? "");
  const secretHash = String(b.p_secret_hash ?? "");
  if (!(await deviceOK(deviceId, secretHash))) {
    return json({ ok: false, error: "device not recognized" }, 401);
  }
  const key = await paystackKey();
  if (!key.startsWith("sk_")) {
    return json({ ok: false, error: "payment key not configured" }, 503);
  }
  const email = String(b.email ?? "").trim();
  const amount = Number(b.amount ?? 0);
  if (!email || !Number.isFinite(amount) || amount <= 0) {
    return json({ ok: false, error: "email and a positive amount are required" }, 400);
  }
  const payload: Record<string, unknown> = { email, amount: Math.round(amount) };
  if (b.currency) payload.currency = String(b.currency);
  if (b.reference) payload.reference = String(b.reference);
  if (b.callback_url) payload.callback_url = String(b.callback_url);
  if (b.metadata !== undefined) payload.metadata = b.metadata;
  const pr = await fetch("https://api.paystack.co/transaction/initialize", {
    method: "POST",
    headers: {
      Authorization: `Bearer ${key}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify(payload),
  });
  const pj = await pr.json().catch(() => null);
  if (!pr.ok || !pj?.status || !pj?.data?.authorization_url) {
    return json(
      { ok: false, error: pj?.message ?? `paystack ${pr.status}` },
      502,
    );
  }
  return json({
    ok: true,
    authorization_url: pj.data.authorization_url,
    access_code: pj.data.access_code ?? "",
    reference: pj.data.reference ?? String(b.reference ?? ""),
  });
});
