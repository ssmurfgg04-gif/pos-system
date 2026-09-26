// Package models defines the API DTOs shared between handlers and the
// frontend. Money is ALWAYS integer cents. Times are RFC3339 strings on
// the wire. JSON keys are camelCase.
package models

import (
        "errors"
        "time"
)

func errInvalidProduct(msg string) error { return errors.New(msg) }

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
        MethodAccount = "account"  // charge to a customer tab (customer credit)
        MethodCredit  = "credit"   // pay from prepaid store credit balance
        MethodPaystack = "paystack" // card / mobile money via Paystack checkout

        // Store-credit ledger kinds (customer_ledger.kind). Store credit is
        // money the shop OWES the customer (prepaid) — the mirror of the
        // tab balance. Top-up adds, redemption subtracts.
        LedgerCreditTopup  = "credit_topup"
        LedgerCreditRedeem = "credit_redeem"

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
        HomePage    string   `json:"homePage"`            // landing route after login ("" = permission cascade)
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
        ID           int64  `json:"id"`
        SKU          string `json:"sku"`
        Barcode      string `json:"barcode"`
        Name         string `json:"name"`
        CategoryID   int64  `json:"categoryId"`
        CategoryName string `json:"categoryName"`
        ImageURL     string `json:"imageUrl"`
        PriceCents   int64  `json:"priceCents"`
        CostCents    int64  `json:"costCents"`
        StockQty     int    `json:"stockQty"`
        TrackStock   bool   `json:"trackStock"`
        Active       bool   `json:"active"`
        IsGiftCard   bool   `json:"isGiftCard"` // mints redeemable codes on paid sale
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
        PaymentMethod     string         `json:"paymentMethod" binding:"required,oneof=cash mpesa account credit paystack"`
        PaymentMode       string         `json:"paymentMode"`   // auto|stk|manual (mpesa only)
        CustomerPhone     string         `json:"customerPhone"`
        CustomerName      string         `json:"customerName"`
        CustomerEmail     string         `json:"customerEmail"` // paystack receipt + receipt email
        CustomerID        int64          `json:"customerId"` // required for account tabs + credit
        Note              string         `json:"note"`
        ClientUUID        string         `json:"clientUuid"` // offline idempotency key
        DiscountCents     int64          `json:"discountCents"` // order-level discount (permission-gated)
        DiscountLabel     string         `json:"discountLabel"` // e.g. "staff 10%", "negotiated"
        RedeemPoints      int64          `json:"redeemPoints"`  // loyalty points spent as payment (permission-gated)
        SplitPayments     []SplitLeg     `json:"splitPayments"` // optional mixed tender (part cash, part M-Pesa…)
}

