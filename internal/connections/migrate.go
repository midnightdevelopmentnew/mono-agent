package connections

import (
	"context"
	"database/sql"
	"fmt"
	"os"
)

// plaintextDataQuery finds connections rows whose data column has not been
// through secrets.EncryptBlob yet. The "vaultenc:v1:" literal must match
// internal/secrets/blob.go's blobPrefix constant.
const plaintextDataQuery = `SELECT COUNT(*) FROM connections WHERE data NOT LIKE 'vaultenc:v1:%'`

// EncryptPlaintextConnections re-encrypts any connections rows whose data
// column isn't yet wrapped by the vault (see secrets.EncryptBlob), by
// re-saving each one through Store.Save, which always encrypts on write.
// This is the shared implementation behind `monoagentcli secret
// encrypt-connections` and the automatic startup checks in the CLI and GUI:
// it first runs a single cheap COUNT query, and only does the full
// profile-enumeration + re-save loop when that count is > 0 — a near-zero
// cost no-op once everything is migrated, and self-healing if a plaintext
// row is ever reintroduced (e.g. a future import feature).
//
// Per-row Save failures are logged to stderr and skipped rather than
// aborting the rest of the batch.
func EncryptPlaintextConnections(ctx context.Context, db *sql.DB) (migrated, total int, err error) {
	store := NewStore(db)
	if err := store.EnsureTable(ctx); err != nil {
		return 0, 0, fmt.Errorf("connections.EncryptPlaintextConnections: ensuring table: %w", err)
	}

	var plaintextCount int
	if err := db.QueryRowContext(ctx, plaintextDataQuery).Scan(&plaintextCount); err != nil {
		return 0, 0, fmt.Errorf("connections.EncryptPlaintextConnections: counting plaintext rows: %w", err)
	}
	if plaintextCount == 0 {
		return 0, 0, nil
	}

	// Store.ListAll filters to an exact profile_id match, so there is no
	// single call that returns connections across every profile — find the
	// distinct profile IDs first and migrate each in turn.
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT COALESCE(profile_id,'default') FROM connections`)
	if err != nil {
		return 0, 0, fmt.Errorf("connections.EncryptPlaintextConnections: listing connection profiles: %w", err)
	}
	var profileIDs []string
	for rows.Next() {
		var profileID string
		if err := rows.Scan(&profileID); err != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("connections.EncryptPlaintextConnections: scanning profile id: %w", err)
		}
		profileIDs = append(profileIDs, profileID)
	}
	if err := rows.Close(); err != nil {
		return 0, 0, fmt.Errorf("connections.EncryptPlaintextConnections: listing connection profiles: %w", err)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("connections.EncryptPlaintextConnections: listing connection profiles: %w", err)
	}

	var conns []Connection
	for _, profileID := range profileIDs {
		profileConns, err := store.ListAll(ctx, profileID)
		if err != nil {
			return 0, 0, fmt.Errorf("connections.EncryptPlaintextConnections: listing connections for profile %q: %w", profileID, err)
		}
		conns = append(conns, profileConns...)
	}

	for i := range conns {
		// Guard against a concurrent RefreshToken (CLI, daemon, or GUI, any of
		// which may share this DB) rotating this connection's tokens between
		// our snapshot read above and this Save: without the same refresh
		// lock RefreshToken itself uses, our Save could overwrite a
		// freshly-rotated access_token/refresh_token with the stale
		// pre-refresh values, permanently breaking providers that rotate
		// single-use refresh tokens.
		acquired, lockErr := store.acquireRefreshLock(ctx, conns[i].ID)
		if lockErr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to acquire refresh lock for connection %s: %v\n", conns[i].ID, lockErr)
			continue
		}
		if !acquired {
			fmt.Fprintf(os.Stderr, "warning: skipping connection %s: refresh lock held by another process; will retry next startup\n", conns[i].ID)
			continue
		}
		// The lock only serializes against another RefreshToken's write, not
		// against a read that already happened before we got here: our
		// pre-loop ListAll snapshot could be stale by the time we reach this
		// row, e.g. a RefreshToken in another process ran (and released the
		// lock) between our snapshot and our acquireRefreshLock call above.
		// Mirror Store.RefreshToken's own pattern (storage.go) and re-read
		// fresh from the DB now that we hold the lock, so we always re-save
		// whatever is currently on disk rather than what we saw earlier.
		fresh, getErr := store.Get(ctx, conns[i].ID)
		if getErr != nil {
			store.releaseRefreshLock(context.WithoutCancel(ctx), conns[i].ID)
			fmt.Fprintf(os.Stderr, "warning: failed to re-read connection %s: %v\n", conns[i].ID, getErr)
			continue
		}
		if fresh == nil {
			// Deleted concurrently — nothing left to migrate.
			store.releaseRefreshLock(context.WithoutCancel(ctx), conns[i].ID)
			continue
		}
		err := store.Save(ctx, fresh)
		store.releaseRefreshLock(context.WithoutCancel(ctx), conns[i].ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to re-encrypt connection %s: %v\n", conns[i].ID, err)
			continue
		}
		migrated++
	}
	return migrated, len(conns), nil
}
