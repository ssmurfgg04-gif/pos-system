package handlers_test

// A browser cannot send an Authorization header on a WebSocket, so the
// route must be reachable without one and authenticate by first message.
// (It sat behind the header check until v1.12.1: every browser got 401 and
// the app silently fell back to polling.)

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func dialWS(t *testing.T, srvURL string) *websocket.Conn {
	t.Helper()
	u := "ws" + strings.TrimPrefix(srvURL, "http") + "/api/v1/ws"
	c, resp, err := websocket.DefaultDialer.Dial(u, nil) // no Authorization header, like a browser
	if err != nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		t.Fatalf("browser-style WebSocket handshake refused (HTTP %d): %v", code, err)
	}
	return c
}

func TestWebSocketReachableWithoutAuthHeader(t *testing.T) {
	engine, admin, _, _ := newTestServer(t)
	srv := httptest.NewServer(engine)
	defer srv.Close()

	// valid token in the first message: socket stays open
	c := dialWS(t, srv.URL)
	defer c.Close()
	if err := c.WriteJSON(map[string]string{"token": admin}); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(700 * time.Millisecond))
	var m map[string]any
	if err := c.ReadJSON(&m); err == nil {
		t.Fatalf("valid token got an immediate reply %v; expected a quiet open socket", m)
	} else if ne, ok := err.(interface{ Timeout() bool }); !ok || !ne.Timeout() {
		t.Fatalf("socket closed after a valid handshake: %v", err)
	}

	// bad token: told so, then closed
	c2 := dialWS(t, srv.URL)
	defer c2.Close()
	_ = c2.WriteJSON(map[string]string{"token": "not-a-token"})
	c2.SetReadDeadline(time.Now().Add(2 * time.Second))
	var e map[string]string
	if err := c2.ReadJSON(&e); err != nil || e["type"] != "ERROR" {
		t.Fatalf("bad token: want ERROR frame, got %v err=%v", e, err)
	}

	// oversized first message is refused (anonymous peers read tiny)
	c3 := dialWS(t, srv.URL)
	defer c3.Close()
	_ = c3.WriteMessage(websocket.TextMessage, []byte(`{"token":"`+strings.Repeat("a", 8000)+`"}`))
	c3.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := c3.ReadMessage(); err == nil {
		t.Fatal("oversized handshake should close the socket")
	}
}
