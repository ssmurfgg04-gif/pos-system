//go:build ignore

// sign_manifest.go — owner-side OTA manifest signing tool (run, not built):
//
//	go run scripts/sign_manifest.go keygen
//	    prints a fresh ed25519 keypair (base64). Put the PUBLIC key into
//	    internal/update/signing.go (embeddedSigningPubKey) and ship a
//	    release; the PRIVATE key goes into the repo secret
//	    OTA_SIGNING_KEY (CI signs every release manifest) AND into an
//	    offline copy (password manager / HSM) — GitHub Secrets are
//	    admin-recoverable only, and a lost key means re-rotating the fleet.
//
//	LEDGERPOS_UPDATE_PRIV_KEY=<b64> go run scripts/sign_manifest.go sign \
//	    <tag> <assetsJSON>
//	    prints the base64 signature for the manifest — store it in the
//	    cloud's app_config row update_manifest_sig. <assetsJSON> must be
//	    BYTE-IDENTICAL to the update_manifest_assets row (the signature
//	    covers the raw strings).
//
//	LEDGERPOS_UPDATE_PRIV_KEY=<b64> go run scripts/sign_manifest.go signfile \
//	    <tag> <path-to-assets.json>
//	    same, reading the assets JSON from a file (no shell-escaping
//	    accidents with the big asset array).
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
)

func main() {
	cmd := ""
	args := []string{}
	if len(os.Args) > 1 {
		cmd = os.Args[1]
		args = os.Args[2:]
	}
	switch cmd {
	case "keygen":
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			fail("keygen: %v", err)
		}
		fmt.Println("PUB:  " + base64.StdEncoding.EncodeToString(pub))
		fmt.Println("PRIV: " + base64.StdEncoding.EncodeToString(priv))
		fmt.Println("# PUB goes into internal/update/signing.go (embeddedSigningPubKey) — ship a release.")
		fmt.Println("# PRIV goes into the repo secret OTA_SIGNING_KEY (CI signs releases with it)")
		fmt.Println("# AND into an offline copy. Anyone holding it can push updates to every till.")
	case "sign", "signfile":
		privB64 := strings.TrimSpace(os.Getenv("LEDGERPOS_UPDATE_PRIV_KEY"))
		if privB64 == "" {
			fail("LEDGERPOS_UPDATE_PRIV_KEY env var not set")
		}
		if len(args) != 2 {
			fail("usage: sign <tag> <assetsJSON>  |  signfile <tag> <assets-file>")
		}
		tag, assets := args[0], args[1]
		if cmd == "signfile" {
			raw, err := os.ReadFile(assets)
			if err != nil {
				fail("read %s: %v", assets, err)
			}
			assets = strings.TrimRight(string(raw), "\n\r")
		}
		raw, err := base64.StdEncoding.DecodeString(privB64)
		if err != nil || len(raw) != ed25519.PrivateKeySize {
			fail("bad private key (want base64 ed25519, 64 bytes)")
		}
		payload := append([]byte("update_manifest_v1\n"+tag+"\n"), assets...)
		sig := ed25519.Sign(ed25519.PrivateKey(raw), payload)
		fmt.Println(base64.StdEncoding.EncodeToString(sig))
	default:
		fail("usage: keygen | sign <tag> <assetsJSON> | signfile <tag> <assets-file>")
	}
}

func fail(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "sign_manifest: "+f+"\n", a...)
	os.Exit(1)
}
