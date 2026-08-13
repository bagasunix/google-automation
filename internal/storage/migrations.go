package storage

import (
	_ "embed"
	"fmt"
)

//go:embed schema.sql
var schemaSQL string

// Migrate executes the embedded schema.sql to create all tables and indexes
// if they do not already exist. Safe to call on every startup.
func (db *DB) Migrate() error {
	if _, err := db.conn.Exec(schemaSQL); err != nil {
		return fmt.Errorf("exec schema: %w", err)
	}
	return nil
}
