// Package router wires every endpoint with its permission gates.
package router

import (
        "io/fs"
        "net/http"
        "strings"

        "github.com/gin-gonic/gin"

        "posapp/internal/auth"
        "posapp/internal/handlers"
)

// New builds the gin engine. Public: health, login, PIN, pin-users,
// branding, M-Pesa callback, and the embedded SPA. Everything else needs
// Bearer auth plus the route's permission.
func New(h *handlers.H, frontend fs.FS) *gin.Engine {
        r := gin.New()
        r.Use(gin.Recovery())
        r.Use(corsMiddleware())

        // Auth middleware: Bearer token → fresh principal from DB.
        authRequired := func(c *gin.Context) {
                header := c.GetHeader("Authorization")
                token := strings.TrimPrefix(header, "Bearer ")
                if token == "" || header == token {
                        c.AbortWithStatusJSON(401, gin.H{"error": "Authorization header required"})
                        return
                }
                userID, err := auth.ParseToken(h.Settings.JWTSecret(), token)
                if err != nil {
                        c.AbortWithStatusJSON(401, gin.H{"error": "invalid or expired token"})
                        return
                }
                p, err := auth.LoadPrincipal(h.DB, userID)
                if err != nil {
                        c.AbortWithStatusJSON(403, gin.H{"error": "account unavailable"})
                        return
                }
                auth.WithPrincipal(c, p)
                // Seeded defaults stop working until rotated: only identity,
                // rotation, and public-config endpoints stay reachable.
                if p.MustRotate {
                        path := c.Request.URL.Path
                        allowed := path == "/api/v1/me" ||
                                path == "/api/v1/branding" ||
                                path == "/api/v1/health" ||
                                strings.HasPrefix(path, "/api/v1/auth/") ||
                                strings.HasSuffix(path, "/password") ||
                                strings.HasSuffix(path, "/pin")
                        if !allowed {
                                c.AbortWithStatusJSON(403, gin.H{"error": "password rotation required"})
                                return
                        }
                }
                c.Next()
        }
        perm := auth.RequirePermission

        api := r.Group("/api/v1")

        // ---- Public ----
        api.GET("/health", func(c *gin.Context) {
                c.JSON(200, gin.H{"status": "ok"})
        })
        api.GET("/system/desktop", h.DesktopInfo)
        api.POST("/auth/login", h.Login)
        api.GET("/auth/pin-users", h.PinUsers)
        api.POST("/auth/pin", h.PinLogin)
        api.GET("/branding", h.Branding)
        api.GET("/settings/logo", h.GetLogo)
        api.POST("/payments/mpesa/callback", h.MpesaCallback)

        // ---- Authenticated ----
        authd := api.Group("", authRequired)
        authd.GET("/me", h.Me)
        authd.GET("/ws", gin.WrapH(h.Hub))

        // Products & categories (POS reads; manage gated).
        authd.GET("/products", h.ListProducts)
        authd.GET("/products/low-stock", perm("products.view"), h.ListLowStock)
        authd.GET("/categories", h.ListCategories)

        prod := authd.Group("", perm("products.manage"))
        prod.POST("/products", h.CreateProduct)
        prod.PUT("/products/:id", h.UpdateProduct)
        prod.DELETE("/products/:id", h.DeactivateProduct)
        prod.POST("/products/:id/adjust-stock", h.AdjustStock)
        prod.POST("/products/import", h.ProductsImport)
        prod.GET("/products/export", h.ProductsExport)
        prod.GET("/products/template", h.ProductsTemplate)
        prod.POST("/categories", h.CreateCategory)
        prod.PUT("/categories/:id", h.UpdateCategory)
        prod.DELETE("/categories/:id", h.DeleteCategory)

        // Orders & payments.
        authd.GET("/orders", perm("orders.view"), h.ListOrders)
        authd.GET("/orders/:id", perm("orders.view"), h.GetOrder)
        authd.GET("/orders/:id/receipt", perm("orders.view"), h.ReceiptHTML)
        authd.POST("/orders/checkout", perm("pos.sell"), h.Checkout)
        authd.POST("/orders/:id/void", perm("pos.void"), h.VoidOrder)
        authd.POST("/orders/:id/settle", perm("pos.sell"), h.SettleTab)
        authd.POST("/orders/:id/stkpush", perm("pos.sell"), h.RetrySTK)
        authd.POST("/orders/:id/manual", perm("payments.manual"), h.ManualConfirm)
        authd.POST("/sync", perm("pos.sell"), h.Sync)

        // Reports / shifts.
        authd.GET("/reports/daily", perm("reports.view"), h.DailyReport)
        authd.GET("/reports/monthly", perm("reports.view"), h.MonthlyReport)
        authd.GET("/reports/monthly.csv", perm("reports.view"), h.MonthlyReportCSV)
        authd.POST("/shifts/open", perm("shifts.manage"), h.OpenShift)
        authd.POST("/shifts/close", perm("shifts.manage"), h.CloseShift)
        authd.GET("/shifts/current", perm("shifts.manage"), h.CurrentShift)
        authd.GET("/shifts", perm("shifts.manage"), h.ListShifts)

        // Design board.
        authd.GET("/design", perm("design.view"), h.ListDesignJobs)
        design := authd.Group("", perm("design.manage"))
        design.POST("/design", h.CreateDesignJob)
        design.PUT("/design/:id", h.UpdateDesignJob)
        design.POST("/design/:id/move", h.MoveDesignJob)

        // Customers & tabs.
        custv := authd.Group("", perm("customers.view"))
        custv.GET("/customers", h.ListCustomers)
        custv.GET("/customers/:id/ledger", h.CustomerLedger)
        custm := authd.Group("", perm("customers.manage"))
        custm.POST("/customers", h.CreateCustomer)
        custm.PUT("/customers/:id", h.UpdateCustomer)
        custm.POST("/customers/:id/adjustments", h.RecordCustomerAdjustment)
        // Walk-in till payments ride pos.sell: cashiers take them all day.
        authd.POST("/customers/:id/payments", perm("pos.sell"), h.RecordCustomerPayment)

        // Suppliers & stock-in.
        supv := authd.Group("", perm("suppliers.view"))
        supv.GET("/suppliers", h.ListSuppliers)
        supv.GET("/purchase-orders", h.ListPOs)
        supv.GET("/purchase-orders/:id", h.GetPO)
        supv.GET("/stock-takes", h.ListTakes)
        supv.GET("/stock-takes/:id", h.GetTake)
        supm := authd.Group("", perm("suppliers.manage"))
        supm.POST("/suppliers", h.CreateSupplier)
        supm.PUT("/suppliers/:id", h.UpdateSupplier)
        supm.POST("/purchase-orders", h.CreatePO)
        supm.POST("/purchase-orders/:id/receive", h.ReceivePO)
        supm.POST("/purchase-orders/:id/cancel", h.CancelPO)
        supm.POST("/stock-takes", h.CreateTake)
        supm.POST("/stock-takes/:id/count", h.CountTake)
        supm.POST("/stock-takes/:id/apply", h.ApplyTake)
        supm.POST("/stock-takes/:id/cancel", h.CancelTake)

        // Users & roles.
        users := authd.Group("", perm("users.manage"))
        users.GET("/users", h.ListUsers)
        users.POST("/users", h.CreateUser)
        users.PUT("/users/:id", h.UpdateUser)
        users.DELETE("/users/:id", h.DeactivateUser)
        // Everyone rotates their own credentials (self-service); managing
        // others still needs users.manage (enforced inside the handlers).
        authd.PUT("/users/:id/password", h.SetPassword)
        authd.PUT("/users/:id/pin", h.SetPIN)

        roles := authd.Group("", perm("roles.manage"))
        roles.GET("/roles", h.ListRoles)
        roles.GET("/permissions", h.Permissions)
        roles.POST("/roles", h.CreateRole)
        roles.PUT("/roles/:id", h.UpdateRole)
        roles.DELETE("/roles/:id", h.DeleteRole)

        // Settings, printer, audit, system.
        authd.GET("/settings", perm("settings.manage"), h.GetSettings)
        authd.PUT("/settings", perm("settings.manage"), h.UpdateSettings)
        authd.POST("/settings/logo", perm("settings.manage"), h.UploadLogo)
        authd.DELETE("/settings/logo", perm("settings.manage"), h.DeleteLogo)
        authd.POST("/settings/test-print", perm("printer.test"), h.TestPrint)
        authd.POST("/printer/kick", perm("printer.test"), h.KickDrawer)
        authd.GET("/print-jobs", perm("printer.test"), h.ListPrintJobs)
        authd.GET("/audit", perm("audit.view"), h.ListAudit)
        authd.POST("/system/backup", perm("settings.manage"), h.RunBackup)
        authd.GET("/system/backups", perm("settings.manage"), h.ListBackups)
        authd.GET("/system/offsite", perm("settings.manage"), h.OffsiteStatus)
        // Desktop-mode admin shutdown (no-op in server mode: OnQuit unset).
        authd.POST("/system/quit", perm("settings.manage"), h.QuitApp)

        // ---- Embedded SPA ----
        // NOTE: gin only runs group middleware for matched routes, so the
        // static file serving must live inside NoRoute (the handler for
        // every unmatched path): try the real file first, then fall back
        // to index.html for client-side routes. API 404s stay JSON.
        if frontend != nil {
                r.GET("/", serveIndex(frontend))
                r.NoRoute(func(c *gin.Context) {
                        if strings.HasPrefix(c.Request.URL.Path, "/api/") {
                                c.JSON(404, gin.H{"error": "not found"})
                                return
                        }
                        if c.Request.Method == http.MethodGet && serveStatic(frontend, c) {
                                return
                        }
                        serveIndex(frontend)(c)
                })
        }
        return r
}

