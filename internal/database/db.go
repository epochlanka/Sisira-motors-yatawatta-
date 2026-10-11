// Package database opens the SQLite database, runs migrations and seeds defaults.
package database

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registers "sqlite"
)

// Open opens (creating if needed) the SQLite database with foreign keys enforced,
// WAL journaling and IMMEDIATE write transactions to avoid upgrade deadlocks.
func Open(path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	var fk int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	if fk != 1 {
		db.Close()
		return nil, fmt.Errorf("foreign keys could not be enabled")
	}
	return db, nil
}

// Migrate applies any migrations that have not been applied yet. Each migration
// runs in its own transaction. Existing data is never dropped.
func Migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	for i, m := range migrations {
		version := i + 1
		var n int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", version).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, m); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES (?, strftime('%Y-%m-%dT%H:%M:%SZ','now'))", version); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %d commit: %w", version, err)
		}
	}
	return nil
}
