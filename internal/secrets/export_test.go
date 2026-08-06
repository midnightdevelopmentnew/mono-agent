package secrets

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"monoagent/internal/storage"

	"github.com/zalando/go-keyring"
)

func newExportTestDB(t *testing.T) *storage.Database {
	t.Helper()
	keyring.MockInit()
	dbPath := filepath.Join(t.TempDir(), "export-test.db")
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

func findEntryID(entries []Entry, name string) string {
	for _, e := range entries {
		if e.Name == name {
			return e.ID
		}
	}
	return ""
}

func TestGenerateExportPassword_IsNonEmptyAndVaries(t *testing.T) {
	a, err := GenerateExportPassword()
	if err != nil {
		t.Fatalf("GenerateExportPassword: %v", err)
	}
	b, err := GenerateExportPassword()
	if err != nil {
		t.Fatalf("GenerateExportPassword: %v", err)
	}
	if a == "" || b == "" {
		t.Fatal("expected non-empty generated values")
	}
	if a == b {
		t.Fatal("expected two independently generated values to differ")
	}
}

func TestExportImport_RoundTrip(t *testing.T) {
	db := newExportTestDB(t)
	ctx := context.Background()

	if _, err := Add(ctx, db.DB, "default", "secret", "e1", map[string]string{"secret": "v-alpha1"}, "", "", "note text"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := Add(ctx, db.DB, "default", "login", "e2", map[string]string{"secret": "p-one1"}, "alice", "https://example.test", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	exportPW := "pw-correct1"
	data, exported, skipped, err := Export(ctx, db.DB, "default", exportPW)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if exported != 2 || skipped != 0 {
		t.Fatalf("expected 2 exported, 0 skipped, got exported=%d skipped=%d", exported, skipped)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty export data")
	}
	if strings.Contains(string(data), "v-alpha1") || strings.Contains(string(data), "p-one1") {
		t.Fatal("export file must not contain plaintext")
	}

	db2 := newExportTestDB(t)
	imported, skipped, err := Import(context.Background(), db2.DB, "default", exportPW, data)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if imported != 2 || skipped != 0 {
		t.Fatalf("expected 2 imported, 0 skipped, got imported=%d skipped=%d", imported, skipped)
	}

	entries, err := List(context.Background(), db2.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries after import, got %d", len(entries))
	}

	fields, _, err := DecryptFields(context.Background(), db2.DB, "default", findEntryID(entries, "e1"))
	if err != nil {
		t.Fatalf("DecryptFields: %v", err)
	}
	if fields["secret"] != "v-alpha1" {
		t.Fatalf("got %q, want %q", fields["secret"], "v-alpha1")
	}
}

// TestExport_SkipsEntryThatFailsToDecrypt covers an unmigrated legacy row
// (see insertLegacyRow in migrate_kv_test.go) sitting alongside normal
// entries: its ciphertext decrypts fine as raw bytes but isn't valid JSON,
// so DecryptFields fails on it specifically. Export must skip just that
// entry — not abort the whole export — and report accurate exported/skipped
// counts, with the bad entry excluded from the resulting payload.
func TestExport_SkipsEntryThatFailsToDecrypt(t *testing.T) {
	db := newExportTestDB(t)
	ctx := context.Background()

	if _, err := Add(ctx, db.DB, "default", "secret", "good-one", map[string]string{"secret": "v-good1"}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	insertLegacyRow(t, db, "sec-legacy", "secret", "legacy-broken", "not-json-plaintext")
	if _, err := Add(ctx, db.DB, "default", "secret", "good-two", map[string]string{"secret": "v-good2"}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	data, exported, skipped, err := Export(ctx, db.DB, "default", "pw-correct1")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if exported != 2 || skipped != 1 {
		t.Fatalf("expected 2 exported, 1 skipped, got exported=%d skipped=%d", exported, skipped)
	}

	// The payload itself must only contain the two decryptable entries.
	db2 := newExportTestDB(t)
	imported, importSkipped, err := Import(context.Background(), db2.DB, "default", "pw-correct1", data)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if imported != 2 || importSkipped != 0 {
		t.Fatalf("expected 2 imported into a fresh vault, 0 skipped, got imported=%d skipped=%d", imported, importSkipped)
	}
	entries, err := List(context.Background(), db2.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries after import, got %d", len(entries))
	}
	if findEntryID(entries, "legacy-broken") != "" {
		t.Fatal("expected the undecryptable legacy entry to be excluded from the export")
	}
}

func TestImport_WrongPassphraseFails(t *testing.T) {
	db := newExportTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "e1", map[string]string{"secret": "v-one1"}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	data, _, _, err := Export(ctx, db.DB, "default", "pw-correct1")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	db2 := newExportTestDB(t)
	if _, _, err := Import(ctx, db2.DB, "default", "pw-incorrect1", data); err == nil {
		t.Fatal("expected import with an incorrect passphrase to fail")
	}
}

func TestImport_SkipsDuplicateNames(t *testing.T) {
	db := newExportTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "shared", map[string]string{"secret": "v-one1"}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	data, _, _, err := Export(ctx, db.DB, "default", "pw-correct1")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Import into the SAME vault — "shared" already exists there.
	imported, skipped, err := Import(ctx, db.DB, "default", "pw-correct1", data)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if imported != 0 || skipped != 1 {
		t.Fatalf("expected 0 imported, 1 skipped, got imported=%d skipped=%d", imported, skipped)
	}
}

func TestImport_RejectsUnrecognizedFormat(t *testing.T) {
	db := newExportTestDB(t)
	if _, _, err := Import(context.Background(), db.DB, "default", "any-passphrase", []byte(`{"format":"something-else","version":1}`)); err == nil {
		t.Fatal("expected error for an unrecognized export format, got nil")
	}
}
