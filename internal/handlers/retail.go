package handlers

// retail.go — HTTP layer for the retail expansion: parked/held sales,
// void-reason catalog, store-credit top-ups, team overview, per-role
// dashboards, design-job attachments, and product photos.

import (
        "encoding/base64"
        "fmt"
        "io"
        "net/http"
        "strings"
        "time"

        "github.com/gin-gonic/gin"

        "posapp/internal/models"
)

// ---- Held (parked) sales ----

type holdBody struct {
        RefName string                 `json:"refName"`
        Cart    models.CheckoutRequest `json:"cart" binding:"required"`
}

// HoldSale (pos.hold) parks the current cart.
func (h *H) HoldSale(c *gin.Context) {
        p := h.principal(c)
        var body holdBody
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, "cart required")
                return
        }
        sale, err := h.svc(c).HoldSale(body.RefName, body.Cart, p)
        if err != nil {
                h.mapErr(c, err)
                return
        }
        h.created(c, sale)
}

// ListHeldSales (pos.hold) — parked carts for the resume drawer.
func (h *H) ListHeldSales(c *gin.Context) {
        sales, err := h.svc(c).ListHeldSales()
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        h.ok(c, sales)
}

// DeleteHeldSale (pos.hold) discards a parked cart.
func (h *H) DeleteHeldSale(c *gin.Context) {
        p := h.principal(c)
        id, ok := h.pathID(c, "id")
        if !ok {
                return
        }
        if err := h.svc(c).DeleteHeldSale(id, p); err != nil {
                h.mapErr(c, err)
                return
        }
        h.ok(c, gin.H{"deleted": true})
}

// ---- Void reasons ----

// ListVoidReasons (any authenticated user — the till dropdown needs it).
func (h *H) ListVoidReasons(c *gin.Context) {
        all := c.Query("all") == "true"
        reasons, err := h.svc(c).ListVoidReasons(all)
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        h.ok(c, reasons)
}

type voidReasonBody struct {
        Label  string `json:"label" binding:"required"`
        Active *bool  `json:"active"`
}

// CreateVoidReason (settings.manage) adds a reason; the change team-syncs.
func (h *H) CreateVoidReason(c *gin.Context) {
        p := h.principal(c)
        var body voidReasonBody
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, "label required")
                return
        }
        r, err := h.svc(c).CreateVoidReason(body.Label, p)
        if err != nil {
                h.mapErr(c, err)
                return
        }
        h.svc(c).EmitVoidReason(r.Label, r.Active, r.SortOrder)
        h.created(c, r)
}

// UpdateVoidReason (settings.manage) renames / toggles a reason.
func (h *H) UpdateVoidReason(c *gin.Context) {
        p := h.principal(c)
        id, ok := h.pathID(c, "id")
        if !ok {
                return
        }
        var body voidReasonBody
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, "label required")
                return
        }
        active := true
        if body.Active != nil {
                active = *body.Active
        }
        // Read the existing row first (sync emits the full record).
        reasons, err := h.svc(c).ListVoidReasons(true)
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        var existing *models.VoidReason
        for i := range reasons {
                if reasons[i].ID == id {
                        existing = &reasons[i]
                }
        }
        if existing == nil {
                h.fail(c, 404, "reason not found")
                return
        }
        if err := h.svc(c).UpdateVoidReason(id, body.Label, active, p); err != nil {
                h.mapErr(c, err)
                return
        }
        h.svc(c).EmitVoidReason(body.Label, active, existing.SortOrder)
        h.ok(c, gin.H{"updated": true})
}

// ---- Store credit ----

type creditTopUpBody struct {
        AmountCents int64  `json:"amountCents" binding:"required,min=1"`
        Note        string `json:"note"`
}

// TopUpCredit (credit.manage) records a prepaid top-up.
func (h *H) TopUpCredit(c *gin.Context) {
        p := h.principal(c)
        id, ok := h.pathID(c, "id")
        if !ok {
                return
        }
        var body creditTopUpBody
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, "amountCents (positive) required")
                return
        }
        cust, err := h.svc(c).TopUpStoreCredit(id, body.AmountCents, body.Note, p)
        if err != nil {
                h.mapErr(c, err)
                return
        }
        h.svc(c).EmitLedger(id, models.LedgerCreditTopup, body.AmountCents, 0, body.Note)
        h.ok(c, cust)
}

// ---- Team overview + role dashboards ----

// TeamOverview (users.manage) — who is on the team and what moved today.
func (h *H) TeamOverview(c *gin.Context) {
        members, err := h.svc(c).TeamOverview()
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        h.ok(c, members)
}

