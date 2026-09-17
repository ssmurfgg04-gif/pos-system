package services

import (
        "context"
        "sync"

        "posapp/internal/database"
        "posapp/internal/offsite"
        "posapp/internal/printer"
        "posapp/internal/settings"
        "posapp/internal/tenants"
        "posapp/internal/ws"
)

// ShopPool serves one Service (own DB + settings + offsite worker) per shop.
// Hub and printer are shared box-wide; everything else is tenant-scoped.
type ShopPool struct {
        mu      sync.Mutex
        dbPool  *tenants.Pool
        hub     *ws.Hub
        printer *printer.Worker
        svcs    map[string]*Service
        ups     map[string]*offsite.Worker
        started map[string]bool
}

// NewShopPool builds the pool (shops open lazily on first request).
func NewShopPool(dbPool *tenants.Pool, hub *ws.Hub, pw *printer.Worker) *ShopPool {
        return &ShopPool{
                dbPool:  dbPool,
                hub:     hub,
                printer: pw,
                svcs:    map[string]*Service{},
                ups:     map[string]*offsite.Worker{},
                started: map[string]bool{},
        }
}

// Service returns the cached service for a shop, building it on first use.
// Exactly one Service (and one settings.Store) exists per shop process-wide.
func (p *ShopPool) Service(shopID string) (*Service, error) {
        p.mu.Lock()
        defer p.mu.Unlock()
        if s, ok := p.svcs[shopID]; ok {
                return s, nil
        }
        db, err := p.dbPool.Open(shopID)
        if err != nil {
                return nil, err
        }
        st, err := settings.New(db)
        if err != nil {
                return nil, err
        }
        svc := New(db, st, p.hub, p.printer)
        ow := offsite.NewWorker(st, func(action, entity, entityID, details string) {
                svc.Audit(0, "system", action, entity, entityID, details)
        })
        svc.AttachOffsite(ow)
        p.svcs[shopID] = svc
        p.ups[shopID] = ow
        return svc, nil
}

// SetPrinter wires the shared printer worker into the pool: present services
// update in place (same pointers handlers hold) and future ones inherit it.
func (p *ShopPool) SetPrinter(pw *printer.Worker) {
        p.mu.Lock()
        defer p.mu.Unlock()
        p.printer = pw
        for _, s := range p.svcs {
                s.SetPrinter(pw)
        }
}

// Uploader returns the shop's offsite worker (for the status endpoint).
func (p *ShopPool) Uploader(shopID string) (*offsite.Worker, error) {
        if _, err := p.Service(shopID); err != nil {
                return nil, err
        }
        p.mu.Lock()
        defer p.mu.Unlock()
        return p.ups[shopID], nil
}

// DB returns the shop's database handle (for handlers that query directly).
func (p *ShopPool) DB(shopID string) (*database.DB, error) {
        return p.dbPool.Open(shopID)
}

// FindCheckoutShop locates the shop holding an M-Pesa checkout request
// (Daraja callbacks carry no shop context; rare op, scans shops).
func (p *ShopPool) FindCheckoutShop(requestID string) (string, bool) {
        if requestID == "" {
                return "", false
        }
        for _, id := range p.dbPool.ShopIDs() {
                db, err := p.dbPool.Open(id)
                if err != nil {
                        continue
                }
                var n int
                q := db.Rebind(`SELECT COUNT(*) FROM payments WHERE checkout_request_id = ?`)
                if err := db.QueryRow(q, requestID).Scan(&n); err == nil && n > 0 {
                        return id, true
                }
        }
        return "", false
}

// FindUserShop locates the shop holding a user id (PIN login path).
func (p *ShopPool) FindUserShop(userID int64) (string, bool) {
        return p.dbPool.FindUserShop(userID)
}

// Settings returns the shop's settings store.
func (p *ShopPool) Settings(shopID string) (*settings.Store, error) {
        svc, err := p.Service(shopID)
        if err != nil {
                return nil, err
        }
        return svc.Settings(), nil
}

// CloseAll closes every pooled database handle (tests; Windows locks open
// files so TempDir cleanup fails otherwise).
func (p *ShopPool) CloseAll() {
        p.dbPool.CloseAll()
}

// StartAll boots backup schedulers, upload workers, and STK sweepers for
// every registered shop (call once at startup; shops created later start on
// first open via EnsureStarted).
func (p *ShopPool) StartAll(ctx context.Context) {
        for _, id := range p.dbPool.ShopIDs() {
                p.EnsureStarted(ctx, id)
        }
}

// EnsureStarted boots a shop's background loops (idempotent).
func (p *ShopPool) EnsureStarted(ctx context.Context, shopID string) {
        svc, err := p.Service(shopID)
        if err != nil {
                return
        }
        p.mu.Lock()
        ow := p.ups[shopID]
        if p.started[shopID] {
                p.mu.Unlock()
                return
        }
        p.started[shopID] = true
        p.mu.Unlock()
        svc.StartBackupScheduler()
        go ow.Run(ctx)
        go NewSweeper(svc).Run(ctx)
}
