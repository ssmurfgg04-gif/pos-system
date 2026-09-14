// Package models defines the API DTOs shared between handlers and the
// frontend. Money is ALWAYS integer cents. Times are RFC3339 strings on
// the wire. JSON keys are camelCase.
package models

import "time"

const (
	OrderPending  = "PENDING"
	OrderPaid     = "PAID"
	OrderVoided   = "VOIDED"

	PaymentPending    = "PENDING"
	PaymentCompleted  = "COMPLETED"
	PaymentFailed     = "FAILED"
	PaymentVoided     = "VOIDED"

	MethodCash    = "cash"
	MethodMpesa   = "mpesa"
	MethodAccount = "account" // charge to a customer tab (customer credit)

	ModeAuto   = "auto"   // STK push first, manual fallback available
	ModeSTK    = "stk"    // force STK push
	ModeManual = "manual" // force manual receipt entry

	MpesaEnvMock       = "mock"
	MpesaEnvSandbox    = "sandbox"
	MpesaEnvProduction = "production"

	PrintQueued   = "queued"
	PrintPrinting = "printing"
	PrintPrinted  = "printed"
	PrintFailed   = "failed"

	DesignQueue      = "queue"
	DesignInProgress = "in_progress"
	DesignReady      = "ready"
	DesignDelivered  = "delivered"
)

// ---- Auth & RBAC ----

type User struct {
	ID          int64    `json:"id"`
	Username    string   `json:"username"`
	FullName    string   `json:"fullName"`
	RoleID      int64    `json:"roleId"`
	RoleName    string   `json:"roleName"`
	Permissions []string `json:"permissions"`
	Active      bool     `json:"active"`
	PINSet      bool     `json:"pinSet"`
	CreatedAt   string   `json:"createdAt"`
}

type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type PinLoginRequest struct {
	PIN string `json:"pin" binding:"required,len=4"`
}

type PinUser struct {
	ID       int64  `json:"id"`
	FullName string `json:"fullName"`
	RoleName string `json:"roleName"`
}

type TokenResponse struct {
	Token string `json:"token"`
	User  User   `json:"user"`
}

type Role struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
	System      bool     `json:"system"`
}

// ---- Catalog ----

type Category struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type Product struct {
	ID           int64  `json:"id"`
	SKU          string `json:"sku"`
	Barcode      string `json:"barcode"`
	Name         string `json:"name"`
	CategoryID   int64  `json:"categoryId"`
	CategoryName string `json:"categoryName"`
	PriceCents   int64  `json:"priceCents"`
	CostCents    int64  `json:"costCents"`
	StockQty     int    `json:"stockQty"`
	TrackStock   bool   `json:"trackStock"`
	Active       bool   `json:"active"`
	UpdatedAt    string `json:"updatedAt"`
}

// ---- Orders ----

type CheckoutItem struct {
	ProductID     int64 `json:"productId" binding:"required"`
	Qty           int   `json:"qty" binding:"required,min=1"`
	UnitPriceCents int64 `json:"unitPriceCents"` // optional override (permission-gated)
}

type CheckoutRequest struct {
	Items             []CheckoutItem `json:"items" binding:"required,min=1"`
	PaymentMethod     string         `json:"paymentMethod" binding:"required,oneof=cash mpesa account"`
	PaymentMode       string         `json:"paymentMode"`   // auto|stk|manual (mpesa only)
	CustomerPhone     string         `json:"customerPhone"`
	CustomerName      string         `json:"customerName"`
	CustomerID        int64          `json:"customerId"` // required for account tabs
	Note              string         `json:"note"`
	ClientUUID        string         `json:"clientUuid"` // offline idempotency key
}

type OrderItem struct {
	ID             int64  `json:"id"`
	ProductID      int64  `json:"productId"`
	Name           string `json:"name"`
	SKU            string `json:"sku"`
	Qty            int    `json:"qty"`
	UnitPriceCents int64  `json:"unitPriceCents"`
	LineTotalCents int64  `json:"lineTotalCents"`
}

type Payment struct {
	ID                int64  `json:"id"`
	OrderID           int64  `json:"orderId"`
	Method            string `json:"method"`
	Mode              string `json:"mode"`
	AmountCents       int64  `json:"amountCents"`
	Status            string `json:"status"`
	Phone             string `json:"phone"`
	MpesaReceipt      string `json:"mpesaReceipt"`
	CheckoutRequestID string `json:"checkoutRequestId"`
	ResultDesc        string `json:"resultDesc"`
	Discrepancy       bool   `json:"discrepancy"`
	CreatedAt         string `json:"createdAt"`
	CompletedAt       string `json:"completedAt"`
}

