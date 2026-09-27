package handlers

// update_rollback.go — data protection around app updates.
//
// UPDATE (UpdateInstall): before the new installer runs, every shop DB is
// snapshotted (SQLite VACUUM INTO), and the tenants registry + vault key +
// (Windows) the running binary are copied into
// <dataDir>/backups/pre-update-<version>-<stamp>/.
//
// UNDO (UndoUpdate, "Undo update" button): picks the newest pre-update
// archive, stages a restore manifest, swaps the binary back (Windows:
// rename-while-running), and quits. On the next boot startApp applies the
// manifest (RestorePending) BEFORE opening any database: pos.db and every
// shop file listed are restored, stale -wal/-shm siblings removed, and the
// manifest deleted. Post-update work is itself snapshotted first, so undo
// is lossless in both directions.

import (
        "encoding/json"
        "fmt"
        "io"
        "os"
        "os/exec"
        "path/filepath"
        "runtime"
        "sort"
        "strings"
        "time"

        "github.com/gin-gonic/gin"
)

// pendingRestore is the boot-time manifest describing what to put back.
// Files maps DESTINATION → SOURCE. Written next to the databases.
type pendingRestore struct {
        When    string            `json:"when"`
        Reason  string            `json:"reason"`
        Files   map[string]string `json:"files"`
        PrevExe string            `json:"prevExe,omitempty"`
}

func restoreManifestPath(dataDir string) string {
        return filepath.Join(dataDir, "restore-pending.json")
}

