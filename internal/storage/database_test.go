package storage

import (
	"path/filepath"
	"testing"
)

// TestApplyMigrationsFreshDatabase is a regression test: migration
// 013_oauth_credentials_per_profile.sql referenced platform_oauth_credentials
// in a SELECT guarded by "WHERE EXISTS (SELECT 1 FROM sqlite_master ...)",
// but SQLite resolves table references in a statement at prepare time
// regardless of runtime WHERE conditions — so on a completely fresh database
// (one that never had that table), ApplyMigrations failed outright with
// "no such table: platform_oauth_credentials", blocking first-time setup
// entirely.
func TestApplyMigrationsFreshDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fresh.db")

	db, err := NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	defer db.DB.Close()

	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations on a fresh database: %v", err)
	}

	var name string
	err = db.DB.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'platform_oauth_credentials'`,
	).Scan(&name)
	if err != nil {
		t.Fatalf("expected platform_oauth_credentials table to exist after migration: %v", err)
	}
}
