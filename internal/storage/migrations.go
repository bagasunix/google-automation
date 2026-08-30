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
	if err := db.addColumnIfMissing("proxies", "api_key_index", "INTEGER DEFAULT 0"); err != nil {
		return err
	}
	if err := db.addColumnIfMissing("articles", "opportunity_score", "REAL DEFAULT 0"); err != nil {
		return err
	}
	return nil
}

// addColumnIfMissing runs an ALTER TABLE ADD COLUMN for databases that were
// created before the column existed — CREATE TABLE IF NOT EXISTS in
// schema.sql only applies to brand-new tables, so pre-existing SQLite files
// need this to pick up new columns.
func (db *DB) addColumnIfMissing(table, column, def string) error {
	rows, err := db.conn.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return fmt.Errorf("pragma table_info(%s): %w", table, err)
	}
	defer rows.Close()

	var found bool
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan table_info(%s): %w", table, err)
		}
		if name == column {
			found = true
			break
		}
	}
	if found {
		return nil
	}

	if _, err := db.conn.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, def)); err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, column, err)
	}
	return nil
}
