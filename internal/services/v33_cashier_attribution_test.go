package services

import "testing"

// v33: a synced sale carries the seller's display name ("Jane Wanjiru"),
// not their username. Resolving only by username missed and fell back to
// the receiving till's FIRST account — every remote sale read as the
// machine owner. Username first, then full name; fallback only last.
func TestV33ResolveCashierByUsernameThenFullName(t *testing.T) {
	s := vaultProbeNewService(t)
	s.db.Exec(s.db.Rebind(`INSERT INTO roles (name) VALUES ('Cashier')`))
	var roleID int64
	s.db.QueryRow(`SELECT id FROM roles WHERE name='Cashier'`).Scan(&roleID)
	s.db.Exec(s.db.Rebind(`INSERT INTO users (username, full_name, role_id) VALUES ('owner', 'Machine Owner', ?)`), roleID)
	s.db.Exec(s.db.Rebind(`INSERT INTO users (username, full_name, role_id) VALUES ('jane', 'Jane Wanjiru', ?)`), roleID)
	var owner, jane int64
	s.db.QueryRow(`SELECT id FROM users WHERE username='owner'`).Scan(&owner)
	s.db.QueryRow(`SELECT id FROM users WHERE username='jane'`).Scan(&jane)

	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if got := s.resolveLocalCashierID(tx, "jane", "Jane Wanjiru"); got != jane {
		t.Errorf("by username: got %d want %d", got, jane)
	}
	// Older till: no username on the wire, display name only.
	if got := s.resolveLocalCashierID(tx, "", "Jane Wanjiru"); got != jane {
		t.Errorf("by full name: got %d want %d (the machine-owner bug)", got, jane)
	}
	if got := s.resolveLocalCashierID(tx, "", "Nobody Here"); got == jane {
		t.Errorf("unknown seller must not resolve to jane")
	}
}
