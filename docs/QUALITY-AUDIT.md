# LedgerPOS engineering quality audit (v1.3.4)

Date: 2026-10-15. Scope: the whole app as shipped in v1.3.3 plus the v1.3.4
fixes. Method: reproduce first, then fix. Every CONFIRMED item below has a
regression test or a documented reproduction; SUSPECTED items are labelled
honestly; UNVERIFIED items could not be exercised from here.

Legend: severity (critical / high / medium / low), status (CONFIRMED /
SUSPECTED / UNVERIFIED), and where the fix or test lives.

## P0 (critical): fixed and pinned

### A1. Every Settings save failed on cloud-connected tills
- Status: CONFIRMED (red-green test).
- Evidence: the Settings page round-trips the entire settings snapshot; on
  any till with the cloud link the snapshot contained runtime bookkeeping
  keys (sync_source, device_id, brand_logo_data, paystack_via_cloud,
  syncstate_*). UpdateSettings rejects non-allowlisted keys with
  "unknown setting key: <k>", so EVERY save failed, including store name.
  Nothing saved, therefore nothing synced: the store-name and branding
  "does not sync" reports trace here.
- Impact: the owner saw "Save failed" for routine edits; other tills never
  received the changes.
- Fix: settings.IsInternalKey classifies runtime keys; the API snapshot
  hides them; save echoes of them are dropped (a stale echo can never
  overwrite live sync state either); genuinely mistyped keys still fail
  loudly with 400.
- Tests: TestSettingsSaveSurvivesRuntimeKeyEchoes (handlers),
  TestSettingsStoreNameSyncsBetweenTills (services, two tills).

### A2. Owner kicked out mid-task by the approval dialog
- Status: CONFIRMED (red-green test).
- Evidence: POST /team-sync/owner-approval answered 401 on wrong portal
  credentials; the frontend session-expiry interceptor wipes pos_token on
  any 401 outside /api/v1/auth/ and redirects to /login. One typo in the
  "Confirm it's you" dialog logged the owner out mid-invite.
- Impact: "it kicks me out of my account when I try to add someone".
- Fix: the endpoint answers 422 for credential problems; the dialog stays
  open for retry. Session-expiry remains exclusively for real dead
  sessions (middleware 401).
- Tests: TestOwnerApprovalBadCredentialKeepsSession (handlers),
  sessioncontract.test.ts (frontend pins 401-vs-422 semantics).

### A3. The owner gate could never pass for owners whose password predates the portal
- Status: CONFIRMED (live evidence + test).
- Evidence: portal_accounts is written ONLY on password saves. The live
  cloud had 0 rows in portal_accounts: this owner never saved a password
  after the v1.3.2 portal existed, so every owner-gated action (invite,
  approve, PIN-reset approval, store create) failed forever with "owner
  approval required".
- Impact: "now the flow doesn't seem to work at all".
- Fix: a successful owner-level password login now self-heals by
  publishing the credential hash (the same call as a password save; the
  plaintext never leaves the machine). One sign-in repairs the gate.
- Tests: TestOwnerLoginPublishesPortalCredential (owner publishes, cashier
  never publishes; plaintext never sent).

### A4. STK prompt cancelled before the customer could pay
- Status: CONFIRMED (4 tests).
- Evidence: the only early-cancel path was the backend sweeper/verify
  flipping the leg to FAILED the moment Paystack's verify said
  abandoned/failed, while the customer was still entering their PIN. The
  cashier screen reads that status and shows failure.
- Impact: customers who took more than a few seconds saw a cancelled
  prompt; cashiers re-pushed or fell back to cash.
- Fix: 10-second STK fail grace. The first terminal verify on an STK leg
  starts the window; during it the leg stays pending and is re-verified
  (5s sweeper tick); success inside the window completes the sale. Popup
  legs fail immediately (unchanged). Late-payment recovery (15-minute
  watch, 90-minute failed sweep, 24-hour recheck, webhook relay) is
  untouched.
- Tests: TestSTKAbandonedStaysPendingThroughGrace,
  TestSTKFailsOnlyAfterGraceExpires, TestPopupAbandonedFailsImmediately,
  TestPaystackVerifySTKGraceViaAPI.

### A5. A revoked till was invisible, making a mistaken revoke unrecoverable
- Status: CONFIRMED (live roster + test).
- Evidence: the cloud sync_roster filtered out revoked rows entirely, so
  the Settings roster showed nothing to un-revoke; recovery required hand
  SQL (which is exactly what happened: DESKTOP-O8A5QMR was revoked by
  mistake and sat invisible for 4 days).
- Impact: days of back-and-forth to restore a working till.
- Fix: sync_roster now keeps revoked rows flagged; the roster shows a
  Revoked pill with an Unrevoke button (Approve-on-revoked un-revokes,
  v1.3.0 semantics kept and pinned). The mistakenly revoked till was
  un-revoked in the live database after a full backup.
