//go:build !windows

package main

// Other platforms keep the default-browser behavior for now; the native
// WebView2 window above is Windows-only (darwin/Linux shells welcome).
func openAppWindow(url, _ string) bool {
	openBrowser(url)
	return false
}
