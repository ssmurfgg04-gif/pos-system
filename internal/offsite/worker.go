package offsite

import (
        "context"
        "fmt"
        "log"
        "os"
        "sort"
        "strconv"
        "sync"
        "time"

        "posapp/internal/settings"
)

// keepDefault bounds remote retention sanity (GetInt fallback covers parse).
const keepDefault = 30

// Worker uploads encrypted snapshots in the background with retries. It never
// blocks sales: failures stay queued (visible in Settings) and retry with
// backoff. The zero value is inert until Run starts.
type Worker struct {
        settings *settings.Store
        audit    func(action, entity, entityID, details string)

        mu      sync.Mutex
        queue   []uploadJob
        lastOk  string
        lastErr string
        lastAt  string
}

type uploadJob struct {
        path     string
        attempts int
        next     time.Time
}

// NewWorker builds the uploader. audit records BACKUP_UPLOADED/FAILED rows
// (pass Service.Audit bound to user 0/"system" at the call site).
func NewWorker(st *settings.Store, audit func(action, entity, entityID, details string)) *Worker {
        return &Worker{settings: st, audit: audit}
}

// Enqueue schedules a snapshot for upload (deduplicates by path).
func (w *Worker) Enqueue(snapshotPath string) {
        w.mu.Lock()
        defer w.mu.Unlock()
        for _, j := range w.queue {
                if j.path == snapshotPath {
                        return
                }
        }
        w.queue = append(w.queue, uploadJob{path: snapshotPath, next: time.Now()})
}

// Status reports worker state for Settings → System.
func (w *Worker) Status() (enabled bool, pending int, lastOk, lastErr, lastAt string) {
        w.mu.Lock()
        defer w.mu.Unlock()
        return w.settings.GetBool("offsite_enabled", false), len(w.queue), w.lastOk, w.lastErr, w.lastAt
}

// backoff schedule: 15m, 1h, 4h, then daily (jobs never drop).
func backoff(attempts int) time.Duration {
        switch {
        case attempts <= 0:
                return 0
        case attempts == 1:
                return 15 * time.Minute
        case attempts == 2:
                return time.Hour
        case attempts == 3:
                return 4 * time.Hour
        default:
                return 24 * time.Hour
        }
}

// Run processes due jobs every 5 minutes until ctx ends.
func (w *Worker) Run(ctx context.Context) {
        t := time.NewTicker(5 * time.Minute)
        defer t.Stop()
        w.drain(ctx)
        for {
                select {
                case <-ctx.Done():
                        return
                case <-t.C:
                        w.drain(ctx)
                }
        }
}

func (w *Worker) drain(ctx context.Context) {
        for {
                w.mu.Lock()
                idx := -1
                now := time.Now()
                for i, j := range w.queue {
                        if !j.next.After(now) {
                                idx = i
                                break
                        }
                }
                if idx < 0 {
                        w.mu.Unlock()
                        return
                }
                job := w.queue[idx]
                w.queue = append(w.queue[:idx], w.queue[idx+1:]...)
                w.mu.Unlock()

                _, err := w.UploadNow(ctx, job.path)
                if err != nil {
                        w.mu.Lock()
                        job.attempts++
                        job.next = time.Now().Add(backoff(job.attempts))
                        w.queue = append(w.queue, job)
                        w.mu.Unlock()
                        log.Printf("[offsite] upload %s failed (attempt %d): %v", job.path, job.attempts, err)
                }
        }
}

// ConfigFromSettings reads live settings (admins fix credentials without restart).
func ConfigFromSettings(s *settings.Store) (Config, string, bool) {
        if !s.GetBool("offsite_enabled", false) {
                return Config{}, "", false
        }
        prefix := s.Get("offsite_prefix")
        if prefix == "" {
                if h, err := os.Hostname(); err == nil && h != "" {
                        prefix = h
                } else {
                        prefix = "shop"
                }
        }
        cfg := Config{
                ProjectURL: s.Get("offsite_endpoint"),
                Bucket:     s.Get("offsite_bucket"),
                Key:        s.Get("offsite_secret_key"),
                Prefix:     prefix,
        }
        pass := s.Get("offsite_passphrase")
        if cfg.ProjectURL == "" || cfg.Bucket == "" || cfg.Key == "" {
                return Config{}, "", false
        }
        if pass == "" {
                return Config{}, "", false
        }
        return cfg, pass, true
}

// UploadNow encrypts, PUTs, prunes remote to keep, audits, and records
// status. Exported for tests; the background loop calls it via drain.
func (w *Worker) UploadNow(ctx context.Context, snapshotPath string) (string, error) {
        key, err := w.upload(ctx, snapshotPath)
        w.mu.Lock()
        defer w.mu.Unlock()
        if err == nil {
                w.lastOk, w.lastErr, w.lastAt = key, "", time.Now().Format(time.RFC3339)
        } else {
                w.lastErr, w.lastAt = err.Error(), time.Now().Format(time.RFC3339)
        }
        return key, err
}

func (w *Worker) upload(ctx context.Context, snapshotPath string) (string, error) {
        cfg, pass, ok := ConfigFromSettings(w.settings)
        if !ok {
                return "", fmt.Errorf("off-site backup not configured (endpoint/bucket/keys/passphrase)")
        }
        if _, err := os.Stat(snapshotPath); err != nil {
                return "", fmt.Errorf("snapshot gone: %w", err)
        }
        enc, err := EncryptFile(snapshotPath, pass)
        if err != nil {
                return "", err
        }
        defer os.Remove(enc)
        f, err := os.Open(enc)
        if err != nil {
                return "", err
        }
        defer f.Close()
        st, err := f.Stat()
        if err != nil {
                return "", err
        }
        key := SnapshotKey(cfg.Prefix, time.Now())
        if err := UploadObject(ctx, cfg, key, f, st.Size()); err != nil {
                return "", err
        }
        if err := w.pruneRemote(ctx, cfg); err != nil {
                log.Printf("[offsite] remote prune failed (upload kept): %v", err)
        }
        w.audit("BACKUP_UPLOADED", "backup", key, strconv.FormatInt(st.Size(), 10)+" bytes")
        return key, nil
}

// pruneRemote keeps the newest offsite_keep keys under the prefix.
func (w *Worker) pruneRemote(ctx context.Context, cfg Config) error {
        keep := w.settings.GetInt("offsite_keep", keepDefault)
        if keep < 1 {
                keep = keepDefault
        }
        keys, err := ListObjects(ctx, cfg, cfg.Prefix+"/")
        if err != nil {
                return err
        }
        sort.Slice(keys, func(i, j int) bool { return keys[i].Name > keys[j].Name })
        if len(keys) <= keep {
                return nil
        }
        doomed := make([]string, 0, len(keys)-keep)
        for _, k := range keys[keep:] {
                doomed = append(doomed, k.Name)
        }
        return DeleteObjects(ctx, cfg, doomed)
}
