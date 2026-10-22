# Access Control — LedgerPOS

Who can do what, enforced where, and how the pieces are tested. This
document describes the code as it exists (commit `b4d4426` onward).

## Identities

**Users** live per-shop in the local SQLite store (`users`, `roles`
tables). The owner creates staff with a username + 4-digit PIN; roles map
to permission strings (`p.Can(perm)` — `internal/auth/middleware.go`).
Sessions are 12-hour JWTs signed with the machine's `jwt_secret`
(never exposed via the settings API — `internal/settings/settings.go`
`Snapshot()` deliberately omits it, and tokens never rotate over the API).

**Session invalidation is credential-bound**: every token carries an
`iat`; the auth middleware compares it to the user's
`password_changed_at` — a password/PIN change kills all sessions issued
before it (`internal/router/router.go`, auth middleware block). PIN
changes ride the same rule (PIN reset flow, `internal/services/pinreset.go`).

**Devices** are the cloud identity: a random 32-byte secret generated
once per installation (`deviceSecret()`), of which only the SHA-256 hash
ever leaves the machine. The cloud stores the hash on `sync_devices`
with `approved` / `revoked` flags; every device-gated RPC and Edge
Function re-validates approval + revocation on every call. Revoking a
device cuts off sync, the config vault, and payment proxying in one
place. There is no device "token" to steal — the secret never travels.

**Owner capability tokens** gate multi-store operations (create store,
assign devices). They are minted by the owner sign-in RPC
(`owner_device_signin` / `owner_mint_capability`), carried per-call, and
never stored on tills.

## Route gating

The router (`internal/router/router.go`) splits endpoints into:

- **Public**: health, login, PIN exchange, `/auth/pin-users` (rate
  limited 60/min/IP — REVIEW-3 hardening), branding, the M-Pesa callback
  (fail-closed amount gate), the embedded SPA.
- **Auth-required + permission-gated**: everything else. Gates are
  declared per-route with `perm("pos.sell")` style calls; the middleware
  resolves the shop from the token claim, loads a FRESH principal from
  that shop's database (a deleted or deactivated account loses access
  immediately), and rejects cross-shop tokens.

Tokens travel as `Authorization: Bearer …` headers — never cookies —
so CSRF is structurally out of scope; the CORS allowlist (same-origin +
explicit dev origins only, wildcard removed in Batch A) blocks other
origins from even reading responses.

## The browser surface

The same SPA runs in the desktop webview and (for the demo) on Netlify.
Both serving layers set identical security headers
(`secureHeaders()` in the router; `[[headers]]` in `netlify.toml`):

- **CSP**: `script-src 'self' https://js.paystack.co` — no inline
  handlers, no other third-party script can ever run. This is the
  control that makes the localStorage session token acceptable: the
  classic "XSS reads localStorage" needs an injected script origin,
  which the CSP refuses. (The Paystack popup runs in its own frame and
  cannot read this origin's storage.)
- `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`,
  `Permissions-Policy` denies camera/mic/geolocation,
  `frame-ancestors 'self'`.

Token storage is centralized behind `setToken/clearToken/token` in
`frontend/src/lib/api.ts` — one adapter, so an httpOnly-cookie session
can be adopted later without touching the 9 call sites.

## Money permissions

- Price override at checkout requires `payments.override_price`
  (`internal/services/orders.go`).
- Order-level discounts are permission-gated and validated against the
  subtotal.
- Voiding a PAID order records a refund requirement instead of reopening
  the sale; money that arrives after a void is flagged for refund
  (`completePayment`'s voided-order branch).
- Manual receipt entry is permission-gated; receipt codes are unique per
  database (partial unique index on `payments.mpesa_receipt`).

## Secret handling

| Secret | Lives in | Never in |
|---|---|---|
| Buni consumer key/secret | Edge Function secrets (`BUNI_*`) | the binary, settings, DB, repo |
| Paystack secret key | Edge Function secret + device-gated vault RPC | the binary |
| jwt_secret | local settings, masked in API | any response payload |
| device secret | local settings | sync payloads (hash only) |
| manifest signing key | offline (owner) | repo, cloud, tills (public key only) |
| OTA callback token (buni) | Edge Function secret | tills, repo |

Settings API masking: `Snapshot()` masks secret-shaped values and hides
internal bookkeeping keys entirely; `AllowedKeys()` is the API-writable
allowlist (`internal/settings/settings.go`).

## Tests that hold the line

- `internal/handlers/mpesa_amount_gate_test.go` — forged callbacks cannot
  complete sales (wrong amount, zero amount, then the real-amount heal).
- `internal/handlers/security_headers_test.go` — CSP present on API and
  public callback responses.
- `internal/handlers/adversarial_probe_test.go` — credential spray,
  escalation, and lockout probes.
- `internal/services/vault_probe_test.go` — device approval/revocation
  gating, vault secrecy before registration, offline behavior.
- `internal/update/signing_test.go` — unsigned/tampered manifests are
  refused; the embedded key parses.
- `internal/services/payments_buni_test.go` — the relay cannot complete
  on a wrong amount; duplicate delivery completes nothing twice.
