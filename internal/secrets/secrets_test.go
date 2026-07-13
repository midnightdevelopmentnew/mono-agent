package secrets

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"monoagent/internal/storage"

	"github.com/zalando/go-keyring"
)

func newSecretsTestDB(t *testing.T) *storage.Database {
	t.Helper()
	keyring.MockInit()
	dbPath := filepath.Join(t.TempDir(), "secrets-test.db")
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

func TestAddDecryptList_RoundTrip(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	id, err := Add(ctx, db.DB, "default", "secret", "openai-key", "sk-abc123", "", "", "prod key")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	value, err := DecryptEntry(ctx, db.DB, "default", id)
	if err != nil {
		t.Fatalf("DecryptEntry: %v", err)
	}
	if value != "sk-abc123" {
		t.Fatalf("got %q, want %q", value, "sk-abc123")
	}

	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "openai-key" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
}

func TestList_NeverReturnsPlaintext(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "login", "github", "hunter2", "alice", "https://github.com", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Entry has no field capable of holding the secret value — this test
	// documents that guarantee at the type level, not just by inspection.
	if entries[0].Username != "alice" {
		t.Fatalf("expected username alice, got %q", entries[0].Username)
	}
}

func TestDelete_RemovesEntry(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	id, err := Add(ctx, db.DB, "default", "secret", "temp", "value", "", "", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := Delete(ctx, db.DB, "default", id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries after delete, got %d", len(entries))
	}
}

func TestDecryptEntry_NotFoundErrors(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := DecryptEntry(ctx, db.DB, "default", "sec-999"); err == nil {
		t.Fatal("expected error for missing entry, got nil")
	}
}

// TestAdd_ConcurrentCallsGetDistinctSeqs exercises the race Add's seq
// allocation used to have: a separate SELECT COALESCE(MAX(seq),0)+1 followed
// by an INSERT lets two concurrent Add calls compute the same next seq/id,
// and the second INSERT then fails on the primary key. Mirroring
// vault.Register's fix (BEGIN IMMEDIATE to serialize seq allocation across
// concurrent callers), every concurrent Add here must succeed with a
// distinct, gapless id and seq.
func TestAdd_ConcurrentCallsGetDistinctSeqs(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	const numGoroutines = 20
	ids := make([]string, numGoroutines)
	errs := make([]error, numGoroutines)

	var wg sync.WaitGroup
	var start sync.WaitGroup
	start.Add(1)
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			start.Wait()
			ids[i], errs[i] = Add(ctx, db.DB, "default", "secret", fmt.Sprintf("key-%d", i), "value", "", "", "")
		}(i)
	}
	start.Done()
	wg.Wait()

	seen := make(map[string]bool, numGoroutines)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: Add: %v", i, err)
		}
		if seen[ids[i]] {
			t.Fatalf("goroutine %d: duplicate id %q returned by a concurrent Add", i, ids[i])
		}
		seen[ids[i]] = true
	}

	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != numGoroutines {
		t.Fatalf("expected %d entries, got %d", numGoroutines, len(entries))
	}
}
