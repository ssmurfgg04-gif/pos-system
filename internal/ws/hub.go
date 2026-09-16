// Package ws is a minimal broadcast hub. Clients authenticate with their
// JWT as the first message; the server pushes JSON events
// ({"type":"ORDER_PAID","data":{...}}) to all authenticated clients.
package ws

import (
        "encoding/json"
        "net/http"
        "sync"
        "time"

        "github.com/gorilla/websocket"

        "posapp/internal/auth"
)

type Client struct {
        conn *websocket.Conn
        send chan []byte
}

type Hub struct {
        mu         sync.RWMutex
        clients    map[*Client]bool
        upgrader   websocket.Upgrader
        secretFn   func() []byte
        register   chan *Client
        unregister chan *Client
}

func NewHub(secretFn func() []byte) *Hub {
        return &Hub{
                clients:    map[*Client]bool{},
                secretFn:   secretFn,
                register:   make(chan *Client, 8),
                unregister: make(chan *Client, 8),
                upgrader: websocket.Upgrader{
                        ReadBufferSize:  1024,
                        WriteBufferSize: 1024,
                        CheckOrigin:     func(r *http.Request) bool { return true }, // LAN appliance
                },
        }
}

// Run drives the hub lifecycle.
func (h *Hub) Run() {
        for {
                select {
                case c := <-h.register:
                        h.mu.Lock()
                        h.clients[c] = true
                        h.mu.Unlock()
                case c := <-h.unregister:
                        h.mu.Lock()
                        if _, ok := h.clients[c]; ok {
                                delete(h.clients, c)
                                close(c.send)
                        }
                        h.mu.Unlock()
                }
        }
}

// BroadcastJSON pushes an event to every connected client.
func (h *Hub) BroadcastJSON(eventType string, data any) {
        msg := struct {
                Type string `json:"type"`
                Data any    `json:"data"`
        }{Type: eventType, Data: data}
        b, err := json.Marshal(msg)
        if err != nil {
                return
        }
        h.mu.RLock()
        targets := make([]*Client, 0, len(h.clients))
        for c := range h.clients {
                targets = append(targets, c)
        }
        h.mu.RUnlock()
        for _, c := range targets {
                select {
                case c.send <- b:
                default: // slow client — drop it
                        h.unregister <- c
                }
        }
}

// ServeHTTP upgrades, authenticates via first message, then pumps.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
        conn, err := h.upgrader.Upgrade(w, r, nil)
        if err != nil {
                return
        }
        client := &Client{conn: conn, send: make(chan []byte, 32)}

        // Auth handshake: first message must be {"token":"..."}.
        conn.SetReadDeadline(time.Now().Add(10 * time.Second))
        _, raw, err := conn.ReadMessage()
        if err != nil {
                conn.Close()
                return
        }
        var hs struct {
                Token string `json:"token"`
        }
        if err := json.Unmarshal(raw, &hs); err != nil || hs.Token == "" {
                conn.WriteJSON(map[string]string{"type": "ERROR", "data": "auth required"})
                conn.Close()
                return
        }
        // Hub is box-shared: any valid token authenticates (per-shop
        // broadcasts stay server-side; the secret must be the master one).
        if _, _, err := auth.ParseToken(h.secretFn(), hs.Token); err != nil {
                conn.WriteJSON(map[string]string{"type": "ERROR", "data": "invalid token"})
                conn.Close()
                return
        }
        conn.SetReadDeadline(time.Now().Add(90 * time.Second))
        conn.SetPongHandler(func(string) error {
                conn.SetReadDeadline(time.Now().Add(90 * time.Second))
                return nil
        })

        h.register <- client
        go h.writePump(client)
        h.readPump(client)
}

func (h *Hub) readPump(c *Client) {
        defer func() {
                h.unregister <- c
                c.conn.Close()
        }()
        for {
                if _, _, err := c.conn.ReadMessage(); err != nil {
                        return
                }
        }
}

func (h *Hub) writePump(c *Client) {
        ticker := time.NewTicker(30 * time.Second)
        defer func() {
                ticker.Stop()
                c.conn.Close()
        }()
        for {
                select {
                case msg, ok := <-c.send:
                        if !ok {
                                return
                        }
                        if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
                                return
                        }
                case <-ticker.C:
                        if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
                                return
                        }
                }
        }
}
