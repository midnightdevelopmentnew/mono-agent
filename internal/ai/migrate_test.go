package ai

import (
	"context"
	"testing"
)

// TestMigrateProvidersToVault_NoOpOnFreshDBWithoutAIProvidersTable simulates
// the very first-ever startup: nothing has called NewAIStore (or anything
// else that runs initTables) yet, so ai_providers doesn't exist at all. The
// migration must ensure the table (and its vault_ref column) exist itself
// rather than erroring on a missing table.
func TestMigrateProvidersToVault_NoOpOnFreshDBWithoutAIProvidersTable(t *testing.T) {
	db := openTestDB(t)

	migrated, total, err := MigrateProvidersToVault(context.Background(), db)
	if err != nil {
		t.Fatalf("MigrateProvidersToVault on a DB with no ai_providers table: %v", err)
	}
	if migrated != 0 || total != 0 {
		t.Fatalf("expected a clean no-op (0, 0), got migrated=%d total=%d", migrated, total)
	}
}

func TestMigrateProvidersToVault_NoOpWhenNoLegacyRows(t *testing.T) {
	db := openTestDB(t)
	if _, err := NewAIStore(db); err != nil {
		t.Fatalf("NewAIStore: %v", err)
	}

	migrated, total, err := MigrateProvidersToVault(context.Background(), db)
	if err != nil {
		t.Fatalf("MigrateProvidersToVault: %v", err)
	}
	if migrated != 0 || total != 0 {
		t.Fatalf("expected a no-op (0, 0), got migrated=%d total=%d", migrated, total)
	}
}

func TestMigrateProvidersToVault_MigratesLegacyRow(t *testing.T) {
	db := openTestDB(t)
	store, err := NewAIStore(db)
	if err != nil {
		t.Fatalf("NewAIStore: %v", err)
	}
	// Seed a pre-migration row directly, bypassing SaveProvider (which
	// already routes through the vault after Task 8) — this simulates a
	// provider saved before this feature shipped, with its credential
	// still sitting in the legacy column.
	legacyValue := "PLACEHOLDER-legacy"
	insertCols := "id, name, provider_id, tier, api_key, base_url, default_model, extra_headers, status, last_tested, profile_id, vault_ref, created_at"
	insertSQL := "INSERT INTO ai_providers (" + insertCols + ") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
	_, err = db.Exec(insertSQL, "p1", "My OpenAI", "openai", "known", legacyValue, "", "", "", "untested", "", "default", "", "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("seeding legacy provider: %v", err)
	}

	migrated, total, err := MigrateProvidersToVault(context.Background(), db)
	if err != nil {
		t.Fatalf("MigrateProvidersToVault: %v", err)
	}
	if migrated != 1 || total != 1 {
		t.Fatalf("expected migrated=1 total=1, got migrated=%d total=%d", migrated, total)
	}

	var rawColumnValue string
	if err := db.QueryRow(`SELECT api_key FROM ai_providers WHERE id = 'p1'`).Scan(&rawColumnValue); err != nil {
		t.Fatalf("reading migrated row: %v", err)
	}
	if rawColumnValue != "" {
		t.Fatalf("expected the legacy column to be cleared after migration, got %q", rawColumnValue)
	}

	got, err := store.GetProvider("p1", "default")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if got.APIKey != legacyValue {
		t.Fatalf("expected the migrated credential to resolve via the vault, got %q", got.APIKey)
	}

	migrated2, total2, err := MigrateProvidersToVault(context.Background(), db)
	if err != nil {
		t.Fatalf("second MigrateProvidersToVault: %v", err)
	}
	if migrated2 != 0 || total2 != 0 {
		t.Fatalf("expected second run to no-op, got migrated=%d total=%d", migrated2, total2)
	}
}
