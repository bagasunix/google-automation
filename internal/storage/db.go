// Package storage provides SQLite persistence for the search-automation orchestrator
// using the pure-Go driver modernc.org/sqlite (no CGO required).
package storage

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps the sql.DB connection and provides data-access methods.
type DB struct {
	conn *sql.DB
}

// New opens (or creates) the SQLite database at the given path and runs
// migrations. The modernc.org/sqlite driver is registered as "sqlite".
func New(dbPath string) (*DB, error) {
	// _pragma=foreign_keys(1) enables FK enforcement; _pragma=journal_mode(WAL)
	// improves concurrency; _pragma=busy_timeout(5000) retries 5s before SQLITE_BUSY.
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", dbPath)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", dbPath, err)
	}

	// SQLite handles one writer at a time; a small pool is sufficient.
	conn.SetMaxOpenConns(4)
	conn.SetMaxIdleConns(2)
	conn.SetConnMaxIdleTime(5 * time.Minute)

	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	db := &DB{conn: conn}
	if err := db.Migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return db, nil
}

// Close releases the database connection.
func (db *DB) Close() error {
	return db.conn.Close()
}

// Conn returns the underlying *sql.DB for advanced use.
func (db *DB) Conn() *sql.DB {
	return db.conn
}
