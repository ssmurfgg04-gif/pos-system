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

**The release train is automated: pushing a tag `v*` builds, signs, and
publishes.** `.github/workflows/release.yml` cross-compiles every
installer from the tagged commit (the artifact can never be older than
the source), signs the OTA manifest with the `OTA_SIGNING_KEY` repo
secret, verifies the signature + hashes independently, publishes the
GitHub Release (installers + `checksums.txt` + `ota-assets.json` +
`ota-manifest.sig`), commits the regenerated `downloads/manifest.json`
and `downloads/checksums.txt` back to `main`, and prints the fleet SQL
in the job summary. `workflow_dispatch` runs the same pipeline in dry
run (build + sign + verify, publishes nothing) — rehearse with it.

What the operator still does by hand:

1. **Bump the version + tag**: merge to `main` (CI gates: vet, sqlite
   suite, postgres migration chain, vitest, site build), then
   `git tag v1.4.0 && git push origin v1.4.0`.
2. **Publish the manifest to the fleet**: copy `ota-assets.json` and
   `ota-manifest.sig` from the release VERBATIM into the `app_config`
   rows (the job summary shows the SQL template):

   ```sql
   begin;
   select public.upsert_app_config('update_manifest_tag', 'v1.4.0', false);
   select public.upsert_app_config('update_manifest_notes', '<release notes>', false);
   select public.upsert_app_config('update_manifest_assets',
          '<contents of ota-assets.json>', false);
   select public.upsert_app_config('update_manifest_sig',
          '<contents of ota-manifest.sig>', false);
   commit;
   ```

   The signature covers the exact stored bytes — never re-serialize the
   assets JSON. Tills refuse unsigned/tampered manifests for auto-OTA
   and fall back to GitHub (manual install) — a release without a
   signature simply doesn't auto-propagate; it can't break anyone.
   Tills re-check at boot and every 6h; installs wait for a quiet till
   (`HasUnfinishedWork` — money in flight or a live sale blocks it).

### OTA signing key

- The ed25519 **private** key lives in the repo secret
  `OTA_SIGNING_KEY` (base64, 64 bytes `seed||pub`) so CI signs every
  release; the **public** half is compiled into the fleet binary
  (`internal/update/signing.go` `embeddedSigningPubKey`) and is what
  tills actually trust. Keep an offline copy of the private key in the
  owner's password manager — GitHub Secrets are recoverable only by
  admins, and a lost key means re-rotating the whole fleet.
- **Consistency guard**: the release workflow refuses to sign if the
  secret's public half does not match `embeddedSigningPubKey` (a
  half-done rotation fails in CI, not on 500 tills).
- **Rotation runbook** (compromise, or planned):
  1. `go run scripts/sign_manifest.go keygen`
  2. Update the `OTA_SIGNING_KEY` repo secret with the new private key.
  3. Replace `embeddedSigningPubKey` in `internal/update/signing.go`
     with the new public key and ship it as a release. That release is
     the cutover: tills only trust keys compiled into them, so the new
     key is trusted only after tills install that release.
  4. Until rollout completes, publish manifests signed with the OLD key
     (tills that have not updated yet still enforce the old key). After
     rollout, the old key is dead — treat it as burned.

### Manual fallback (no CI)

`scripts/package_desktop.py` + `scripts/create_release.py` +
`LEDGERPOS_UPDATE_PRIV_KEY=… go run scripts/sign_manifest.go signfile`
still work from a dev box (see git history of this file for the old
step-by-step). CI is the supported path; the fallback is for outages.

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
