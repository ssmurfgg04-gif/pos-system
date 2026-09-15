package services

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---- Local backup worker ----
//
// Reliability: SQLite VACUUM INTO produces a consistent, compact snapshot
// even while the database is being written (it reads through the WAL). We
// keep the last N snapshots in a `backups/` directory next to the database
// and take one automatically each day when enabled (settings: backup_auto,
// default on; backup_keep, default 7). When off-site is enabled, each
// snapshot is also encrypted and pushed to S3-compatible storage by the
// offsite worker (async, retried) — nobody commutes to copy files.

// BackupResult describes one completed snapshot.
type BackupResult struct {
	File  string `json:"file"` // file name inside backups/
	Bytes int64  `json:"bytes"`
	At    string `json:"at"`
}

// BackupNow writes a consistent snapshot into <dbdir>/backups/.
func (s *Service) BackupNow() (*BackupResult, error) {
	if !s.db.IsSQLite() {
		return nil, fmt.Errorf("backups are SQLite-only; use pg_dump for PostgreSQL")
	}
	src := s.db.Path()
	dir := filepath.Join(filepath.Dir(src), "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create backups dir: %w", err)
	}
	name := fmt.Sprintf("pos-backup-%s.db", time.Now().Format("20060102-150405"))
	dst := filepath.Join(dir, name)
	if _, err := os.Stat(dst); err == nil {
		return nil, fmt.Errorf("backup %s already exists", name)
	}
	if _, err := s.db.Exec(fmt.Sprintf("VACUUM INTO '%s'", strings.ReplaceAll(dst, "'", "''"))); err != nil {
		return nil, fmt.Errorf("vacuum into: %w", err)
	}
        st, err := os.Stat(dst)
        if err != nil {
                return nil, err
        }
        s.pruneBackups(dir)
        // Off-site push is async and best-effort: sales never wait for it.
        s.enqueueOffsite(dst)
        return &BackupResult{File: name, Bytes: st.Size(), At: time.Now().Format(time.RFC3339)}, nil
}

// ListBackups returns stored snapshots, newest first.
func (s *Service) ListBackups() []BackupResult {
	if !s.db.IsSQLite() {
		return []BackupResult{}
	}
	dir := filepath.Join(filepath.Dir(s.db.Path()), "backups")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []BackupResult{}
	}
	out := make([]BackupResult, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "pos-backup-") || !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, BackupResult{File: e.Name(), Bytes: info.Size(), At: info.ModTime().Format(time.RFC3339)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File > out[j].File })
	return out
}

// pruneBackups enforces the retention setting (default 7).
func (s *Service) pruneBackups(dir string) {
	keep := s.settings.GetInt("backup_keep", 7)
	if keep < 1 {
		keep = 1
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "pos-backup-") && strings.HasSuffix(e.Name(), ".db") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files) // oldest first (timestamped names)
	for len(files) > keep {
		if err := os.Remove(filepath.Join(dir, files[0])); err == nil {
			log.Printf("[backup] pruned %s", files[0])
		}
		files = files[1:]
	}
}

// StartBackupScheduler runs the daily auto-backup loop (call once at boot;
// SQLite only). It re-checks the backup_auto setting every cycle so admins
// can toggle it from Settings without a restart.
func (s *Service) StartBackupScheduler() {
	if !s.db.IsSQLite() {
		return
	}
	go func() {
		// Run at 02:00 local time each day; also take one shortly after boot
		// (a restarted box is often a "something happened" box).
		s.maybeAutoBackup()
		for {
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day(), 2, 0, 0, 0, now.Location())
			if !next.After(now) {
				next = next.AddDate(0, 0, 1)
			}
			time.Sleep(time.Until(next))
			s.maybeAutoBackup()
		}
	}()
}

func (s *Service) maybeAutoBackup() {
	if !s.settings.GetBool("backup_auto", true) {
		return
	}
	// Skip if today's snapshot already exists (restarts shouldn't spam files).
	for _, b := range s.ListBackups() {
		if strings.HasPrefix(b.File, "pos-backup-"+time.Now().Format("20060102")) {
			return
		}
	}
	if res, err := s.BackupNow(); err != nil {
		log.Printf("[backup] auto-backup failed: %v", err)
	} else {
		log.Printf("[backup] auto-backup written: %s (%d bytes)", res.File, res.Bytes)
	}
}
