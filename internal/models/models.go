// Package models defines the API DTOs shared between handlers and the
// frontend. Money is ALWAYS integer cents. Times are RFC3339 strings on
// the wire. JSON keys are camelCase.
package models

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

func errInvalidProduct(msg string) error { return errors.New(msg) }

const (
	OrderPending = "PENDING"
	OrderPaid    = "PAID"
	OrderVoided  = "VOIDED"

	// Order-event kinds (per-order activity timeline).
	OrderEventJob      = "job"
	OrderEventWhatsApp = "whatsapp"
	OrderEventNote     = "note"
	OrderEventStatus   = "status"

	PaymentPending   = "PENDING"
	PaymentCompleted = "COMPLETED"
	PaymentFailed    = "FAILED"
	PaymentVoided    = "VOIDED"

	MethodCash     = "cash"
	MethodMpesa    = "mpesa"
	MethodAccount  = "account"  // charge to a customer tab (customer credit)
	MethodCredit   = "credit"   // pay from prepaid store credit balance
	MethodPaystack = "paystack" // card / mobile money via Paystack checkout

	// Store-credit ledger kinds (customer_ledger.kind). Store credit is
	// money the shop OWES the customer (prepaid) — the mirror of the
	// tab balance. Top-up adds, redemption subtracts.
	LedgerCreditTopup  = "credit_topup"
	LedgerCreditRedeem = "credit_redeem"
	LedgerRefund       = "refund" // store-credit refund of a returned sale

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
	MustRotate  bool     `json:"mustRotate"`
	ShopID      string   `json:"shopId,omitempty"`
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
	Username string `json:"username"`
	FullName string `json:"fullName"`
	RoleName string `json:"roleName"`
	// v1.3.2 shop binding: PIN sign-in must land in the EXACT shop this
	// list came from (user ids are per-shop autoincrements and collide).
	ShopID string `json:"shopId"`
}

type TokenResponse struct {
	Token string `json:"token"`
	User  User   `json:"user"`
}

