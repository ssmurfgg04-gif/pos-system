package services

// v1.3.2 services tests: owner-capability injection (review 1.1) and the
// outbox reconciliation backfill (review 8.11).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRpcOwnerCallInjectsCapability(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/rpc/sync_create_store") {
			http.NotFound(w, r)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "team_code": "TXXX-YYYY"})
	}))
	defer srv.Close()

	orig := ownerCapabilityTokenFor
	defer func() { ownerCapabilityTokenFor = orig }()

	c := &syncClient{base: srv.URL, mode: "rpc", team: "TEAM-A", device: "dev-1", secretHash: "h"}
	// No cached capability: the arg must be absent.
	ownerCapabilityTokenFor = func(c *syncClient) string { return "" }
	ok, _, err := c.rpcOwnerCall("sync_create_store", map[string]any{"p_name": "S"})
	if err != nil || !ok {
		t.Fatalf("rpc failed: ok=%v err=%v", ok, err)
	}
	if _, present := got["p_owner_capability"]; present {
		t.Fatal("capability injected without a cached token")
	}
	// Cached capability: injected verbatim.
	ownerCapabilityTokenFor = func(c *syncClient) string { return "tok.abc" }
	ok, _, err = c.rpcOwnerCall("sync_create_store", map[string]any{"p_name": "S"})
	if err != nil || !ok {
		t.Fatalf("rpc failed: ok=%v err=%v", ok, err)
	}
	if got["p_owner_capability"] != "tok.abc" {
		t.Fatalf("capability arg = %v, want tok.abc", got["p_owner_capability"])
	}
}

func TestMintOwnerCapabilityCachesTeamScoped(t *testing.T) {
	// Minimal cloud fake: bootstrap row (rpc-mode discovery) + the mint RPC.
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/v1/sync_bootstrap", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{{
			"project_url": "http://placeholder", "team_code": "TEAM-A", "auto_approve": true,
		}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	// Rewrite the bootstrap payload to point at ourselves (the fake needs
	// its own URL, which is only known after ListenAndServe).
	mux2 := http.NewServeMux()
	var realURL string
	mux2.HandleFunc("/rest/v1/sync_bootstrap", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{{
			"project_url": realURL, "team_code": "TEAM-A", "auto_approve": true,
		}})
	})
	mux2.HandleFunc("/rest/v1/rpc/owner_mint_capability", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		if in["p_username"] != "owner" || in["p_password"] != "secret" {
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "those owner credentials were not recognized for this store"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "capability": "payload.sig", "teamCode": "TEAM-A",
			"owner": "owner", "expiresAt": "2100-01-01T00:00:00Z",
		})
	})
	srv2 := httptest.NewServer(mux2)
	defer srv2.Close()
	realURL = srv2.URL

	oldURL := cloudBaseURL
	cloudBaseURL = srv2.URL
	t.Cleanup(func() { cloudBaseURL = oldURL })

	s := newSyncTestService(t)
	ClearOwnerCapability()
	defer ClearOwnerCapability()

	// Wrong credential → refused, nothing cached.
	if _, _, err := s.MintOwnerCapability("owner", "wrong"); err == nil {
		t.Fatal("wrong credential accepted")
	}
	if st := MarshalOwnerCapabilityState(); st != nil {
		t.Fatalf("nothing should be cached after a refusal: %v", st)
	}
	// Correct credential → cached with team + owner.
	owner, d, err := s.MintOwnerCapability("owner", "secret")
	if err != nil || owner != "owner" || d <= 0 {
		t.Fatalf("mint: owner=%q d=%v err=%v", owner, d, err)
	}
	st := MarshalOwnerCapabilityState()
	if st == nil || st["team"] != "TEAM-A" {
		t.Fatalf("cache state = %v, want team TEAM-A", st)
	}
}