- Tests: TestApproveUnrevokesRevokedDevice (roster includes revoked rows,
  approve reaches the cloud for them).

## P1 (high): fixed

### B1. Join links 404ed in the browser and never self-filled in the app
- Status: CONFIRMED.
- Evidence: the website served no /join page (Netlify 404) and Join.tsx
  never read the URL fragment, so a worker who clicked the link got a
  dead end and had to hand-copy the link into a textarea.
- Fix: scripts/assets/join-page.html is a real instructions page with a
  copy-link box (reads the fragment); Join.tsx pre-fills from the
  fragment and shows team/role; the landing page links /join.
- Tests: frontend sessioncontract.test.ts pins the link shape; E2E
  redemption covered backend-side (below).

### B2. Username removal from the join flow
- Status: CONFIRMED (was required by the old dialog).
- Evidence: the owner dialog demanded a username on top of the password;
  links themselves never carried one, but nothing pinned that.
- Fix: the dialog pre-fills the signed-in owner's username; the link
  format is team/role/token only; a test asserts "user" never appears in
  a generated link.
- Tests: sessioncontract.test.ts; Settings.tsx ownerUser prefill.

### B3. Invisible headings on the Join and PIN screens
- Status: CONFIRMED (VLM screenshot review, before/after).
- Evidence: text-on-shell (#eaf0f8, near-white sidebar ink) on bg-shell
  (#f4f5f7, light canvas) produced unreadable headings on both screens.
- Fix: headings and supporting text use text-ink / text-ink-muted; the
  PIN screen's footer link uses the same treatment.
- Tests: visual (VLM review set 2026-10-15), no automated contrast test.

### B4. Em dashes across user-facing copy
- Status: CONFIRMED (826 removed).
- Evidence: scanner over Go strings, TS/TSX strings/JSX, HTML assets,
  SQL string literals, markdown docs.
- Fix: swept to commas/periods/colons/parentheses. Code comments are
  exempt (engineer-facing). Live cloud functions that still carry em
  dashes in rare error strings are a known item until their next
  deliberate redeploy (only sync_roster was redeployed this release).

## Verified sound (no fix needed)

- Sync pipeline: two-till tests through the fake cloud prove store name,
  logo set/clear (file + flag), product create and price change all
  propagate with exactly-once application. (TestSettingsStoreName...,
  TestLogoSetAndClear..., plus the pre-existing cloud identity tests.)
- Join redemption E2E: invite mint on till A, redeem on fresh till B,
  single-use enforcement, role + PIN + onboarding skip.
  (TestTeamJoinLinkRedeemsOnFreshTill.)
- Late-payment recovery paths untouched (tests still green).
- Secrets handling: masked echoes, never served, never logged (existing
  suites).

## SUSPECTED (watch, do not act without field evidence)

- S1. paystack_via_cloud has no production writer: the vault/cloud apply
  path never sets it. Tills appear to get it via app_config distribution;
  if a fresh till ever routes Daraja instead of Paystack, check this
  flag first. (Flag read at payments_paystack_cloud.go; tests set it
  manually.)
- S2. Two test files live outside the vitest include glob
  (src/demo/parity.test.ts, src/moneyinput.regression.test.tsx) and only
  run when pointed at directly. Moving them under __tests__/ would make
  CI cover them; left as-is this release to keep the diff small.
- S3. /join renders only when logged out; a logged-in user who opens a
  join link lands on NotFound. Correct for safety (Join would overwrite
  the session token), but a friendlier "already signed in" screen could
  replace the 404.

## UNVERIFIED from here

- U1. Real printer hardware (test print, drawer kick) — no device in the
  test environment; the API paths are covered by unit tests only.
- U2. macOS installers on Apple Silicon/Intel — packages build and hash,
  but no mac hardware here to install on.
- U3. Paystack dashboard-side settings (webhook URL pointing at the cloud
  relay) — assumed unchanged from v1.3.3, which was field-verified.

## Security question assessment (as requested: do NOT add)

A "simple security question and answer" recovery flow was evaluated and
rejected. Security answers are guessable, rarely rotated, and would sit as
a permanent backdoor beside an owner-consent recovery path (PIN reset
approval + recovery code) that already covers every lockout with stronger
guarantees. Adding the question flow would replace a strong mechanism with
a weaker one for convenience, which the product standard forbids.

## Verification state for this release

- go vet clean; full Go suite green (all packages, 1.23.4).
- tsc clean; frontend vitest 99/99 in the include glob (+2 files outside
  it, see S2).
- 11 new regression/integration tests pin every CONFIRMED fix above.
- Two-client sync verified through the fake cloud (the same RPC contract
  as production); live tills prove the real path nightly.
