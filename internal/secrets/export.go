package secrets

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	exportFormat  = "monoagent-vault-export"
	exportVersion = 1

	argon2Time    = 1
	argon2Memory  = 64 * 1024 // KiB
	argon2Threads = 4
	argon2KeyLen  = 32

	exportSaltSize  = 16
	exportRandBytes = 16
)

// crockfordAlphabet excludes visually ambiguous characters (I, L, O, U) so a
// human transcribing the generated passphrase by hand is less likely to
// make a mistake — the standard Crockford Base32 alphabet.
const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var crockfordEncoding = base32.NewEncoding(crockfordAlphabet).WithPadding(base32.NoPadding)

// exportEnvelope is the on-disk JSON container written by Export and read
// by Import. Binary fields are base64 via encoding/json's []byte handling.
type exportEnvelope struct {
	Format     string `json:"format"`
	Version    int    `json:"version"`
	KDF        string `json:"kdf"`
	Salt       []byte `json:"salt"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

// exportEntry is one vault entry inside the encrypted payload. id/seq are
// deliberately omitted — Import always allocates fresh ones.
type exportEntry struct {
	Kind      string            `json:"kind"`
	Name      string            `json:"name"`
	Username  string            `json:"username"`
	URL       string            `json:"url"`
	Notes     string            `json:"notes"`
	Fields    map[string]string `json:"fields"`
	CreatedAt string            `json:"created_at"`
	UpdatedAt string            `json:"updated_at"`
}

type exportPayload struct {
	ExportedAt string        `json:"exported_at"`
	ProfileID  string        `json:"profile_id"`
	Entries    []exportEntry `json:"entries"`
}

// GenerateExportPassword returns a fresh random passphrase for protecting
// one export file: 16 bytes from crypto/rand, Crockford base32-encoded
// (~26 chars, no ambiguous characters) and dash-grouped in blocks of 4 for
// readability.
func GenerateExportPassword() (string, error) {
	raw := make([]byte, exportRandBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("secrets.GenerateExportPassword: %w", err)
	}
	encoded := crockfordEncoding.EncodeToString(raw)
	var grouped strings.Builder
	for i, r := range encoded {
		if i > 0 && i%4 == 0 {
			grouped.WriteByte('-')
		}
		grouped.WriteRune(r)
	}
	return grouped.String(), nil
}

// Export builds the encrypted export payload for every entry under
// profileID, protected by passphrase. Returns the JSON bytes to write to a
// file (see exportEnvelope).
func Export(ctx context.Context, db *sql.DB, profileID, passphrase string) ([]byte, error) {
	entries, err := List(ctx, db, profileID)
	if err != nil {
		return nil, fmt.Errorf("secrets.Export: listing entries: %w", err)
	}

	payload := exportPayload{
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		ProfileID:  profileID,
	}
	for _, e := range entries {
		fields, notes, err := DecryptFields(ctx, db, profileID, e.ID)
		if err != nil {
			return nil, fmt.Errorf("secrets.Export: decrypting %q: %w", e.Name, err)
		}
		payload.Entries = append(payload.Entries, exportEntry{
			Kind: e.Kind, Name: e.Name, Username: e.Username, URL: e.URL,
			Notes: notes, Fields: fields,
			CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
		})
	}

	plaintext, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("secrets.Export: marshaling payload: %w", err)
	}

	salt := make([]byte, exportSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("secrets.Export: generating salt: %w", err)
	}
	key := argon2.IDKey([]byte(passphrase), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
	ciphertext, nonce, err := Encrypt(key, plaintext)
	if err != nil {
		return nil, fmt.Errorf("secrets.Export: encrypting: %w", err)
	}

	envelope := exportEnvelope{
		Format: exportFormat, Version: exportVersion, KDF: "argon2id",
		Salt: salt, Nonce: nonce, Ciphertext: ciphertext,
	}
	out, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("secrets.Export: marshaling envelope: %w", err)
	}
	return out, nil
}

// Import decrypts fileData (an exportEnvelope produced by Export) with
// passphrase and adds every entry to profileID, skipping any whose name
// already exists there. A per-entry failure other than a name collision is
// logged to stderr and skipped, not fatal to the batch.
func Import(ctx context.Context, db *sql.DB, profileID, passphrase string, fileData []byte) (imported, skipped int, err error) {
	var envelope exportEnvelope
	if err := json.Unmarshal(fileData, &envelope); err != nil {
		return 0, 0, fmt.Errorf("secrets.Import: not a valid vault export file: %w", err)
	}
	if envelope.Format != exportFormat {
		return 0, 0, fmt.Errorf("secrets.Import: unrecognized export format %q", envelope.Format)
	}

	key := argon2.IDKey([]byte(passphrase), envelope.Salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
	plaintext, decErr := Decrypt(key, envelope.Ciphertext, envelope.Nonce)
	if decErr != nil {
		return 0, 0, fmt.Errorf("secrets.Import: incorrect passphrase or corrupted file")
	}

	var payload exportPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return 0, 0, fmt.Errorf("secrets.Import: decrypted payload is not valid: %w", err)
	}

	existing, err := List(ctx, db, profileID)
	if err != nil {
		return 0, 0, fmt.Errorf("secrets.Import: listing existing entries: %w", err)
	}
	existingNames := make(map[string]bool, len(existing))
	for _, e := range existing {
		existingNames[e.Name] = true
	}

	for _, entry := range payload.Entries {
		if existingNames[entry.Name] {
			skipped++
			continue
		}
		if _, err := Add(ctx, db, profileID, entry.Kind, entry.Name, entry.Fields, entry.Username, entry.URL, entry.Notes); err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping import of %q: %v\n", entry.Name, err)
			continue
		}
		existingNames[entry.Name] = true
		imported++
	}
	return imported, skipped, nil
}