type Order struct {
	ID            int64       `json:"id"`
	Number        string      `json:"number"`
	Status        string      `json:"status"`
	SubtotalCents int64       `json:"subtotalCents"`
	TaxCents      int64       `json:"taxCents"`
	TotalCents    int64       `json:"totalCents"`
	CashierID     int64       `json:"cashierId"`
	CashierName   string      `json:"cashierName"`
	CustomerName  string      `json:"customerName"`
	CustomerID    int64       `json:"customerId"`
	Note          string      `json:"note"`
	ClientUUID    string      `json:"clientUuid"`
	Discrepancy   bool        `json:"discrepancy"`
	CreatedAt     string      `json:"createdAt"`
	PaidAt        string      `json:"paidAt"`
	VoidedAt      string      `json:"voidedAt"`
	VoidReason    string      `json:"voidReason"`
	Items         []OrderItem `json:"items"`
	Payments      []Payment   `json:"payments"`
}

// PaidAtOrCreated is the display timestamp for receipts and lists.
func (o *Order) PaidAtOrCreated() string {
	if o.PaidAt != "" {
		return o.PaidAt
	}
	return o.CreatedAt
}

// ---- Customers (tabs & credit) ----

// Ledger entry kinds: charge raises what the customer owes, payment lowers
// it, adjustment is a manual correction (signed amount), loyalty only moves
// points.
const (
	LedgerCharge  = "charge"
	LedgerPayment = "payment"
	LedgerAdjust  = "adjustment"
	LedgerLoyalty = "loyalty"
)

type Customer struct {
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	Phone            string `json:"phone"`
	CreditLimitCents int64  `json:"creditLimitCents"` // 0 = no tab allowed
	LoyaltyPoints    int64  `json:"loyaltyPoints"`
	BalanceCents     int64  `json:"balanceCents"` // >0 means the customer owes the shop
	Active           bool   `json:"active"`
	CreatedAt        string `json:"createdAt"`
	UpdatedAt        string `json:"updatedAt"`
}

type LedgerEntry struct {
	ID          int64  `json:"id"`
	CustomerID  int64  `json:"customerId"`
	OrderID     int64  `json:"orderId"`
	Kind        string `json:"kind"`
	AmountCents int64  `json:"amountCents"` // signed: +charge, -payment
	PointsDelta int64  `json:"pointsDelta"`
	Note        string `json:"note"`
	CreatedBy   int64  `json:"createdBy"`
	CreatedAt   string `json:"createdAt"`
}

// ---- Shifts ----

type Shift struct {
	ID                int64  `json:"id"`
	UserID            int64  `json:"userId"`
	UserName          string `json:"userName"`
	OpeningFloatCents int64  `json:"openingFloatCents"`
	ExpectedCents     int64  `json:"expectedCents"`
	CountedCents      int64  `json:"countedCents"`
	VarianceCents     int64  `json:"varianceCents"`
	OpenedAt          string `json:"openedAt"`
	ClosedAt          string `json:"closedAt"`
}

// ---- Design board ----

type DesignJob struct {
	ID           int64  `json:"id"`
	Title        string `json:"title"`
	ProductName  string `json:"productName"`
	CustomerName string `json:"customerName"`
	Notes        string `json:"notes"`
	Status       string `json:"status"`
	AssigneeID   int64  `json:"assigneeId"`
	AssigneeName string `json:"assigneeName"`
	CreatedBy    string `json:"createdBy"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
}

// ---- Misc ----

type AuditEntry struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"userId"`
	Username  string `json:"username"`
	Action    string `json:"action"`
	Entity    string `json:"entity"`
	EntityID  string `json:"entityId"`
	Details   string `json:"details"`
	CreatedAt string `json:"createdAt"`
}

type PrintJob struct {
	ID         int64  `json:"id"`
	OrderID    int64  `json:"orderId"`
	OrderNumber string `json:"orderNumber"`
	Status     string `json:"status"`
	Target     string `json:"target"`
	Attempts   int    `json:"attempts"`
	LastError  string `json:"lastError"`
	CreatedAt  string `json:"createdAt"`
	PrintedAt  string `json:"printedAt"`
}

type SyncRequest struct {
	Transactions []CheckoutRequest `json:"transactions" binding:"required"`
}

type SyncResult struct {
	ClientUUID string `json:"clientUuid"`
	OrderID    int64  `json:"orderId"`
	OrderNumber string `json:"orderNumber"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

type ManualEntryRequest struct {
	ReceiptCode string `json:"receiptCode" binding:"required"`
}

type STKRetryRequest struct {
	Phone string `json:"phone" binding:"required"`
}

type VoidRequest struct {
	Reason string `json:"reason" binding:"required"`
}

func FmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