// GetRole (roles.manage) — full role incl. dashboard config.
func (h *H) GetRole(c *gin.Context) {
        id, ok := h.pathID(c, "id")
        if !ok {
                return
        }
        role, err := h.svc(c).RoleByID(id)
        if err != nil {
                h.mapErr(c, err)
                return
        }
        h.ok(c, role)
}

type dashboardBody struct {
        HomePage string                  `json:"homePage"`
        Config   *models.DashboardConfig `json:"config"`
}

// SetRoleDashboard (roles.manage) — admins edit what each role lands on
// and which nav sections its members see.
func (h *H) SetRoleDashboard(c *gin.Context) {
        p := h.principal(c)
        id, ok := h.pathID(c, "id")
        if !ok {
                return
        }
        var body dashboardBody
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, err.Error())
                return
        }
        if body.Config != nil && !validHomePage(body.HomePage) {
                h.fail(c, 422, "unknown home page")
                return
        }
        if err := h.svc(c).SetRoleDashboard(id, body.HomePage, body.Config); err != nil {
                h.mapErr(c, err)
                return
        }
        h.svc(c).Audit(p.ID, p.Username, "ROLE_DASHBOARD_SET", "role", itoa64(id), body.HomePage)
        h.ok(c, gin.H{"updated": true})
}

// validHomePage whitelists the landing-page keys the SPA routes accept.
func validHomePage(page string) bool {
        switch page {
        case "", "pos", "orders", "inventory", "customers", "suppliers",
                "shifts", "reports", "design", "team", "settings", "users":
                return true
        }
        return false
}

// ---- Design job attachments ----

const maxDesignFileSize = 5 << 20 // 5 MB — matches the services cap

// UploadDesignFile (design.manage) accepts a multipart "file" share.
func (h *H) UploadDesignFile(c *gin.Context) {
        p := h.principal(c)
        id, ok := h.pathID(c, "id")
        if !ok {
                return
        }
        file, header, err := c.Request.FormFile("file")
        if err != nil {
                h.fail(c, 400, "multipart file field required")
                return
        }
        defer file.Close()
        if header.Size > maxDesignFileSize {
                h.fail(c, 413, "file too large (max 5 MB)")
                return
        }
        data, err := io.ReadAll(io.LimitReader(file, maxDesignFileSize+1))
        if err != nil || len(data) > maxDesignFileSize {
                h.fail(c, 413, "file too large (max 5 MB)")
                return
        }
        filename := sanitizeFilename(header.Filename)
        if filename == "" {
                h.fail(c, 422, "filename required")
                return
        }
        f, err := h.svc(c).AddDesignFile(id, filename, header.Header.Get("Content-Type"), data, p)
        if err != nil {
                h.mapErr(c, err)
                return
        }
        h.created(c, f)
}

// ListDesignFiles (design.view) — attachment metadata for one job.
func (h *H) ListDesignFiles(c *gin.Context) {
        id, ok := h.pathID(c, "id")
        if !ok {
                return
        }
        files, err := h.svc(c).ListDesignFiles(id)
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        h.ok(c, files)
}

// DownloadDesignFile (design.view) streams the attachment bytes.
func (h *H) DownloadDesignFile(c *gin.Context) {
        jobID, ok1 := h.pathID(c, "id")
        fileID, ok2 := h.pathID(c, "fileId")
        if !ok1 || !ok2 {
                return
        }
        f, data, err := h.svc(c).ReadDesignFile(jobID, fileID)
        if err != nil {
                h.mapErr(c, err)
                return
        }
        c.Data(http.StatusOK, fileMimeOr(f.Mime), data)
}

// DeleteDesignFile (design.manage).
func (h *H) DeleteDesignFile(c *gin.Context) {
        p := h.principal(c)
        jobID, ok1 := h.pathID(c, "id")
        fileID, ok2 := h.pathID(c, "fileId")
        if !ok1 || !ok2 {
                return
        }
        if err := h.svc(c).DeleteDesignFile(jobID, fileID, p); err != nil {
                h.mapErr(c, err)
                return
        }
        h.ok(c, gin.H{"deleted": true})
}

func fileMimeOr(mime string) string {
        if strings.TrimSpace(mime) == "" {
                return "application/octet-stream"
        }
        return mime
}

// sanitizeFilename strips path components and control chars from an
// uploaded filename (defense against traversal and header injection).
func sanitizeFilename(name string) string {
        name = strings.ReplaceAll(name, "\\", "/")
        if idx := strings.LastIndex(name, "/"); idx >= 0 {
                name = name[idx+1:]
        }
        var b strings.Builder
        for _, r := range name {
                if r < 32 || r > 126 || r == '"' || r == '\'' {
                        continue
                }
                b.WriteRune(r)
        }
        out := strings.TrimSpace(b.String())
        if len(out) > 160 {
                out = out[len(out)-160:] // keep the extension end
        }
        return out
}

