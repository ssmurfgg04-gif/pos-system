// paystack-charge — Edge Function (Deno).
// Opens a direct charge (M-Pesa STK push) WITHOUT the secret ever touching a
// till. Mirrors paystack-init: device-gated, secret dual-read (Deno.env
// PAYSTACK_SECRET_KEY first, app_config vault row fallback until retired).
// The Go backend calls this when it holds no local key (or the owner flips
// paystack_via_cloud). Completion is NEVER proven here — the sweeper
// (transaction/verify via paystack-verify) and the charge.success webhook
// (paystack-webhook) own completion, exactly as with the local-key path.
//
// POST /functions/v1/paystack-charge
//   { p_device_id, p_secret_hash, email, amount, currency?, reference?,
//     mobile_money: { phone, provider? }, metadata? }
//   -> { ok, reference, display_text, status }
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
  const mm = (b.mobile_money ?? {}) as Record<string, unknown>;
  const phone = String(mm.phone ?? "").trim();
  if (!email || !Number.isFinite(amount) || amount <= 0 || !phone) {
    return json({ ok: false, error: "email, a positive amount and mobile_money.phone are required" }, 400);
  }
  const mmPayload: Record<string, unknown> = { phone };
  if (mm.provider) mmPayload.provider = String(mm.provider);
  const payload: Record<string, unknown> = {
    email,
    amount: Math.round(amount),
    mobile_money: mmPayload,
  };
  if (b.currency) payload.currency = String(b.currency);
  if (b.reference) payload.reference = String(b.reference);
  if (b.metadata !== undefined) payload.metadata = b.metadata;
  const pr = await fetch("https://api.paystack.co/charge", {
    method: "POST",
    headers: {
      Authorization: `Bearer ${key}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify(payload),
  });
  const pj = await pr.json().catch(() => null);
  if (!pr.ok || !pj?.status || !pj?.data?.reference) {
    return json({ ok: false, error: pj?.message ?? `paystack ${pr.status}` }, 502);
  }
  const d = pj.data as Record<string, unknown>;
  return json({
    ok: true,
    reference: String(d.reference ?? ""),
    display_text: String(d.display_text ?? ""),
    status: String(d.status ?? "pending"),
  });
});
