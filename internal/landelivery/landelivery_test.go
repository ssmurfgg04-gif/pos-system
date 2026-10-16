package landelivery

// landelivery_test.go — the till-to-till LAN line: identity answers, a
// pushed code only lands on a real, matching reset request, and the
// WiFi hand-off finds the right till (or honestly reports it couldn't).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"posapp/internal/mdns"
)

func startTestServer(t *testing.T, identity Identity, pending func() int64, store func(int64, string) error) *Server {
	t.Helper()
	srv, err := Start(context.Background(), 35000, func() Identity { return identity }, pending, store)
	if err != nil {
		t.Fatalf("start landelivery: %v", err)
	}
	return srv
}

func postCode(t *testing.T, port int, body map[string]any) (int, map[string]any) {
	t.Helper()
	payload, _ := json.Marshal(body)
	resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/lan/pin-reset-code", port), "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestWhoamiServesIdentity(t *testing.T) {
	srv := startTestServer(t, Identity{DeviceID: "dev-1", DeviceName: "TILL-1", Team: "KIAMBU-MAIN", AppVersion: "test"},
		func() int64 { return 0 }, func(int64, string) error { return nil })
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/lan/whoami", srv.Port))
	if err != nil {
		t.Fatalf("GET whoami: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		OK       bool     `json:"ok"`
		Identity Identity `json:"identity"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OK || out.Identity.DeviceID != "dev-1" || out.Identity.Team != "KIAMBU-MAIN" {
		t.Fatalf("whoami = %+v", out)
	}
}

func TestCodeLandsOnlyOnMatchingPendingRequest(t *testing.T) {
	stored := map[int64]string{}
	srv := startTestServer(t, Identity{DeviceID: "dev-1", AppVersion: "test"},
		func() int64 { return 42 },
		func(id int64, code string) error { stored[id] = code; return nil })

	// matching id + well-formed code → stored
	status, out := postCode(t, srv.Port, map[string]any{"requestId": 42, "code": "12345678"})
	if status != http.StatusOK || out["ok"] != true || stored[42] != "12345678" {
		t.Fatalf("matching push = %d %v stored=%v", status, out, stored)
	}

	// wrong id → refused, nothing stored
	status, _ = postCode(t, srv.Port, map[string]any{"requestId": 43, "code": "87654321"})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("wrong id = %d, want 422", status)
	}
	if _, hit := stored[43]; hit {
		t.Fatal("code for a foreign request id was stored")
	}

	// short code → refused
	status, _ = postCode(t, srv.Port, map[string]any{"requestId": 42, "code": "12345"})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("short code = %d, want 422", status)
	}

	// non-numeric code → refused
	status, _ = postCode(t, srv.Port, map[string]any{"requestId": 42, "code": "1234abcd"})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("alpha code = %d, want 422", status)
	}

	// no pending request at all → refused
	srv2 := startTestServer(t, Identity{DeviceID: "dev-2", AppVersion: "test"},
		func() int64 { return 0 }, func(int64, string) error { return nil })
	status, _ = postCode(t, srv2.Port, map[string]any{"requestId": 42, "code": "12345678"})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("no pending = %d, want 422", status)
	}

	// GET on the POST endpoint → 405
	resp, _ := http.Get(fmt.Sprintf("http://127.0.0.1:%d/lan/pin-reset-code", srv.Port))
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET on code endpoint = %d, want 405", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestCodeEndpointRateLimits(t *testing.T) {
	srv := startTestServer(t, Identity{DeviceID: "dev-rl", AppVersion: "test"},
		func() int64 { return 9 }, func(int64, string) error { return nil })
	last := 0
	for i := 0; i < 35; i++ {
		status, _ := postCode(t, srv.Port, map[string]any{"requestId": 9, "code": "12345678"})
		last = status
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("after 35 pushes last=%d, want 429", last)
	}
}

func TestPushPinResetCodeFindsRightTill(t *testing.T) {
	stored := map[int64]string{}
	target := startTestServer(t, Identity{DeviceID: "dev-target", DeviceName: "SALMA-PC", AppVersion: "test"},
		func() int64 { return 7 }, func(id int64, code string) error { stored[id] = code; return nil })
	other := startTestServer(t, Identity{DeviceID: "dev-other", DeviceName: "TILL-2", AppVersion: "test"},
		func() int64 { return 0 }, func(int64, string) error { return nil })

	oldBrowse := mdns.Browse
	t.Cleanup(func() { mdns.Browse = oldBrowse })

	// LAN scan returns both tills, the target second: the walk must pick
	// by device id, not by order.
	mdns.Browse = func(ctx context.Context, d time.Duration) []mdns.Peer {
		return []mdns.Peer{{Addr: "127.0.0.1", Port: other.Port}, {Addr: "127.0.0.1", Port: target.Port}}
	}
	if !PushPinResetCode(context.Background(), "dev-target", 7, "91827364") {
		t.Fatal("push to a LAN-present till reported failure")
	}
	if stored[7] != "91827364" {
		t.Fatalf("stored = %v", stored)
	}

	// wrong device id → nothing delivered
	stored = map[int64]string{}
	if PushPinResetCode(context.Background(), "dev-missing", 7, "11112222") {
		t.Fatal("push to an absent till reported success")
	}
	if len(stored) != 0 {
		t.Fatalf("code stored on a miss: %v", stored)
	}

	// no peers discovered → false, no panic
	mdns.Browse = func(ctx context.Context, d time.Duration) []mdns.Peer { return nil }
	if PushPinResetCode(context.Background(), "dev-target", 7, "33334444") {
		t.Fatal("push with no LAN peers reported success")
	}
}
