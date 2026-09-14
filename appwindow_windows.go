//go:build windows

package main

import (
	"log"
	"net/http"
	"path/filepath"
	"runtime"
	"time"

	"github.com/jchv/go-webview2"
)

// waitForServer blocks until the sidecar answers health (or a timeout),
// so the window never opens on a listener that isn't serving yet.
func waitForServer(url string) {
	client := &http.Client{Timeout: 700 * time.Millisecond}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url + "/api/v1/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// openAppWindow shows the URL in a native OS window backed by the system's
// WebView2 runtime (preinstalled on Windows 10/11) — no browser installation
// involved, no tabs or address bar. The web profile lives under the app data
// dir, keeping shop storage separate from personal browsing.
//
// It blocks until the user closes the window and reports whether it hosted
// a session: true means the caller should shut the server down (the user
// closed the app); false means it fell back to the default browser tab and
// the server must stay up.
func openAppWindow(url, dataDir string) (hosted bool) {
	// The Win32 message loop must stay on one OS thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	waitForServer(url)

	var w webview2.WebView
	func() {
		defer func() {
			// No WebView2 runtime (or init failure): fall through to the
			// default-browser fallback below instead of crashing.
			if recover() != nil {
				w = nil
			}
		}()
		w = webview2.NewWithOptions(webview2.WebViewOptions{
			DataPath: filepath.Join(dataDir, "webprofile"),
		})
	}()
	if w == nil {
		openBrowser(url)
		return false
	}
	defer w.Destroy()
	w.SetTitle(appName)
	w.SetSize(1280, 800, webview2.HintNone)
	w.Navigate(url)
	log.Printf("app window open — close the window to quit %s", appName)
	w.Run() // returns when the user closes the window
	return true
}
