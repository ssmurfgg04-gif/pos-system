// Package database opens the datastore (SQLite WAL by default, PostgreSQL
// optional) and runs versioned, dialect-aware migrations.
package database

import (
        "database/sql"
        "fmt"
        "strings"

        _ "github.com/lib/pq"
        _ "modernc.org/sqlite"
)

type DB struct {
        *sql.DB
        driver string // "sqlite" | "postgres"
        path    string // sqlite file path ("" for postgres)
}

// Open initializes the datastore. SQLite runs in WAL mode with a single
// pooled connection (serialized access — see docs/DEADLOCK.md discipline:
// never issue queries while rows from another query are still open).
func Open(driver, sqlitePath, pgDSN string) (*DB, error) {
        switch driver {
        case "postgres":
                db, err := sql.Open("postgres", pgDSN)
                if err != nil {
                        return nil, fmt.Errorf("open postgres: %w", err)
                }
                db.SetMaxOpenConns(10)
                if err := db.Ping(); err != nil {
                        return nil, fmt.Errorf("ping postgres: %w", err)
                }
                return &DB{db, "postgres", ""}, nil
        default: // sqlite
                dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", sqlitePath)
                db, err := sql.Open("sqlite", dsn)
                if err != nil {
                        return nil, fmt.Errorf("open sqlite: %w", err)
                }
                // One connection serializes all access; combined with WAL this gives
                // crash-safety without SQLITE_BUSY storms. Code must never nest
                // queries inside an open rows loop (enforced by review + regression test).
                db.SetMaxOpenConns(1)
                if err := db.Ping(); err != nil {
                        return nil, fmt.Errorf("ping sqlite: %w", err)
                }
                return &DB{db, "sqlite", sqlitePath}, nil
        }
}

func (d *DB) Driver() string { return d.driver }

func (d *DB) IsSQLite() bool { return d.driver == "sqlite" }

// Path returns the SQLite file path (empty for PostgreSQL).
func (d *DB) Path() string { return d.path }

// Rebind converts `?` placeholders to `$N` for PostgreSQL.
func (d *DB) Rebind(query string) string {
        if d.driver != "postgres" {
                return query
        }
        var b strings.Builder
        n := 0
        for _, r := range query {
                if r == '?' {
                        n++
                        b.WriteString(fmt.Sprintf("$%d", n))
                } else {
                        b.WriteRune(r)
                }
        }
        return b.String()
}

func (d *DB) nowExpr() string {
        if d.IsSQLite() {
                return "CURRENT_TIMESTAMP"
        }
        return "NOW()"
}
