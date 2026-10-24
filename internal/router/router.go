// Package router wires every endpoint with its permission gates.
package router

import (
	"io/fs"
	"net/http"
	"os"
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
	r.Use(secureHeaders())
	r.Use(corsMiddleware())

	// Auth middleware: Bearer token → shop from claim → fresh principal
	// from that shop's file. Unknown shops and cross-shop tokens 403.
	authRequired := func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		token := strings.TrimPrefix(header, "Bearer ")
		if token == "" || header == token {
			c.AbortWithStatusJSON(401, gin.H{"error": "Authorization header required"})
			return
		}
		userID, shopID, issuedAt, err := auth.ParseToken(h.MasterSecret, token)
		if err != nil {
			c.AbortWithStatusJSON(401, gin.H{"error": "invalid or expired token"})
			return
		}
		if shopID == "" {
			shopID = h.DefaultShop // legacy pre-tenancy tokens
		}
		svc, err := h.Shops.Service(shopID)
		if err != nil {
			c.AbortWithStatusJSON(403, gin.H{"error": "unknown shop"})
			return
		}
		p, err := auth.LoadPrincipal(svc.DB(), userID)
		if err != nil {
			c.AbortWithStatusJSON(403, gin.H{"error": "account unavailable"})
			return
		}
		// Sessions die with the credentials they were issued for.
		// Millisecond-exact via the iat_ms claim (plain iat is only
		// second-precision, which cannot order same-second events).
		changedAt := auth.ParseChangedAt(p.PasswordChangedAt)
		if p.PasswordChangedAt != "" && issuedAt.Before(changedAt) {
			c.AbortWithStatusJSON(401, gin.H{"error": "session expired, sign in again"})
			return
		}
		p.ShopID = shopID
		auth.WithPrincipal(c, p)
		auth.WithShopID(c, shopID)
		handlers.WithShopService(c, svc)
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
	// Authentication lifecycle: first-run owner setup (replaces seeded
	// demo accounts) + the recovery-code forgot-password flow.
	api.GET("/auth/has-users", h.HasUsers)
	api.POST("/auth/bootstrap", h.Bootstrap)
	api.POST("/auth/forgot-password", h.ForgotPassword)
	// Team join link redemption (public: the single-use token IS the
	// credential — minted by the cloud, expiring, shown once).
	api.POST("/auth/team-join", h.TeamJoin)
	// Forgot-PIN recovery (public: the requester is locked out by
	// definition). The cloud gates every step on device identity; the
	// owner till approves and reads out a single-use code.
	api.POST("/auth/forgot-pin", h.ForgotPinRequest)
	api.GET("/auth/forgot-pin/status", h.ForgotPinStatus)
	api.POST("/auth/forgot-pin/redeem", h.ForgotPinRedeem)
	// Owner cloud sign-in (public: the cloud verifies the portal
	// credential and approves the device server-side) — for an owner
	// reinstalling the app or setting up a new till for an existing
	// business, so first-run never forces "create a new store".
	api.POST("/auth/owner-signin", h.OwnerSignin)
	api.POST("/auth/signup", h.Signup)
	api.GET("/branding", h.Branding)
	api.GET("/settings/logo", h.GetLogo)
	api.POST("/payments/mpesa/callback", h.MpesaCallback)
	// Paystack server-to-server events (HMAC-SHA512 verified in-handler).
	api.POST("/payments/paystack/webhook", h.PaystackWebhook)
	// Product photos are marketing content served to <img> tags (which
	// cannot send Authorization headers) — public like the brand logo.
	api.GET("/products/:id/image", h.GetProductImage)

	// ---- Authenticated ----
	authd := api.Group("", authRequired)
	authd.GET("/me", h.Me)
	authd.POST("/me/recovery-code", h.NewRecoveryCode)
	authd.GET("/shops", h.MyShops)
	authd.POST("/auth/switch", h.SwitchShop)
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
	prod.POST("/products/:id/image", h.UploadProductImage)
	prod.DELETE("/products/:id/image", h.DeleteProductImage)
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
	authd.POST("/orders/:id/recheck", perm("pos.sell"), h.OrderRecheck)
	authd.POST("/orders/:id/paystack/init", perm("pos.sell"), h.PaystackInit)
	authd.POST("/orders/:id/paystack/verify", perm("pos.sell"), h.PaystackVerify)
	authd.GET("/payments/config", h.PaymentConfig)
	authd.POST("/sync", perm("pos.sell"), h.Sync)

	// Parked sales (pos.hold) — hold, list, resume-read, discard.
	hold := authd.Group("", perm("pos.hold"))
	hold.POST("/held-sales", h.HoldSale)
	hold.GET("/held-sales", h.ListHeldSales)
	hold.DELETE("/held-sales/:id", h.DeleteHeldSale)

	// Void-reason catalog: every till offers the same reasons, and the
	// catalog itself team-syncs (settings.manage to edit).
	authd.GET("/void-reasons", h.ListVoidReasons)
	vr := authd.Group("", perm("settings.manage"))
	vr.POST("/void-reasons", h.CreateVoidReason)
	vr.PUT("/void-reasons/:id", h.UpdateVoidReason)

	// Reports / shifts. DailyReport doubles as the multi-store
	// consolidated summary (?shop=all sums every shop the user
	// belongs to, with per-shop rows).
	authd.GET("/reports/daily", perm("reports.view"), h.ConsolidatedDailyReport)
	authd.GET("/reports/monthly", perm("reports.view"), h.MonthlyReport)
	authd.GET("/reports/monthly.csv", perm("reports.view"), h.MonthlyReportCSV)
	authd.POST("/shifts/open", perm("shifts.manage"), h.OpenShift)
	authd.POST("/shifts/close", perm("shifts.manage"), h.CloseShift)
	authd.GET("/shifts/current", perm("shifts.manage"), h.CurrentShift)
	authd.GET("/shifts", perm("shifts.manage"), h.ListShifts)
	// X/Z cash-session report (X = live for the caller's open shift,
	// Z = any closed shift via ?shift=id).
	authd.GET("/shifts/report", perm("shifts.manage"), h.ShiftReportXZ)

	// Design board.
	authd.GET("/design", perm("design.view"), h.ListDesignJobs)
	design := authd.Group("", perm("design.manage"))
	design.POST("/design", h.CreateDesignJob)
	design.PUT("/design/:id", h.UpdateDesignJob)
	design.POST("/design/:id/move", h.MoveDesignJob)
	// Job attachments: share files with the team (upload/manage gated,
	// download rides design.view).
	design.POST("/design/:id/files", h.UploadDesignFile)
	design.DELETE("/design/:id/files/:fileId", h.DeleteDesignFile)
	authd.GET("/design/:id/files", perm("design.view"), h.ListDesignFiles)
	authd.GET("/design/:id/files/:fileId", perm("design.view"), h.DownloadDesignFile)

	// Customers & tabs.
	custv := authd.Group("", perm("customers.view"))
	custv.GET("/customers", h.ListCustomers)
	custv.GET("/customers/:id/ledger", h.CustomerLedger)
	custv.GET("/customers/:id/orders", h.CustomerOrders)
	custm := authd.Group("", perm("customers.manage"))
	custm.POST("/customers", h.CreateCustomer)
	custm.PUT("/customers/:id", h.UpdateCustomer)
	custm.POST("/customers/:id/adjustments", h.RecordCustomerAdjustment)
	custm.POST("/customers/:id/credit-topup", h.TopUpCredit)
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
	users.GET("/team", h.TeamOverview)
	users.POST("/users", h.CreateUser)
	users.PUT("/users/:id", h.UpdateUser)
	users.DELETE("/users/:id", h.DeactivateUser)
	// Everyone rotates their own credentials (self-service); managing
	// others still needs users.manage (enforced inside the handlers).
	authd.PUT("/users/:id/password", h.SetPassword)
	authd.PUT("/users/:id/pin", h.SetPIN)

	roles := authd.Group("", perm("roles.manage"))
	roles.GET("/roles", h.ListRoles)
	roles.GET("/roles/:id", h.GetRole)
	roles.GET("/permissions", h.Permissions)
	roles.POST("/roles", h.CreateRole)
	roles.PUT("/roles/:id", h.UpdateRole)
	roles.PUT("/roles/:id/dashboard", h.SetRoleDashboard)
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
	authd.GET("/system/update", perm("settings.manage"), h.UpdateStatus)
	authd.POST("/system/update/refresh", perm("settings.manage"), h.UpdateRefresh)
	authd.POST("/system/update/download", perm("settings.manage"), h.UpdateDownload)
	authd.POST("/system/update/install", perm("settings.manage"), h.UpdateInstall)
	authd.POST("/system/update/undo", perm("settings.manage"), h.UndoUpdate)
	// Desktop-mode admin shutdown (no-op in server mode: OnQuit unset).
	authd.POST("/system/quit", perm("settings.manage"), h.QuitApp)

	// Team sync admin (Settings → Team): link tills through Supabase.
	ts := authd.Group("", perm("settings.manage"))
	ts.GET("/team-sync", h.TeamSyncStatus)
	ts.PUT("/team-sync", h.TeamSyncConfigure)
	ts.POST("/team-sync/create", h.TeamSyncCreate)
	ts.POST("/team-sync/now", h.TeamSyncNow)
	ts.POST("/team-sync/retry-failed", h.TeamSyncRetryFailed)
	ts.POST("/team-sync/use-cloud", h.TeamSyncUseCloud)
	// Multi-store: stores under one owner cloud + device assignment.
	ts.POST("/team-sync/stores", h.TeamStoreCreate)
	ts.POST("/team-sync/assign", h.TeamDeviceAssign)
	ts.POST("/team-sync/remove", h.TeamDeviceRemove)
	ts.POST("/team-sync/approve", h.TeamDeviceApprove)
	// Team join links (owner mints; workers redeem from a new machine).
	ts.POST("/team-sync/invites", h.TeamInviteCreate)
	ts.POST("/team-sync/invites/revoke", h.TeamInviteRevoke)
	// Forgot-PIN recovery: open requests from locked tills + approve.
	ts.GET("/team-sync/pin-resets", h.PinResetList)
	ts.POST("/team-sync/pin-resets/approve", h.PinResetApprove)
	// Owner-approval dialog: mint + cache the cloud owner capability
	// (v1.3.2 — cloud owner RPCs verify a HUMAN, not just a device).
	ts.POST("/team-sync/owner-approval", h.TeamOwnerApproval)
	ts.GET("/team-sync/owner-approval", h.TeamOwnerApprovalStatus)

	// Stocktake + gift cards (count sessions, code redemption).
	ct := authd.Group("", perm("suppliers.manage"))
	ct.GET("/stock-counts", h.ListStockCounts)
	ct.POST("/stock-counts", h.StartStockCount)
	ct.GET("/stock-counts/:id", h.GetStockCount)
	ct.PUT("/stock-counts/:id/lines", h.SaveCountLine)
	ct.POST("/stock-counts/:id/complete", h.CompleteStockCount)
	authd.POST("/gift-cards/redeem", perm("credit.manage"), h.RedeemGiftCard)
	authd.GET("/gift-cards", perm("credit.manage"), h.ListGiftCards)

	authd.POST("/orders/:id/notify", perm("orders.notify"), h.NotifyOrderReady)
	// Quotations: view for the till floor, manage to edit the pipeline.
	authd.GET("/quotes", perm("quotes.view"), h.ListQuotes)
	authd.GET("/quotes/:id", perm("quotes.view"), h.GetQuote)
	qm := authd.Group("", perm("quotes.manage"))
	qm.POST("/quotes", h.CreateQuote)
	qm.PUT("/quotes/:id", h.UpdateQuote)
	qm.POST("/quotes/:id/status", h.SetQuoteStatus)
	qm.POST("/quotes/:id/convert", h.ConvertQuote)
	authd.POST("/design-jobs/:id/notify", perm("orders.notify"), h.NotifyJobReady)
	authd.GET("/orders/:id/events", perm("orders.view"), h.GetOrderEvents)
	authd.POST("/orders/:id/notes", perm("orders.view"), h.AddOrderNote)
	authd.POST("/orders/:id/jobs", perm("orders.assign"), h.CreateJobFromOrder)
	authd.GET("/assignees", perm("orders.assign"), h.ListAssignees)
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

// secureHeaders sets the browser-facing defense headers on every
// response. The CSP is the XSS backstop for the desktop webview AND any
// browser tab on the LAN: scripts may only come from this origin plus the
// Paystack popup toolkit (which runs in its own frame and cannot read
// this origin's storage — the reason the session token in localStorage is
// acceptable). Inline handlers and third-party packages are refused;
// style-src keeps 'unsafe-inline' because React style attributes need it.
func secureHeaders() gin.HandlerFunc {
	csp := strings.Join([]string{
		"default-src 'self'",
		"script-src 'self' https://js.paystack.co",
		"script-src-elem 'self' https://js.paystack.co",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob:",
		"font-src 'self' data:",
		"connect-src 'self' https://api.paystack.co https://*.supabase.co ws: wss:",
		"frame-src https://js.paystack.co",
		"frame-ancestors 'self'",
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
	}, "; ")
	return func(c *gin.Context) {
		c.Header("Content-Security-Policy", csp)
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "SAMEORIGIN")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		c.Next()
	}
}

