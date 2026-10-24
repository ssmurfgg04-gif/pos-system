// Package landelivery gives tills on the same WiFi a narrow, direct line
// to each other. The full API stays on 127.0.0.1 — this listener serves
// exactly two endpoints and nothing else:
//
//	GET  /lan/whoami           → this till's cloud identity (device id and
//	                             name are not secrets; the cloud roster
//	                             already shows them to every team member)
//	POST /lan/pin-reset-code   → deliver a PIN-reset code the owner just
//	                             minted, so the worker on the locked till
//	                             types only their new PIN
//
// Safety: the code POST is accepted ONLY while this till has an active
// pending reset request with the exact id sent, the code is only a PRE-FILL
// (the cloud still verifies and burns it on redeem, max 6 wrong tries), the
// endpoints are rate-limited, and a failure here merely falls back to the
// owner reading the code out loud.
package landelivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"posapp/internal/auth"
	"posapp/internal/mdns"
)

// Identity is what a till tells its siblings about itself.
type Identity struct {
	DeviceID   string `json:"deviceId"`
	DeviceName string `json:"deviceName"`
	Team       string `json:"team"`
	AppVersion string `json:"appVersion"`
}

// Server is the restricted LAN listener.
type Server struct {
	srv      *http.Server
	ln       net.Listener
	Port     int
	identity func() Identity
	// pendingRequestID returns the id of this till's open reset request
	// (0 when none), so a pushed code can only land on a real request.
	pendingRequestID func() int64
	// storeCode pre-fills the code for the given request id.
	storeCode func(requestID int64, code string) error
	rl        *auth.RateLimiter
}

// Start binds 0.0.0.0 on the first free port among port+1..port+9 and
// serves until ctx is cancelled. Best-effort: on failure it returns an
// error and the till simply runs without LAN delivery.
func Start(ctx context.Context, port int, identity func() Identity,
	pendingRequestID func() int64, storeCode func(int64, string) error) (*Server, error) {
	if identity == nil || pendingRequestID == nil || storeCode == nil {
		return nil, errors.New("landelivery: handlers required")
	}
	s := &Server{
		identity:         identity,
		pendingRequestID: pendingRequestID,
		storeCode:        storeCode,
		rl:               auth.NewRateLimiter(time.Minute, 30),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/lan/whoami", s.handleWhoami)
	mux.HandleFunc("/lan/pin-reset-code", s.handlePinResetCode)

	var lastErr error
	for p := port + 1; p <= port+9; p++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", p))
		if err != nil {
			lastErr = err
			continue
		}
		s.ln = ln
		s.Port = p
		break
	}
	if s.ln == nil {
		return nil, fmt.Errorf("landelivery: no free port above %d: %v", port, lastErr)
	}
	s.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(shut)
	}()
	go func() {
		if err := s.srv.Serve(s.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[lan] delivery listener stopped: %v", err)
		}
	}()
	log.Printf("[lan] delivery listening on 0.0.0.0:%d (identity + pin-reset codes only)", s.Port)
	return s, nil
}

func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "identity": s.identity()})
}

type pinResetCodeBody struct {
	RequestID int64  `json:"requestId"`
	Code      string `json:"code"`
}

func (s *Server) handlePinResetCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !s.rl.Allow("lan-code:" + ip) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"ok": false, "error": "too many attempts, wait a minute"})
		return
	}
	defer r.Body.Close()
	var body pinResetCodeBody
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	if err := dec.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad request body"})
		return
	}
	code := strings.TrimSpace(body.Code)
	if len(code) != 8 || !allDigits(code) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"ok": false, "error": "the code must be 8 digits"})
		return
	}
	pending := s.pendingRequestID()
	if pending <= 0 || pending != body.RequestID {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"ok": false, "error": "no matching reset request on this till"})
		return
	}
	if err := s.storeCode(body.RequestID, code); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "could not store the code"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// PushPinResetCode finds the till with the given device id and hands it
// the freshly minted code. Returns true when the code was delivered (the
// till then only asks the worker for their new PIN). The cloud-registered
// LAN address (lanAddr) is tried first — it works even on WiFi networks
// that block multicast — then the mDNS scan. Every step is short-timeout:
// the whole walk costs at most ~4 seconds, and the caller degrades to
// "read the code out loud" on any miss.
func PushPinResetCode(ctx context.Context, deviceID string, requestID int64, code, lanAddr string) bool {
	if deviceID == "" || requestID <= 0 || code == "" {
		return false
	}
	client := &http.Client{Timeout: 800 * time.Millisecond}
	tried := map[string]bool{}
	// The cloud knows where this till last answered: host:port of its LAN
	// listener. A direct unicast needs no multicast at all. Parse strictly:
	// lan_addr is registry text, so anything that is not a bare host:port
	// with an IP host (the @-trick, hostnames, paths) is refused.
	if addr := strings.TrimSpace(lanAddr); addr != "" {
		if u, err := url.Parse("http://" + addr); err == nil && u.Path == "" && u.User == nil {
			if ip := net.ParseIP(u.Hostname()); ip != nil && u.Port() != "" && !ip.IsLoopback() {
				if deliverTo(ctx, client, "http://"+addr, deviceID, requestID, code) {
					return true
				}
				tried[addr] = true
			}
		}
	}
	peers := mdns.Browse(ctx, 1200*time.Millisecond)
	if len(peers) == 0 {
		log.Printf("[lan] no sibling tills discovered, the code stays read-aloud only")
		return false
	}
	if len(peers) > 16 {
		peers = peers[:16]
	}
	for _, p := range peers {
		base := fmt.Sprintf("http://%s:%d", p.Addr, p.Port)
		if tried[strings.TrimPrefix(base, "http://")] {
			continue // already tried the cloud-registered address
		}
		if deliverTo(ctx, client, base, deviceID, requestID, code) {
			return true
		}
	}
	log.Printf("[lan] till %s not reachable on this network, the code stays read-aloud only", deviceID)
	return false
}

// deliverTo talks to one candidate base URL: identify, match, hand over.
func deliverTo(ctx context.Context, client *http.Client, base, deviceID string, requestID int64, code string) bool {
	var who struct {
		OK       bool     `json:"ok"`
		Identity Identity `json:"identity"`
	}
	resp, err := client.Get(base + "/lan/whoami")
	if err != nil {
		return false
	}
	err = json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 8192)).Decode(&who)
	resp.Body.Close()
	if err != nil || !who.OK || who.Identity.DeviceID == "" {
		return false
	}
	if who.Identity.DeviceID != deviceID {
		return false
	}
	payload, _ := json.Marshal(pinResetCodeBody{RequestID: requestID, Code: code})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/lan/pin-reset-code", strings.NewReader(string(payload)))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp2, err := client.Do(req)
	if err != nil {
		log.Printf("[lan] found %s but the code hand-off failed: %v", deviceID, err)
		return false
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	err = json.NewDecoder(http.MaxBytesReader(nil, resp2.Body, 8192)).Decode(&out)
	resp2.Body.Close()
	if err == nil && out.OK {
		log.Printf("[lan] PIN-reset code delivered straight to %s (%s)", deviceID, who.Identity.DeviceName)
		return true
	}
	msg := ""
	if err == nil {
		msg = out.Error
	}
	log.Printf("[lan] %s refused the code hand-off: %s", deviceID, msg)
	return false
}
