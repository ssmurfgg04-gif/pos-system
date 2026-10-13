package models

// permissions_test.go — demo parity gate (review 8.12).
// The web demo's permission vocabulary must exactly match the server
// catalog. frontend/src/demo/permissions.catalog.json is the shared source:
// regenerate it whenever PermissionCatalog changes, or this test fails the
// build (and CI with it).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type demoPermission struct {
	Key   string `json:"key"`
	Group string `json:"group"`
	Label string `json:"label"`
}

func TestDemoCatalogMatchesServerCatalog(t *testing.T) {
	// repo root = two levels up from internal/models
	root := filepath.Join("..", "..")
	path := filepath.Join(root, "frontend", "src", "demo", "permissions.catalog.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run the generator and commit the file)", path, err)
	}
	var demo []demoPermission
	if err := json.Unmarshal(raw, &demo); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(demo) != len(PermissionCatalog) {
		t.Fatalf("demo catalog has %d entries, server catalog has %d — regenerate permissions.catalog.json", len(demo), len(PermissionCatalog))
	}
	for i, d := range demo {
		s := PermissionCatalog[i]
		if d.Key != s.Key || d.Group != s.Group || d.Label != s.Label {
			t.Fatalf("entry %d differs:\n  demo   %+v\n  server %+v\nregenerate permissions.catalog.json from internal/models/permissions.go", i, d, s)
		}
	}
	// every server key must be represented exactly once
	seen := map[string]int{}
	for _, d := range demo {
		seen[d.Key]++
	}
	for _, s := range PermissionCatalog {
		if seen[s.Key] != 1 {
			t.Fatalf("permission %s appears %d times in the demo catalog", s.Key, seen[s.Key])
		}
	}
	fmt.Printf("demo catalog parity: %d/%d permissions match\n", len(demo), len(PermissionCatalog))
}
