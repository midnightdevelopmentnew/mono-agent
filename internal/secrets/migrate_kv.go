package secrets

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
)

// MigrateFieldsToKV re-encrypts any vault_secrets row still holding the
// pre-key-value single-string ciphertext (kv = 0) as a JSON
// {"secret": "<old value>"} blob instead, so DecryptFields can read every
// row uniformly. Mirrors connections.MigrateConnectionsToVault: a single
// cheap COUNT query first, a near-zero-cost no-op once everything is
// migrated, and self-healing if an unmigrated row is ever reintroduced.
// Applies uniformly to "secret"- and "login"-kind rows alike. A per-row
// failure is logged to stderr and skipped, not fatal to the batch.
func MigrateFieldsToKV(ctx context.Context, db *sql.DB) (migrated, total int, err error) {
	var unmigratedCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vault_secrets WHERE kv = 0`).Scan(&unmigratedCount); err != nil {
		return 0, 0, fmt.Errorf("secrets.MigrateFieldsToKV: counting unmigrated rows: %w", err)
	}
	if unmigratedCount == 0 {
		return 0, 0, nil
	}

	rows, err := db.QueryContext(ctx, `SELECT id, ciphertext, nonce FROM vault_secrets WHERE kv = 0`)
	if err != nil {
		return 0, 0, fmt.Errorf("secrets.MigrateFieldsToKV: listing unmigrated rows: %w", err)
	}
	type legacyRow struct {
		id         string
		ciphertext []byte
		nonce      []byte
	}
	var toMigrate []legacyRow
	for rows.Next() {
		var r legacyRow
		if err := rows.Scan(&r.id, &r.ciphertext, &r.nonce); err != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("secrets.MigrateFieldsToKV: scanning row: %w", err)
		}
		toMigrate = append(toMigrate, r)
	}
	if err := rows.Close(); err != nil {
		return 0, 0, fmt.Errorf("secrets.MigrateFieldsToKV: %w", err)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("secrets.MigrateFieldsToKV: %w", err)
	}

	dek, err := getOrCreateDEK(ctx, db)
	if err != nil {
		return 0, 0, fmt.Errorf("secrets.MigrateFieldsToKV: %w", err)
	}

	for _, r := range toMigrate {
		plaintext, err := Decrypt(dek, r.ciphertext, r.nonce)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping kv migration of %s: decrypt: %v\n", r.id, err)
			continue
		}
		fieldsJSON, err := json.Marshal(map[string]string{"secret": string(plaintext)})
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping kv migration of %s: marshal: %v\n", r.id, err)
			continue
		}
		newCiphertext, newNonce, err := Encrypt(dek, fieldsJSON)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping kv migration of %s: encrypt: %v\n", r.id, err)
			continue
		}
		if _, err := db.ExecContext(ctx,
			`UPDATE vault_secrets SET ciphertext = ?, nonce = ?, kv = 1, field_count = 1 WHERE id = ?`,
			newCiphertext, newNonce, r.id,
		); err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping kv migration of %s: update: %v\n", r.id, err)
			continue
		}
		migrated++
	}
	return migrated, len(toMigrate), nil
}
