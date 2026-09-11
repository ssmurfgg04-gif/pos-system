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
        "posapp/internal/printer"
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
        LoginRL  *auth.RateLimiter
        PinRL    *auth.RateLimiter

        // Desktop (single-machine) mode. Set by main after New(); when
        // Desktop.Desktop is true the SPA shows an admin "Quit" affordance.
        Desktop DesktopStatus
        // OnQuit is invoked (once) when an admin POSTs /system/quit.
        OnQuit func()
}

// DesktopStatus reports how the binary was launched. Server mode keeps
// the zero value (desktop=false); desktop mode fills it in.
type DesktopStatus struct {
        Desktop  bool   `json:"desktop"`
        Version  string `json:"version,omitempty"`
        FirstRun bool   `json:"firstRun,omitempty"`
        Port     string `json:"port,omitempty"`
}

func New(db *database.DB, st *settings.Store, svc *services.Service, hub *ws.Hub, pw *printer.Worker) *H {
        return &H{
                DB: db, Settings: st, Svc: svc, Hub: hub, Printer: pw,
                LoginRL: auth.NewRateLimiter(60*time.Second, 10),
                PinRL:   auth.NewRateLimiter(60*time.Second, 15),
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
        case errors.Is(err, services.ErrInsufficientStock),
                errors.Is(err, services.ErrInvalidState),
                errors.Is(err, services.ErrOrderAlreadyPaid),
                errors.Is(err, services.ErrDuplicateReceipt),
                errors.Is(err, services.ErrShiftOpen):
                h.fail(c, 409, err.Error())
        case strings.Contains(msg, "phone"), strings.Contains(msg, "receipt code"),
                strings.Contains(msg, "invalid payment"), strings.Contains(msg, "quantity"),
                strings.Contains(msg, "invalid design status"), strings.Contains(msg, "invalid payment mode"):
                h.fail(c, 422, err.Error())
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
