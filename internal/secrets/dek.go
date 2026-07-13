package secrets

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// dekCache memoizes getOrCreateDEK per *sql.DB: unlike the KEK, a process
// may legitimately hold several distinct DBs open at once (e.g. a test suite
// opening multiple in-memory SQLite databases), each with its own DEK, so
// the cache is keyed by the *sql.DB pointer rather than shared process-wide.
var (
	dekCacheMu sync.Mutex
	dekCache   = map[*sql.DB][]byte{}
)

// getOrCreateDEK returns the unwrapped 32-byte Data Encryption Key, reading
// and unwrapping the singleton vault_keys row if present, or generating a
// new DEK (wrapped under the KEK from the OS keychain) and persisting it
// if this is the first use. The result is cached per db so repeated calls
// within a process skip the keychain/table round trip.
func getOrCreateDEK(ctx context.Context, db *sql.DB) ([]byte, error) {
	dekCacheMu.Lock()
	if cached, ok := dekCache[db]; ok {
		dekCacheMu.Unlock()
		return cached, nil
	}
	dekCacheMu.Unlock()

	dek, err := fetchOrCreateDEK(ctx, db)
	if err != nil {
		return nil, err
	}

	dekCacheMu.Lock()
	dekCache[db] = dek
	dekCacheMu.Unlock()
	return dek, nil
}

func fetchOrCreateDEK(ctx context.Context, db *sql.DB) ([]byte, error) {
	kek, err := getOrCreateKEK()
	if err != nil {
		return nil, fmt.Errorf("secrets: getOrCreateDEK: %w", err)
	}

	var wrappedDEK, wrappedNonce []byte
	err = db.QueryRowContext(ctx, `SELECT wrapped_dek, wrapped_nonce FROM vault_keys WHERE id = 1`).
		Scan(&wrappedDEK, &wrappedNonce)
	if err == sql.ErrNoRows {
		return createDEK(ctx, db, kek)
	}
	if err != nil {
		return nil, fmt.Errorf("secrets: reading vault_keys: %w", err)
	}

	dek, err := Decrypt(kek, wrappedDEK, wrappedNonce)
	if err != nil {
		return nil, fmt.Errorf("secrets: unwrapping DEK: %w", err)
	}
	return dek, nil
}

func createDEK(ctx context.Context, db *sql.DB, kek []byte) ([]byte, error) {
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		return nil, fmt.Errorf("secrets: generating DEK: %w", err)
	}
	wrappedDEK, wrappedNonce, err := Encrypt(kek, dek)
	if err != nil {
		return nil, fmt.Errorf("secrets: wrapping DEK: %w", err)
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO vault_keys (id, wrapped_dek, wrapped_nonce, created_at) VALUES (1, ?, ?, ?)`,
		wrappedDEK, wrappedNonce, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("secrets: storing wrapped DEK: %w", err)
	}
	return dek, nil
}
