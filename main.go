package main

import (
        "context"
        "embed"
        "io/fs"
        "log"
        "net/http"
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

func main() {
        cfg := config.Load()
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

        // LAN discovery broadcast (best-effort).
        if cfg.MDNSEnabled {
                mdns.Broadcast(ctx, st.GetString("app_name", "Point of Sale"), atoi(cfg.Port))
        }

        srv := &http.Server{
                Addr:    ":" + cfg.Port,
                Handler: engine,
        }
        go func() {
                log.Printf("%s listening on :%s (db=%s, mpesa=%s)", st.GetString("app_name", "Point of Sale"), cfg.Port, cfg.DBDriver, st.GetString("mpesa_env", "mock"))
                if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
                        log.Fatalf("listen: %v", err)
                }
        }()

        <-ctx.Done()
        log.Printf("shutting down...")
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
