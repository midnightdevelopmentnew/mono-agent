package secrets

import (
	"context"
	"path/filepath"
	"testing"

	"monoagent/internal/storage"

	"github.com/zalando/go-keyring"
)

func newDEKTestDB(t *testing.T) *storage.Database {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "dek-test.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	t.Cleanup(func() { db.DB.Close() })
	return db
}

func TestGetOrCreateDEK_PersistsAcrossCalls(t *testing.T) {
	keyring.MockInit()
	db := newDEKTestDB(t)
	ctx := context.Background()

	dek1, err := getOrCreateDEK(ctx, db.DB)
	if err != nil {
		t.Fatalf("getOrCreateDEK (first call): %v", err)
	}
	if len(dek1) != 32 {
		t.Fatalf("expected 32-byte DEK, got %d bytes", len(dek1))
	}

	dek2, err := getOrCreateDEK(ctx, db.DB)
	if err != nil {
		t.Fatalf("getOrCreateDEK (second call): %v", err)
	}
	if string(dek1) != string(dek2) {
		t.Fatal("second call must return the same DEK, not regenerate")
	}

	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM vault_keys`).Scan(&count); err != nil {
		t.Fatalf("counting vault_keys rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 vault_keys row, got %d", count)
	}
}

// TestGetOrCreateDEK_DifferentDBsGetIndependentDEKs verifies the per-*sql.DB
// cache doesn't leak a DEK across distinct databases: two separate temp
// SQLite files, both sharing the same (mocked, process-wide) KEK, must each
// get their own independently-generated DEK.
func TestGetOrCreateDEK_DifferentDBsGetIndependentDEKs(t *testing.T) {
	keyring.MockInit()
	ctx := context.Background()

	dbA := newDEKTestDB(t)
	dbB := newDEKTestDB(t)

	dekA, err := getOrCreateDEK(ctx, dbA.DB)
	if err != nil {
		t.Fatalf("getOrCreateDEK (db A): %v", err)
	}
	dekB, err := getOrCreateDEK(ctx, dbB.DB)
	if err != nil {
		t.Fatalf("getOrCreateDEK (db B): %v", err)
	}
	if string(dekA) == string(dekB) {
		t.Fatal("expected distinct DEKs for distinct databases, got the same key")
	}

	// Confirm the cache is stable per-db across repeated calls too.
	dekA2, err := getOrCreateDEK(ctx, dbA.DB)
	if err != nil {
		t.Fatalf("getOrCreateDEK (db A, second call): %v", err)
	}
	if string(dekA) != string(dekA2) {
		t.Fatal("db A's second call must return the same cached DEK")
	}
}