type Role struct {
	ID          int64            `json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Permissions []string         `json:"permissions"`
	System      bool             `json:"system"`
	HomePage    string           `json:"homePage"`            // landing route after login ("" = permission cascade)
	Dashboard   *DashboardConfig `json:"dashboard,omitempty"` // per-role dashboard tailoring
}

// DashboardConfig lets an admin tailor what each role sees: which nav
// entries are hidden (on top of permissions) and which daily-report stat
// widgets are suppressed. Stored as JSON in roles.dashboard_config.
type DashboardConfig struct {
	HiddenNav    []string `json:"hiddenNav,omitempty"`    // route prefixes, e.g. ["/suppliers"]
	HiddenStats  []string `json:"hiddenStats,omitempty"`  // daily-report stat keys
	WidgetsOrder []string `json:"widgetsOrder,omitempty"` // reserved for future ordering
}

// ---- Catalog ----

type Category struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type Product struct {
	ID            int64  `json:"id"`
	SKU           string `json:"sku"`
	Barcode       string `json:"barcode"`
	Name          string `json:"name"`
	CategoryID    int64  `json:"categoryId"`
	CategoryName  string `json:"categoryName"`
	ImageURL      string `json:"imageUrl"`
	PriceCents    int64  `json:"priceCents"`
	CostCents     int64  `json:"costCents"`
	StockQtyMilli int64  `json:"stockQtyMilli"`
	TrackStock    bool   `json:"trackStock"`
	Active        bool   `json:"active"`
	IsGiftCard    bool   `json:"isGiftCard"` // mints redeemable codes on paid sale
	// v1.8.0: sellable unit ('pc', 'kg', 'l', …) + whether the till accepts
	// fractional quantities (0.5 kg of sugar) + batch/expiry tracking.
	Unit          string `json:"unit"`
	AllowFraction bool   `json:"allowFraction"`
	TrackBatches  bool   `json:"trackBatches"`
	// v1.6.3: wholesale tier pricing + per-product low-stock threshold
	// (0 = fall back to the global low_stock_threshold setting).
	WholesalePriceCents int64   `json:"wholesalePriceCents"`
	LowStockUnits       int     `json:"lowStockUnits"`
	EtimsTaxType        string  `json:"etimsTaxType"` // v1.10: A-E, '' = shop default
	EtimsClsCd          string  `json:"etimsClsCd"`   // v1.10: KRA item class, '' = shop default
	VatType             string  `json:"vatType"`      // v1.11: effective tax type (A-E, X = gift card)
	VatRate             float64 `json:"vatRate"`      // v1.11: effective VAT percent the till prices with
	UpdatedAt           string  `json:"updatedAt"`
}

// ---- Orders ----

type CheckoutItem struct {
	ProductID int64 `json:"productId" binding:"required"`
	// v1.8.0: quantities are integer milli-units (1000 = one unit). A
	// whole-unit line is qtyMilli=2000; 250 g of a kilo-sold product is 250.
	QtyMilli       int64 `json:"qtyMilli" binding:"required,min=1"`
	UnitPriceCents int64 `json:"unitPriceCents"` // optional override (permission-gated)
}

type CheckoutRequest struct {
	Items         []CheckoutItem `json:"items" binding:"required,min=1"`
	PaymentMethod string         `json:"paymentMethod" binding:"required,oneof=cash mpesa account credit paystack"`
	PaymentMode   string         `json:"paymentMode"` // auto|stk|manual (mpesa only)
	CustomerPhone string         `json:"customerPhone"`
	CustomerName  string         `json:"customerName"`
	CustomerEmail string         `json:"customerEmail"` // paystack receipt + receipt email
	CustomerID    int64          `json:"customerId"`    // required for account tabs + credit
	BuyerPIN      string         `json:"buyerPin"`      // eTIMS: customer KRA PIN — turns the receipt into a tax invoice
	Note          string         `json:"note"`
	ClientUUID    string         `json:"clientUuid"`    // offline idempotency key
	DiscountCents int64          `json:"discountCents"` // order-level discount (permission-gated)
	DiscountLabel string         `json:"discountLabel"` // e.g. "staff 10%", "negotiated"
	RedeemPoints  int64          `json:"redeemPoints"`  // loyalty points spent as payment (permission-gated)
	SplitPayments []SplitLeg     `json:"splitPayments"` // optional mixed tender (part cash, part M-Pesa…)
}

// SplitLeg is one tender in a mixed payment. Loyalty redemption is NOT a
// leg (it already reduced the payable); account tabs cannot be split.
type SplitLeg struct {
	Method      string `json:"method" binding:"required,oneof=cash mpesa paystack credit"`
	AmountCents int64  `json:"amountCents"`
	Phone       string `json:"phone"` // mpesa leg (auto/stk)
	Email       string `json:"email"` // paystack leg
}

type OrderItem struct {
	ID             int64  `json:"id"`
	ProductID      int64  `json:"productId"`
	Name           string `json:"name"`
	SKU            string `json:"sku"`
	QtyMilli       int64  `json:"qtyMilli"`
	UnitPriceCents int64  `json:"unitPriceCents"`
	LineTotalCents int64  `json:"lineTotalCents"`
	CostCents      int64  `json:"costCents"` // v1.7.0: unit cost snapshot at sale time (0 = pre-v26 line)
	// v1.11 per-line VAT, frozen at sale time ('' / 0 = pre-v31 line):
	TaxType   string  `json:"taxType"`   // KRA A-E, or X (outside VAT: gift cards)
	TaxRate   float64 `json:"taxRate"`   // VAT percent
	TaxCents  int64   `json:"taxCents"`  // the line's share of the order VAT
	PaidCents int64   `json:"paidCents"` // paid for the line, VAT incl., after discount + points
}

// QtyDisplay renders a milli-unit quantity for receipts/documents: whole
// numbers plain ("3"), fractional trimmed ("0.5", "0.25").
func (it OrderItem) QtyDisplay() string {
	if it.QtyMilli%MilliPerUnit == 0 {
		return fmt.Sprintf("%d", it.QtyMilli/MilliPerUnit)
	}
	s := fmt.Sprintf("%.3f", float64(it.QtyMilli)/float64(MilliPerUnit))
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	return s
}

type Payment struct {
	ID                int64  `json:"id"`
	OrderID           int64  `json:"orderId"`
	Method            string `json:"method"`
	Mode              string `json:"mode"`
	AmountCents       int64  `json:"amountCents"`
	Status            string `json:"status"`
	Phone             string `json:"phone"`
	Email             string `json:"email"`             // paystack: address the charge was opened for
	MpesaReceipt      string `json:"mpesaReceipt"`      // mpesa code, or the paystack reference once completed
	CheckoutRequestID string `json:"checkoutRequestId"` // daraja checkout id, or paystack reference
	ResultDesc        string `json:"resultDesc"`
	Discrepancy       bool   `json:"discrepancy"`
	CreatedAt         string `json:"createdAt"`
	CompletedAt       string `json:"completedAt"`
}

type Order struct {
	ID             int64   `json:"id"`
	Number         string  `json:"number"`
	Status         string  `json:"status"`
	SubtotalCents  int64   `json:"subtotalCents"`
	DiscountCents  int64   `json:"discountCents"`
	DiscountLabel  string  `json:"discountLabel"`
	PointsRedeemed int64   `json:"pointsRedeemed"`
	TaxCents       int64   `json:"taxCents"`
	TotalCents     int64   `json:"totalCents"`
	TaxPercent     float64 `json:"taxPercent"`
	TaxIncluded    bool    `json:"taxIncluded"`
	CashierID      int64   `json:"cashierId"`
	CashierName    string  `json:"cashierName"`
	CustomerName   string  `json:"customerName"`
	CustomerID     int64   `json:"customerId"`
	Note           string  `json:"note"`
	ClientUUID     string  `json:"clientUuid"`
	Discrepancy    bool    `json:"discrepancy"`
	CreatedAt      string  `json:"createdAt"`
	PaidAt         string  `json:"paidAt"`
	VoidedAt       string  `json:"voidedAt"`
	VoidReason     string  `json:"voidReason"`
	// Originating till (v19). '' on pre-v19 rows; visibility is
	// scoped on cashier_id + orders.view.all, never on this alone.
	DeviceID string      `json:"deviceId"`
	Items    []OrderItem `json:"items"`
	Payments []Payment   `json:"payments"`
	// Connected job lifecycle: fulfilment jobs spawned from this sale
	// (loaded on the detail view) + the last WhatsApp contact attempt.
	Jobs         []DesignJob `json:"jobs,omitempty"`
	AssignedToID int64       `json:"assignedToId"`
	NotifiedAt   string      `json:"notifiedAt"`
	// ERP depth (P6): eTIMS-ready invoice identity. BuyerPIN is the
	// customer's KRA PIN (present ⇒ tax invoice); InvoiceNumber is the
	// document number (defaults to the order number).
	BuyerPIN      string `json:"buyerPin"`
	InvoiceNumber string `json:"invoiceNumber"`
	// Gift-card codes minted by this sale — the code IS the value, so it
	// rides the detail payload the on-screen receipt prints (not stored
	// on the orders table; loaded from gift_cards on the detail read).
	GiftCards []string `json:"giftCards,omitempty"`
	// v1.10.0 KRA eTIMS: the control-unit signature once KRA signed the
	// sale (status "signed"), or where the send stands ("pending",
	// "failed"). nil when eTIMS is off or the sale predates it.
	Etims *EtimsStamp `json:"etims,omitempty"`
}

// EtimsStamp is what a KRA-signed receipt prints (OSCU response + device).
type EtimsStamp struct {
	Status      string `json:"status"` // pending | signed | failed
	InvcNo      int64  `json:"invcNo"`
	CurRcptNo   int64  `json:"curRcptNo"`
	TotRcptNo   int64  `json:"totRcptNo"`
	IntrlData   string `json:"intrlData"`
	RcptSign    string `json:"rcptSign"`
	SdcDateTime string `json:"sdcDateTime"`
	SdcID       string `json:"sdcId"`
	MrcNo       string `json:"mrcNo"`
	QR          string `json:"qr"`
	LastError   string `json:"lastError,omitempty"`
	Attempts    int    `json:"attempts"`
}

// OrderSummary is one row of a customer's purchase history.
type OrderSummary struct {
	ID         int64  `json:"id"`
	Number     string `json:"number"`
	Status     string `json:"status"`
	TotalCents int64  `json:"totalCents"`
	CreatedAt  string `json:"createdAt"`
	PaidAt     string `json:"paidAt"`
}

// WhatsAppContact is the prepared "contact customer" action for a ready
// order: the deep link opens WhatsApp with the message prefilled, the
// staff member reviews and presses send — the app never auto-sends.
type WhatsAppContact struct {
	OrderID    int64  `json:"orderId"`
	Phone      string `json:"phone"`
	Message    string `json:"message"`
	URL        string `json:"url"`
	OrderRef   string `json:"orderRef"`
	NotifiedAt string `json:"notifiedAt"`
}

// OrderEvent is one line of the per-order activity timeline (job moves,
// WhatsApp contacts, status changes, notes).
type OrderEvent struct {
	ID        int64  `json:"id"`
	OrderID   int64  `json:"orderId"`
	Kind      string `json:"kind"`
	Message   string `json:"message"`
	UserID    int64  `json:"userId"`
	Username  string `json:"username"`
	CreatedAt string `json:"createdAt"`
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
	LedgerOpening = "opening" // v1.7.0: debt carried in when the shop joins (SmartPOS opening-balance pattern)
)

type Customer struct {
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	Phone            string `json:"phone"`
	Email            string `json:"email"`
	Notes            string `json:"notes"`
	CreditLimitCents int64  `json:"creditLimitCents"` // 0 = no tab allowed
	LoyaltyPoints    int64  `json:"loyaltyPoints"`
	BalanceCents     int64  `json:"balanceCents"`     // >0 means the customer owes the shop
	StoreCreditCents int64  `json:"storeCreditCents"` // >0 means the shop owes the customer (prepaid)
	PriceTier        string `json:"priceTier"`        // 'retail' (default) | 'wholesale' — till auto-prices
	Active           bool   `json:"active"`
	CreatedAt        string `json:"createdAt"`
	UpdatedAt        string `json:"updatedAt"`
}

type LedgerEntry struct {
	ID           int64  `json:"id"`
	CustomerID   int64  `json:"customerId"`
	OrderID      int64  `json:"orderId"`
	Kind         string `json:"kind"`
	AmountCents  int64  `json:"amountCents"` // signed: +charge, -payment
	PointsDelta  int64  `json:"pointsDelta"`
	Note         string `json:"note"`
	CreatedBy    int64  `json:"createdBy"`
	CreatedAt    string `json:"createdAt"`
	BalanceAfter *int64 `json:"balanceAfter,omitempty"` // v1.7.0: what the customer owed right after this row (nil = pre-v26)
	ReversalOf   int64  `json:"reversalOf,omitempty"`   // v1.7.0: non-zero = this row cancels that entry
}

// ---- Suppliers & stock-in ----

const (
	POPending     = "PENDING"
	POReceived    = "RECEIVED"
	POCancelled   = "CANCELLED"
	TakeOpen      = "OPEN"
	TakeApplied   = "APPLIED"
	TakeCancelled = "CANCELLED"
)

type Supplier struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Phone     string `json:"phone"`
	Email     string `json:"email"`
	Address   string `json:"address"`
	Notes     string `json:"notes"`
	Active    bool   `json:"active"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type POItem struct {
	ID             int64  `json:"id"`
	POID           int64  `json:"poId"`
	ProductID      int64  `json:"productId"`
	Name           string `json:"name"`
	SKU            string `json:"sku"`
	QtyMilli       int64  `json:"qtyMilli"`
	CostCents      int64  `json:"costCents"`
	LineTotalCents int64  `json:"lineTotalCents"`
	// v1.8.0: batch metadata captured at goods-in for batch-tracked
	// products (a received line BECOMES a lot).
	ExpiryDate string `json:"expiryDate"`
	BatchNo    string `json:"batchNo"`
}

type PurchaseOrder struct {
	ID            int64    `json:"id"`
	Number        string   `json:"number"`
	SupplierID    int64    `json:"supplierId"`
	SupplierName  string   `json:"supplierName"`
	Status        string   `json:"status"`
	SubtotalCents int64    `json:"subtotalCents"`
	Note          string   `json:"note"`
	Items         []POItem `json:"items"`
	CreatedAt     string   `json:"createdAt"`
	ReceivedAt    string   `json:"receivedAt"`
}

type StockTakeItem struct {
	ID               int64  `json:"id"`
	TakeID           int64  `json:"takeId"`
	ProductID        int64  `json:"productId"`
	Name             string `json:"name"`
	SKU              string `json:"sku"`
	ExpectedQtyMilli int64  `json:"expectedQtyMilli"`
	CountedQtyMilli  int64  `json:"countedQtyMilli"`
}

type StockTake struct {
	ID        int64           `json:"id"`
	Number    string          `json:"number"`
	Status    string          `json:"status"`
	Note      string          `json:"note"`
	Items     []StockTakeItem `json:"items"`
	ItemCount int             `json:"itemCount"`
	CreatedAt string          `json:"createdAt"`
	AppliedAt string          `json:"appliedAt"`
}

// ---- Money & quantity bounds ----
//
// Integer cents keep money exact, but int64 still overflows near 9.2e18:
// price 1e12 × qty 1e7 would flip a total negative (free money + infinite
// change). These ceilings sit orders of magnitude above any real duka sale
// while keeping every multiplication below ~1e17.

const (
	// MilliPerUnit is the v1.8.0 quantity scale: every quantity column and
	// wire field counts thousandths of a unit (1000 = one piece / one kg).
	// Integer milli-units keep quantities exact the way integer cents keep
	// money exact — SQLite has no decimal type, and float quantities drift.
	MilliPerUnit = 1000
	// MaxOrderQtyMilli caps one line's quantity (pre- and post-merge), in
	// milli-units: 100,000,000 = 100,000 whole units (or 100 tonnes of a
	// kilo-sold good — far above any real till line).
	MaxOrderQtyMilli = 100000000
	// MaxPriceCents caps any unit price or cost (10B KES).
	MaxPriceCents = 1000000000000
	// MaxOrderLines caps checkout/PO line counts (merge-bypass).
	MaxOrderLines = 500
	// MaxStockDeltaMilli caps single stock adjustments and take counts.
	MaxStockDeltaMilli = 100000000000
	// MaxStockQtyMilli caps stored stock quantities.
	MaxStockQtyMilli = 1000000000000
	// MaxNameLen caps product/customer/supplier names (receipt/UI sanity).
	MaxNameLen = 200
	// MaxCodeLen caps SKUs, barcodes, phones.
	MaxCodeLen = 64
	// MaxTaxPercent caps VAT configuration.
	MaxTaxPercent = 100
)

// Sellable units (v1.8.0). 'pc' is the default piece-count unit; the rest
// are weigh/measure units the till sells fractionally. The list is closed
// so receipts and reports can format confidently.
var Units = []string{"pc", "kg", "g", "l", "ml", "pack", "box", "dozen", "m"}

// UnitFractionDefault reports whether a unit is normally sold fractionally.
func UnitFractionDefault(unit string) bool {
	switch unit {
	case "kg", "g", "l", "ml", "m":
		return true
	}
	return false
}

// ValidUnit checks a client-supplied unit string.
func ValidUnit(unit string) bool {
	if unit == "" {
		return true // empty = 'pc'
	}
	for _, u := range Units {
		if u == unit {
			return true
		}
	}
	return false
}

// CheckProductInput validates catalog fields shared by API + CSV import.
// stockMilli is the opening stock in milli-units (1000 = one unit).
func CheckProductInput(name, sku, barcode string, priceCents, costCents int64, stockMilli int64) error {
	if len(name) == 0 || len(name) > MaxNameLen {
		return errInvalidProduct("name must be 1-200 characters")
	}
	if len(sku) > MaxCodeLen || len(barcode) > MaxCodeLen {
		return errInvalidProduct("sku/barcode too long (max 64)")
	}
	if priceCents < 0 || priceCents > MaxPriceCents {
		return errInvalidProduct("price out of range")
	}
	if costCents < 0 || costCents > MaxPriceCents {
		return errInvalidProduct("cost out of range")
	}
	if stockMilli < 0 || stockMilli > MaxStockQtyMilli {
		return errInvalidProduct("stock out of range")
	}
	return nil
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
	// Connected job lifecycle (P3): the sale that requested this work,
	// the deadline promised to the customer, and the stage timestamps.
	OrderID     int64  `json:"orderId"`
	Deadline    string `json:"deadline"`
	Priority    string `json:"priority"`
	AssignedAt  string `json:"assignedAt"`
	ReadyAt     string `json:"readyAt"`
	CollectedAt string `json:"collectedAt"`
}

// ---- Held (parked) sales ----

// HeldSale is a cart parked mid-sale: items frozen as JSON, optionally
// tied to a customer. Resuming rehydrates the cart; nothing is charged
// until a normal checkout completes.
type HeldSale struct {
	ID            int64          `json:"id"`
	RefName       string         `json:"refName"`
	Items         []CheckoutItem `json:"items"`
	CustomerID    int64          `json:"customerId"`
	CustomerName  string         `json:"customerName"`
	Note          string         `json:"note"`
	DeviceID      string         `json:"deviceId"`
	CreatedBy     int64          `json:"createdBy"`
	CreatedByName string         `json:"createdByName"`
	CreatedAt     string         `json:"createdAt"`
}

// ---- Void reasons ----

type VoidReason struct {
	ID        int64  `json:"id"`
	Label     string `json:"label"`
	Active    bool   `json:"active"`
	SortOrder int    `json:"sortOrder"`
}

// ---- Design job attachments ----

// DesignFile is metadata for one attachment stored in design_files.data
// (BLOB, 5 MB cap). Data never serializes to JSON — it is served only by
// the download route.
type DesignFile struct {
	ID             int64  `json:"id"`
	JobID          int64  `json:"jobId"`
	Filename       string `json:"filename"`
	Mime           string `json:"mime"`
	Size           int64  `json:"size"`
	UploadedBy     int64  `json:"uploadedBy"`
	UploadedByName string `json:"uploadedByName"`
	CreatedAt      string `json:"createdAt"`
}

// ---- Team overview (People) ----

type TeamMember struct {
	User
	SalesToday      int64  `json:"salesToday"`
	SalesTodayCents int64  `json:"salesTodayCents"`
	LastOrderAt     string `json:"lastOrderAt"`
}

// ---- Team sync (cloud linking) ----

type TeamSyncStatus struct {
	Enabled     bool         `json:"enabled"`
	TeamCode    string       `json:"teamCode"`
	DeviceID    string       `json:"deviceId"`
	DeviceName  string       `json:"deviceName"`
	LastPush    string       `json:"lastPush"`
	LastPull    string       `json:"lastPull"`
	Pending     int64        `json:"pending"`
	LastError   string       `json:"lastError"`
	Devices     []TeamDevice `json:"devices"`
	Source      string       `json:"source"`      // "cloud" (auto) | "manual" | ""
	Registered  bool         `json:"registered"`  // this till is known to the cloud
	Approved    bool         `json:"approved"`    // cloud accepts this till's events
	AutoApprove bool         `json:"autoApprove"` // new devices self-approve
	// Multi-store: an owner can run several stores under one cloud project.
	// This till waits (StorePending) until the owner assigns it to a store;
	// approved tills see the store list and the unassigned devices.
	StorePending   bool         `json:"storePending"`
	MultiStore     bool         `json:"multiStore"`
	Stores         []TeamStore  `json:"stores"`
	PendingDevices []TeamDevice `json:"pendingDevices"`
	Invites        []TeamInvite `json:"invites"`
	// Hardened-sync surface: events that could not be applied or pushed
	// (quarantined to the dead-letter table) — visible, retryable, never
	// silently dropped.
	FailedEvents int64 `json:"failedEvents"`
	// v1.3.2 outbox reconciliation: local write failures of the outbox
	// row itself (the mutation committed, the sync event did not). The
	// backfill re-emits these; the counter stays visible until reset.
	OutboxFailures  int64             `json:"outboxFailures"`
	OutboxLastError string            `json:"outboxLastError,omitempty"`
	Failed          []SyncFailedEvent `json:"failed"`
}

// SyncFailedEvent is one quarantined sync event shown in the Team panel.
type SyncFailedEvent struct {
	Direction string `json:"direction"` // "apply" | "push"
	Entity    string `json:"entity"`
	Op        string `json:"op"`
	Error     string `json:"error"`
	Attempts  int    `json:"attempts"`
	UpdatedAt string `json:"updatedAt"`
}

// TeamStore is one store under the owner's cloud project. TeamCode is the
// sync partition minted by the database — devices only ever see events from
// their own store's team.
type TeamStore struct {
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	TeamCode string `json:"teamCode"`
	Devices  int    `json:"devices"`
}

type TeamStoreCreateRequest struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type TeamAssignRequest struct {
	DeviceID string `json:"deviceId"`
	TeamCode string `json:"teamCode"`
}

type TeamRemoveRequest struct {
	DeviceID string `json:"deviceId"`
}

// TeamInvite is a team join link the owner minted (invite rows carry the
// role; the token lives only in the link and is stored hashed cloud-side).
type TeamInvite struct {
	ID        int64  `json:"id"`
	RoleName  string `json:"roleName"`
	Note      string `json:"note"`
	CreatedAt string `json:"createdAt"`
	ExpiresAt string `json:"expiresAt"`
	UsedBy    string `json:"usedBy"`
	UsedAt    string `json:"usedAt"`
	Revoked   bool   `json:"revoked"`
}

// TeamInviteCreated is returned once, right after minting: the token is
// shown exactly once (the cloud keeps only its hash).
type TeamInviteCreated struct {
	TeamCode  string `json:"teamCode"`
	Token     string `json:"token"`
	RoleName  string `json:"roleName"`
	ExpiresAt string `json:"expiresAt"`
}

type TeamInviteCreateRequest struct {
	RoleName    string   `json:"roleName"`
	Permissions []string `json:"permissions"`
	Note        string   `json:"note"`
}

type TeamInviteRevokeRequest struct {
	ID int64 `json:"id"`
}

// TeamJoinInfo is what a till learns when it redeems a join link.
type TeamJoinInfo struct {
	TeamCode  string
	StoreName string
	RoleName  string
	Username  string
	UserID    int64
}

// ---- Forgot-PIN cloud recovery ----

// PinResetRequested is the ack after a locked till files a reset request.
type PinResetRequested struct {
	RequestID int64  `json:"requestId"`
	Username  string `json:"username"`
	CreatedAt string `json:"createdAt"`
	Reused    bool   `json:"reused"`
	// PushedCode is a code the owner till delivered straight over the
	// shop WiFi (pre-fill only; the cloud still verifies it on redeem).
	PushedCode string `json:"pushedCode,omitempty"`
}

// PinResetRedeemed is the ack after the owner's code set a new PIN. UserID
// lets the till sign the person straight in (nobody retypes a fresh PIN).
type PinResetRedeemed struct {
	Username string `json:"username"`
	UserID   int64  `json:"userId"`
}

// ---- Quotations ----

// Quote statuses: draft → sent → accepted/declined; converted is stamped
// automatically when the quote's lines move to a till as a parked sale.
const (
	QuoteDraft     = "draft"
	QuoteSent      = "sent"
	QuoteAccepted  = "accepted"
	QuoteDeclined  = "declined"
	QuoteConverted = "converted"
)

type QuoteItem struct {
	ProductID      int64  `json:"productId"`
	SKU            string `json:"sku"`
	Name           string `json:"name"`
	QtyMilli       int64  `json:"qtyMilli"`
	UnitPriceCents int64  `json:"unitPriceCents"`
	LineTotalCents int64  `json:"lineTotalCents"`
}

type Quote struct {
	ID                  int64       `json:"id"`
	QuoteNo             string      `json:"quoteNo"`
	ClientUUID          string      `json:"clientUuid"`
	CustomerID          int64       `json:"customerId"`
	CustomerName        string      `json:"customerName"`
	CustomerPhone       string      `json:"customerPhone"`
	Status              string      `json:"status"`
	Items               []QuoteItem `json:"items"`
	SubtotalCents       int64       `json:"subtotalCents"`
	DiscountCents       int64       `json:"discountCents"`
	DiscountLabel       string      `json:"discountLabel"`
	TaxCents            int64       `json:"taxCents"`
	TotalCents          int64       `json:"totalCents"`
	Notes               string      `json:"notes"`
	ValidUntil          string      `json:"validUntil"`
	CreatedBy           int64       `json:"createdBy"`
	CreatedByName       string      `json:"createdByName"`
	ConvertedHeldSaleID int64       `json:"convertedHeldSaleId"`
	CreatedAt           string      `json:"createdAt"`
	UpdatedAt           string      `json:"updatedAt"`
}

type QuoteInput struct {
	CustomerID    int64       `json:"customerId"`
	CustomerName  string      `json:"customerName"`
	CustomerPhone string      `json:"customerPhone"`
	Items         []QuoteItem `json:"items"`
	DiscountCents int64       `json:"discountCents"`
	DiscountLabel string      `json:"discountLabel"`
	Notes         string      `json:"notes"`
	ValidUntil    string      `json:"validUntil"`
}

type TeamDevice struct {
	DeviceID   string `json:"deviceId"`
	DeviceName string `json:"deviceName"`
	AppVersion string `json:"appVersion"`
	LastSeen   string `json:"lastSeen"`
	ThisDevice bool   `json:"thisDevice"`
	Approved   bool   `json:"approved"`
	Revoked    bool   `json:"revoked"`
}

type TeamSyncConfigRequest struct {
	ProjectURL string `json:"projectUrl"`
	ServiceKey string `json:"serviceKey"` // masked __SET__ value keeps existing
	TeamCode   string `json:"teamCode"`
	Enabled    *bool  `json:"enabled"`
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
	ID          int64  `json:"id"`
	OrderID     int64  `json:"orderId"`
	OrderNumber string `json:"orderNumber"`
	Status      string `json:"status"`
	Target      string `json:"target"`
	Attempts    int    `json:"attempts"`
	LastError   string `json:"lastError"`
	CreatedAt   string `json:"createdAt"`
	PrintedAt   string `json:"printedAt"`
}

type SyncRequest struct {
	Transactions []CheckoutRequest `json:"transactions" binding:"required"`
}

type SyncResult struct {
	ClientUUID  string `json:"clientUuid"`
	OrderID     int64  `json:"orderId"`
	OrderNumber string `json:"orderNumber"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	// Rejected=true means the server UNDERSTOOD the sale and definitively
	// refused it (stock, credit limit, duplicate receipt…) — the offline
	// queue may drop it after telling staff. Anything else (5xx text, HTML
	// error pages, empty strings, unknown errors) must be retried, never
	// deleted — the old string-matching frontend deleted real sales when an
	// unfamiliar error merely CONTAINED a word like "invalid".
	Rejected bool `json:"rejected,omitempty"`
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

// PaymentConfig is the till-facing payment capability snapshot (public
// identifiers only — no secret keys).
type PaymentConfig struct {
	Mpesa struct {
		Env     string `json:"env"`
		Route   string `json:"route"` // buni | mock | manual (v1.4.4 rail set)
		Till    string `json:"till"`
		Paybill string `json:"paybill"`
	} `json:"mpesa"`
	CreditEnabled  bool `json:"creditEnabled"`
	LoyaltyEnabled bool `json:"loyaltyEnabled"`
}

func FmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// ---- Stocktake (count sessions with variance report) ----

// StockCount is a counting session: open it (snapshot expected stock),
// record counted quantities per product, then close it — optionally
// applying the variance as the new system stock (audited + team-synced).
type StockCount struct {
	ID                 int64  `json:"id"`
	Number             string `json:"number"`
	Status             string `json:"status"` // OPEN | DONE | CANCELLED
	Note               string `json:"note"`
	CountedBy          int64  `json:"countedBy"`
	CountedByName      string `json:"countedByName"`
	OpenedAt           string `json:"openedAt"`
	ClosedAt           string `json:"closedAt"`
	LinesTotal         int64  `json:"linesTotal"`
	LinesCounted       int64  `json:"linesCounted"`
	VarianceUnits      int64  `json:"varianceUnits"`
	VarianceValueCents int64  `json:"varianceValueCents"`
}

// StockCountLine is one product inside a counting session. CountedQty nil
// means "not counted yet"; SystemQty is the live stock at close time.
type StockCountLine struct {
	ID               int64  `json:"id"`
	CountID          int64  `json:"countId"`
	ProductID        int64  `json:"productId"`
	SKU              string `json:"sku"`
	Name             string `json:"name"`
	ExpectedQtyMilli int64  `json:"expectedQtyMilli"`
	CountedQtyMilli  *int64 `json:"countedQtyMilli"`
	SystemQtyMilli   int64  `json:"systemQtyMilli"`
	UnitCostCents    int64  `json:"unitCostCents"`
	Applied          bool   `json:"applied"`
}

type SaveCountLineRequest struct {
	ProductID       int64  `json:"productId" binding:"required"`
	CountedQtyMilli *int64 `json:"countedQtyMilli"` // nil clears the count
}

type CompleteCountRequest struct {
	Apply bool `json:"apply"` // true: make counted the new system stock
}

// ---- Gift cards ----

// GiftCard is a prepaid code minted when a gift-card product is paid for.
// Redeeming converts the remaining value into the customer's prepaid store
// credit in one step (spending then works partially, from the balance).
type GiftCard struct {
	ID                 int64  `json:"id"`
	Code               string `json:"code"`
	OrderID            int64  `json:"orderId"`
	InitialCents       int64  `json:"initialCents"`
	RemainingCents     int64  `json:"remainingCents"`
	Status             string `json:"status"` // ACTIVE | EMPTY
	IssuedAt           string `json:"issuedAt"`
	RedeemedAt         string `json:"redeemedAt"`
	RedeemedByCustomer int64  `json:"redeemedByCustomerId"`
}

type RedeemGiftCardRequest struct {
	Code       string `json:"code" binding:"required"`
	CustomerID int64  `json:"customerId" binding:"required"`
}

// ---- Stock ledger (v1.6.3) ----

// Stock-movement kinds. The ledger is append-only: products.stock_qty is
// the fast projection, stock_movements is the audit trail (OSPOS pattern).
const (
	MoveOpening       = "OPENING"        // migration anchor / new tracked product
	MoveSale          = "SALE"           // stock left the shelf through the till
	MoveVoidRestore   = "VOID_RESTORE"   // voided paid order puts stock back
	MoveAdjustment    = "ADJUSTMENT"     // manual +/- on Inventory
	MoveCount         = "COUNT"          // stock-take apply
	MovePOReceive     = "PO_RECEIVE"     // supplier goods-in
	MoveReturnRestock = "RETURN_RESTOCK" // customer return puts stock back
	MoveDisposal      = "DISPOSAL"       // v1.7.0: written-off stock (expired/spoilt/lost/damaged)
	MoveBatchIn       = "BATCH_IN"       // v1.8.0: manual goods-in recorded directly on a batch
	MoveBatchRecall   = "BATCH_RECALL"   // v1.8.0: a lot pulled off the shelf (safety/quality)
)

// StockMovement is one audited stock change.
type StockMovement struct {
	ID                int64  `json:"id"`
	ProductID         int64  `json:"productId"`
	ProductName       string `json:"productName"`
	SKU               string `json:"sku"`
	DeltaMilli        int64  `json:"deltaMilli"`
	BalanceAfterMilli int64  `json:"balanceAfterMilli"`
	Kind              string `json:"kind"`
	Reference         string `json:"reference"`
	Note              string `json:"note"`
	UserID            int64  `json:"userId"`
	Username          string `json:"username"`
	DeviceID          string `json:"deviceId"`
	CreatedAt         string `json:"createdAt"`
}

// ---- Batches (v1.8.0) ----

// Batch statuses. ACTIVE lots sell (FEFO); EXHAUSTED is bookkeeping for a
// fully-sold lot; RECALLED lots are frozen — remaining quantity comes off
// the shelf and can only be disposed.
const (
	BatchActive    = "ACTIVE"
	BatchExhausted = "EXHAUSTED"
	BatchRecalled  = "RECALLED"
)

// ProductBatch is one received lot of a batch-tracked product: a batch
// number, an expiry date, what came in, what remains, and what it cost —
// everything the FEFO allocator and the expiry report need.
type ProductBatch struct {
	ID                int64  `json:"id"`
	ProductID         int64  `json:"productId"`
	ProductName       string `json:"productName"`
	SKU               string `json:"sku"`
	BatchNo           string `json:"batchNo"`
	ExpiryDate        string `json:"expiryDate"` // 'YYYY-MM-DD', '' = no expiry tracked
	QtyReceivedMilli  int64  `json:"qtyReceivedMilli"`
	QtyRemainingMilli int64  `json:"qtyRemainingMilli"`
	CostCents         int64  `json:"costCents"` // per-unit cost at receipt
	SupplierID        int64  `json:"supplierId"`
	SupplierName      string `json:"supplierName"`
	Reference         string `json:"reference"` // PO number / goods-in note
	Status            string `json:"status"`
	CreatedAt         string `json:"createdAt"`
	UpdatedAt         string `json:"updatedAt"`
}

// ExpiryBatchRow is one lot in the expiry report: what remains, when it
// dies, and the money at risk if it is written off.
type ExpiryBatchRow struct {
	BatchID           int64  `json:"batchId"`
	ProductID         int64  `json:"productId"`
	Name              string `json:"name"`
	SKU               string `json:"sku"`
	BatchNo           string `json:"batchNo"`
	ExpiryDate        string `json:"expiryDate"`
	QtyRemainingMilli int64  `json:"qtyRemainingMilli"`
	CostCents         int64  `json:"costCents"`
	ValueCents        int64  `json:"valueCents"` // remaining × cost
	Status            string `json:"status"`
}

// ---- Returns (v1.6.3) ----

// Refund methods for a return.
const (
	RefundCash        = "CASH"
	RefundStoreCredit = "STORE_CREDIT"
)

// ReturnDoc is a completed customer return against a paid order. Lines are
// snapshots of the original sale lines (price does not drift).
type ReturnDoc struct {
	ID              int64        `json:"id"`
	Number          string       `json:"number"`
	OrderID         int64        `json:"orderId"`
	OrderNumber     string       `json:"orderNumber"`
	OrderClientUUID string       `json:"-"` // cross-till identity of the source sale
	ClientUUID      string       `json:"-"` // this document's replay key
	TotalCents      int64        `json:"totalCents"`
	Method          string       `json:"method"`
	Reason          string       `json:"reason"`
	Status          string       `json:"status"`
	CreatedBy       int64        `json:"createdBy"`
	CreatedByName   string       `json:"createdByName"`
	CreatedAt       string       `json:"createdAt"`
	Items           []ReturnItem `json:"items"`
}

type ReturnItem struct {
	ID             int64  `json:"id"`
	ReturnID       int64  `json:"returnId"`
	OrderItemID    int64  `json:"orderItemId"`
	ProductID      int64  `json:"productId"`
	Name           string `json:"name"`
	SKU            string `json:"sku"`
	QtyMilli       int64  `json:"qtyMilli"`
	UnitPriceCents int64  `json:"unitPriceCents"`
	LineTotalCents int64  `json:"lineTotalCents"`
}

type CreateReturnRequest struct {
	OrderID    int64              `json:"orderId" binding:"required"`
	CustomerID int64              `json:"customerId"` // required when Method = STORE_CREDIT
	Method     string             `json:"method"`
	Reason     string             `json:"reason"`
	Lines      []CreateReturnLine `json:"lines" binding:"required,min=1"`
}

type CreateReturnLine struct {
	OrderItemID int64 `json:"orderItemId" binding:"required"`
	QtyMilli    int64 `json:"qtyMilli" binding:"required,min=1"`
}

// ---- Expenses (v1.6.3) ----

type ExpenseType struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	IsActive bool   `json:"isActive"`
}

type Expense struct {
	ID            int64  `json:"id"`
	Number        string `json:"number"`
	ExpenseTypeID int64  `json:"expenseTypeId"`
	TypeName      string `json:"typeName"`
	ClientUUID    string `json:"-"`
	AmountCents   int64  `json:"amountCents"`
	Method        string `json:"method"`
	Reference     string `json:"reference"`
	Note          string `json:"note"`
	SpentOn       string `json:"spentOn"`
	CreatedBy     int64  `json:"createdBy"`
	CreatedByName string `json:"createdByName"`
	CreatedAt     string `json:"createdAt"`
}

type CreateExpenseRequest struct {
	ExpenseTypeID int64  `json:"expenseTypeId" binding:"required"`
	AmountCents   int64  `json:"amountCents" binding:"required,min=1"`
	Method        string `json:"method"`
	Reference     string `json:"reference"`
	Note          string `json:"note"`
	SpentOn       string `json:"spentOn"` // YYYY-MM-DD; defaults to today (EAT)
}

// VatGroup is one line of a mixed-rate receipt's VAT analysis.
type VatGroup struct {
	Rate       float64
	TaxType    string // first KRA type seen at this rate (X = gift cards)
	TaxCents   int64
	GrossCents int64 // paid at this rate, VAT incl.
}

// VatBreakdown (v1.11) groups a sale's VAT by rate. It returns nil when
// the sale has one rate (or predates per-line VAT): the receipt then keeps
// its single "VAT (16%)" line.
func VatBreakdown(items []OrderItem) []VatGroup {
	var out []VatGroup
	idx := map[float64]int{}
	for _, it := range items {
		if it.TaxType == "" {
			return nil
		}
		k, ok := idx[it.TaxRate]
		if !ok {
			k = len(out)
			idx[it.TaxRate] = k
			out = append(out, VatGroup{Rate: it.TaxRate, TaxType: it.TaxType})
		}
		out[k].TaxCents += it.TaxCents
		out[k].GrossCents += it.PaidCents
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// VatGroupLabel: "VAT 16%", "VAT 8%", "Exempt", "Zero-rated", "No VAT", "Gift cards".
func VatGroupLabel(g VatGroup, pct func(float64) string) string {
	if g.Rate > 0 {
		return "VAT " + pct(g.Rate) + "%"
	}
	switch g.TaxType {
	case "A":
		return "Exempt"
	case "C":
		return "Zero-rated"
	case "X":
		return "Gift cards (no VAT)"
	}
	return "No VAT"
}
