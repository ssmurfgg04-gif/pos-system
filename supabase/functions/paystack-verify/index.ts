// paystack-verify — Edge Function (Deno).
// Server-side verification of a Paystack reference (the authority that
// completes a sale). Secret dual-read: Deno.env first, vault row fallback.
// Auth: till device identity (approved, non-revoked).
//
// POST /functions/v1/paystack-verify
//   { p_device_id, p_secret_hash, reference }
//   -> { ok, status, message, data }   // data = Paystack's data object
//      | { ok:false, error }
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
  const reference = String(b.reference ?? "").trim();
  if (!reference) return json({ ok: false, error: "reference required" }, 400);
  const key = await paystackKey();
  if (!key.startsWith("sk_")) {
    return json({ ok: false, error: "payment key not configured" }, 503);
  }
  const pr = await fetch(
    `https://api.paystack.co/transaction/verify/${encodeURIComponent(reference)}`,
    { headers: { Authorization: `Bearer ${key}` } },
  );
  const pj = await pr.json().catch(() => null);
  if (!pr.ok || !pj) {
    return json({ ok: false, error: pj?.message ?? `paystack ${pr.status}` }, 502);
  }
  // Mirror the fields the till's completion guard consumes.
  return json({
    ok: true,
    status: !!pj.status,
    message: String(pj.message ?? ""),
    data: pj.data ?? null,
  });
});
