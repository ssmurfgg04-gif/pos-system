package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// chromiumAppBrowser must return an existing binary or "" (fallback path).
func TestChromiumAppBrowserContract(t *testing.T) {
	got := chromiumAppBrowser()
	if got == "" {
		t.Skip("no Chromium browser installed — fallback path")
	}
	if !fileExists(got) {
		t.Fatalf("chromiumAppBrowser returned missing binary %q", got)
	}
}

// Unknown platform resolves to no browser (default-browser fallback).
func TestChromiumAppBrowserUnknownOS(t *testing.T) {
	oldGOOS, oldArch := runtimeGOOS, runtimeGOARCH
	runtimeGOOS = "plan9"
	defer func() { runtimeGOOS, runtimeGOARCH = oldGOOS, oldArch }()
	if got := chromiumAppBrowser(); got != "" {
		t.Fatalf("expected no browser on plan9, got %q", got)
	}
}

// A fake Edge install is discovered through the real lookup logic.
func TestChromiumAppBrowserFindsEdge(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows lookup paths")
	}
	dir := t.TempDir()
	edge := filepath.Join(dir, "Microsoft", "Edge", "Application", "msedge.exe")
	if err := os.MkdirAll(filepath.Dir(edge), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(edge, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldPF, oldPF86 := os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")
	t.Setenv("ProgramFiles", dir)
	t.Setenv("ProgramFiles(x86)", filepath.Join(dir, "x86-nonexistent"))
	defer func() {
		os.Setenv("ProgramFiles", oldPF)
		os.Setenv("ProgramFiles(x86)", oldPF86)
	}()
	if got := chromiumAppBrowser(); got != edge {
		t.Fatalf("expected fake Edge %q, got %q", edge, got)
	}
}
