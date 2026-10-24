//go:build ignore

// verify_manifest.go — owner-side OTA manifest verification (run, not
// built). Uses the PUBLIC key embedded in the fleet binary — the exact
// check a till performs before it will auto-install an update:
//
//      go run scripts/verify_manifest.go verify <tag> <assets.json> <sig.txt>
//          prints "SIGNATURE VALID: true/false" — verify BEFORE writing the
//          update_manifest_* rows into app_config.
//
//      go run scripts/verify_manifest.go check-cloud
//          the till's exact flow against the production cloud: fetch the
//          update_manifest_* rows from app_config over anon REST, verify the
//          ed25519 signature, print the manifest (tag + assets). Run this
//          AFTER publishing the rows — it is what every till will see.
//
//      LEDGERPOS_UPDATE_PUB_KEY=<b64> go run scripts/verify_manifest.go ...
//          verify against a different public key (rotation rehearsals).
//
// The assets JSON must be BYTE-IDENTICAL to the update_manifest_assets
// row (the signature covers the raw strings).

package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"posapp/internal/update"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: go run scripts/verify_manifest.go verify <tag> <assets.json> <sig.txt>")
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	switch cmd {
	case "verify":
		if len(args) != 3 {
			fmt.Println("verify needs: <tag> <assets.json> <sig.txt>")
			os.Exit(2)
		}
		assets, err := os.ReadFile(args[1])
		if err != nil {
			fmt.Println("read assets:", err)
			os.Exit(1)
		}
		// signfile signs the file WITHOUT its trailing newline (and CI's
		// independent verifier strips it too) — match that semantics so a
		// file fresh off the release verifies. The app_config row must
		// carry the same bytes: JSON with NO trailing newline.
		assetsJSON := strings.TrimRight(string(assets), "\n\r")
		sig, err := os.ReadFile(args[2])
		if err != nil {
			fmt.Println("read sig:", err)
			os.Exit(1)
		}
		pub, ok := update.UpdateSigningPubKey()
		if !ok {
			fmt.Println("no signing key embedded/env — nothing to verify against")
			os.Exit(1)
		}
		valid := update.VerifyManifestSignature(pub, args[0], assetsJSON, string(sig))
		fmt.Printf("SIGNATURE VALID: %v\n", valid)
		if !valid {
			os.Exit(1)
		}
	case "check-cloud":
		ctx := context.Background()
		m, err := update.FetchCloudManifest(ctx, "")
		if err != nil {
			fmt.Println("cloud manifest fetch failed (this is what tills see):", err)
			os.Exit(1)
		}
		pub, ok := update.UpdateSigningPubKey()
		if !ok {
			fmt.Println("no signing key embedded — tills would accept UNSIGNED manifests (transition mode)")
			os.Exit(1)
		}
		valid := update.VerifyManifestSignature(pub, m.Tag, m.AssetsJSON, m.Sig)
		fmt.Printf("cloud tag: %s\n", m.Tag)
		fmt.Printf("cloud assets: %s\n", m.AssetsJSON)
		fmt.Printf("SIGNATURE VALID: %v\n", valid)
		if !valid {
			fmt.Println("tills will REFUSE this manifest (GitHub fallback, manual install only)")
			os.Exit(1)
		}
		for _, a := range m.Rel.Assets {
			fmt.Printf("  %s  sha256=%s\n", a.Name, a.SHA256)
		}
	default:
		fmt.Printf("unknown command %q\n", cmd)
		os.Exit(2)
	}
}
