# Deployment & Release — LedgerPOS

How a release goes from source to customers' tills, and how the fleet
cloud is operated. Anchored to the repo as of commit `b4d4426` or later.

## Channels (what lives where)

| Artifact | Home | Notes |
|---|---|---|
| Installer binaries (exe/zip/tar.xz) | **GitHub Releases only** | the repo does NOT host binaries (purged 2026-10; ~800 MB of history removed) |
| Landing page + demo + portal | Netlify (`awesomeposs.netlify.app`) | links to Release assets; ships checksums.txt + manifest.json |
| Update manifest | cloud `app_config` (`update_manifest_*`) | **ed25519-signed** (`update_manifest_sig`) |
| Buni/Paystack secrets | Supabase Edge Function secrets | never in the binary or repo |
| Fleet sync + vault | Supabase `ledgerpos-backups` (eu-west-1) | `sync_*` RPCs, device registry, app_config |

## Cutting a release

1. **Bump the version** (netlify_build `VERSION`, app `appVersion` in
   `main.go`'s build stamps, tag) and merge to `main`. CI gates: vet,
   sqlite suite, postgres migration chain, vitest, site build.
2. **Build installers**: `scripts/package_desktop.py` produces the four
   packages under `build/netlify-site/downloads/` (Go cross-builds +
   per-OS packaging; `rsrc_windows_amd64.syso` regenerates locally — it
   is untracked on purpose).
3. **Checksums**: `scripts/write_checksums.py` (or
   `scripts/build_site.py <ver>` regenerates them alongside the site).
4. **Publish the release**: `GITHUB_TOKEN=… python3 scripts/create_release.py v1.4.0`
   — uploads the four assets, deletes stale same-name assets, and
   **regenerates `downloads/manifest.json`** (version, sizes, sha256)
   which the download site renders.
5. **Sign the OTA manifest** (the critical step for auto-updates):

   ```bash
   # one-time (already done for this fleet — keys live OFFLINE):
   go run scripts/sign_manifest.go keygen

   # per release — sign the EXACT app_config values:
   LEDGERPOS_UPDATE_PRIV_KEY=<b64-private> \
     go run scripts/sign_manifest.go signfile v1.4.0 /tmp/assets.json \
     > /tmp/manifest.sig

   # then SQL:
   INSERT INTO app_config (key, value, is_secret)
   VALUES ('update_manifest_sig', '<sig from file>', false)
   ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;
   ```

   `/tmp/assets.json` must be byte-identical to the
   `update_manifest_assets` row (copy it out of the DB, do not
   re-serialize). Tills refuse unsigned/tampered manifests for auto-OTA
   and fall back to GitHub (manual install) — a release without a
   signature simply doesn't auto-propagate; it can't break anyone.
6. **Update the cloud manifest rows** (`update_manifest_tag`,
   `_notes`, `_assets`) in the same transaction as the signature. Tills
   re-check at boot and every 6h; installs wait for a quiet till
   (`HasUnfinishedWork` — money in flight or a live sale blocks it).

## Cloud operations (ledgerpos-backups)

**Edge functions** (deploy via Management API `POST/PATCH
/v1/projects/ixxiqrobcwkvyjtxdkvh/functions`, body = the `index.ts`
source): `buni-stkpush`, `buni-callback` (public + token),
`paystack-init`, `paystack-charge`, `paystack-verify`,
`paystack-webhook` (public + HMAC). Sources are tracked in
`supabase/functions/`.

**Secrets** (Management API `POST /v1/projects/{ref}/secrets`):
`BUNI_CONSUMER_KEY`, `BUNI_CONSUMER_SECRET`, `BUNI_TILL` (7853959),
`BUNI_CALLBACK_TOKEN`, `BUNI_CALLBACK_URL` (the buni-callback URL with
`?token=`), `PAYSTACK_SECRET_KEY`. Rotation is a secrets call — tills
notice on their next payment, no redeploy.

**Rolling out Buni to a shop**: insert `app_config` row
`buni_via_cloud = true`. Tills adopt at boot or within 6h; the local
`buni_enabled` setting is the kill-switch. See docs/PAYMENTS.md.

**Device lifecycle**: approve/revoke via `sync_approve_device` or the
owner portal. Revocation cuts sync + vault + payment proxying for that
device immediately (every RPC re-validates).

## History & repo hygiene

The repo was history-purged (git-filter-repo, forced 2026-10-22): build
artifacts and committed installers are gone from every commit; HEAD is
content-identical to the pre-rewrite HEAD. **Collaborators must
re-clone** (old clones reference dead objects and will push stale
history back if not careful). The gitignore covers `posapp`,
`rsrc_windows_*.syso`, `frontend/dist` — keep it that way.

## Netlify

Build: base `frontend`, command `python3 ../scripts/netlify_build.py`,
publish `dist`, `NODE_VERSION=24` (aligned with CI). The deploy job in
CI also VERIFIES the live site serves this commit's checksums.txt — a
stale site fails the build instead of silently shipping old links.
Zero-secrets fallback: `python3 scripts/build_site.py` then drag
`build/netlify-site/` onto app.netlify.com.

## Rollbacks

- **App**: flip `update_manifest_tag` (+`_assets`, +`_sig` re-signed for
  the OLD version) back — tills auto-install the previous release. Or
  direct-install a previous Release asset manually.
- **Cloud config**: `app_config` rows are versioned by `updated_at`;
  re-set the previous values (and re-sign if manifest rows moved).
- **Edge functions**: redeploy the previous `index.ts` from git history
  (functions sources are tracked in-repo).
