# Off-Site Backup + Owner-Remote Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** No one commutes to copy database files — every night the shop encrypts its snapshot and pushes it off-site by itself with retries, and the owner checks the shop from Nairobi over an encrypted tunnel with a read-only login.

**Architecture:** Hook the existing `StartBackupScheduler` flow: after the local 02:00 VACUUM INTO snapshot lands, enqueue an upload job to a new `internal/offsite` worker (same tick/retry/backoff shape as `printer.Worker`). Payloads are AES-256-GCM encrypted (key from a scrypt-derived passphrase, `golang.org/x/crypto` — already vendored) and PUT to any S3-compatible endpoint via a hand-rolled SigV4 client (stdlib only, no new dependency). Remote visibility is a Tailscale playbook doc + an owner role recipe — zero code.

**Tech Stack:** Go stdlib + `golang.org/x/crypto`; S3-compatible storage (Cloudflare R2 free tier documented as default); Tailscale for remote access.

**Spec:** Owner requirement (no travel for backups, async without anyone present) + `docs/ROADMAP.md` distribution theme.

## Global Constraints

- Repo Go files are mixed tabs/spaces PER FILE — match each file's own style, never run gofmt -w.
- Uploads never block sales: background worker only, failures logged + retried, surfaced in Settings → System (never a modal).
- Secrets (`offsite_secret_key`, `offsite_passphrase`) are masked `__SET__` in the settings API exactly like Daraja secrets (read the masking code in settings handlers first and reuse it).
- CGO-free, no new Go module dependencies.

---

### Task 1: Settings keys for off-site

**Files:**
- Modify: `internal/database/seed.go` (`DefaultSettings` map): add keys below.
- Modify: settings allowlist + secret-masking (find `AllowedKeys`/`isSecretKey` equivalents — mirror `mpesa_consumer_secret` handling exactly).

**Interfaces:**
- Produces keys: `offsite_enabled` ("false"), `offsite_endpoint` ("" e.g. `https://<account>.r2.cloudflarestorage.com`), `offsite_bucket` (""), `offsite_region` ("auto"), `offsite_access_key` (""), `offsite_secret_key` ("" SECRET), `offsite_prefix` ("" defaults to hostname), `offsite_keep` ("30"), `offsite_passphrase` ("" SECRET — shown once at setup, owner keeps a copy).

- [ ] **Step 1: Add keys + masking.**
- [ ] **Step 2: Compile** (`go build ./internal/database/ ./internal/settings/`, expect 0).
- [ ] **Step 3: Commit**

```bash
git add internal/database/seed.go internal/settings/settings.go internal/handlers/settings.go
git commit -m "feat: off-site backup settings keys with secret masking"
```

### Task 2: `internal/offsite` — encrypt + SigV4 PUT client

**Files:**
- Create: `internal/offsite/offsite.go` (new package, tabs): `EncryptFile(srcPath, passphrase) (encPath string, err error)`, `PutObject(ctx, cfg, key, body) error`, `ListObjects`, `DeleteObject`, `Config` struct + `ConfigFromSettings`.
- Test: `internal/offsite/offsite_test.go`.

**Interfaces:**
- Consumes: nothing internal (pure stdlib + x/crypto).
- Produces: `EncryptFile`, S3 `PutObject/ListObjects/DeleteObject`, `Config{Endpoint, Bucket, Region, AccessKey, SecretKey}`.

- [ ] **Step 1: EncryptFile (AES-256-GCM + scrypt)**

```go
// EncryptFile encrypts srcPath with passphrase and returns the path of the
// ciphertext file (<src>.age-style layout, own format):
// magic[4]="LPB1" | salt[16] | nonce[12] | ciphertext.
// Key = scrypt(passphrase, salt, N=32768, r=8, p=1, keyLen=32).
func EncryptFile(srcPath, passphrase string) (string, error)
```

And `DecryptFile(encPath, passphrase, dstPath)` (needed for restore + tests).

- [ ] **Step 2: Minimal SigV4 S3 client (header auth for all ops)**

Sign `PUT /{bucket}/{key}`, `GET /{bucket}?list-type=2&prefix=…`, `DELETE /{bucket}/{key}` with `Authorization: AWS4-HMAC-SHA256 Credential=…/…/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=…`. Canonical request per AWS docs; `x-amz-content-sha256` = hex payload hash (no unsigned-payload shortcut — R2 accepts it, keeps one code path). Parse ListObjectsV2 XML minimally (encoding/xml, `<Key>` + `<LastModified>` only).

- [ ] **Step 3: Round-trip tests (no network)**

```go
func TestEncryptDecryptRoundTrip(t *testing.T) {
        // write 1MB patterned temp file → EncryptFile → DecryptFile → bytes.Equal
        // wrong passphrase must fail
}
func TestSigV4KnownVector(t *testing.T) {
        // fixed credentials/date/request → expected Signature from AWS's
        // documented example (hardcode the published test-suite value)
}
```

For the SigV4 vector, use the AWS Signature Version 4 Test Suite `get-vanilla` case values hardcoded (method/uri/date/expected signature).

