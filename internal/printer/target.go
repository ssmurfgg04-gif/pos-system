package printer

import (
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strings"
	"time"
)

// Target strings (configured in settings):
//   tcp://HOST:9100   — network thermal printer (most common)
//   file:///dev/usb/lp0 — USB printer device
//   (empty)           — printing disabled; jobs stay queued harmlessly? No —
//                       Enqueue skips when no target is configured.
type Target string

func (t Target) Enabled() bool { return strings.TrimSpace(string(t)) != "" }

// Open resolves the target to a writer and connects it.
func (t Target) Open() (io.WriteCloser, error) {
	s := strings.TrimSpace(string(t))
	switch {
	case strings.HasPrefix(s, "tcp://"):
		addr := strings.TrimPrefix(s, "tcp://")
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			return nil, fmt.Errorf("printer dial %s: %w", addr, err)
		}
		return conn, nil
	case strings.HasPrefix(s, "file://"):
		if !t.Valid() || s == "" {
			return nil, fmt.Errorf("unsupported printer target %q", s)
		}
		path := strings.TrimPrefix(s, "file://")
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return nil, fmt.Errorf("printer open %s: %w", path, err)
		}
		return f, nil
	default:
		return nil, fmt.Errorf("unsupported printer target %q", s)
	}
}

// Valid reports whether the target parses to a supported scheme.
// file:// is locked to USB printer devices: an unrestricted file target
// would let a compromised admin account truncate arbitrary files (the
// worker opens targets write-only).
func (t Target) Valid() bool {
        s := strings.TrimSpace(string(t))
        if s == "" {
                return true // disabled is valid
        }
        if strings.HasPrefix(s, "tcp://") {
                return true
        }
        if strings.HasPrefix(s, "file:///dev/usb/lp") {
                return true
        }
        if runtime.GOOS == "windows" && strings.HasPrefix(s, "file://COM") {
                return true // COM1:-style receipt printers
        }
        return false
}
