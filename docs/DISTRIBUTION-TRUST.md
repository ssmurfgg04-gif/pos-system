# Distribution Trust — SmartScreen, signing, and reputation

Why Windows flags the installer, what we do about it at each stage, and
what we deliberately don't do. Grounded in Microsoft's 2026 SmartScreen
docs (reputation doc, May 2026) and independent-developer reports.

## The mechanism (60 seconds)

SmartScreen judges **reputation, not malware**: file-hash reputation +
publisher reputation + download URL + policy. An unknown file from an
unknown publisher warns even when it's clean — "Windows protected your
PC" means *unjudged*, not *malicious*. Warnings accumulate or clear based
on real-world download/execution telemetry. There is no whitelist, no
fast-track, and (since the policy change) EV certificates no longer
bypass anything. Key consequences:

- Every rebuild resets file-hash reputation to zero → freeze one binary
  per version, never re-upload rebuilds.
- Publisher reputation only exists with a consistent signing identity.
- `More info → Run anyway` is a *user override*, not a fix. Training
  users to click through is an anti-pattern (cr0x.net documents a real
  phishing cleanup caused by exactly that habit).

## Stage 0 — now (no certificate): do the free things

1. **Version info in every exe** (done: `scripts/build_installer.py`
   stamps FileDescription/CompanyName/ProductVersion + icon + manifest
   into one generated `resource_windows_amd64.syso`; sources:
   `scripts/assets/app.manifest`, `build/app.ico`). Unsigned exes
   *without* version info look maximally suspicious.
2. **Stable artifacts**: one build per version, byte-identical from CI to
   release asset. Never swap files under a published URL.
3. **Download page honesty** (README + site): exact filename, version,
   date, SHA-256, publisher name as displayed, and More-info → Run-anyway
   steps framed as *verifying*, not dismissing. Never advise disabling
   SmartScreen.
4. **Submit each release** to Microsoft Security Intelligence
   (https://www.microsoft.com/en-us/wdsi/filesubmission) as a Software
   Developer, product = SmartScreen. Free, slow (weeks), sometimes helps.
   Keep submission IDs per release.

## Stage 1 — paid signing (when shops pay for installs)

- **OV certificate** (~$70–200/yr via Sectigo resellers) OR **Azure
  Trusted Signing** (metered, no hardware token to guard — OV keys now
  require HSM/token storage since 2023, which is its own operational
  burden for a two-person shop).
- Sign **installer + exe + updater** with the SAME identity every
  release, SHA-256 (`/fd`, `/td`) with a timestamp server. Expect
  warnings to *shrink*, not vanish: reputation still accumulates.
- Then: sign in CI (key/token in CI secrets, never in repo), keep the
  WDSI submission habit, keep hashes frozen.

Explicitly rejected: EV-for-bypass (dead since the policy change),
self-signed for public distribution (treated as unsigned), telling
users to disable SmartScreen, ZIP-repackaging tricks (MOTW + hash
reputation follow the bytes, not the container).

## Stage 2 — if warnings still hurt (later)

- **Microsoft Store (MSIX)**: Store-signed packages don't warn. Costs a
  dev account + packaging work + Store policies; revisit when Windows
  installs dominate support load.
- **Enterprise shops**: Intune/GPO/WDAC allow rules keyed on signer —
  only possible once Stage 1 gives us a stable signer identity.

## Checklist per release (until Stage 1)

- [ ] Built once via `scripts/build_installer.py <version>` (stamped exe)
- [ ] SHA-256 published next to the download link
- [ ] Submitted to WDSI as Software Developer (record the ID)
- [ ] Never re-uploaded a rebuild under the same asset name
