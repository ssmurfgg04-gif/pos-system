//go:build !windows

package handlers

// shellOpen is Windows-only; other systems use open / xdg-open.
func shellOpen(string) bool { return false }
