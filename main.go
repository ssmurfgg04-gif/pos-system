package main

import (
        "context"
        "embed"
        "fmt"
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
                            data under the OS user directory, opens your browser
  ledgerpos serve           server/appliance mode: env-driven (PORT, DB_PATH,
                            DB_DRIVER, POSTGRES_DSN, MDNS_ENABLED, …), binds :PORT
  ledgerpos uninstall       remove the desktop app (shortcuts, registry, files)
  ledgerpos version         print build version`

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

        // Background workers: STK sweeper + print queue + daily backups.
        sweeper := services.NewSweeper(svc)
        go sweeper.Run(ctx)
        go pw.Run(ctx)
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
        go func() {
                log.Printf("%s listening on %s (db=%s, mpesa=%s)", st.GetString("app_name", "Point of Sale"), ln.Addr().String(), cfg.DBDriver, st.GetString("mpesa_env", "mock"))
                if desk != nil && desk.OnReady != nil {
                        desk.OnReady()
                }
                if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
                        log.Fatalf("serve: %v", err)
                }
        }()

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
