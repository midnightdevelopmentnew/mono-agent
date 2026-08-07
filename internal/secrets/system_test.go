package secrets

import (
	"context"
	"testing"
)

func TestPutSystemEntry_CreatesThenUpdatesInPlace(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	firstToken := "PLACEHOLDER-token-one"
	id, err := PutSystemEntry(ctx, db.DB, "default", "connection", "", "GitHub — work",
		map[string]string{"access_token": firstToken}, "acct-1", "")
	if err != nil {
		t.Fatalf("PutSystemEntry create: %v", err)
	}
	if id == "" {
		t.Fatal("expected a non-empty vault id")
	}

	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Kind != "connection" || entries[0].Name != "GitHub — work" {
		t.Fatalf("unexpected entries after create: %+v", entries)
	}

	// Update in place: same id, new token, must not create a second entry
	// and must not change the name even though a different name is passed
	// implicitly by not passing one at all (PutSystemEntry never renames).
	secondToken := "PLACEHOLDER-token-two"
	id2, err := PutSystemEntry(ctx, db.DB, "default", "connection", id, "GitHub — work",
		map[string]string{"access_token": secondToken}, "acct-1", "")
	if err != nil {
		t.Fatalf("PutSystemEntry update: %v", err)
	}
	if id2 != id {
		t.Fatalf("expected update to keep the same id, got %q want %q", id2, id)
	}

	entries, err = List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List after update: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected still exactly one entry after update, got %d", len(entries))
	}

	fields, _, err := DecryptFields(ctx, db.DB, "default", id)
	if err != nil {
		t.Fatalf("DecryptFields: %v", err)
	}
	if fields["access_token"] != secondToken {
		t.Fatalf("expected updated token, got %q", fields["access_token"])
	}
}

func TestPutSystemEntry_DisambiguatesNameCollision(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	tokenA := "PLACEHOLDER-a"
	id1, err := PutSystemEntry(ctx, db.DB, "default", "connection", "", "GitHub", map[string]string{"access_token": tokenA}, "", "")
	if err != nil {
		t.Fatalf("first PutSystemEntry: %v", err)
	}
	tokenB := "PLACEHOLDER-b"
	id2, err := PutSystemEntry(ctx, db.DB, "default", "connection", "", "GitHub", map[string]string{"access_token": tokenB}, "", "")
	if err != nil {
		t.Fatalf("second PutSystemEntry: %v", err)
	}
	if id1 == id2 {
		t.Fatal("expected two distinct entries for the name collision")
	}

	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = true
	}
	if !names["GitHub"] || !names["GitHub (2)"] {
		t.Fatalf("expected names {GitHub, GitHub (2)}, got %+v", names)
	}
}

func TestDeleteCascade_RemovesLinkedConnectionRow(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	// crawler_sessions is created by the real migrations newSecretsTestDB
	// applies; connections/ai_providers are not (see Task 1's note), so this
	// test creates a minimal connections table by hand — mirroring how
	// internal/connections/storage_test.go hand-creates vault_keys instead
	// of importing internal/secrets.
	if _, err := db.DB.Exec(`CREATE TABLE connections (
		id TEXT PRIMARY KEY, platform TEXT, method TEXT, label TEXT, account_id TEXT,
		data TEXT, status TEXT, last_tested TEXT, profile_id TEXT, vault_ref TEXT, created_at TEXT, updated_at TEXT
	)`); err != nil {
		t.Fatalf("creating connections table: %v", err)
	}

	tokenA := "PLACEHOLDER-a"
	vaultID, err := PutSystemEntry(ctx, db.DB, "default", "connection", "", "GitHub", map[string]string{"access_token": tokenA}, "", "")
	if err != nil {
		t.Fatalf("PutSystemEntry: %v", err)
	}
	if _, err := db.DB.Exec(`INSERT INTO connections (id, platform, profile_id, vault_ref) VALUES ('conn-1', 'github', 'default', ?)`, vaultID); err != nil {
		t.Fatalf("seeding connections row: %v", err)
	}

	if err := DeleteCascade(ctx, db.DB, "default", vaultID); err != nil {
		t.Fatalf("DeleteCascade: %v", err)
	}

	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected the vault entry to be gone, got %+v", entries)
	}
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM connections WHERE id = 'conn-1'`).Scan(&count); err != nil {
		t.Fatalf("counting connections: %v", err)
	}
	if count != 0 {
		t.Fatal("expected the linked connections row to be deleted too")
	}
}

func TestDeleteCascade_PlainSecretDegradesToDelete(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	val := "PLACEHOLDER-x"
	id, err := Add(ctx, db.DB, "default", "secret", "openai-key", map[string]string{"secret": val}, "", "", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := DeleteCascade(ctx, db.DB, "default", id); err != nil {
		t.Fatalf("DeleteCascade: %v", err)
	}
	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Fatal("expected the secret entry to be deleted")
	}
}