// ---- Product photos ----

const (
        maxProductImageBytes = 2 << 20 // 2 MB raw
        allowedImagePrefix   = "image/"
)

// UploadProductImage (products.manage) stores the photo as a data URL on
// the product row and serves it cache-busted from a public GET route.
func (h *H) UploadProductImage(c *gin.Context) {
        p := h.principal(c)
        id, ok := h.pathID(c, "id")
        if !ok {
                return
        }
        file, header, err := c.Request.FormFile("file")
        if err != nil {
                h.fail(c, 400, "multipart file field required")
                return
        }
        defer file.Close()
        if header.Size > maxProductImageBytes+64<<10 {
                h.fail(c, 413, "image too large (max 2 MB)")
                return
        }
        data, err := io.ReadAll(io.LimitReader(file, maxProductImageBytes+1))
        if err != nil || len(data) > maxProductImageBytes {
                h.fail(c, 413, "image too large (max 2 MB)")
                return
        }
        mime := header.Header.Get("Content-Type")
        if !strings.HasPrefix(mime, allowedImagePrefix) ||
                strings.Contains(mime, "svg") { // svg can carry scripts — never allow
                h.fail(c, 422, "only png, jpeg, webp or gif images are allowed")
                return
        }
        // Double-check magic bytes — the Content-Type header is client-set.
        if !sniffedImage(data) {
                h.fail(c, 422, "file content is not a recognised image")
                return
        }
        dataURL := fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(data))
        res, err := h.db(c).Exec(h.db(c).Rebind(`UPDATE products SET image_url = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`), dataURL, id)
        if err != nil || n(res) != 1 {
                h.fail(c, 404, "product not found")
                return
        }
        var updatedAt string
        h.db(c).QueryRow(`SELECT updated_at FROM products WHERE id = ?`, id).Scan(&updatedAt)
        h.svc(c).Audit(p.ID, p.Username, "PRODUCT_IMAGE_SET", "product", itoa64(id), fmt.Sprintf("%d bytes", len(data)))
        h.svc(c).EmitProduct(id, false)
        h.ok(c, gin.H{"imageUrl": fmt.Sprintf("/api/v1/products/%d/image?v=%d", id, time.Now().Unix())})
}

// DeleteProductImage (products.manage).
func (h *H) DeleteProductImage(c *gin.Context) {
        p := h.principal(c)
        id, ok := h.pathID(c, "id")
        if !ok {
                return
        }
        res, err := h.db(c).Exec(h.db(c).Rebind(`UPDATE products SET image_url = '', updated_at = CURRENT_TIMESTAMP WHERE id = ?`), id)
        if err != nil || n(res) != 1 {
                h.fail(c, 404, "product not found")
                return
        }
        h.svc(c).Audit(p.ID, p.Username, "PRODUCT_IMAGE_CLEARED", "product", itoa64(id), "")
        h.svc(c).EmitProduct(id, false)
        h.ok(c, gin.H{"deleted": true})
}

// sniffedImage validates magic bytes for the image formats we allow.
func sniffedImage(b []byte) bool {
        switch {
        case len(b) >= 8 && b[0] == 0x89 && b[1] == 'P' && b[2] == 'N' && b[3] == 'G':
                return true // png
        case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
                return true // jpeg
        case len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
                return true // gif
        case len(b) >= 12 && string(b[8:12]) == "WEBP":
                return true // webp
        }
        return false
}

// GetProductImage is PUBLIC (like the brand logo): <img> tags cannot send
// Authorization headers, and product photos are marketing content.
func (h *H) GetProductImage(c *gin.Context) {
        id, ok := h.pathID(c, "id")
        if !ok {
                return
        }
        var dataURL string
        err := h.db(c).QueryRow(`SELECT COALESCE(image_url,'') FROM products WHERE id = ?`, id).Scan(&dataURL)
        if err != nil || dataURL == "" {
                c.Status(http.StatusNotFound)
                return
        }
        idx := strings.Index(dataURL, ";base64,")
        if idx < 0 || !strings.HasPrefix(dataURL, "data:") {
                c.Status(http.StatusNotFound)
                return
        }
        mime := strings.TrimPrefix(dataURL[:idx], "data:")
        raw, err := base64.StdEncoding.DecodeString(dataURL[idx+len(";base64,"):])
        if err != nil {
                c.Status(http.StatusNotFound)
                return
        }
        c.Header("Cache-Control", "public, max-age=86400")
        c.Data(http.StatusOK, fileMimeOr(mime), raw)
}

// ---- Team sync (Settings → Team) ----

