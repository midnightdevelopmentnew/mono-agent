package ai

import (
	"context"
	"database/sql"
	"fmt"
	"os"
)

// MigrateProvidersToVault backfills vault_ref for any ai_providers row
// still carrying a credential in its legacy column (from before this
// feature shipped) by re-saving it through AIStore.SaveProvider, which (as
// of SaveProvider's vault integration) writes the credential to the vault
// and clears that column. Mirrors connections.MigrateConnectionsToVault's
// shape: a cheap COUNT-first guard, per-row failures logged to stderr and
// skipped rather than aborting the batch, idempotent.
func MigrateProvidersToVault(ctx context.Context, db *sql.DB) (migrated, total int, err error) {
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_providers WHERE api_key != ''`).Scan(&count); err != nil {
		return 0, 0, fmt.Errorf("ai.MigrateProvidersToVault: counting legacy rows: %w", err)
	}
	if count == 0 {
		return 0, 0, nil
	}

	store := &AIStore{db: db}
	rows, err := db.QueryContext(ctx, `SELECT id, COALESCE(profile_id,'default') FROM ai_providers WHERE api_key != ''`)
	if err != nil {
		return 0, 0, fmt.Errorf("ai.MigrateProvidersToVault: listing legacy rows: %w", err)
	}
	type idProfile struct{ id, profileID string }
	var toMigrate []idProfile
	for rows.Next() {
		var r idProfile
		if err := rows.Scan(&r.id, &r.profileID); err != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("ai.MigrateProvidersToVault: scanning row: %w", err)
		}
		toMigrate = append(toMigrate, r)
	}
	if err := rows.Close(); err != nil {
		return 0, 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	for _, r := range toMigrate {
		// Read the still-legacy row directly (GetProvider would try to
		// resolve the credential from the vault via vault_ref, which is
		// empty for these rows — the legacy column is the only place it
		// lives until this loop migrates it).
		var p AIProvider
		selectErr := db.QueryRowContext(ctx, `SELECT id, name, provider_id, tier, api_key, base_url, default_model, extra_headers, status, last_tested, profile_id, created_at
			FROM ai_providers WHERE id = ?`, r.id).Scan(
			&p.ID, &p.Name, &p.ProviderID, &p.Tier, &p.APIKey,
			&p.BaseURL, &p.DefaultModel, &p.ExtraHeaders,
			&p.Status, &p.LastTested, &p.ProfileID, &p.CreatedAt,
		)
		if selectErr != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping provider %s migration: %v\n", r.id, selectErr)
			continue
		}
		if saveErr := store.SaveProvider(p); saveErr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to migrate provider %s to vault: %v\n", r.id, saveErr)
			continue
		}
		migrated++
	}
	return migrated, len(toMigrate), nil
}