func TestOutboxBackfillRecoversLostOrderEvent(t *testing.T) {
	s := newSyncTestService(t)
	cashier := seedUser(t, s)
	// A committed order with NO outbox row (the emit insert failed).
	res, err := s.db.Exec(s.db.Rebind(`INSERT INTO orders (number, status, subtotal_cents, total_cents, cashier_id, client_uuid, created_at)
                VALUES ('ORD-BF-1', 'PAID', 100, 116, ?, 'bf-uuid-1', datetime('now'))`), cashier)
	if err != nil {
		t.Fatalf("seed order: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatal("seed order not inserted")
	}
	count := func() int {
		var n int
		s.db.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE entity='order' AND json_extract(payload,'$.clientUuid')='bf-uuid-1'`).Scan(&n)
		return n
	}
	if count() != 0 {
		t.Fatal("precondition: no order event expected")
	}
	if n := s.BackfillMissingEvents(); n == 0 {
		t.Fatal("backfill recovered nothing")
	}
	if count() != 1 {
		t.Fatal("backfill did not re-emit the lost order event")
	}
	// Idempotent: a second pass must not duplicate the event.
	if n := s.BackfillMissingEvents(); n != 0 {
		t.Fatalf("second backfill re-emitted %d event(s) — must be idempotent", n)
	}
}

func TestOutboxBackfillRecoversVoidAndLedger(t *testing.T) {
	s := newSyncTestService(t)
	cashier := seedUser(t, s)
	s.db.Exec(s.db.Rebind(`INSERT INTO orders (number, status, subtotal_cents, total_cents, cashier_id, client_uuid, created_at)
                VALUES ('ORD-BF-2', 'voided', 100, 116, ?, 'bf-uuid-2', datetime('now'))`), cashier)
	// A customer + an unsynced ledger movement (money path).
	s.db.Exec(`INSERT INTO customers (name, phone) VALUES ('BF Customer', '254700000001')`)
	var custID int64
	s.db.QueryRow(`SELECT id FROM customers WHERE phone='254700000001'`).Scan(&custID)
	s.db.Exec(`INSERT INTO customer_ledger (customer_id, order_id, kind, amount_cents, points_delta, note, created_at)
                VALUES (?, 1, 'charge', 5000, 0, 'tab', datetime('now'))`, custID)

	if n := s.BackfillMissingEvents(); n == 0 {
		t.Fatal("backfill recovered nothing")
	}
	var voids, ledgers int
	s.db.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE entity='void' AND json_extract(payload,'$.orderClientUuid')='bf-uuid-2'`).Scan(&voids)
	s.db.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE entity='ledger'`).Scan(&ledgers)
	if voids != 1 {
		t.Fatalf("void event backfill = %d, want 1", voids)
	}
	if ledgers < 1 {
		t.Fatalf("ledger event backfill = %d, want >= 1", ledgers)
	}
	// The ledger cursor must now mark the row synced (emit-once holds).
	var unsynced int
	s.db.QueryRow(`SELECT COUNT(*) FROM customer_ledger WHERE COALESCE(synced_at,'')=''`).Scan(&unsynced)
	if unsynced != 0 {
		t.Fatalf("%d ledger row(s) still unsynced after backfill", unsynced)
	}
}

func TestNoteOutboxFailureSurfacesInSyncState(t *testing.T) {
	s := newSyncTestService(t)
	// A payload that cannot marshal → Emit records a visible failure.
	type badCh chan int
	s.Emit("product", "upsert", map[string]any{"bad": badCh(make(chan int))})
	var n int
	s.db.QueryRow(`SELECT CAST(COALESCE(value,'0') AS INTEGER) FROM sync_state WHERE key='sync_outbox_failures'`).Scan(&n)
	if n != 1 {
		t.Fatalf("outbox failure counter = %d, want 1", n)
	}
	var lastErr string
	s.db.QueryRow(`SELECT COALESCE(value,'') FROM sync_state WHERE key='sync_outbox_last_error'`).Scan(&lastErr)
	if !strings.Contains(lastErr, "marshal product") {
		t.Fatalf("last error = %q, want a marshal failure", lastErr)
	}
}

// seedUser inserts one active user (orders.cashier_id has an FK to users)
// and returns its id.
func seedUser(t *testing.T, s *Service) int64 {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO roles (name) VALUES ('Cashier')`); err != nil {
		t.Fatalf("seed role: %v", err)
	}
	var roleID int64
	s.db.QueryRow(`SELECT id FROM roles WHERE name='Cashier'`).Scan(&roleID)
	res, err := s.db.Exec(s.db.Rebind(`INSERT INTO users (username, role_id) VALUES ('bf-cashier', ?)`), roleID)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// TestPaystackVerifyRefCloudProxyMapping was retired with the v1.4.4
// paystack removal: the paystack-verify cloud proxy no longer exists, and
// neither does the rail it fed. The claim-RPC + relay semantics that
// superseded it are pinned by payments_buni_test.go
// (claim → ParseCallback → HandleCallback, fail-closed amount gate).