func corsMiddleware() gin.HandlerFunc {
	// Strict origin allowlist. The appliance serves its own SPA (same-origin)
	// and dev-mode Vite proxies through :3000, so legitimate browser clients
	// never need cross-origin CORS. The previous wildcard (`*`) let ANY website
	// open in a cashier's browser read the staff/PIN list cross-origin —
	// the /auth/pin-users enumeration surface. Extra origins (e.g. BYO-domain
	// hosting) can be listed in CORS_EXTRA_ORIGINS (comma-separated).
	// Non-allowlisted origins get no ACAO header (browsers block the read)
	// and preflights get 403.
	allowed := map[string]bool{
		"http://localhost:5173":  true,
		"http://127.0.0.1:5173":  true,
		"https://localhost:5173": true,
		"http://localhost:3000":  true,
		"http://127.0.0.1:3000":  true,
	}
	for _, o := range strings.Split(os.Getenv("CORS_EXTRA_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			allowed[o] = true
		}
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		// No Origin header = same-origin / server-to-server; an Origin equal
		// to the request host is the SPA served by this very process.
		sameOrigin := origin == "" ||
			origin == "http://"+c.Request.Host ||
			origin == "https://"+c.Request.Host
		if !sameOrigin && allowed[origin] {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type")
		}
		if c.Request.Method == http.MethodOptions {
			if sameOrigin || allowed[origin] {
				c.AbortWithStatus(http.StatusNoContent)
			} else {
				c.AbortWithStatus(http.StatusForbidden)
			}
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
