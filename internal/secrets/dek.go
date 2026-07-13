package secrets

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"time"
)

// getOrCreateDEK returns the unwrapped 32-byte Data Encryption Key, reading
// and unwrapping the singleton vault_keys row if present, or generating a
// new DEK (wrapped under the KEK from the OS keychain) and persisting it
// if this is the first use.
func getOrCreateDEK(ctx context.Context, db *sql.DB) ([]byte, error) {
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