// SplitLeg is one tender in a mixed payment. Loyalty redemption is NOT a
// leg (it already reduced the payable); account tabs cannot be split.
type SplitLeg struct {
        Method      string `json:"method" binding:"required,oneof=cash mpesa paystack credit"`
        AmountCents int64  `json:"amountCents"`
        Phone       string `json:"phone"`  // mpesa leg (auto/stk)
        Email       string `json:"email"`  // paystack leg
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
        Email             string `json:"email"` // paystack: address the charge was opened for
        MpesaReceipt      string `json:"mpesaReceipt"` // mpesa code, or the paystack reference once completed
        CheckoutRequestID string `json:"checkoutRequestId"` // daraja checkout id, or paystack reference
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
        DiscountCents int64       `json:"discountCents"`
        DiscountLabel string      `json:"discountLabel"`
        PointsRedeemed int64      `json:"pointsRedeemed"`
        TaxCents      int64       `json:"taxCents"`
        TotalCents    int64       `json:"totalCents"`
        TaxPercent    float64     `json:"taxPercent"`
        TaxIncluded   bool        `json:"taxIncluded"`
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
        CreditLimitCents int64  `json:"creditLimitCents"`  // 0 = no tab allowed
        LoyaltyPoints    int64  `json:"loyaltyPoints"`
        BalanceCents     int64  `json:"balanceCents"`      // >0 means the customer owes the shop
        StoreCreditCents int64  `json:"storeCreditCents"`  // >0 means the shop owes the customer (prepaid)
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

// ---- Suppliers & stock-in ----

const (
        POPending   = "PENDING"
        POReceived  = "RECEIVED"
        POCancelled = "CANCELLED"
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
        Qty            int    `json:"qty"`
        CostCents      int64  `json:"costCents"`
        LineTotalCents int64  `json:"lineTotalCents"`
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
        ID          int64  `json:"id"`
        TakeID      int64  `json:"takeId"`
        ProductID   int64  `json:"productId"`
        Name        string `json:"name"`
        SKU         string `json:"sku"`
        ExpectedQty int    `json:"expectedQty"`
        CountedQty  int    `json:"countedQty"`
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
        // MaxOrderQty caps one line's quantity (pre- and post-merge).
        MaxOrderQty = 100000
        // MaxPriceCents caps any unit price or cost (10B KES).
        MaxPriceCents = 1000000000000
        // MaxOrderLines caps checkout/PO line counts (merge-bypass).
        MaxOrderLines = 500
        // MaxStockDelta caps single stock adjustments and take counts.
        MaxStockDelta = 100000000
        // MaxStockQty caps stored stock quantities.
        MaxStockQty = 1000000000
        // MaxNameLen caps product/customer/supplier names (receipt/UI sanity).
        MaxNameLen = 200
        // MaxCodeLen caps SKUs, barcodes, phones.
        MaxCodeLen = 64
        // MaxTaxPercent caps VAT configuration.
        MaxTaxPercent = 100
)

// CheckProductInput validates catalog fields shared by API + CSV import.
func CheckProductInput(name, sku, barcode string, priceCents, costCents int64, stock int) error {
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
        if stock < 0 || stock > MaxStockQty {
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

type TeamDevice struct {
        DeviceID   string `json:"deviceId"`
        DeviceName string `json:"deviceName"`
        AppVersion string `json:"appVersion"`
        LastSeen   string `json:"lastSeen"`
        ThisDevice bool   `json:"thisDevice"`
        Approved   bool   `json:"approved"`
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

// ---- Paystack card / mobile-money checkout ----

// PaystackInitRequest starts (or re-opens) a Paystack checkout for a
// pending order. Email is optional: a deterministic placeholder is used
// when the till does not collect one.
type PaystackInitRequest struct {
        Email string `json:"email"`
}

// PaystackInitResult carries what the frontend needs to open the popup:
// the PUBLIC key goes to PaystackPop.setup({key, access_code}); the
// authorization URL is the redirect fallback. The secret key never leaves
// the server.
type PaystackInitResult struct {
        Reference        string `json:"reference"`
        AccessCode       string `json:"accessCode"`
        AuthorizationURL string `json:"authorizationUrl"`
        PublicKey        string `json:"publicKey"`
        Currency         string `json:"currency"`
        AmountCents      int64  `json:"amountCents"`
}

// PaystackVerifyRequest completes a payment from the popup callback —
// the reference is re-verified server-side before anything is marked paid.
type PaystackVerifyRequest struct {
        Reference string `json:"reference" binding:"required"`
}

// PaymentConfig is the till-facing payment capability snapshot (public
// identifiers only — no secret keys).
type PaymentConfig struct {
        Paystack struct {
                Enabled   bool   `json:"enabled"`
                PublicKey string `json:"publicKey"`
                Currency  string `json:"currency"`
                Callback  string `json:"callbackUrl"`
                Configured bool `json:"configured"` // secret key present server-side
        } `json:"paystack"`
        Mpesa struct {
                Env    string `json:"env"`
                Till   string `json:"till"`
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
        ID            int64  `json:"id"`
        CountID       int64  `json:"countId"`
        ProductID     int64  `json:"productId"`
        SKU           string `json:"sku"`
        Name          string `json:"name"`
        ExpectedQty   int    `json:"expectedQty"`
        CountedQty    *int   `json:"countedQty"`
        SystemQty     int    `json:"systemQty"`
        UnitCostCents int64  `json:"unitCostCents"`
        Applied       bool   `json:"applied"`
}

type SaveCountLineRequest struct {
        ProductID  int64 `json:"productId" binding:"required"`
        CountedQty *int  `json:"countedQty"` // nil clears the count
}

type CompleteCountRequest struct {
        Apply bool `json:"apply"` // true: make counted the new system stock
}

// ---- Gift cards ----

// GiftCard is a prepaid code minted when a gift-card product is paid for.
// Redeeming converts the remaining value into the customer's prepaid store
// credit in one step (spending then works partially, from the balance).
type GiftCard struct {
        ID                int64  `json:"id"`
        Code              string `json:"code"`
        OrderID           int64  `json:"orderId"`
        InitialCents      int64  `json:"initialCents"`
        RemainingCents    int64  `json:"remainingCents"`
        Status            string `json:"status"` // ACTIVE | EMPTY
        IssuedAt          string `json:"issuedAt"`
        RedeemedAt        string `json:"redeemedAt"`
        RedeemedByCustomer int64 `json:"redeemedByCustomerId"`
}

type RedeemGiftCardRequest struct {
        Code       string `json:"code" binding:"required"`
        CustomerID int64  `json:"customerId" binding:"required"`
}
