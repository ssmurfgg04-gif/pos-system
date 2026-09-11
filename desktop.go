package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"posapp/internal/config"
)

// Desktop mode: the binary is a self-contained local app. It resolves a
// per-OS user data directory (SQLite DB, backups, logs live there), binds
// 127.0.0.1 only, and opens the default browser once the server is up.
// Running with no arguments always means desktop mode — "download,
// double-click, sell".

const (
	appName            = "LedgerPOS"
	desktopDefaultPort = 8765
	portFile           = "app.port"
)

var (
	runtimeGOOS   = runtime.GOOS
	runtimeGOARCH = runtime.GOARCH
)

// runDesktop returns a process exit code.
func runDesktop() int {
	// Windows: an exe launched from Downloads/Desktop installs itself
	// (copy to %LOCALAPPDATA%\Programs, shortcuts, uninstall entry) and
	// relaunches the installed copy. Other platforms: no-op.
	if desktopBeforeRun() {
		return 0
	}

	dir, err := userDataDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: cannot resolve a user data directory: %v\n", appName, err)
		return 1
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "%s: cannot create %s: %v\n", appName, dir, err)
		return 1
	}
	// Relative paths (backup snapshots etc.) land in the data dir.
	_ = os.Chdir(dir)
	setupDesktopLog(dir)

	dbPath := filepath.Join(dir, "pos.db")
	firstRun := !fileExists(dbPath)

	// Single instance: if our server already answers, just open a tab.
	if port := readPortFile(dir); port != "" && probeOurs(port) {
		log.Printf("%s already running on port %s — opening browser", appName, port)
		openBrowser("http://127.0.0.1:" + port)
		return 0
	}

	port := pickPort()
	if port == "" {
		fmt.Fprintf(os.Stderr, "%s: no free local port between %d and %d\n", appName, desktopDefaultPort, desktopDefaultPort+49)
		return 1
	}
	_ = os.WriteFile(filepath.Join(dir, portFile), []byte(port), 0o644)

	// Server-mode knobs are env-driven; desktop pins them explicitly.
	_ = os.Setenv("PORT", port)
	_ = os.Setenv("DB_PATH", dbPath)
	_ = os.Setenv("MDNS_ENABLED", "false")

	log.Printf("%s %s starting — data: %s — http://127.0.0.1:%s", appName, version, dir, port)

	quit := make(chan struct{})
	meta := &desktopMeta{
		Port:     port,
		FirstRun: firstRun,
		OnReady:  func() { openBrowser("http://127.0.0.1:" + port) },
	}
	startApp(config.Load(), "127.0.0.1:"+port, meta, quit)
	return 0
}

// userDataDir returns the per-OS application data directory.
func userDataDir() (string, error) {
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("APPDATA")
		if base == "" {
			return "", errors.New("APPDATA is not set")
		}
		return filepath.Join(base, appName), nil
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", appName), nil
	default: // linux and the BSDs
		if x := os.Getenv("XDG_DATA_HOME"); x != "" {
			return filepath.Join(x, appName), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "share", appName), nil
	}
}

// setupDesktopLog appends to <data>/logs/desktop.log. On a windowsgui
// build there is no console, so the log file is the only trace.
func setupDesktopLog(dir string) {
	_ = os.MkdirAll(filepath.Join(dir, "logs"), 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "logs", "desktop.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return // stderr fallback
	}
	log.SetOutput(io.MultiWriter(f))
	log.SetFlags(log.LstdFlags)
	log.Printf("---- %s %s session ----", appName, version)
}

// probeOurs reports whether our own health endpoint answers on the port.
func probeOurs(port string) bool {
	client := &http.Client{Timeout: 700 * time.Millisecond}
	resp, err := client.Get("http://127.0.0.1:" + port + "/api/v1/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	ct := resp.Header.Get("Content-Type")
	return resp.StatusCode == 200 && strings.Contains(ct, "json")
}

// pickPort returns the first free 127.0.0.1 port in [default, default+50).
func pickPort() string {
	for p := desktopDefaultPort; p < desktopDefaultPort+50; p++ {
		s := strconv.Itoa(p)
		ln, err := net.Listen("tcp", "127.0.0.1:"+s)
		if err == nil {
			_ = ln.Close()
			return s
		}
	}
	return ""
}

func readPortFile(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, portFile))
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(b))
	if _, err := strconv.Atoi(s); err != nil {
		return ""
	}
	return s
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// openBrowser launches the default browser without blocking or failing
// the app (headless machines simply no-op).
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	go func() {
		if err := cmd.Run(); err != nil {
			log.Printf("browser open: %v — open %s manually", err, url)
		}
	}()
}
