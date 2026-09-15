package main

import (
        "context"
        "embed"
        "flag"
        "fmt"
        "io"
        "io/fs"
        "log"
        "net"
        "net/http"
        "os"
        "os/signal"
        "strconv"
        "syscall"
        "time"

        "github.com/gin-gonic/gin"

        "posapp/internal/config"
        "posapp/internal/database"
        "posapp/internal/handlers"
        "posapp/internal/mdns"
        "posapp/internal/offsite"
        "posapp/internal/printer"
        "posapp/internal/router"
        "posapp/internal/services"
        "posapp/internal/settings"
        "posapp/internal/ws"
)

//go:embed all:frontend/dist
var frontendFiles embed.FS

// Version is stamped at build time (-ldflags "-X main.version=…").
var version = "dev"

const usage = `usage:
  ledgerpos                 desktop mode (default): local app on 127.0.0.1,
                            data under the OS user directory, opens its own
                            app window (Windows) or your browser elsewhere
  ledgerpos serve           server/appliance mode: env-driven (PORT, DB_PATH,
                            DB_DRIVER, POSTGRES_DSN, MDNS_ENABLED, …), binds :PORT
  ledgerpos uninstall       remove the desktop app (shortcuts, registry, files)
  ledgerpos version         print build version
  ledgerpos restore-backup  download + decrypt an off-site snapshot:
                            --endpoint URL --bucket NAME --key KEY
                            --passphrase PASS [--out FILE] [--force] [--list]`

func main() {
        args := os.Args[1:]
        if len(args) == 0 {
                os.Exit(runDesktop())
                return
        }
        switch args[0] {
        case "serve":
                runServer()
        case "uninstall", "--uninstall":
                os.Exit(runUninstall())
        case "version", "--version", "-v":
                fmt.Printf("%s %s (%s/%s)\n", appName, version, runtimeGOOS, runtimeGOARCH)
        case "restore-backup":
                os.Exit(runRestoreBackup(args[1:]))
        case "help", "--help", "-h":
                fmt.Println(usage)
        default:
                fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s\n", args[0], usage)
                os.Exit(2)
        }
}

// runServer is the appliance path: every knob comes from the environment.
func runServer() {
        cfg := config.Load()
        startApp(cfg, ":"+cfg.Port, nil, nil)
}

// desktopMeta carries the desktop-mode facts into startApp.
type desktopMeta struct {
        Port     string
        FirstRun bool
        OnReady  func() // invoked once the listener accepts (open the browser)
}

// startApp boots the whole stack and blocks until SIGINT/SIGTERM or an
// admin POST /system/quit (desktop mode only). desk == nil → server mode.
func startApp(cfg *config.Config, addr string, desk *desktopMeta, onQuit chan struct{}) {
        gin.SetMode(cfg.GinMode)

        db, err := database.Open(cfg.DBDriver, cfg.SQLitePath, cfg.PostgresDSN)
        if err != nil {
                log.Fatalf("database: %v", err)
        }
        if err := db.Migrate(); err != nil {
                log.Fatalf("migrate: %v", err)
        }
        if err := db.Seed(cfg.SeedDemoData); err != nil {
                log.Fatalf("seed: %v", err)
        }
        st, err := settings.New(db)
        if err != nil {
                log.Fatalf("settings: %v", err)
        }

        hub := ws.NewHub(st.JWTSecret)
        go hub.Run()

        pw := printer.NewWorker(db, st)
        if err := pw.Recover(); err != nil {
                log.Printf("[printer] recover: %v", err)
        }

        svc := services.New(db, st, hub, pw)
        h := handlers.New(db, st, svc, hub, pw)

        // Encrypted off-site uploads (async; inert unless configured).
        ow := offsite.NewWorker(st, func(action, entity, entityID, details string) {
                svc.Audit(0, "system", action, entity, entityID, details)
        })
        svc.AttachOffsite(ow)
        h.Offsite = ow

        if desk != nil {
                h.Desktop = handlers.DesktopStatus{
                        Desktop:  true,
                        Version:  version,
                        FirstRun: desk.FirstRun,
                        Port:     desk.Port,
                }
                if onQuit != nil {
                        h.OnQuit = func() { close(onQuit) }
                }
        }

        // Frontend assets from the embedded build (single binary deploys).
        frontend, err := fs.Sub(frontendFiles, "frontend/dist")
        if err != nil {
                log.Fatalf("embed: %v", err)
        }
        engine := router.New(h, frontend)

        ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
        defer stop()

        // Background workers: STK sweeper + print queue + daily backups + uploads.
        sweeper := services.NewSweeper(svc)
        go sweeper.Run(ctx)
        go pw.Run(ctx)
        go ow.Run(ctx)
        svc.StartBackupScheduler()

        // LAN discovery broadcast (best-effort, server mode only).
        if cfg.MDNSEnabled {
                mdns.Broadcast(ctx, st.GetString("app_name", "Point of Sale"), atoi(cfg.Port))
        }

        srv := &http.Server{
                Addr:    addr,
                Handler: engine,
        }

        // Own the listener so the port is known before serving starts
        // (desktop mode needs it for the browser URL + port file).
        ln, err := net.Listen("tcp", addr)
        if err != nil {
                log.Fatalf("listen: %v", err)
        }
        // Serve first, then the ready hook: OnReady may block (the native
        // app window runs its message loop there until the user closes it),
        // and Serve must already be accepting or the window loads a dead
        // page. Shutdown still flows through the quit channel below.
        go func() {
                log.Printf("%s listening on %s (db=%s, mpesa=%s)", st.GetString("app_name", "Point of Sale"), ln.Addr().String(), cfg.DBDriver, st.GetString("mpesa_env", "mock"))
                if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
                        log.Fatalf("serve: %v", err)
                }
        }()
        if desk != nil && desk.OnReady != nil {
                go func() { desk.OnReady() }()
        }

        // Block until OS signal or (desktop mode) admin quit.
        sigDone := make(chan struct{})
        go func() {
                <-ctx.Done()
                close(sigDone)
        }()
        select {
        case <-sigDone:
                log.Printf("shutting down...")
        case <-onQuit:
                log.Printf("quit requested by admin session — shutting down...")
        }

        shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        _ = srv.Shutdown(shutdownCtx)
        _ = db.Close()
}