// PreUpdateSnapshot archives shop data + registry + vault + binary.
// Returns the archive dir. Never touches the live files (VACUUM INTO
// reads a consistent snapshot; the copies are side files).
func (h *H) PreUpdateSnapshot(version string) (string, error) {
        if h.Shops == nil || h.Tenants == nil {
                return "", fmt.Errorf("tenancy not configured")
        }
        dbPath := h.DB.Path()
        if dbPath == "" {
                return "", fmt.Errorf("pre-update backup needs SQLite (postgres deployments: use pg_dump)")
        }
        dataDir := filepath.Dir(dbPath)
        stamp := time.Now().UTC().Format("20060102-150405")
        archive := filepath.Join(dataDir, "backups", "pre-update-"+sanitizeLabel(version)+"-"+stamp)
        if err := os.MkdirAll(archive, 0o755); err != nil {
                return "", err
        }

        // 1. Every registered shop DB (including shops not yet opened).
        var snapErrs []string
        for _, sid := range h.Shops.ShopIDs() {
                db, err := h.Shops.DB(sid)
                if err != nil {
                        continue
                }
                src := db.Path()
                if src == "" {
                        continue
                }
                dst := filepath.Join(archive, filepath.Base(src)+".snap")
                if _, err := db.Exec(db.Rebind(`VACUUM INTO ?`), dst); err != nil {
                        snapErrs = append(snapErrs, filepath.Base(src)+": "+err.Error())
                }
        }
        if len(snapErrs) > 0 {
                return "", fmt.Errorf("snapshot failed: %s", strings.Join(snapErrs, "; "))
        }

        // 2. Tenants registry + vault key (they live next to the databases).
        for _, name := range []string{"shops.json", "secret.key"} {
                p := filepath.Join(dataDir, name)
                if raw, err := os.ReadFile(p); err == nil {
                        if err := os.WriteFile(filepath.Join(archive, name), raw, 0o600); err != nil {
                                return "", err
                        }
                }
        }

        // 3. The running binary (Windows rollback). Linux/mac users reinstall
        // the previous asset; the DB restore is the critical part everywhere.
        if runtime.GOOS == "windows" {
                if exe, err := os.Executable(); err == nil {
                        if in, err := os.Open(exe); err == nil {
                                defer in.Close()
                                out, err := os.OpenFile(filepath.Join(archive, "ledgerpos-"+sanitizeLabel(version)+".exe"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
                                if err == nil {
                                        _, err = io.Copy(out, in)
                                        out.Close()
                                        if err != nil {
                                                return "", err
                                        }
                                }
                        }
                }
        }

        // 4. Manifest note for the operator.
        _ = os.WriteFile(filepath.Join(archive, "README.txt"), []byte(
                "Pre-update archive created "+time.Now().UTC().Format(time.RFC3339)+"\n"+
                        "Version at capture: "+version+"\n"+
                        "Use Settings → System → Undo update to roll back.\n"), 0o644)
        return archive, nil
}

// UndoUpdate (settings.manage) stages a rollback to the newest pre-update
// archive and quits so the previous build + data come back on next boot.
func (h *H) UndoUpdate(c *gin.Context) {
        p := h.principal(c)
        dbPath := h.DB.Path()
        if dbPath == "" {
                h.fail(c, 400, "undo needs SQLite (postgres deployments: use pg_dump)")
                return
        }
        dataDir := filepath.Dir(dbPath)
        archive, err := newestPreUpdateArchive(dataDir)
        if err != nil {
                h.fail(c, 404, err.Error())
                return
        }

        // Protect whatever the (possibly updated) build produced since.
        if after, err := h.PreUpdateSnapshot(h.Version + "-before-undo"); err == nil {
                h.svc(c).Audit(p.ID, p.Username, "UPDATE_UNDO_SNAPSHOT", "system", "update", after)
        }

        manifest := pendingRestore{
                When:   time.Now().UTC().Format(time.RFC3339),
                Reason: "undo update → " + filepath.Base(archive),
                Files:  map[string]string{},
        }
        entries, _ := os.ReadDir(archive)
        for _, e := range entries {
                name := e.Name()
                switch {
                case strings.HasSuffix(name, ".snap"):
                        // X.db.snap → restore onto X.db (basename may itself contain dots).
                        dst := filepath.Join(dataDir, strings.TrimSuffix(name, ".snap"))
                        manifest.Files[dst] = filepath.Join(archive, name)
                case name == "shops.json" || name == "secret.key":
                        manifest.Files[filepath.Join(dataDir, name)] = filepath.Join(archive, name)
                }
        }
        if len(manifest.Files) == 0 {
                h.fail(c, 500, "archive is empty — nothing to restore")
                return
        }

        // Windows: swap the binary back while we still control the process.
        // A running exe can be RENAMED (not overwritten) — so: running exe →
        // .updating-old, archive exe → install path, then quit + relaunch.
        if runtime.GOOS == "windows" {
                archiveExe := ""
                entries, _ := os.ReadDir(archive)
                for _, e := range entries {
                        if strings.HasSuffix(e.Name(), ".exe") {
                                archiveExe = filepath.Join(archive, e.Name())
                                break
                        }
                }
                if archiveExe != "" {
                        if exe, err := os.Executable(); err == nil {
                                if real, rerr := filepath.EvalSymlinks(exe); rerr == nil {
                                        exe = real
                                }
                                old := exe + ".undo-old"
                                _ = os.Remove(old)
                                if err := os.Rename(exe, old); err == nil {
                                        if err := copyFileExclusive(archiveExe, exe); err != nil {
                                                _ = os.Rename(old, exe) // put it back — abort exe swap
                                                h.svc(c).Audit(p.ID, p.Username, "UPDATE_UNDO_EXE_FAILED", "system", "update", err.Error())
                                        } else {
                                                manifest.PrevExe = exe
                                                _ = os.WriteFile(filepath.Join(dataDir, "relaunch-after-quit"), []byte(exe), 0o644)
                                        }
                                }
                        }
                }
        }

        raw, _ := json.MarshalIndent(manifest, "", "  ")
        if err := os.WriteFile(restoreManifestPath(dataDir), raw, 0o600); err != nil {
                h.fail(c, 500, "stage restore: "+err.Error())
                return
        }
        h.svc(c).Audit(p.ID, p.Username, "UPDATE_UNDONE", "system", "update", manifest.Reason)
        h.ok(c, gin.H{"undo": true, "restoring": manifest.Reason})
        if h.OnQuit != nil {
                go func() {
                        time.Sleep(300 * time.Millisecond) // let the response flush
                        h.OnQuit()
                }()
        }
}

// RestorePending applies a staged restore manifest BEFORE any database is
// opened. Returns the reason when a restore ran ("" otherwise). Called
// from startApp; never touches running handles.
func RestorePending(dataDir string) string {
        raw, err := os.ReadFile(restoreManifestPath(dataDir))
        if err != nil {
                return ""
        }
        var m pendingRestore
        if json.Unmarshal(raw, &m) != nil {
                _ = os.Remove(restoreManifestPath(dataDir))
                return ""
        }
        for dst, src := range m.Files {
                // Remove SQLite sidecars so the restored file is authoritative.
                _ = os.Remove(dst + "-wal")
                _ = os.Remove(dst + "-shm")
                if err := copyFileExclusive(src, dst); err != nil {
                        continue // keep the manifest for a retry on next boot
                }
        }
        _ = os.Remove(restoreManifestPath(dataDir))
        return m.Reason
}

// RelaunchAfterQuit returns (and clears) a staged relaunch path — set by
// UndoUpdate on Windows so the rolled-back exe starts automatically.
func RelaunchAfterQuit(dataDir string) string {
        p := filepath.Join(dataDir, "relaunch-after-quit")
        raw, err := os.ReadFile(p)
        if err != nil {
                return ""
        }
        _ = os.Remove(p)
        return strings.TrimSpace(string(raw))
}

// newestPreUpdateArchive finds the latest backups/pre-update-* dir.
func newestPreUpdateArchive(dataDir string) (string, error) {
        root := filepath.Join(dataDir, "backups")
        entries, err := os.ReadDir(root)
        if err != nil {
                return "", fmt.Errorf("no backups directory — nothing to undo")
        }
        var dirs []string
        for _, e := range entries {
                if e.IsDir() && strings.HasPrefix(e.Name(), "pre-update-") && !strings.Contains(e.Name(), "before-undo") {
                        dirs = append(dirs, e.Name())
                }
        }
        if len(dirs) == 0 {
                return "", fmt.Errorf("no pre-update archive found — nothing to undo")
        }
        sort.Strings(dirs) // stamps sort chronologically
        return filepath.Join(root, dirs[len(dirs)-1]), nil
}

func sanitizeLabel(s string) string {
        s = strings.TrimSpace(s)
        var b strings.Builder
        for _, r := range s {
                switch {
                case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.':
                        b.WriteRune(r)
                default:
                        b.WriteRune('_')
                }
        }
        if b.Len() == 0 {
                return "unknown"
        }
        return b.String()
}

// copyFileExclusive writes dst via a temp file + rename so a crash never
// leaves a half-copied database behind.
func copyFileExclusive(src, dst string) error {
        in, err := os.Open(src)
        if err != nil {
                return err
        }
        defer in.Close()
        tmp := dst + ".restore-tmp"
        out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
        if err != nil {
                return err
        }
        if _, err := io.Copy(out, in); err != nil {
                out.Close()
                _ = os.Remove(tmp)
                return err
        }
        if err := out.Close(); err != nil {
                _ = os.Remove(tmp)
                return err
        }
        return os.Rename(tmp, dst)
}

// RelaunchExternal starts a detached process (best-effort) — used after an
// undo so the previous build comes back up without the operator digging
// through menus.
func RelaunchExternal(exe string) {
        if exe == "" {
                return
        }
        cmd := exec.Command(exe)
        cmd.Dir = filepath.Dir(exe)
        _ = cmd.Start()
}
