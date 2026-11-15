package handlers

// accounting.go — QuickBooks / Xero export (v1.12.0).

import (
	"github.com/gin-gonic/gin"

	"posapp/internal/services"
)

// AccountingJournal (accounting.export) — GET /api/v1/accounting/journal?from&to
func (h *H) AccountingJournal(c *gin.Context) {
	j, err := h.svc(c).Journal(c.Query("from"), c.Query("to"))
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, j)
}

// AccountingExport (accounting.export) — GET /api/v1/accounting/export.csv?from&to&format
func (h *H) AccountingExport(c *gin.Context) {
	out, name, err := h.svc(c).JournalCSV(c.Query("from"), c.Query("to"), c.Query("format"))
	if err != nil {
		h.mapErr(c, err)
		return
	}
	p := h.principal(c)
	h.svc(c).Audit(p.ID, p.Username, "BOOKS_EXPORTED", "accounting", c.Query("format"), c.Query("from")+".."+c.Query("to"))
	c.Header("Content-Disposition", `attachment; filename="`+name+`"`)
	c.Data(200, "text/csv; charset=utf-8", []byte(out))
}

// AccountingAccounts (accounting.export) — GET /api/v1/accounting/accounts
func (h *H) AccountingAccounts(c *gin.Context) {
	h.ok(c, h.svc(c).AccountingAccounts())
}

// SaveAccountingAccounts (accounting.export) — PUT /api/v1/accounting/accounts {key: {name, code}}
func (h *H) SaveAccountingAccounts(c *gin.Context) {
	var body map[string]services.AccountRef
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	if err := h.svc(c).SaveAccountingAccounts(body); err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, h.svc(c).AccountingAccounts())
}
