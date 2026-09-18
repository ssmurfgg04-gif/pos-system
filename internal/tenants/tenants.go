// Package tenants maps usernames to shops (database-per-tenant isolation).
// Each shop is one SQLite file; a tiny JSON registry in the data dir maps
// shop ids to files and usernames to shops. No shared tables, no shop_id
// columns — a user can only ever open their own shop's file.
package tenants

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"posapp/internal/database"
)

// Shop is one tenant: a name plus its database file (absolute path).
// NOTE: multi-tenancy is a SQLite-file feature; postgres mode serves one
// shop per DSN (pool returns the shared handle regardless of shop id).
type Shop struct {
	ID        string `json:"id"` // sh-<8 hex>, never sequential
	Name      string `json:"name"`
	DBFile    string `json:"dbFile"`
	CreatedAt string `json:"createdAt"`
}

// Registry is the on-disk index: shops by id + usernames to shop ids.
// A username may live in multiple shops (owner with two shops) — the value
// is a list. Old single-string files are migrated on load.
type Registry struct {
	mu   sync.Mutex
	path string
	dir  string
	ShopList []Shop              `json:"shops"`
	Users    map[string][]string `json:"users"`
	JWTSecret string             `json:"jwtSecret"`
}

// UnmarshalJSON handles both old {"users":{"alice":"sh-..."}} and new
// {"users":{"alice":["sh-..."]}} files.
func (r *Registry) UnmarshalJSON(data []byte) error {
	type raw struct {
		ShopList []Shop          `json:"shops"`
		Users    json.RawMessage `json:"users"`
		JWTSecret string         `json:"jwtSecret"`
	}
	var tmp raw
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}
	r.ShopList = tmp.ShopList
	r.JWTSecret = tmp.JWTSecret
	r.Users = map[string][]string{}
	if len(tmp.Users) == 0 || string(tmp.Users) == "null" {
		return nil
	}
	// Try new form first.
	var m2 map[string][]string
	if err := json.Unmarshal(tmp.Users, &m2); err == nil {
		r.Users = m2
		return nil
	}
	// Old form: map[string]string
	var m1 map[string]string
	if err := json.Unmarshal(tmp.Users, &m1); err != nil {
		return err
	}
	for k, v := range m1 {
		r.Users[k] = []string{v}
	}
	return nil
}

// EnsureJWTSecret sets the shared secret once (fallback seeds it, e.g. from
// a pre-tenancy database, so upgrades keep existing sessions valid).
func (r *Registry) EnsureJWTSecret(fallback string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.JWTSecret != "" {
		return r.JWTSecret
	}
	if fallback == "" {
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			panic("rand: " + err.Error())
		}
		fallback = hex.EncodeToString(b[:])
	}
	r.JWTSecret = fallback
	_ = r.save()
	return r.JWTSecret
}

