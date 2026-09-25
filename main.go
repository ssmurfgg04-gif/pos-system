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
        "path/filepath"
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
        "posapp/internal/tenants"
        "posapp/internal/update"
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
                            --endpoint PROJECT-URL --bucket NAME
                            --api-key KEY --passphrase PASS
                            [--key REMOTE-KEY] [--out FILE] [--force] [--list]`

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

// ensureDefaultShop adopts the pre-tenancy database as the "default" shop
// (zero data migration) and registers every existing username for routing.
// Returns the default shop id.
func ensureDefaultShop(reg *tenants.Registry, db *database.DB, dbPath, storeName string) string {
        for _, s := range reg.ShopList {
                if s.Name != "" {
                        return s.ID
                }
        }
        abs, err := filepath.Abs(dbPath)
        if err != nil {
                abs = dbPath
        }
        shop, err := reg.CreateShop(storeName, abs, time.Now().UTC().Format(time.RFC3339))
        if err != nil {
                log.Fatalf("tenant registry: %v", err)
        }
        rows, err := db.Query(`SELECT username FROM users`)
        if err == nil {
                defer rows.Close()
                for rows.Next() {
                        var u string
                        if err := rows.Scan(&u); err == nil {
                                _ = reg.RegisterUser(u, shop.ID) // best-effort; dupes impossible here
                        }
                }
        }
        log.Printf("adopted %s as default shop %s", abs, shop.ID)
        return shop.ID
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

        // Tenant registry: existing single-shop boxes adopt their database
        // as the "default" shop (zero data migration); new boxes start empty.
        regDir := filepath.Dir(cfg.SQLitePath)
        if regDir == "" || regDir == "." {
                regDir = "."
        }
        reg, err := tenants.Load(regDir)
        if err != nil {
                log.Fatalf("tenants: %v", err)
        }
        storeName := st.Get("store_name")
        if storeName == "" {
                storeName = "My Shop"
        }
        defaultShop := ensureDefaultShop(reg, db, cfg.SQLitePath, storeName)
        masterSecret := reg.EnsureJWTSecret(st.Get("jwt_secret"))

        hub := ws.NewHub(func() []byte { return []byte(masterSecret) })
        go hub.Run()

        dbPool := tenants.NewPool(reg, cfg.DBDriver, cfg.PostgresDSN)
        dbPool.Inject(defaultShop, db)
        shopPool := services.NewShopPool(dbPool, hub, nil)
        defSvc, err := shopPool.Service(defaultShop)
        if err != nil {
                log.Fatalf("shop service: %v", err)
        }
        pw := printer.NewWorker(db, defSvc.Settings())
        if err := pw.Recover(); err != nil {
                log.Printf("[printer] recover: %v", err)
        }
        shopPool.SetPrinter(pw)

        h := handlers.New(db, defSvc.Settings(), defSvc, hub, pw)
        h.Tenants = reg
        h.Shops = shopPool
        h.DefaultShop = defaultShop
        h.MasterSecret = []byte(masterSecret)
        h.Updater = update.NewChecker(version, update.Repo, defSvc.Settings())
        // Version stamps team-sync heartbeats (device roster shows releases).
        defSvc.SetVersion(version)
        shopPool.SetVersion(version)
        if ow, err := shopPool.Uploader(defaultShop); err == nil {
                h.Offsite = ow
        }

        h.Desktop.SignupAllowed = os.Getenv("ALLOW_SIGNUP") == "true"
        if desk != nil {
                h.Desktop = handlers.DesktopStatus{
                        Desktop:       true,
                        Version:       version,
                        FirstRun:      desk.FirstRun,
                        Port:          desk.Port,
                        SignupAllowed: os.Getenv("ALLOW_SIGNUP") == "true",
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

        // Background workers: print queue shared; sweeper, backups, and
        // uploads run per shop (default shop starts here, rest on open).
        go pw.Run(ctx)
        h.Shops.EnsureStarted(ctx, h.DefaultShop)
        go h.Updater.StartLoop(ctx)

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
        endpoint := fs.String("endpoint", "", "Supabase project URL")
        bucket := fs.String("bucket", "", "storage bucket name")
        apikey := fs.String("api-key", "", "service_role key (or env OFFSITE_API_KEY)")
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
        if *apikey == "" {
                *apikey = os.Getenv("OFFSITE_API_KEY")
        }
        if *pass == "" {
                *pass = os.Getenv("OFFSITE_PASSPHRASE")
        }
        cfg := offsite.Config{ProjectURL: *endpoint, Bucket: *bucket, Key: *apikey, Prefix: *prefix}
        if cfg.ProjectURL == "" || cfg.Bucket == "" || cfg.Key == "" {
                fmt.Fprintln(os.Stderr, "restore-backup: endpoint, bucket, and api key are required")
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
        rc, err := offsite.DownloadObject(ctx, cfg, *key)
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
