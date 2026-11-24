package auth

// lastseen.go — v33 presence: every authenticated request marks the user
// as seen, throttled to one write per user per minute so busy tills don't
// hammer the database. Admins read users.last_seen_at on the Team page.

import (
	"strconv"
	"sync"
	"time"

	"posapp/internal/database"
)

const lastSeenEvery = time.Minute

var (
	lastSeenMu  sync.Mutex
	lastSeenMem = map[string]time.Time{}
)

// TouchLastSeen stamps users.last_seen_at (UTC RFC3339) at most once a minute.
func TouchLastSeen(db *database.DB, shopID string, userID int64) {
	if db == nil || userID == 0 {
		return
	}
	key := shopID + ":" + strconv.FormatInt(userID, 10)
	now := time.Now()
	lastSeenMu.Lock()
	if t, ok := lastSeenMem[key]; ok && now.Sub(t) < lastSeenEvery {
		lastSeenMu.Unlock()
		return
	}
	lastSeenMem[key] = now
	lastSeenMu.Unlock()
	_, _ = db.Exec(db.Rebind(`UPDATE users SET last_seen_at = ? WHERE id = ?`),
		now.UTC().Format(time.RFC3339), userID)
}
