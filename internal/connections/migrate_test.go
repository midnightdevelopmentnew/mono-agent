package connections

import (
	"context"
	"strings"
	"testing"
)

// TestEncryptPlaintextConnections_NoOpWhenAlreadyEncrypted verifies the cheap
// COUNT check short-circuits the whole migration when every row already
// carries the vaultenc:v1: prefix (e.g. because it was written through
// Store.Save, which always encrypts).
func TestEncryptPlaintextConnections_NoOpWhenAlreadyEncrypted(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	store := NewStore(db)

	if err := store.Save(ctx, &Connection{ID: "conn-1", Platform: "x", Method: "oauth", Label: "Test",
		Data: map[string]interface{}{"access_token": "already-encrypted"}}); err != nil {
		t.Fatalf("seeding encrypted connection: %v", err)
	}

	migrated, total, err := EncryptPlaintextConnections(ctx, db)
	if err != nil {
		t.Fatalf("EncryptPlaintextConnections: %v", err)
	}
	if migrated != 0 || total != 0 {
		t.Fatalf("expected a no-op (0, 0), got migrated=%d total=%d", migrated, total)
	}
}

// TestEncryptPlaintextConnections_MigratesPlaintextRows verifies rows
// inserted with raw plaintext JSON (as connections created before the vault
// feature shipped would have) get re-encrypted.
func TestEncryptPlaintextConnections_MigratesPlaintextRows(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := NewStore(db).EnsureTable(ctx); err != nil {
		t.Fatalf("EnsureTable: %v", err)
	}
	_, err := db.Exec(`
		INSERT INTO connections (id, platform, method, label, account_id, data, status, last_tested, profile_id, created_at, updated_at)
		VALUES ('conn-1', 'x', 'oauth', 'Test', '', '{"access_token":"plaintext-token"}', 'active', '', 'default', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("seeding plaintext connection: %v", err)
	}

	migrated, total, err := EncryptPlaintextConnections(ctx, db)
	if err != nil {
		t.Fatalf("EncryptPlaintextConnections: %v", err)
	}
	if migrated != 1 || total != 1 {
		t.Fatalf("expected migrated=1 total=1, got migrated=%d total=%d", migrated, total)
	}

	var rawData string
	if err := db.QueryRow(`SELECT data FROM connections WHERE id = 'conn-1'`).Scan(&rawData); err != nil {
		t.Fatalf("reading migrated row: %v", err)
	}
	if strings.Contains(rawData, "plaintext-token") {
		t.Fatal("connections.data must not contain plaintext after migration")
	}
	if !strings.HasPrefix(rawData, "vaultenc:v1:") {
		t.Fatalf("expected vaultenc-prefixed ciphertext, got: %s", rawData)
	}
}

// TestEncryptPlaintextConnections_ContinuesPastPerRowFailure verifies that a
// Save failure on one row doesn't abort the rest of the batch. A trigger
// simulates a row whose UPDATE (the path Save's INSERT...ON CONFLICT takes
// for a pre-existing id) always fails.
func TestEncryptPlaintextConnections_ContinuesPastPerRowFailure(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := NewStore(db).EnsureTable(ctx); err != nil {
		t.Fatalf("EnsureTable: %v", err)
	}
	_, err := db.Exec(`
		INSERT INTO connections (id, platform, method, label, account_id, data, status, last_tested, profile_id, created_at, updated_at)
		VALUES
		('bad-row', 'x', 'oauth', 'Bad', '', '{"access_token":"bad-token"}', 'active', '', 'default', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
		('good-row', 'x', 'oauth', 'Good', '', '{"access_token":"good-token"}', 'active', '', 'default', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("seeding plaintext connections: %v", err)
	}
	_, err = db.Exec(`
		CREATE TRIGGER fail_bad_row BEFORE UPDATE ON connections
		WHEN NEW.id = 'bad-row'
		BEGIN
			SELECT RAISE(FAIL, 'simulated failure');
		END;`)
	if err != nil {
		t.Fatalf("creating failure trigger: %v", err)
	}

	migrated, total, err := EncryptPlaintextConnections(ctx, db)
	if err != nil {
		t.Fatalf("EncryptPlaintextConnections should not abort on a per-row failure: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected total=2, got %d", total)
	}
	if migrated != 1 {
		t.Fatalf("expected migrated=1 (good-row only), got %d", migrated)
	}

	var badData, goodData string
	if err := db.QueryRow(`SELECT data FROM connections WHERE id = 'bad-row'`).Scan(&badData); err != nil {
		t.Fatalf("reading bad-row: %v", err)
	}
	if !strings.Contains(badData, "bad-token") {
		t.Fatal("bad-row should remain unmigrated (plaintext) since its Save failed")
	}
	if err := db.QueryRow(`SELECT data FROM connections WHERE id = 'good-row'`).Scan(&goodData); err != nil {
		t.Fatalf("reading good-row: %v", err)
	}
	if !strings.HasPrefix(goodData, "vaultenc:v1:") {
		t.Fatalf("expected good-row to be migrated, got: %s", goodData)
	}
}