func atoi(s string) int {
        n, err := strconv.Atoi(s)
        if err != nil {
                return 0
        }
        return n
}

// runRestoreBackup downloads (latest or named key) and decrypts an off-site
// snapshot. Flags only — scriptable for a dead-box recovery.
func runRestoreBackup(args []string) int {
        fs := flag.NewFlagSet("restore-backup", flag.ContinueOnError)
        endpoint := fs.String("endpoint", "", "S3-compatible endpoint URL")
        bucket := fs.String("bucket", "", "bucket name")
        region := fs.String("region", "auto", "region (auto works on R2)")
        access := fs.String("access-key", "", "access key (or env OFFSITE_ACCESS_KEY)")
        secret := fs.String("secret-key", "", "secret key (or env OFFSITE_SECRET_KEY)")
        pass := fs.String("passphrase", "", "backup passphrase (or env OFFSITE_PASSPHRASE)")
        key := fs.String("key", "", "exact remote key (default: newest under --prefix)")
        prefix := fs.String("prefix", "shop", "key prefix for --list / newest lookup")
        out := fs.String("out", "pos-restored.db", "decrypted output file")
        list := fs.Bool("list", false, "list remote keys and exit")
        force := fs.Bool("force", false, "overwrite existing --out file")
        if err := fs.Parse(args); err != nil {
                fmt.Fprintln(os.Stderr, err)
                return 2
        }
        if *access == "" {
                *access = os.Getenv("OFFSITE_ACCESS_KEY")
        }
        if *secret == "" {
                *secret = os.Getenv("OFFSITE_SECRET_KEY")
        }
        if *pass == "" {
                *pass = os.Getenv("OFFSITE_PASSPHRASE")
        }
        cfg := offsite.Config{Endpoint: *endpoint, Bucket: *bucket, Region: *region,
                AccessKey: *access, SecretKey: *secret, Prefix: *prefix}
        if cfg.Endpoint == "" || cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
                fmt.Fprintln(os.Stderr, "restore-backup: endpoint, bucket, and keys are required")
                return 2
        }
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
        defer cancel()
        if *list {
                keys, err := offsite.ListObjects(ctx, cfg, *prefix+"/")
                if err != nil {
                        fmt.Fprintln(os.Stderr, "list:", err)
                        return 1
                }
                for _, k := range keys {
                        fmt.Printf("%s  %s\n", k.LastModified, k.Name)
                }
                return 0
        }
        if *key == "" {
                keys, err := offsite.ListObjects(ctx, cfg, *prefix+"/")
                if err != nil {
                        fmt.Fprintln(os.Stderr, "list:", err)
                        return 1
                }
                if len(keys) == 0 {
                        fmt.Fprintln(os.Stderr, "restore-backup: no snapshots under prefix "+*prefix)
                        return 1
                }
                best := keys[0].Name
                for _, k := range keys[1:] {
                        if k.Name > best {
                                best = k.Name
                        }
                }
                *key = best
        }
        if *pass == "" {
                fmt.Fprintln(os.Stderr, "restore-backup: passphrase is required")
                return 2
        }
        if _, err := os.Stat(*out); err == nil && !*force {
                fmt.Fprintf(os.Stderr, "restore-backup: %s exists (use --force)\n", *out)
                return 1
        }
        rc, err := offsite.GetObject(ctx, cfg, *key)
        if err != nil {
                fmt.Fprintln(os.Stderr, "download:", err)
                return 1
        }
        tmp, err := os.CreateTemp("", "restore-*.enc")
        if err != nil {
                rc.Close()
                fmt.Fprintln(os.Stderr, err)
                return 1
        }
        tmpName := tmp.Name()
        _, err = io.Copy(tmp, rc)
        rc.Close()
        tmp.Close()
        if err != nil {
                os.Remove(tmpName)
                fmt.Fprintln(os.Stderr, "download:", err)
                return 1
        }
        if err := offsite.DecryptFile(tmpName, *pass, *out); err != nil {
                os.Remove(tmpName)
                fmt.Fprintln(os.Stderr, "decrypt:", err)
                return 1
        }
        os.Remove(tmpName)
        fmt.Printf("restored %s -> %s\n", *key, *out)
        return 0
}
