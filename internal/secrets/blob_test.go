package secrets

import (
	"context"
	"path/filepath"
	"testing"

	"monoagent/internal/storage"

	"github.com/zalando/go-keyring"
)

func newBlobTestDB(t *testing.T) *storage.Database {
	t.Helper()
	keyring.MockInit()
	dbPath := filepath.Join(t.TempDir(), "blob-test.db")
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

func TestEncryptDecryptBlob_RoundTrip(t *testing.T) {
	db := newBlobTestDB(t)
	ctx := context.Background()
	plaintext := []byte(`{"access_token":"abc123"}`)

	encoded, err := EncryptBlob(ctx, db.DB, plaintext)
	if err != nil {
		t.Fatalf("EncryptBlob: %v", err)
	}
	if encoded == string(plaintext) {
		t.Fatal("encoded blob must not equal plaintext")
	}

	got, err := DecryptBlob(ctx, db.DB, encoded)
	if err != nil {
		t.Fatalf("DecryptBlob: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Fatalf("got %q, want %q", got, plaintext)
	}
}

func TestDecryptBlob_PassesThroughLegacyPlaintext(t *testing.T) {
	db := newBlobTestDB(t)
	ctx := context.Background()
	legacy := `{"access_token":"legacy-plaintext"}`

	got, err := DecryptBlob(ctx, db.DB, legacy)
	if err != nil {
		t.Fatalf("DecryptBlob on legacy plaintext: %v", err)
	}
	if string(got) != legacy {
		t.Fatalf("got %q, want unchanged %q", got, legacy)
	}
}
