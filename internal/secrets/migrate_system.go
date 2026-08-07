package secrets

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
)

// MigrateSessionsToVault backfills vault_ref for any crawler_sessions row
// still carrying its cookie jar directly in cookies_json (as
// secrets.EncryptBlob ciphertext, from before this feature) instead of as a
// linked "session"-kind vault entry. Mirrors
// connections.MigrateConnectionsToVault/MigrateFieldsToKV's shape: a cheap
// COUNT-first guard, per-row failures logged to stderr and skipped rather
// than aborting the batch, idempotent.
func MigrateSessionsToVault(ctx context.Context, db *sql.DB) (migrated, total int, err error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, username, platform, cookies_json, COALESCE(profile_id,'default') FROM crawler_sessions WHERE COALESCE(vault_ref,'') = '' AND cookies_json != ''`)
	if err != nil {
		return 0, 0, fmt.Errorf("secrets.MigrateSessionsToVault: listing unmigrated rows: %w", err)
	}
	type legacyRow struct {
		id, username, platform, cookiesJSON, profileID string
	}
	var toMigrate []legacyRow
	for rows.Next() {
		var r legacyRow
		if err := rows.Scan(&r.id, &r.username, &r.platform, &r.cookiesJSON, &r.profileID); err != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("secrets.MigrateSessionsToVault: scanning row: %w", err)
		}
		toMigrate = append(toMigrate, r)
	}
	if err := rows.Close(); err != nil {
		return 0, 0, fmt.Errorf("secrets.MigrateSessionsToVault: %w", err)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("secrets.MigrateSessionsToVault: %w", err)
	}
	if len(toMigrate) == 0 {
		return 0, 0, nil
	}

	for _, r := range toMigrate {
		plaintextCookies, decErr := DecryptBlob(ctx, db, r.cookiesJSON)
		if decErr != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping session migration for %s/%s: %v\n", r.platform, r.username, decErr)
			continue
		}
		// Validate that cookies are valid JSON; skip if not.
		var cookiesVal interface{}
		if jsonErr := json.Unmarshal(plaintextCookies, &cookiesVal); jsonErr != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping session migration for %s/%s: invalid cookies JSON: %v\n", r.platform, r.username, jsonErr)
			continue
		}
		entryName := fmt.Sprintf("%s session — %s", r.platform, r.username)
		cookieField := map[string]string{"cookies": string(plaintextCookies)}
		vaultID, putErr := PutSystemEntry(ctx, db, r.profileID, "session", "", entryName, cookieField, r.username, r.platform)
		if putErr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to migrate session %s/%s to vault: %v\n", r.platform, r.username, putErr)
			continue
		}
		if _, execErr := db.ExecContext(ctx, `UPDATE crawler_sessions SET vault_ref = ?, cookies_json = '' WHERE id = ?`, vaultID, r.id); execErr != nil {
			fmt.Fprintf(os.Stderr, "warning: migrated session %s/%s to vault but failed to update its row: %v\n", r.platform, r.username, execErr)
			continue
		}
		migrated++
	}
	return migrated, len(toMigrate), nil
}
