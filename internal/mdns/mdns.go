// Package mdns broadcasts _pos-server._tcp.local so LAN terminals can
// discover the server automatically (pattern proven by GO-POS-Server,
// implemented with graceful shutdown), and browses for sibling tills so
// tills on the same WiFi can talk to each other directly (PIN-reset code
// hand-off). Both halves are best-effort: multicast is unsupported on many
// networks and must never take the POS down.
package mdns

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"
)

// Service is the DNS-SD service both Broadcast and Browse agree on.
const Service = "_pos-server._tcp"

// Domain for registration and browsing.
const Domain = "local."

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
		server, err := zeroconf.Register(instance, Service, Domain, port,
			[]string{"version=2.0.0"}, nil)
		if err != nil {
			log.Printf("[mdns] broadcast unavailable: %v", err)
			return
		}
		defer server.Shutdown()
		<-ctx.Done()
	}()
}

// Peer is one discovered till on the LAN.
type Peer struct {
	Addr string // IP without port
	Port int
}

// Browse scans the LAN for sibling tills for up to `timeout`, returning
// every distinct address:port that answered. Errors are logged and returned
// empty — callers treat an empty list as "nothing found, fall back".
var Browse = func(ctx context.Context, timeout time.Duration) []Peer {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		log.Printf("[mdns] browse unavailable: %v", err)
		return nil
	}
	entries := make(chan *zeroconf.ServiceEntry, 32)
	bctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	seen := map[string]bool{}
	var peers []Peer
	done := make(chan struct{})
	go func() {
		for e := range entries {
			if e == nil || e.Port <= 0 || len(e.AddrIPv4) == 0 {
				continue
			}
			addr := e.AddrIPv4[0].String()
			key := fmt.Sprintf("%s:%d", addr, e.Port)
			if seen[key] {
				continue
			}
			seen[key] = true
			peers = append(peers, Peer{Addr: addr, Port: e.Port})
		}
		close(done)
	}()
	if err := resolver.Browse(bctx, Service, Domain, entries); err != nil {
		log.Printf("[mdns] browse failed: %v", err)
		cancel()
		return peers
	}
	select {
	case <-bctx.Done():
	case <-done:
	}
	// Give late answers a brief grace window already covered by bctx.
	return peers
}

// LANIP returns this machine's preferred outbound LAN address (for logs),
// or the loopback when nothing else is available.
func LANIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:53")
	if err == nil {
		defer conn.Close()
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
			return addr.IP.String()
		}
	}
	return "127.0.0.1"
}
