// paystack-webhook — Edge Function (Deno). PUBLIC endpoint.
// The Paystack dashboard webhook URL points here. Verifies the HMAC-SHA512
// signature with the secret from Deno.env (dual-read fallback: app_config
// vault row) in constant time, then stores the VERIFIED event for tills to
// claim through the device-gated paystack_claim_events RPC. Tills run the
// event through their existing exactly-once completion path.
//
// Paystack retries on non-2xx: verified events always answer 200 (fast,
// storage-only); bad signatures answer 401; unconfigured key answers 503.
import { createHmac } from "node:crypto";

const SB_URL = Deno.env.get("SUPABASE_URL") ?? "";
const SB_KEY = Deno.env.get("SUPABASE_SERVICE_ROLE_KEY") ?? "";

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
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

// constant-time hex compare (never early-exits on a mismatch)
function safeEqualHex(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return diff === 0;
}

Deno.serve(async (req) => {
  if (req.method !== "POST") return json({ ok: false, error: "POST only" }, 405);
  // Size cap BEFORE reading the body: this is the only fully-public cloud
  // endpoint, and the signature check is HMAC over the raw bytes — a huge
  // body would cost memory + HMAC CPU before any auth (the local Go twin
  // caps at 1 MB, handlers/paystack.go). Real Paystack events are KBs;
  // 256 KB is ~100x headroom.
  const len = Number(req.headers.get("content-length") ?? "0");
  if (Number.isFinite(len) && len > 256 * 1024) {
    return json({ ok: false, error: "body too large" }, 413);
  }
  const raw = new Uint8Array(await req.arrayBuffer());
  if (raw.byteLength > 256 * 1024) {
    return json({ ok: false, error: "body too large" }, 413);
  }
  const signature = (req.headers.get("x-paystack-signature") ?? "").trim().toLowerCase();
  const key = await paystackKey();
  if (!key.startsWith("sk_")) return json({ ok: false, error: "not configured" }, 503);
  if (!signature) return json({ ok: false, error: "missing signature" }, 401);

  const mac = createHmac("sha512", key).update(raw).digest("hex");
  if (!safeEqualHex(mac, signature)) {
    return json({ ok: false, error: "invalid signature" }, 401);
  }

  let ev: Record<string, unknown>;
  try {
    ev = JSON.parse(new TextDecoder().decode(raw));
  } catch {
    return json({ ok: false, error: "invalid JSON after signature check" }, 400);
  }
  const eventType = String(ev?.event ?? "");
  const data = (ev?.data ?? {}) as Record<string, unknown>;
  const reference = String(data?.reference ?? "");
  if (!eventType) return json({ ok: false, error: "missing event type" }, 400);

  // Store the VERIFIED event. Tills claim it by their own pending reference.
  const ins = await fetch(`${SB_URL}/rest/v1/paystack_webhook_events`, {
    method: "POST",
    headers: {
      apikey: SB_KEY,
      Authorization: `Bearer ${SB_KEY}`,
      "Content-Type": "application/json",
      Prefer: "return=minimal",
    },
    body: JSON.stringify({
      event_type: eventType,
      reference,
      payload: ev,
    }),
  });
  if (!ins.ok) {
    // Storage hiccup: 503 makes Paystack retry; the event is not lost.
    return json({ ok: false, error: "storage unavailable" }, 503);
  }
  return json({ ok: true, received: eventType, reference });
});
