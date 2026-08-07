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
// deliberately omitted — Import always allocates fresh ones. Meta carries
// the non-secret columns of a system-managed entry's linked row
// (connections/crawler_sessions/ai_providers) — see systemMetaColumns —
// so Import can re-materialize that row on the destination machine, not
// just the vault entry. Empty/omitted for "secret"/"login" kind entries.
type exportEntry struct {
	Kind      string            `json:"kind"`
	Name      string            `json:"name"`
	Username  string            `json:"username"`
	URL       string            `json:"url"`
	Notes     string            `json:"notes"`
	Fields    map[string]string `json:"fields"`
	Meta      map[string]string `json:"meta,omitempty"`
	CreatedAt string            `json:"created_at"`
	UpdatedAt string            `json:"updated_at"`
}

type exportPayload struct {
	ExportedAt string        `json:"exported_at"`
	ProfileID  string        `json:"profile_id"`
	Entries    []exportEntry `json:"entries"`
}

// systemMetaColumns lists, per system kind, which non-secret columns of the
// linked table to carry in an export's Meta — exactly what each kind's
// RematerializeFunc (wired up in cmd/monoagentcli/secret_export.go) needs
// to reconstruct that row on another machine.
var systemMetaColumns = map[string][]string{
	"connection":  {"platform", "method", "label", "account_id"},
	"session":     {"platform", "username"},
	"ai_provider": {"provider_id", "tier", "base_url", "default_model", "extra_headers"},
}

// systemMeta reads the non-secret metadata columns for a system-managed
// entry's linked row, by raw SQL against the literal table name (see
// systemTableForKind in system.go) — internal/secrets never imports
// internal/connections/internal/ai, so it cannot ask those packages to do
// this for it. Returns nil (not an error) if there is no linked row, which
// simply means the exported entry carries no Meta.
func systemMeta(ctx context.Context, db *sql.DB, kind, vaultID string) map[string]string {
	table, ok := systemTableForKind[kind]
	if !ok {
		return nil
	}
	cols, ok := systemMetaColumns[kind]
	if !ok {
		return nil
	}
	q := fmt.Sprintf(`SELECT %s FROM %s WHERE vault_ref = ?`, strings.Join(cols, ", "), table)
	row := db.QueryRowContext(ctx, q, vaultID)
	vals := make([]interface{}, len(cols))
	ptrs := make([]interface{}, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := row.Scan(ptrs...); err != nil {
		return nil
	}
	meta := make(map[string]string, len(cols))
	for i, c := range cols {
		if s, ok := vals[i].(string); ok {
			meta[c] = s
		}
	}
	return meta
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
// file (see exportEnvelope), plus how many entries made it into the
// payload. An entry that fails to decrypt (e.g. an unmigrated legacy row —
// see MigrateFieldsToKV — or otherwise corrupted ciphertext) is logged to
// stderr and skipped rather than aborting the whole export, mirroring
// Import's and MigrateFieldsToKV's per-entry failure handling.
func Export(ctx context.Context, db *sql.DB, profileID, passphrase string) (data []byte, exported, skipped int, err error) {
	entries, err := List(ctx, db, profileID)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("secrets.Export: listing entries: %w", err)
	}

	payload := exportPayload{
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		ProfileID:  profileID,
	}
	for _, e := range entries {
		fields, notes, decErr := DecryptFields(ctx, db, profileID, e.ID)
		if decErr != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping export of %q: %v\n", e.Name, decErr)
			skipped++
			continue
		}
		payload.Entries = append(payload.Entries, exportEntry{
			Kind: e.Kind, Name: e.Name, Username: e.Username, URL: e.URL,
			Notes: notes, Fields: fields, Meta: systemMeta(ctx, db, e.Kind, e.ID),
			CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
		})
		exported++
	}

	plaintext, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("secrets.Export: marshaling payload: %w", err)
	}

	salt := make([]byte, exportSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, 0, 0, fmt.Errorf("secrets.Export: generating salt: %w", err)
	}
	key := argon2.IDKey([]byte(passphrase), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
	ciphertext, nonce, err := Encrypt(key, plaintext)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("secrets.Export: encrypting: %w", err)
	}

	envelope := exportEnvelope{
		Format: exportFormat, Version: exportVersion, KDF: "argon2id",
		Salt: salt, Nonce: nonce, Ciphertext: ciphertext,
	}
	out, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return nil, 0, 0, fmt.Errorf("secrets.Export: marshaling envelope: %w", err)
	}
	return out, exported, skipped, nil
}

// RematerializeFunc reconstructs the linked row for one system-managed
// vault entry (kind "connection", "session", or "ai_provider") on the
// importing machine — inserting a new connections/crawler_sessions/
// ai_providers row (or upserting an existing one matched by natural key:
// platform+label for connections, platform+username for sessions, provider
// name for AI providers) with vault_ref set to vaultID. internal/secrets
// cannot do this itself (it would need to import internal/connections/
// internal/ai, which already import internal/secrets); the real
// implementations are wired up by cmd/monoagentcli/secret_export.go, the
// only real caller of Import, where all three packages are importable
// together. A nil func simply skips rematerializing that kind — the vault
// entry itself still imports.
type RematerializeFunc func(ctx context.Context, db *sql.DB, profileID, vaultID, name string, meta map[string]string) error

// Import decrypts fileData (an exportEnvelope produced by Export) with
// passphrase and adds every entry to profileID, skipping any whose name
// already exists there. A per-entry failure other than a name collision is
// logged to stderr and skipped, not fatal to the batch. For a
// system-managed entry that imports successfully, the matching
// rematerializeConnection/rematerializeSession/rematerializeProvider
// callback is invoked to reconstruct its linked row too; a rematerialize
// failure is logged and skipped like any other per-entry failure — the
// vault entry itself is not rolled back.
func Import(ctx context.Context, db *sql.DB, profileID, passphrase string, fileData []byte,
	rematerializeConnection, rematerializeSession, rematerializeProvider RematerializeFunc,
) (imported, skipped int, err error) {
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

	rematerializers := map[string]RematerializeFunc{
		"connection":  rematerializeConnection,
		"session":     rematerializeSession,
		"ai_provider": rematerializeProvider,
	}

	for _, entry := range payload.Entries {
		if existingNames[entry.Name] {
			skipped++
			continue
		}
		id, err := addEntry(ctx, db, profileID, entry.Kind, entry.Name, entry.Fields, entry.Username, entry.URL, entry.Notes)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping import of %q: %v\n", entry.Name, err)
			continue
		}
		existingNames[entry.Name] = true
		imported++

		if rematerialize := rematerializers[entry.Kind]; rematerialize != nil {
			if err := rematerialize(ctx, db, profileID, id, entry.Name, entry.Meta); err != nil {
				fmt.Fprintf(os.Stderr, "warning: imported %q but failed to reconnect it: %v\n", entry.Name, err)
			}
		}
	}
	return imported, skipped, nil
}
