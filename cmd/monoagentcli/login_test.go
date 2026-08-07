package main

import (
	"context"
	"testing"

	"monoagent/internal/secrets"
	"monoagent/internal/storage"

	"github.com/zalando/go-keyring"
)

func newLoginTestDB(t *testing.T) *storage.Database {
	t.Helper()
	keyring.MockInit()
	db, err := storage.NewDatabase(t.TempDir() + "/login-test.db")
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	t.Cleanup(func() { db.DB.Close() })
	return db
}

func TestUpsertSessionRow_CreatesThenUpdatesInPlace(t *testing.T) {
	db := newLoginTestDB(t)
	ctx := context.Background()

	firstCookies := []byte(`[{"name":"sid","value":"abc"}]`)
	if err := upsertSessionRow(ctx, db.DB, "default", "instagram", "alice", firstCookies); err != nil {
		t.Fatalf("first upsertSessionRow: %v", err)
	}

	var vaultRef string
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*), vault_ref FROM crawler_sessions WHERE platform = 'instagram' AND username = 'alice'`).Scan(&count, &vaultRef); err != nil {
		t.Fatalf("reading session row: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one session row, got %d", count)
	}
	if vaultRef == "" {
		t.Fatal("expected vault_ref to be populated")
	}

	resolved, _, err := secrets.DecryptFields(ctx, db.DB, "default", vaultRef)
	if err != nil {
		t.Fatalf("DecryptFields: %v", err)
	}
	if resolved["cookies"] != string(firstCookies) {
		t.Fatalf("unexpected cookies field: %q", resolved["cookies"])
	}

	// Second call for the same platform+username must update in place, not
	// create a second row or a second vault entry.
	secondCookies := []byte(`[{"name":"sid","value":"xyz"}]`)
	if err := upsertSessionRow(ctx, db.DB, "default", "instagram", "alice", secondCookies); err != nil {
		t.Fatalf("second upsertSessionRow: %v", err)
	}
	var vaultRef2 string
	if err := db.DB.QueryRow(`SELECT COUNT(*), vault_ref FROM crawler_sessions WHERE platform = 'instagram' AND username = 'alice'`).Scan(&count, &vaultRef2); err != nil {
		t.Fatalf("reading session row after update: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected still exactly one session row, got %d", count)
	}
	if vaultRef2 != vaultRef {
		t.Fatalf("expected the same vault entry to be reused, got %q want %q", vaultRef2, vaultRef)
	}
	resolved, _, err = secrets.DecryptFields(ctx, db.DB, "default", vaultRef2)
	if err != nil {
		t.Fatalf("DecryptFields after update: %v", err)
	}
	if resolved["cookies"] != string(secondCookies) {
		t.Fatalf("expected updated cookies, got %q", resolved["cookies"])
	}
}