- [ ] **Step 4: Run** `C:\Users\Jackb\tools\go\bin\go.exe test ./internal/offsite/ -count=1` — expect PASS.
- [ ] **Step 5: Commit**

```bash
git add internal/offsite/
git commit -m "feat: encrypted off-site S3 client (no new deps)"
```

### Task 3: Upload worker hooked to the backup scheduler

**Files:**
- Create: `internal/offsite/worker.go`: `Worker{db, settings}` with `Enqueue(snapshotPath)`, `Run(ctx)` tick loop (every 15 min), per-job attempts with exponential backoff (15m → 1h → 4h, then daily), `Status()` (last result per backup: ok/error + timestamp) persisted in a `offsite_jobs` table? No new tables — keep in-memory + audit rows (`BACKUP_UPLOADED` / `BACKUP_UPLOAD_FAILED` via existing `s.Audit`). Remote prune to `offsite_keep` newest after each success.
- Modify: `internal/services/backup.go`: after `BackupNow()` snapshot succeeds and `offsite_enabled == "true"`, call uploader `Enqueue`. Wire worker start in `main.go` next to `StartBackupScheduler` (read the call site first).
- Modify: `internal/handlers/system.go`: `GET /system/offsite` (perm `settings.manage`) → `{enabled, lastUpload, lastError, pending}` from worker status.
- Modify: `internal/router/router.go`: register route.
- Test: `internal/handlers` test with worker pointed at an `httptest` S3 stub? The SigV4 client takes endpoint — point it at `httptest.Server` that records PUTs and returns canned List XML. Assert: enqueue → PUT received with encrypted bytes (not equal to plaintext), prune DELETEs beyond keep.

**Interfaces:**
- Consumes: `offsite.PutObject/ListObjects/DeleteObject/EncryptFile`, `Service.Audit`, `settings.Store`.
- Produces: automatic post-snapshot upload; status endpoint.

- [ ] **Step 1: Worker + hook + status endpoint + route.**
- [ ] **Step 2: Stub-server test.**
- [ ] **Step 3: Full backend suite green.**
- [ ] **Step 4: Commit** (worker, backup hook, handler, router, test).

### Task 4: Restore command + passphrase ceremony

**Files:**
- Modify: `main.go` usage + new `restore` subcommand: `ledgerpos restore-backup --endpoint … --bucket … --key … --passphrase … --out pos.db` (flags, no interactive prompts — scriptable; lists remote keys with `--list`).
- Reuse: `offsite` List/Get/DecryptFile.

**Interfaces:**
- Produces: `ledgerpos restore-backup [--list] --endpoint E --bucket B --key K --passphrase P [--out FILE]`.

- [ ] **Step 1: Implement subcommand** (download latest or named key → decrypt → write; refuse to overwrite without `--force`).
- [ ] **Step 2: Manual verify** against the Task-3 stub? Unit-test decrypt path already covered; verify `--help` output text.
- [ ] **Step 3: Commit.**

### Task 5: Settings UI + owner remote playbook (docs)

**Files:**
- Modify: `frontend/src/pages/Settings.tsx`: System section gains off-site card (enable toggle, endpoint/bucket/region/keys/prefix/keep/passphrase fields reusing the secret-input pattern Daraja fields use, status line from `GET /system/offsite`, Save + `Back up now` stays).
- Create: `docs/REMOTE-OWNER.md`: Tailscale install on shop box (Windows service) + owner phone/laptop join same tailnet; open `http://<shop-tailscale-ip>:<port>`; create Owner role (`reports.view`, `orders.view`, `shifts.manage`? — NO: read-only recipe = `reports.view` + `orders.view` + `products.view` + `audit.view`; shifts.manage lets them close shifts — list it as optional); JWT 12h note; server-mode bind note (Tailscale interface is covered by `:PORT` bind); printer/USB stays local-only warning.
- Test: `npx tsc --noEmit` clean; demo backend: off-site settings keys accepted (settings allowlist mirror), status endpoint stub returns `{enabled:false}`.

- [ ] **Step 1: Settings UI card.**
- [ ] **Step 2: Playbook doc.**
- [ ] **Step 3: Demo parity + `npm test`.**
- [ ] **Step 4: Commit.**

### Task 6: README + full verification

- [ ] **Step 1:** README bullets: encrypted off-site backups (bring-your-own S3/R2), remote owner playbook link.
- [ ] **Step 2:** Full gates: `go build`, `go test ./internal/...`, `npx tsc --noEmit`, `npm test`.
- [ ] **Step 3: Commit.**

## Self-Review

- Spec coverage: no-commute backups (T1–T4: scheduled, encrypted, retried, restorable), async-safe (background worker, never blocks sales), owner-remote (T5 playbook + read-only role recipe).
- Type consistency: `offsite.Config`, `Worker.Enqueue/Status`, `GET /system/offsite`, `ledgerpos restore-backup`, settings keys used verbatim everywhere.
- No new Go dependencies; secrets masked like Daraja; passphrase recovery is honest (owner-kept copy, documented in playbook).