// Load opens (or creates) the registry at dir/shops.json.
func Load(dir string) (*Registry, error) {
	r := &Registry{path: filepath.Join(dir, "shops.json"), dir: dir, Users: map[string][]string{}}
	raw, err := os.ReadFile(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(raw, r); err != nil {
		return nil, fmt.Errorf("corrupt registry: %w", err)
	}
	if r.Users == nil {
		r.Users = map[string][]string{}
	}
	return r, nil
}

func (r *Registry) save() error {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

// newShopID mints an unguessable shop id.
func newShopID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "sh-" + hex.EncodeToString(b[:]), nil
}

// CreateShop registers a shop; its database file is shops/<id>.db.
// Pass dbFile != "" only to adopt a pre-existing file (desktop upgrade).
func (r *Registry) CreateShop(name, dbFile, createdAt string) (Shop, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if name == "" {
		return Shop{}, fmt.Errorf("shop name required")
	}
	id, err := newShopID()
	if err != nil {
		return Shop{}, err
	}
	if dbFile == "" {
		dbFile = filepath.Join(r.dir, "shops", id+".db")
	}
	if err := os.MkdirAll(filepath.Dir(dbFile), 0o755); err != nil {
		return Shop{}, err
	}
	s := Shop{ID: id, Name: name, DBFile: dbFile, CreatedAt: createdAt}
	r.ShopList = append(r.ShopList, s)
	if err := r.save(); err != nil {
		return Shop{}, err
	}
	return s, nil
}

// RegisterUser binds a username to a shop. A username may live in multiple
// shops (owner with two shops) — same username in same shop is still unique.
func (r *Registry) RegisterUser(username, shopID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := lower(username)
	if key == "" {
		return fmt.Errorf("username required")
	}
	found := false
	for _, s := range r.ShopList {
		if s.ID == shopID {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("unknown shop")
	}
	for _, sid := range r.Users[key] {
		if sid == shopID {
			return fmt.Errorf("username taken in this shop")
		}
	}
	r.Users[key] = append(r.Users[key], shopID)
	return r.save()
}

// ShopForUser resolves a username to its (first) shop id ("", false if unknown).
// For multi-shop users, use ShopsForUser.
func (r *Registry) ShopForUser(username string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids, ok := r.Users[lower(username)]
	if !ok || len(ids) == 0 {
		return "", false
	}
	return ids[0], true
}

// ShopsForUser returns all shop ids a username belongs to.
func (r *Registry) ShopsForUser(username string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids, _ := r.Users[lower(username)]
	out := make([]string, len(ids))
	copy(out, ids)
	return out
}

// DBPath returns the database file path for a new shop (shops/<id>.db).
func (r *Registry) DBPath(shopID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return filepath.Join(r.dir, "shops", shopID+".db")
}

// Find returns a shop by id.
func (r *Registry) Find(shopID string) (Shop, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.ShopList {
		if s.ID == shopID {
			return s, true
		}
	}
	return Shop{}, false
}

func lower(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out = append(out, c)
	}
	return string(out)
}

// Pool caches open database handles per shop (SQLite single-conn each).
type Pool struct {
	mu     sync.Mutex
	reg    *Registry
	open   map[string]*database.DB
	driver string
	pgDSN  string
}

// ShopIDs lists registered shop ids (for startup loops).
func (p *Pool) ShopIDs() []string {
	p.reg.mu.Lock()
	defer p.reg.mu.Unlock()
	out := make([]string, 0, len(p.reg.ShopList))
	for _, s := range p.reg.ShopList {
		out = append(out, s.ID)
	}
	return out
}

// FindUserShop locates the shop holding a user id (login-rate op; scans
// registered shops). Returns "", false when unknown.
func (p *Pool) FindUserShop(userID int64) (string, bool) {
	for _, id := range p.ShopIDs() {
		db, err := p.Open(id)
		if err != nil {
			continue
		}
		var n int
		q := db.Rebind(`SELECT COUNT(*) FROM users WHERE id = ? AND is_active = 1`)
		if err := db.QueryRow(q, userID).Scan(&n); err == nil && n == 1 {
			return id, true
		}
	}
	return "", false
}

// NewPool builds a pool over a registry (sqlite files under dir unless pgDSN set).
func NewPool(reg *Registry, driver, pgDSN string) *Pool {
	return &Pool{reg: reg, open: map[string]*database.DB{}, driver: driver, pgDSN: pgDSN}
}

// Open returns the cached handle for a shop, migrating on first open.
// Postgres deployments are single-tenant (one DSN = one shop): opening a
// second shop there errors instead of silently sharing data.
func (p *Pool) Open(shopID string) (*database.DB, error) {
        p.mu.Lock()
        defer p.mu.Unlock()
        if db, ok := p.open[shopID]; ok {
                return db, nil
        }
        if p.driver == "postgres" && len(p.open) > 0 {
                return nil, fmt.Errorf("multi-tenancy requires SQLite files")
        }
	shop, ok := p.reg.Find(shopID)
	if !ok {
		return nil, fmt.Errorf("unknown shop")
	}
	var (
		db  *database.DB
		err error
	)
	if p.driver == "postgres" {
		db, err = database.Open("postgres", "", p.pgDSN)
	} else {
		db, err = database.Open("sqlite", shop.DBFile, "")
	}
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(); err != nil {
		db.Close()
		return nil, err
	}
	p.open[shopID] = db
	return db, nil
}

// Inject registers an already-open handle (boot path opens the default
// database directly; the pool must reuse it, never double-open a file).
func (p *Pool) Inject(shopID string, db *database.DB) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.open[shopID] = db
}

// CloseAll closes every pooled handle (tests + shutdown).
func (p *Pool) CloseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, db := range p.open {
		db.Close()
		delete(p.open, id)
	}
}
