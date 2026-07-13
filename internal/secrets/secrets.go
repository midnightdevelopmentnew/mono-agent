package secrets

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Entry is the credential-free projection of a vault_secrets row — safe to
// list, log, or serialize as --json output. It never carries the secret
// value; only DecryptEntry does, and only when explicitly called.
type Entry struct {
	ID        string `json:"id"`
	ProfileID string `json:"profile_id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Username  string `json:"username,omitempty"`
	URL       string `json:"url,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// Add creates a new vault_secrets entry, encrypting value (and notes, if
// given) under the vault's DEK before storage.
func Add(ctx context.Context, db *sql.DB, profileID, kind, name, value, username, url, notes string) (string, error) {
	dek, err := getOrCreateDEK(ctx, db)
	if err != nil {
		return "", fmt.Errorf("secrets.Add: %w", err)
	}
	ciphertext, nonce, err := Encrypt(dek, []byte(value))
	if err != nil {
		return "", fmt.Errorf("secrets.Add: encrypting value: %w", err)
	}

	var notesCiphertext, notesNonce []byte
	if notes != "" {
		notesCiphertext, notesNonce, err = Encrypt(dek, []byte(notes))
		if err != nil {
			return "", fmt.Errorf("secrets.Add: encrypting notes: %w", err)
		}
	}

	var seq int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM vault_secrets`).Scan(&seq); err != nil {
		return "", fmt.Errorf("secrets.Add: next seq: %w", err)
	}
	id := fmt.Sprintf("sec-%03d", seq)
	now := time.Now().UTC().Format(time.RFC3339)

	_, err = db.ExecContext(ctx, `
		INSERT INTO vault_secrets (id, seq, profile_id, kind, name, username, url, ciphertext, nonce, notes_ciphertext, notes_nonce, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, seq, profileID, kind, name, nullStr(username), nullStr(url), ciphertext, nonce, notesCiphertext, notesNonce, now, now,
	)
	if err != nil {
		return "", fmt.Errorf("secrets.Add: insert: %w", err)
	}
	return id, nil
}

// DecryptEntry returns the plaintext secret value for id. This is the only
// function in the package that returns plaintext, and it is called
// exclusively by the CLI's `secret reveal --reveal` command and the Wails
// RevealSecret method.
func DecryptEntry(ctx context.Context, db *sql.DB, profileID, id string) (string, error) {
	dek, err := getOrCreateDEK(ctx, db)
	if err != nil {
		return "", fmt.Errorf("secrets.DecryptEntry: %w", err)
	}
	var ciphertext, nonce []byte
	err = db.QueryRowContext(ctx,
		`SELECT ciphertext, nonce FROM vault_secrets WHERE id = ? AND profile_id = ?`, id, profileID,
	).Scan(&ciphertext, &nonce)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("secrets.DecryptEntry: entry %q not found", id)
	}
	if err != nil {
		return "", fmt.Errorf("secrets.DecryptEntry: %w", err)
	}
	plaintext, err := Decrypt(dek, ciphertext, nonce)
	if err != nil {
		return "", fmt.Errorf("secrets.DecryptEntry: %w", err)
	}
	return string(plaintext), nil
}

// List returns metadata for every entry under profileID — never decrypts.
func List(ctx context.Context, db *sql.DB, profileID string) ([]Entry, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, profile_id, kind, name, COALESCE(username,''), COALESCE(url,''), created_at, updated_at
		FROM vault_secrets WHERE profile_id = ? ORDER BY seq DESC`, profileID)
	if err != nil {
		return nil, fmt.Errorf("secrets.List: %w", err)
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.ProfileID, &e.Kind, &e.Name, &e.Username, &e.URL, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, fmt.Errorf("secrets.List: scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Delete removes an entry.
func Delete(ctx context.Context, db *sql.DB, profileID, id string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM vault_secrets WHERE id = ? AND profile_id = ?`, id, profileID)
	if err != nil {
		return fmt.Errorf("secrets.Delete: %w", err)
	}
	return nil
}

func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
