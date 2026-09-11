// Package mdns broadcasts _pos-server._tcp.local so LAN terminals can
// discover the server automatically (pattern proven by GO-POS-Server,
// implemented with graceful shutdown).
package mdns

import (
	"context"
	"log"
	"os"
	"strings"

	"github.com/grandcat/zeroconf"
)

// Broadcast registers the service until the context is cancelled. Failures
// are logged and swallowed — multicast is unsupported in many networks and
// must never take the POS down.
func Broadcast(ctx context.Context, instance string, port int) {
	go func() {
		host, _ := os.Hostname()
		instance = strings.TrimSpace(instance)
		if instance == "" {
			instance = "POS " + host
		}
		server, err := zeroconf.Register(instance, "_pos-server._tcp", "local.", port,
			[]string{"version=1.0.0"}, nil)
		if err != nil {
			log.Printf("[mdns] broadcast unavailable: %v", err)
			return
		}
		defer server.Shutdown()
		<-ctx.Done()
	}()
}
