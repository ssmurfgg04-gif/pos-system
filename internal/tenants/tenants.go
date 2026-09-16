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

// Registry is the on-disk index: shops by id + username to shop id.
type Registry struct {
	mu   sync.Mutex
	path string
	dir  string
	ShopList []Shop          `json:"shops"`
	Users    map[string]string `json:"users"`
}

// Load opens (or creates) the registry at dir/shops.json.
func Load(dir string) (*Registry, error) {
	r := &Registry{path: filepath.Join(dir, "shops.json"), dir: dir, Users: map[string]string{}}
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
		r.Users = map[string]string{}
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

// CreateShop registers a shop with an explicit database file name.
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
	s := Shop{ID: id, Name: name, DBFile: dbFile, CreatedAt: createdAt}
	r.ShopList = append(r.ShopList, s)
	if err := r.save(); err != nil {
		return Shop{}, err
	}
	return s, nil
}

// RegisterUser binds a username to a shop (usernames unique per box).
// Lookup is case-insensitive; the canonical stored form is lowercased.
func (r *Registry) RegisterUser(username, shopID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := lower(username)
	if key == "" {
		return fmt.Errorf("username required")
	}
	if _, taken := r.Users[key]; taken {
		return fmt.Errorf("username taken")
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
	r.Users[key] = shopID
	return r.save()
}

// ShopForUser resolves a username to its shop id ("", false if unknown).
func (r *Registry) ShopForUser(username string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.Users[lower(username)]
	return id, ok
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

// NewPool builds a pool over a registry (sqlite files under dir unless pgDSN set).
func NewPool(reg *Registry, driver, pgDSN string) *Pool {
	return &Pool{reg: reg, open: map[string]*database.DB{}, driver: driver, pgDSN: pgDSN}
}

// Open returns the cached handle for a shop, migrating on first open.
func (p *Pool) Open(shopID string) (*database.DB, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if db, ok := p.open[shopID]; ok {
		return db, nil
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

// CloseAll closes every pooled handle (tests + shutdown).
func (p *Pool) CloseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, db := range p.open {
		db.Close()
		delete(p.open, id)
	}
}
