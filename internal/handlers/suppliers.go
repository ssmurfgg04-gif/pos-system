package handlers

import (
        "github.com/gin-gonic/gin"

        "posapp/internal/services"
)

// ListSuppliers (suppliers.view) — ?search=name-or-phone.
func (h *H) ListSuppliers(c *gin.Context) {
        list, err := h.Svc.ListSuppliers(c.Query("search"))
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        h.ok(c, list)
}

// CreateSupplier (suppliers.manage).
func (h *H) CreateSupplier(c *gin.Context) {
        p := h.principal(c)
        var body struct {
                Name    string `json:"name" binding:"required"`
                Phone   string `json:"phone"`
                Email   string `json:"email"`
                Address string `json:"address"`
                Notes   string `json:"notes"`
        }
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, err.Error())
                return
        }
        sup, err := h.Svc.CreateSupplier(body.Name, body.Phone, body.Email, body.Address, body.Notes, p)
        if err != nil {
                h.mapErr(c, err)
                return
        }
        h.created(c, sup)
}

// UpdateSupplier (suppliers.manage).
func (h *H) UpdateSupplier(c *gin.Context) {
        p := h.principal(c)
        id, ok := h.pathID(c, "id")
        if !ok {
                return
        }
        var body struct {
                Name    string `json:"name" binding:"required"`
                Phone   string `json:"phone"`
                Email   string `json:"email"`
                Address string `json:"address"`
                Notes   string `json:"notes"`
                Active  *bool  `json:"active"`
        }
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, err.Error())
                return
        }
        active := true
        if body.Active != nil {
                active = *body.Active
        }
        sup, err := h.Svc.UpdateSupplier(id, body.Name, body.Phone, body.Email, body.Address, body.Notes, active, p)
        if err != nil {
                h.mapErr(c, err)
                return
        }
        h.ok(c, sup)
}

// ListPOs (suppliers.view).
func (h *H) ListPOs(c *gin.Context) {
        list, err := h.Svc.ListPOs()
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        h.ok(c, list)
}

// GetPO (suppliers.view).
func (h *H) GetPO(c *gin.Context) {
        id, ok := h.pathID(c, "id")
        if !ok {
                return
        }
        po, err := h.Svc.GetPO(id)
        if err != nil {
                h.mapErr(c, err)
                return
        }
        h.ok(c, po)
}

// CreatePO (suppliers.manage).
func (h *H) CreatePO(c *gin.Context) {
        p := h.principal(c)
        var body struct {
                SupplierID int64 `json:"supplierId" binding:"required"`
                Items      []struct {
                        ProductID int64 `json:"productId" binding:"required"`
                        Qty       int   `json:"qty" binding:"required"`
                        CostCents int64 `json:"costCents"`
                } `json:"items" binding:"required,min=1"`
                Note string `json:"note"`
        }
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, err.Error())
                return
        }
        items := make([]services.POItemInput, 0, len(body.Items))
        for _, it := range body.Items {
                items = append(items, services.POItemInput{ProductID: it.ProductID, Qty: it.Qty, CostCents: it.CostCents})
        }
        po, err := h.Svc.CreatePO(body.SupplierID, items, body.Note, p)
        if err != nil {
                h.mapErr(c, err)
                return
        }
        h.created(c, po)
}

// ReceivePO (suppliers.manage).
func (h *H) ReceivePO(c *gin.Context) {
        p := h.principal(c)
        id, ok := h.pathID(c, "id")
        if !ok {
                return
        }
        po, err := h.Svc.ReceivePO(id, p)
        if err != nil {
                h.mapErr(c, err)
                return
        }
        h.ok(c, po)
}

// CancelPO (suppliers.manage).
func (h *H) CancelPO(c *gin.Context) {
        p := h.principal(c)
        id, ok := h.pathID(c, "id")
        if !ok {
                return
        }
        var body struct {
                Reason string `json:"reason"`
        }
        _ = c.ShouldBindJSON(&body)
        po, err := h.Svc.CancelPO(id, body.Reason, p)
        if err != nil {
                h.mapErr(c, err)
                return
        }
        h.ok(c, po)
}

// ListTakes (suppliers.view).
func (h *H) ListTakes(c *gin.Context) {
        list, err := h.Svc.ListTakes()
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        h.ok(c, list)
}

// GetTake (suppliers.view).
func (h *H) GetTake(c *gin.Context) {
        id, ok := h.pathID(c, "id")
        if !ok {
                return
        }
        t, err := h.Svc.GetTake(id)
        if err != nil {
                h.mapErr(c, err)
                return
        }
        h.ok(c, t)
}

// CreateTake (suppliers.manage).
func (h *H) CreateTake(c *gin.Context) {
        p := h.principal(c)
        var body struct {
                ProductIDs []int64 `json:"productIds"`
                Note       string  `json:"note"`
        }
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, err.Error())
                return
        }
        t, err := h.Svc.CreateTake(body.ProductIDs, body.Note, p)
        if err != nil {
                h.mapErr(c, err)
                return
        }
        h.created(c, t)
}

// CountTake (suppliers.manage).
func (h *H) CountTake(c *gin.Context) {
        p := h.principal(c)
        id, ok := h.pathID(c, "id")
        if !ok {
                return
        }
        var body struct {
                Counts map[int64]int `json:"counts" binding:"required"`
        }
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, err.Error())
                return
        }
        t, err := h.Svc.CountTake(id, body.Counts, p)
        if err != nil {
                h.mapErr(c, err)
                return
        }
        h.ok(c, t)
}

// ApplyTake (suppliers.manage).
func (h *H) ApplyTake(c *gin.Context) {
        p := h.principal(c)
        id, ok := h.pathID(c, "id")
        if !ok {
                return
        }
        t, err := h.Svc.ApplyTake(id, p)
        if err != nil {
                h.mapErr(c, err)
                return
        }
        h.ok(c, t)
}

// CancelTake (suppliers.manage).
func (h *H) CancelTake(c *gin.Context) {
        p := h.principal(c)
        id, ok := h.pathID(c, "id")
        if !ok {
                return
        }
        var body struct {
                Reason string `json:"reason"`
        }
        _ = c.ShouldBindJSON(&body)
        t, err := h.Svc.CancelTake(id, body.Reason, p)
        if err != nil {
                h.mapErr(c, err)
                return
        }
        h.ok(c, t)
}
