// Package handlers is the thin HTTP/JSON layer over services.
package handlers

import (
        "database/sql"
        "errors"
        "net/http"
        "strconv"
        "strings"
        "time"

        "github.com/gin-gonic/gin"

        "posapp/internal/auth"
        "posapp/internal/database"
        "posapp/internal/offsite"
        "posapp/internal/printer"
        "posapp/internal/tenants"
        "posapp/internal/update"
        "posapp/internal/services"
        "posapp/internal/settings"
        "posapp/internal/ws"
)

type H struct {
        DB       *database.DB
        Settings *settings.Store
        Svc      *services.Service
        Hub      *ws.Hub
        Printer  *printer.Worker
        Offsite  *offsite.Worker
        Updater  *update.Checker
        // Multi-tenancy (set post-New; DefaultShop keeps single-shop behavior).
        Tenants      *tenants.Registry
        Shops        *services.ShopPool
        DefaultShop  string
        MasterSecret []byte
        LoginRL    *auth.RateLimiter
        PinRL      *auth.RateLimiter
        CallbackRL *auth.RateLimiter

        // Desktop (single-machine) mode. Set by main after New(); when
        // Desktop.Desktop is true the SPA shows an admin "Quit" affordance.
        Desktop DesktopStatus
        // OnQuit is invoked (once) when an admin POSTs /system/quit.
        OnQuit func()
}

// DesktopStatus reports how the binary was launched. Server mode keeps
// the zero value (desktop=false); desktop mode fills it in.
type DesktopStatus struct {
        Desktop       bool   `json:"desktop"`
        Version       string `json:"version,omitempty"`
        FirstRun      bool   `json:"firstRun,omitempty"`
        Port          string `json:"port,omitempty"`
        SignupAllowed bool   `json:"signupAllowed,omitempty"`
}

func New(db *database.DB, st *settings.Store, svc *services.Service, hub *ws.Hub, pw *printer.Worker) *H {
        return &H{
                DB: db, Settings: st, Svc: svc, Hub: hub, Printer: pw,
                // Dev checker by default (version "dev" never reports updates);
                // main overrides with the stamped build version.
                Updater: update.NewChecker("dev", update.Repo, st),
                LoginRL: auth.NewRateLimiter(60*time.Second, 10),
                PinRL:   auth.NewRateLimiter(60*time.Second, 15),
                // Callbacks are rare (one per STK push); throttling kills
                // blind checkout_request_id guessing without touching Daraja
                // retries (minutes apart).
                CallbackRL: auth.NewRateLimiter(60*time.Second, 20),
        }
}

func (h *H) ok(c *gin.Context, data any) {
        c.JSON(http.StatusOK, gin.H{"data": data})
}

func (h *H) created(c *gin.Context, data any) {
        c.JSON(http.StatusCreated, gin.H{"data": data})
}

func (h *H) fail(c *gin.Context, code int, msg string) {
        c.JSON(code, gin.H{"error": msg})
}

func (h *H) principal(c *gin.Context) *auth.Principal { return auth.FromContext(c) }

const ctxShopSvc = "shopSvc"

// WithShopService stores the request's resolved tenant service (set by the
// auth middleware after opening the shop from the token claim).
func WithShopService(c *gin.Context, s *services.Service) { c.Set(ctxShopSvc, s) }

// shopID resolves the request's tenant (empty = legacy/default context).
func (h *H) shopID(c *gin.Context) string {
        if id := auth.ShopID(c); id != "" {
                return id
        }
        return h.DefaultShop
}

// publicShopID resolves the tenant for unauthenticated endpoints from
// ?shop= (a terminal belongs to one shop), else the default shop.
func (h *H) publicShopID(c *gin.Context) string {
        if q := strings.TrimSpace(c.Query("shop")); q != "" {
                return q
        }
        return h.DefaultShop
}

// shopDB opens the public (unauthenticated) request's shop database.
func (h *H) shopDB(c *gin.Context) (*database.DB, error) {
        if h.Shops == nil {
                return h.DB, nil
        }
        return h.Shops.DB(h.publicShopID(c))
}

// shopSettings opens the public request's shop settings store.
func (h *H) shopSettings(c *gin.Context) (*settings.Store, error) {
        if h.Shops == nil {
                return h.Settings, nil
        }
        return h.Shops.Settings(h.publicShopID(c))
}

// svc returns the request's shop service. The auth middleware stores the
// resolved handle per request; the field fallback covers direct calls.
// Isolation by construction: every query below runs on the tenant's file.
func (h *H) svc(c *gin.Context) *services.Service {
        if v, ok := c.Get(ctxShopSvc); ok {
                if s, ok := v.(*services.Service); ok && s != nil {
                        return s
                }
        }
        return h.Svc
}

// db returns the request's shop database handle.
func (h *H) db(c *gin.Context) *database.DB {
        return h.svc(c).DB()
}

// settings returns the request's shop settings store.
func (h *H) settings(c *gin.Context) *settings.Store {
        return h.svc(c).Settings()
}

func (h *H) pathID(c *gin.Context, param string) (int64, bool) {
        id, err := strconv.ParseInt(c.Param(param), 10, 64)
        if err != nil || id <= 0 {
                h.fail(c, 400, "invalid id")
                return 0, false
        }
        return id, true
}

// mapErr converts domain errors to HTTP codes.
func (h *H) mapErr(c *gin.Context, err error) {
        if err == nil {
                return
        }
        msg := strings.ToLower(err.Error())
        switch {
        case errors.Is(err, services.ErrNotFound), errors.Is(err, sql.ErrNoRows):
                h.fail(c, 404, err.Error())
        case errors.Is(err, services.ErrNotConfigured):
                h.fail(c, 422, err.Error())
        case errors.Is(err, services.ErrInsufficientStock),
                errors.Is(err, services.ErrInvalidState),
                errors.Is(err, services.ErrOrderAlreadyPaid),
                errors.Is(err, services.ErrDuplicateReceipt),
                errors.Is(err, services.ErrCreditLimit),
                errors.Is(err, services.ErrOverpayment),
                errors.Is(err, services.ErrShiftOpen):
                h.fail(c, 409, err.Error())
        case strings.Contains(msg, "phone"), strings.Contains(msg, "receipt code"),
                strings.Contains(msg, "invalid payment"), strings.Contains(msg, "quantity"),
                strings.Contains(msg, "invalid design status"), strings.Contains(msg, "invalid payment mode"),
                strings.Contains(msg, "out of range"), strings.Contains(msg, "exceeds maximum"),
                strings.Contains(msg, "too many lines"):
                h.fail(c, 422, err.Error())
        case strings.Contains(msg, "needs a customer"):
                h.fail(c, 400, err.Error())
        case strings.Contains(msg, "is inactive"), strings.Contains(msg, "no credit"):
                h.fail(c, 409, err.Error())
        case strings.Contains(msg, "permission"):
                h.fail(c, 403, err.Error())
        default:
                h.fail(c, 500, err.Error())
        }
}

func btoi(b bool) int {
        if b {
                return 1
        }
        return 0
}

func itob(i int) bool { return i != 0 }