// TeamSyncStatus (settings.manage) — link status + device roster.
func (h *H) TeamSyncStatus(c *gin.Context) {
        st, err := h.svc(c).TeamSyncStatus(h.svc(c).AppVersion())
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        h.ok(c, st)
}

type teamSyncConfigBody struct {
        ProjectURL string `json:"projectUrl"`
        ServiceKey string `json:"serviceKey"`
        TeamCode   string `json:"teamCode"`
        Enabled    *bool  `json:"enabled"`
}

// TeamSyncConfigure (settings.manage) — save/enable the cloud link.
func (h *H) TeamSyncConfigure(c *gin.Context) {
        p := h.principal(c)
        var body teamSyncConfigBody
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, err.Error())
                return
        }
        req := models.TeamSyncConfigRequest(body)
        if err := h.svc(c).TeamSyncConfigure(req); err != nil {
                h.fail(c, 422, err.Error())
                return
        }
        h.svc(c).Audit(p.ID, p.Username, "TEAM_SYNC_CONFIGURED", "settings", "", "team sync settings changed")
        h.ok(c, gin.H{"saved": true})
}

// TeamSyncCreate (settings.manage) — mint a team code on the first device.
func (h *H) TeamSyncCreate(c *gin.Context) {
        p := h.principal(c)
        code, err := h.svc(c).TeamSyncCreate()
        if err != nil {
                h.fail(c, 422, err.Error())
                return
        }
        h.svc(c).Audit(p.ID, p.Username, "TEAM_SYNC_CREATED", "settings", "", code)
        h.ok(c, gin.H{"teamCode": code})
}

// TeamSyncNow (settings.manage) — push+pull immediately.
func (h *H) TeamSyncNow(c *gin.Context) {
        pushed, applied, err := h.svc(c).TeamSyncNow(h.svc(c).AppVersion())
        if err != nil {
                h.fail(c, 502, err.Error())
                return
        }
        h.ok(c, gin.H{"pushed": pushed, "applied": applied})
}

// TeamSyncUseCloud (settings.manage) — revert this till to automatic cloud
// identity after a manual configuration (the join-code-free default).
func (h *H) TeamSyncUseCloud(c *gin.Context) {
        p := h.principal(c)
        if err := h.svc(c).TeamSyncUseCloud(); err != nil {
                h.fail(c, 502, err.Error())
                return
        }
        h.svc(c).Audit(p.ID, p.Username, "TEAM_SYNC_CLOUD", "settings", "", "switched to automatic cloud identity")
        h.ok(c, gin.H{"saved": true})
}

// TeamStoreCreate (settings.manage) — add a store under this owner's cloud
// project; the team code is minted by the database.
func (h *H) TeamStoreCreate(c *gin.Context) {
        p := h.principal(c)
        var body models.TeamStoreCreateRequest
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, err.Error())
                return
        }
        if len(body.Name) < 2 {
                h.fail(c, 422, "give the store a name (2+ characters)")
                return
        }
        st, err := h.svc(c).TeamCreateStore(body.Name, body.Slug)
        if err != nil {
                h.fail(c, 422, err.Error())
                return
        }
        h.svc(c).Audit(p.ID, p.Username, "TEAM_STORE_CREATED", "settings", "",
                st.Name+" ("+st.TeamCode+")")
        h.ok(c, st)
}

// TeamDeviceAssign (settings.manage) — assign a registered till to a store.
func (h *H) TeamDeviceAssign(c *gin.Context) {
        p := h.principal(c)
        var body models.TeamAssignRequest
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, err.Error())
                return
        }
        if body.DeviceID == "" || body.TeamCode == "" {
                h.fail(c, 422, "deviceId and teamCode are required")
                return
        }
        if err := h.svc(c).TeamAssignDevice(body.DeviceID, body.TeamCode); err != nil {
                h.fail(c, 422, err.Error())
                return
        }
        h.svc(c).Audit(p.ID, p.Username, "TEAM_DEVICE_ASSIGNED", "settings", "",
                body.DeviceID+" → "+body.TeamCode)
        h.ok(c, gin.H{"saved": true})
}

// TeamDeviceRemove (settings.manage) — revoke a till outright (kill switch).
func (h *H) TeamDeviceRemove(c *gin.Context) {
        p := h.principal(c)
        var body models.TeamRemoveRequest
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, err.Error())
                return
        }
        if body.DeviceID == "" {
                h.fail(c, 422, "deviceId is required")
                return
        }
        if err := h.svc(c).TeamRemoveDevice(body.DeviceID); err != nil {
                h.fail(c, 422, err.Error())
                return
        }
        h.svc(c).Audit(p.ID, p.Username, "TEAM_DEVICE_REVOKED", "settings", "",
                body.DeviceID)
        h.ok(c, gin.H{"saved": true})
}