// serveStatic serves a real embedded file (with proper MIME) when it
// exists. Returns false when the path is not a file (SPA fallback ahead).
func serveStatic(frontend fs.FS, c *gin.Context) bool {
        path := strings.TrimPrefix(c.Request.URL.Path, "/")
        if path == "" {
                return false
        }
        f, err := frontend.Open(path)
        if err != nil {
                return false
        }
        st, serr := f.Stat()
        f.Close()
        if serr != nil || st.IsDir() {
                return false
        }
        http.FileServer(http.FS(frontend)).ServeHTTP(c.Writer, c.Request)
        return true
}

func corsMiddleware() gin.HandlerFunc {
        return func(c *gin.Context) {
                // LAN appliance: terminals and admin browsers hit the box directly
                // or through the embedded SPA (same-origin). Allow all origins for
                // dev-mode Vite (5173 → 3000 proxy) and BYO-domain hosting.
                c.Header("Access-Control-Allow-Origin", "*")
                c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
                c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type")
                if c.Request.Method == http.MethodOptions {
                        c.AbortWithStatus(204)
                        return
                }
                c.Next()
        }
}

func serveIndex(frontend fs.FS) gin.HandlerFunc {
        return func(c *gin.Context) {
                data, err := fs.ReadFile(frontend, "index.html")
                if err != nil {
                        c.String(500, "frontend not built")
                        return
                }
                c.Data(200, "text/html; charset=utf-8", data)
        }
}
