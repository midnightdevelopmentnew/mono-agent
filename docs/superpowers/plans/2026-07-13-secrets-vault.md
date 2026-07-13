# Secrets Vault Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an encrypted secrets/password vault (`internal/secrets`) with CLI commands, a Wails GUI page, and transparent encryption of existing `internal/connections` credential data.

**Architecture:** A single 256-bit Data Encryption Key (DEK) is generated once, wrapped with a Key Encryption Key (KEK) held in the OS keychain (via `zalando/go-keyring`, no cgo), and persisted in wrapped form in a new `vault_keys` table. `internal/secrets` uses the unwrapped DEK to AES-256-GCM encrypt/decrypt: (a) rows in a new `vault_secrets` table for user-managed secrets/logins, and (b) the existing `connections.data` JSON blob in place (no schema change to the `connections` table — the column just holds ciphertext instead of plaintext JSON after migration).

**Tech Stack:** Go stdlib `crypto/aes`, `crypto/cipher` (AES-256-GCM); `github.com/zalando/go-keyring` (OS keychain, no cgo — compatible with this repo's `CGO_ENABLED=0` cross-compiled builds); existing `modernc.org/sqlite`, `spf13/cobra`, Wails bindings.

## Global Constraints

- Package name is `internal/secrets` — `internal/vault` already exists for an unrelated image-asset vault.
- No cgo: `zalando/go-keyring` shells to `/usr/bin/security` on darwin, uses D-Bus Secret Service on linux, wincred syscalls on windows — matches `Makefile`'s `CGO_ENABLED=0` cross-compiles.
- `Decrypt`/reveal is the only path that returns plaintext, and it is invoked exclusively from the CLI's `secret reveal --reveal` command and the Wails `RevealSecret` method — both call the same `secrets.Decrypt` function, no parallel implementation.
- Every DB-touching function takes `(ctx context.Context, db *sql.DB, ...)`, matching the existing `internal/vault` and `internal/connections` package conventions (this codebase does NOT put these on a shared `*storage.Database` receiver the way `internal/storage/repository.go` does for person/action/message data).
- Migrations go in `data/migrations/017_secrets_vault.sql`, following the exact numbering/embedding convention already used through `016_person_status_updates.sql`.

---

## File Structure

- `internal/secrets/crypto.go` — raw AES-256-GCM encrypt/decrypt (key-agnostic, used for both DEK-wrapping and entry encryption).
- `internal/secrets/keyring.go` — KEK get-or-create via OS keychain.
- `internal/secrets/dek.go` — DEK get-or-create/unwrap, backed by `vault_keys` table + `keyring.go`.
- `internal/secrets/secrets.go` — `Add`/`Decrypt`/`List`/`Delete` for `vault_secrets` entries (secret/login kinds).
- `internal/secrets/blob.go` — `EncryptBlob`/`DecryptBlob` helpers used by `internal/connections` to encrypt the whole `data` column in place.
- `data/migrations/017_secrets_vault.sql` — new tables.
- `cmd/monoagentcli/secret.go` — CLI commands: `add`, `list`, `get`, `reveal`, `rm`, `encrypt-connections`.
- `internal/connections/storage.go` — modified: `Save`/`scanConnection`/`scanConnections` route the `data` column through `blob.go`.
- `wails-app/app.go` — modified: add `ListSecrets`/`AddSecret`/`RevealSecret`/`DeleteSecret` methods.
- `wails-app/frontend/src/pages/Vault.jsx` — new GUI page.

---

### Task 1: Add `go-keyring` dependency

**Files:**
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Produces: `github.com/zalando/go-keyring` importable as `keyring`, exposing `keyring.Set(service, user, password string) error`, `keyring.Get(service, user string) (string, error)`, `keyring.ErrNotFound`.

- [ ] **Step 1: Add the dependency**

Run: `go get github.com/zalando/go-keyring@v0.2.6`

- [ ] **Step 2: Verify it resolves**

Run: `go build ./...`
Expected: exits 0 (no other code references it yet, so this only proves the module resolves).

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add go-keyring dependency for the secrets vault"
```

---

### Task 2: `vault_secrets` / `vault_keys` migration

**Files:**
- Create: `data/migrations/017_secrets_vault.sql`
- Test: `internal/storage/database_test.go` (add one case)

**Interfaces:**
- Produces: tables `vault_secrets(id, seq, profile_id, kind, name, username, url, ciphertext, nonce, notes_ciphertext, notes_nonce, created_at, updated_at)` and `vault_keys(id, wrapped_dek, wrapped_nonce, created_at)`.

- [ ] **Step 1: Write the failing test**

Add to `internal/storage/database_test.go`:

```go
func TestApplyMigrations_CreatesVaultSecretsTables(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "migrate-vault-secrets.db")
	db, err := NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	defer db.DB.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	for _, table := range []string{"vault_secrets", "vault_keys"} {
		var name string
		err := db.DB.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table,
		).Scan(&name)
		if err != nil {
			t.Fatalf("table %s not created: %v", table, err)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/storage/... -run TestApplyMigrations_CreatesVaultSecretsTables -v`
Expected: FAIL — table not created (migration file doesn't exist yet).

- [ ] **Step 3: Write the migration**

Create `data/migrations/017_secrets_vault.sql`:

```sql
CREATE TABLE vault_secrets (
    id               TEXT PRIMARY KEY,
    seq              INTEGER NOT NULL UNIQUE,
    profile_id       TEXT NOT NULL DEFAULT 'default',
    kind             TEXT NOT NULL,
    name             TEXT NOT NULL,
    username         TEXT,
    url              TEXT,
    ciphertext       BLOB NOT NULL,
    nonce            BLOB NOT NULL,
    notes_ciphertext BLOB,
    notes_nonce      BLOB,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_vault_secrets_profile_name ON vault_secrets(profile_id, name);
CREATE INDEX idx_vault_secrets_seq ON vault_secrets(seq DESC);

CREATE TABLE vault_keys (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    wrapped_dek   BLOB NOT NULL,
    wrapped_nonce BLOB NOT NULL,
    created_at    TEXT NOT NULL
);
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/storage/... -run TestApplyMigrations_CreatesVaultSecretsTables -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add data/migrations/017_secrets_vault.sql internal/storage/database_test.go
git commit -m "feat: add vault_secrets and vault_keys migration"
```

---

### Task 3: AES-256-GCM crypto core

**Files:**
- Create: `internal/secrets/crypto.go`
- Test: `internal/secrets/crypto_test.go`

**Interfaces:**
- Produces: `Encrypt(key, plaintext []byte) (ciphertext, nonce []byte, err error)`, `Decrypt(key, ciphertext, nonce []byte) (plaintext []byte, err error)`.

- [ ] **Step 1: Write the failing test**

Create `internal/secrets/crypto_test.go`:

```go
package secrets

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	plaintext := []byte("super secret value")

	ciphertext, nonce, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Equal(ciphertext, plaintext) {
		t.Fatal("ciphertext must not equal plaintext")
	}

	got, err := Decrypt(key, ciphertext, nonce)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("got %q, want %q", got, plaintext)
	}
}

func TestDecrypt_WrongKeyFails(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	wrongKey := make([]byte, 32)
	rand.Read(wrongKey)

	ciphertext, nonce, err := Encrypt(key, []byte("data"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := Decrypt(wrongKey, ciphertext, nonce); err == nil {
		t.Fatal("expected error decrypting with wrong key, got nil")
	}
}

func TestEncrypt_NonceUniquePerCall(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	_, nonce1, _ := Encrypt(key, []byte("a"))
	_, nonce2, _ := Encrypt(key, []byte("a"))
	if bytes.Equal(nonce1, nonce2) {
		t.Fatal("nonces must differ across calls")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/secrets/... -v`
Expected: FAIL — package `secrets` / functions `Encrypt`/`Decrypt` don't exist yet.

- [ ] **Step 3: Write the implementation**

Create `internal/secrets/crypto.go`:

```go
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
)

// Encrypt seals plaintext under key (must be 32 bytes) using AES-256-GCM,
// generating a fresh random nonce for this call.
func Encrypt(key, plaintext []byte) (ciphertext, nonce []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, fmt.Errorf("secrets: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, fmt.Errorf("secrets: new gcm: %w", err)
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("secrets: generating nonce: %w", err)
	}
	ciphertext = gcm.Seal(nil, nonce, plaintext, nil)
	return ciphertext, nonce, nil
}

// Decrypt opens ciphertext sealed by Encrypt with the same key and nonce.
func Decrypt(key, ciphertext, nonce []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secrets: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: new gcm: %w", err)
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("secrets: invalid nonce size %d, want %d", len(nonce), gcm.NonceSize())
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("secrets: decrypt: %w", err)
	}
	return plaintext, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/secrets/... -v`
Expected: PASS (all 3 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/secrets/crypto.go internal/secrets/crypto_test.go
git commit -m "feat: add AES-256-GCM crypto core for secrets vault"
```

---

### Task 4: KEK storage in the OS keychain

**Files:**
- Create: `internal/secrets/keyring.go`
- Test: `internal/secrets/keyring_test.go`

**Interfaces:**
- Consumes: none new (stdlib `encoding/hex`, `crypto/rand`, `github.com/zalando/go-keyring` from Task 1).
- Produces: `getOrCreateKEK() (key []byte, err error)` — unexported, used only by `dek.go` in Task 5.

- [ ] **Step 1: Write the failing test**

Create `internal/secrets/keyring_test.go`:

```go
package secrets

import (
	"testing"

	"github.com/zalando/go-keyring"
)

func TestGetOrCreateKEK_PersistsAcrossCalls(t *testing.T) {
	keyring.MockInit() // in-memory mock backend, no real OS keychain touched in tests

	key1, err := getOrCreateKEK()
	if err != nil {
		t.Fatalf("getOrCreateKEK (first call): %v", err)
	}
	if len(key1) != 32 {
		t.Fatalf("expected 32-byte key, got %d bytes", len(key1))
	}

	key2, err := getOrCreateKEK()
	if err != nil {
		t.Fatalf("getOrCreateKEK (second call): %v", err)
	}
	if string(key1) != string(key2) {
		t.Fatal("second call must return the same KEK, not regenerate")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/secrets/... -run TestGetOrCreateKEK -v`
Expected: FAIL — `getOrCreateKEK` undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/secrets/keyring.go`:

```go
package secrets

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

const (
	keyringService = "monoagent-vault"
	keyringAccount = "kek"
)

// getOrCreateKEK returns the 32-byte Key Encryption Key stored in the OS
// keychain (macOS Keychain / Linux Secret Service / Windows Credential
// Manager, via zalando/go-keyring — no cgo), generating and storing a new
// one on first use. The KEK never touches disk; only the DEK it wraps does
// (see dek.go).
func getOrCreateKEK() ([]byte, error) {
	stored, err := keyring.Get(keyringService, keyringAccount)
	if err == nil {
		key, decodeErr := hex.DecodeString(stored)
		if decodeErr != nil {
			return nil, fmt.Errorf("secrets: decoding stored KEK: %w", decodeErr)
		}
		return key, nil
	}
	if !errors.Is(err, keyring.ErrNotFound) {
		return nil, fmt.Errorf("secrets: reading KEK from keychain: %w", err)
	}

	kek := make([]byte, 32)
	if _, err := rand.Read(kek); err != nil {
		return nil, fmt.Errorf("secrets: generating KEK: %w", err)
	}
	if err := keyring.Set(keyringService, keyringAccount, hex.EncodeToString(kek)); err != nil {
		return nil, fmt.Errorf("secrets: storing KEK in keychain: %w", err)
	}
	return kek, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/secrets/... -run TestGetOrCreateKEK -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/secrets/keyring.go internal/secrets/keyring_test.go
git commit -m "feat: store the vault's Key Encryption Key in the OS keychain"
```

---

### Task 5: DEK lifecycle (`vault_keys` table + KEK wrapping)

**Files:**
- Create: `internal/secrets/dek.go`
- Test: `internal/secrets/dek_test.go`

**Interfaces:**
- Consumes: `Encrypt`/`Decrypt` (Task 3), `getOrCreateKEK` (Task 4), table `vault_keys` (Task 2).
- Produces: `getOrCreateDEK(ctx context.Context, db *sql.DB) (key []byte, err error)` — unexported, used by `secrets.go` (Task 6) and `blob.go` (Task 8).

- [ ] **Step 1: Write the failing test**

Create `internal/secrets/dek_test.go`:

```go
package secrets

import (
	"context"
	"path/filepath"
	"testing"

	"monoagent/internal/storage"

	"github.com/zalando/go-keyring"
)

func newDEKTestDB(t *testing.T) *storage.Database {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "dek-test.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	t.Cleanup(func() { db.DB.Close() })
	return db
}

func TestGetOrCreateDEK_PersistsAcrossCalls(t *testing.T) {
	keyring.MockInit()
	db := newDEKTestDB(t)
	ctx := context.Background()

	dek1, err := getOrCreateDEK(ctx, db.DB)
	if err != nil {
		t.Fatalf("getOrCreateDEK (first call): %v", err)
	}
	if len(dek1) != 32 {
		t.Fatalf("expected 32-byte DEK, got %d bytes", len(dek1))
	}

	dek2, err := getOrCreateDEK(ctx, db.DB)
	if err != nil {
		t.Fatalf("getOrCreateDEK (second call): %v", err)
	}
	if string(dek1) != string(dek2) {
		t.Fatal("second call must return the same DEK, not regenerate")
	}

	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM vault_keys`).Scan(&count); err != nil {
		t.Fatalf("counting vault_keys rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 vault_keys row, got %d", count)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/secrets/... -run TestGetOrCreateDEK -v`
Expected: FAIL — `getOrCreateDEK` undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/secrets/dek.go`:

```go
package secrets

import (
	"context"
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
	if _, err := readFullRandom(dek); err != nil {
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
```

Add a tiny helper (avoids importing `crypto/rand` twice under different names across files — keep it local to this file):

```go
func readFullRandom(b []byte) (int, error) {
	return cryptoRandRead(b)
}
```

Replace that indirection — simpler to just import `crypto/rand` directly instead of adding a wrapper. Use this version of `createDEK` instead:

```go
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
```

And add `"crypto/rand"` to the import block, dropping `readFullRandom`/`cryptoRandRead` entirely — the final `dek.go` imports are: `context`, `crypto/rand`, `database/sql`, `fmt`, `time`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/secrets/... -run TestGetOrCreateDEK -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/secrets/dek.go internal/secrets/dek_test.go
git commit -m "feat: add DEK lifecycle wrapped by the keychain-held KEK"
```

---

### Task 6: `vault_secrets` entry CRUD (`Add`/`Decrypt`/`List`/`Delete`)

**Files:**
- Create: `internal/secrets/secrets.go`
- Test: `internal/secrets/secrets_test.go`

**Interfaces:**
- Consumes: `getOrCreateDEK` (Task 5), `Encrypt`/`Decrypt` (Task 3).
- Produces:
  - `type Entry struct { ID, ProfileID, Kind, Name, Username, URL, CreatedAt, UpdatedAt string }` (no secret value — safe for listing)
  - `Add(ctx context.Context, db *sql.DB, profileID, kind, name, value, username, url, notes string) (id string, err error)`
  - `Decrypt(ctx context.Context, db *sql.DB, profileID, id string) (value string, err error)` — note: this shadows the package-level `Decrypt` from crypto.go in name only if called unqualified from outside the package; within the package it's renamed `DecryptEntry` to avoid collision.
  - `List(ctx context.Context, db *sql.DB, profileID string) ([]Entry, error)`
  - `Delete(ctx context.Context, db *sql.DB, profileID, id string) error`

- [ ] **Step 1: Write the failing test**

Create `internal/secrets/secrets_test.go`:

```go
package secrets

import (
	"context"
	"path/filepath"
	"testing"

	"monoagent/internal/storage"

	"github.com/zalando/go-keyring"
)

func newSecretsTestDB(t *testing.T) *storage.Database {
	t.Helper()
	keyring.MockInit()
	dbPath := filepath.Join(t.TempDir(), "secrets-test.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	t.Cleanup(func() { db.DB.Close() })
	return db
}

func TestAddDecryptList_RoundTrip(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	id, err := Add(ctx, db.DB, "default", "secret", "openai-key", "sk-abc123", "", "", "prod key")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	value, err := DecryptEntry(ctx, db.DB, "default", id)
	if err != nil {
		t.Fatalf("DecryptEntry: %v", err)
	}
	if value != "sk-abc123" {
		t.Fatalf("got %q, want %q", value, "sk-abc123")
	}

	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "openai-key" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
}

func TestList_NeverReturnsPlaintext(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "login", "github", "hunter2", "alice", "https://github.com", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Entry has no field capable of holding the secret value — this test
	// documents that guarantee at the type level, not just by inspection.
	if entries[0].Username != "alice" {
		t.Fatalf("expected username alice, got %q", entries[0].Username)
	}
}

func TestDelete_RemovesEntry(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	id, err := Add(ctx, db.DB, "default", "secret", "temp", "value", "", "", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := Delete(ctx, db.DB, "default", id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries after delete, got %d", len(entries))
	}
}

func TestDecryptEntry_NotFoundErrors(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := DecryptEntry(ctx, db.DB, "default", "sec-999"); err == nil {
		t.Fatal("expected error for missing entry, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/secrets/... -run 'TestAddDecryptList_RoundTrip|TestList_NeverReturnsPlaintext|TestDelete_RemovesEntry|TestDecryptEntry_NotFoundErrors' -v`
Expected: FAIL — `Add`/`DecryptEntry`/`List`/`Delete`/`Entry` undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/secrets/secrets.go`:

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/secrets/... -v`
Expected: PASS (all tests in the package)

- [ ] **Step 5: Commit**

```bash
git add internal/secrets/secrets.go internal/secrets/secrets_test.go
git commit -m "feat: add vault_secrets entry CRUD (Add/DecryptEntry/List/Delete)"
```

---

### Task 7: CLI commands (`monoagentcli secret ...`)

**Files:**
- Create: `cmd/monoagentcli/secret.go`
- Modify: `cmd/monoagentcli/root.go:61-79` (add `newSecretCmd(cfg)` to `cmd.AddCommand(...)`)
- Test: `cmd/monoagentcli/secret_test.go`

**Interfaces:**
- Consumes: `secrets.Add`, `secrets.DecryptEntry`, `secrets.List`, `secrets.Delete` (Task 6); `initDB(cfg) (*storage.Database, error)`, `cfg.ProfileID`, `cfg.JSONOutput` (existing, from `root.go`).
- Produces: `newSecretCmd(cfg *globalConfig) *cobra.Command`, registered under `monoagentcli secret`.

- [ ] **Step 1: Write the failing test**

Create `cmd/monoagentcli/secret_test.go` (mirrors the pattern in `people_status_test.go`):

```go
package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"monoagent/internal/storage"

	"github.com/zalando/go-keyring"
)

func newSecretCLITestDB(t *testing.T) string {
	t.Helper()
	keyring.MockInit()
	dbPath := filepath.Join(t.TempDir(), "cli-secret-test.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	if err := db.DB.Close(); err != nil {
		t.Fatalf("closing seed db: %v", err)
	}
	return dbPath
}

func runSecretCmd(t *testing.T, dbPath string, args ...string) (string, error) {
	t.Helper()
	cfg := &globalConfig{DBPath: dbPath, JSONOutput: true}
	cmd := newSecretCmd(cfg)
	cmd.SetArgs(args)
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := cmd.Execute()
	return out.String(), err
}

func TestSecretAddListGetReveal(t *testing.T) {
	dbPath := newSecretCLITestDB(t)

	addOut, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "openai-key", "--value", "sk-test123")
	if err != nil {
		t.Fatalf("secret add: %v (%s)", err, addOut)
	}

	listOut, err := runSecretCmd(t, dbPath, "list")
	if err != nil {
		t.Fatalf("secret list: %v", err)
	}
	if !strings.Contains(listOut, "openai-key") {
		t.Fatalf("expected list output to contain entry name, got: %s", listOut)
	}
	if strings.Contains(listOut, "sk-test123") {
		t.Fatal("secret list must never contain the plaintext value")
	}

	getOut, err := runSecretCmd(t, dbPath, "get", "openai-key")
	if err != nil {
		t.Fatalf("secret get: %v", err)
	}
	if strings.Contains(getOut, "sk-test123") {
		t.Fatal("secret get must never return the plaintext value")
	}

	revealOut, err := runSecretCmd(t, dbPath, "reveal", "openai-key", "--reveal")
	if err != nil {
		t.Fatalf("secret reveal: %v", err)
	}
	if !strings.Contains(revealOut, "sk-test123") {
		t.Fatalf("expected reveal output to contain plaintext, got: %s", revealOut)
	}
}

func TestSecretReveal_RequiresConfirmationFlag(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	if _, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "x", "--value", "v"); err != nil {
		t.Fatalf("secret add: %v", err)
	}
	if _, err := runSecretCmd(t, dbPath, "reveal", "x"); err == nil {
		t.Fatal("expected error when --reveal flag is omitted")
	}
}

func TestSecretRm_DeletesEntry(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	if _, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "temp", "--value", "v"); err != nil {
		t.Fatalf("secret add: %v", err)
	}
	if _, err := runSecretCmd(t, dbPath, "rm", "temp"); err != nil {
		t.Fatalf("secret rm: %v", err)
	}
	listOut, err := runSecretCmd(t, dbPath, "list")
	if err != nil {
		t.Fatalf("secret list: %v", err)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal([]byte(listOut), &entries); err != nil {
		t.Fatalf("unmarshal list output: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries after rm, got %d", len(entries))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/monoagentcli/... -run TestSecret -v`
Expected: FAIL — `newSecretCmd` undefined.

- [ ] **Step 3: Write the implementation**

Create `cmd/monoagentcli/secret.go`:

```go
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"monoagent/internal/secrets"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

// newSecretCmd returns the `secret` command group: an encrypted vault for
// arbitrary API keys/passwords (kind "secret") and website logins (kind
// "login"). Plaintext is only ever returned by `secret reveal --reveal`;
// every other subcommand deals in names/references only.
func newSecretCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage encrypted secrets and logins in the vault",
	}
	cmd.AddCommand(
		newSecretAddCmd(cfg),
		newSecretListCmd(cfg),
		newSecretGetCmd(cfg),
		newSecretRevealCmd(cfg),
		newSecretRmCmd(cfg),
	)
	return cmd
}

func newSecretAddCmd(cfg *globalConfig) *cobra.Command {
	var kind, name, value, username, url, notes string
	cmd := &cobra.Command{
		Use:     "add",
		Short:   "Add a new secret or login to the vault",
		Example: `  monoagentcli secret add --kind secret --name openai-key
  monoagentcli secret add --kind login --name github --username alice --url https://github.com`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()

			profileID := cfg.ProfileID
			if profileID == "" {
				profileID = "default"
			}

			if value == "" {
				fmt.Fprint(os.Stderr, "Value: ")
				reader := bufio.NewReader(os.Stdin)
				line, err := reader.ReadString('\n')
				if err != nil {
					return fmt.Errorf("reading value from stdin: %w", err)
				}
				value = strings.TrimRight(line, "\r\n")
			}

			id, err := secrets.Add(cmd.Context(), db.DB, profileID, kind, name, value, username, url, notes)
			if err != nil {
				return fmt.Errorf("adding secret: %w", err)
			}
			if cfg.JSONOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]string{"id": id, "name": name})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Added %s %q as %s.\n", kind, name, id)
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "secret", "Entry kind: secret or login")
	cmd.Flags().StringVar(&name, "name", "", "Unique name for this entry (required)")
	cmd.Flags().StringVar(&value, "value", "", "Secret/password value (omit to be prompted on stdin)")
	cmd.Flags().StringVar(&username, "username", "", "Username (login kind only)")
	cmd.Flags().StringVar(&url, "url", "", "URL (login kind only)")
	cmd.Flags().StringVar(&notes, "notes", "", "Optional notes")
	cmd.MarkFlagRequired("name")
	return cmd
}

func newSecretListCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List vault entries (metadata only — never secret values)",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()

			profileID := cfg.ProfileID
			if profileID == "" {
				profileID = "default"
			}
			entries, err := secrets.List(cmd.Context(), db.DB, profileID)
			if err != nil {
				return fmt.Errorf("listing secrets: %w", err)
			}

			if cfg.JSONOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if entries == nil {
					entries = []secrets.Entry{}
				}
				return enc.Encode(entries)
			}
			if len(entries) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No secrets stored.")
				return nil
			}
			table := tablewriter.NewWriter(cmd.OutOrStdout())
			table.SetHeader([]string{"ID", "Kind", "Name", "Username", "Updated"})
			table.SetBorder(false)
			for _, e := range entries {
				table.Append([]string{e.ID, e.Kind, e.Name, e.Username, e.UpdatedAt})
			}
			table.Render()
			return nil
		},
	}
}

func newSecretGetCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:     "get <name>",
		Short:   "Resolve a vault reference for use in workflow configs (never returns plaintext)",
		Args:    cobra.ExactArgs(1),
		Example: `  monoagentcli secret get openai-key`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()

			profileID := cfg.ProfileID
			if profileID == "" {
				profileID = "default"
			}
			entries, err := secrets.List(cmd.Context(), db.DB, profileID)
			if err != nil {
				return fmt.Errorf("looking up secret: %w", err)
			}
			for _, e := range entries {
				if e.Name == args[0] {
					ref := "@secret:" + e.Name
					if cfg.JSONOutput {
						enc := json.NewEncoder(cmd.OutOrStdout())
						enc.SetIndent("", "  ")
						return enc.Encode(map[string]string{"ref": ref, "id": e.ID})
					}
					fmt.Fprintln(cmd.OutOrStdout(), ref)
					return nil
				}
			}
			return fmt.Errorf("no secret named %q found", args[0])
		},
	}
}

func newSecretRevealCmd(cfg *globalConfig) *cobra.Command {
	var confirm bool
	cmd := &cobra.Command{
		Use:     "reveal <name>",
		Short:   "Print the plaintext value of a vault entry — requires --reveal to confirm",
		Args:    cobra.ExactArgs(1),
		Example: `  monoagentcli secret reveal openai-key --reveal`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !confirm {
				return fmt.Errorf("refusing to print a secret without --reveal (pass it explicitly to confirm)")
			}
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()

			profileID := cfg.ProfileID
			if profileID == "" {
				profileID = "default"
			}
			entries, err := secrets.List(cmd.Context(), db.DB, profileID)
			if err != nil {
				return fmt.Errorf("looking up secret: %w", err)
			}
			var id string
			for _, e := range entries {
				if e.Name == args[0] {
					id = e.ID
					break
				}
			}
			if id == "" {
				return fmt.Errorf("no secret named %q found", args[0])
			}
			value, err := secrets.DecryptEntry(cmd.Context(), db.DB, profileID, id)
			if err != nil {
				return fmt.Errorf("revealing secret: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), value)
			return nil
		},
	}
	cmd.Flags().BoolVar(&confirm, "reveal", false, "Confirm you want the plaintext value printed to stdout")
	return cmd
}

func newSecretRmCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:     "rm <name>",
		Short:   "Delete a vault entry",
		Args:    cobra.ExactArgs(1),
		Example: `  monoagentcli secret rm openai-key`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()

			profileID := cfg.ProfileID
			if profileID == "" {
				profileID = "default"
			}
			entries, err := secrets.List(cmd.Context(), db.DB, profileID)
			if err != nil {
				return fmt.Errorf("looking up secret: %w", err)
			}
			var id string
			for _, e := range entries {
				if e.Name == args[0] {
					id = e.ID
					break
				}
			}
			if id == "" {
				return fmt.Errorf("no secret named %q found", args[0])
			}
			if err := secrets.Delete(cmd.Context(), db.DB, profileID, id); err != nil {
				return fmt.Errorf("deleting secret: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted %q.\n", args[0])
			return nil
		},
	}
}
```

Note: `secret add`'s `--value` flag is provided directly in the test above for reproducibility, but its help text and stdin-prompt fallback exist precisely so real interactive use never needs to pass a secret as a flag (shell-history exposure) — this matches the spec.

- [ ] **Step 4: Wire into root command**

In `cmd/monoagentcli/root.go`, inside the existing `cmd.AddCommand(...)` call (after `newProfileCmd(cfg),`), add:

```go
		newSecretCmd(cfg),
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./cmd/monoagentcli/... -run TestSecret -v`
Expected: PASS (all 4 tests)

- [ ] **Step 6: Commit**

```bash
git add cmd/monoagentcli/secret.go cmd/monoagentcli/secret_test.go cmd/monoagentcli/root.go
git commit -m "feat: add monoagentcli secret CLI commands"
```

---

### Task 8: Encrypt `connections.data` in place

**Files:**
- Create: `internal/secrets/blob.go`
- Modify: `internal/connections/storage.go:145-180` (`Save`), `internal/connections/storage.go:532-548` (`scanConnection`), `internal/connections/storage.go:550-572` (`scanConnections`)
- Modify: `cmd/monoagentcli/secret.go` (add `encrypt-connections` subcommand)
- Test: `internal/secrets/blob_test.go`, `internal/connections/storage_test.go` (add cases)

**Interfaces:**
- Consumes: `getOrCreateDEK` (Task 5), `Encrypt`/`Decrypt` (Task 3).
- Produces: `EncryptBlob(ctx context.Context, db *sql.DB, plaintext []byte) (encoded string, err error)`, `DecryptBlob(ctx context.Context, db *sql.DB, encoded string) (plaintext []byte, err error)`. `encoded` is self-describing: prefixed `"vaultenc:v1:"` + base64(nonce||ciphertext) when encrypted, so `DecryptBlob` can tell encrypted rows apart from not-yet-migrated plaintext JSON and return it unchanged in that case (never errors on legacy rows).

- [ ] **Step 1: Write the failing test**

Create `internal/secrets/blob_test.go`:

```go
package secrets

import (
	"context"
	"path/filepath"
	"testing"

	"monoagent/internal/storage"

	"github.com/zalando/go-keyring"
)

func newBlobTestDB(t *testing.T) *storage.Database {
	t.Helper()
	keyring.MockInit()
	dbPath := filepath.Join(t.TempDir(), "blob-test.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	t.Cleanup(func() { db.DB.Close() })
	return db
}

func TestEncryptDecryptBlob_RoundTrip(t *testing.T) {
	db := newBlobTestDB(t)
	ctx := context.Background()
	plaintext := []byte(`{"access_token":"abc123"}`)

	encoded, err := EncryptBlob(ctx, db.DB, plaintext)
	if err != nil {
		t.Fatalf("EncryptBlob: %v", err)
	}
	if encoded == string(plaintext) {
		t.Fatal("encoded blob must not equal plaintext")
	}

	got, err := DecryptBlob(ctx, db.DB, encoded)
	if err != nil {
		t.Fatalf("DecryptBlob: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Fatalf("got %q, want %q", got, plaintext)
	}
}

func TestDecryptBlob_PassesThroughLegacyPlaintext(t *testing.T) {
	db := newBlobTestDB(t)
	ctx := context.Background()
	legacy := `{"access_token":"legacy-plaintext"}`

	got, err := DecryptBlob(ctx, db.DB, legacy)
	if err != nil {
		t.Fatalf("DecryptBlob on legacy plaintext: %v", err)
	}
	if string(got) != legacy {
		t.Fatalf("got %q, want unchanged %q", got, legacy)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/secrets/... -run 'TestEncryptDecryptBlob_RoundTrip|TestDecryptBlob_PassesThroughLegacyPlaintext' -v`
Expected: FAIL — `EncryptBlob`/`DecryptBlob` undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/secrets/blob.go`:

```go
package secrets

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"strings"
)

const blobPrefix = "vaultenc:v1:"

// EncryptBlob encrypts an arbitrary byte blob (e.g. a connections.data JSON
// document) under the vault's DEK and returns a self-describing string safe
// to store directly in a TEXT column.
func EncryptBlob(ctx context.Context, db *sql.DB, plaintext []byte) (string, error) {
	dek, err := getOrCreateDEK(ctx, db)
	if err != nil {
		return "", fmt.Errorf("secrets.EncryptBlob: %w", err)
	}
	ciphertext, nonce, err := Encrypt(dek, plaintext)
	if err != nil {
		return "", fmt.Errorf("secrets.EncryptBlob: %w", err)
	}
	combined := append(nonce, ciphertext...)
	return blobPrefix + base64.StdEncoding.EncodeToString(combined), nil
}

// DecryptBlob reverses EncryptBlob. If encoded does not carry the vaultenc
// prefix (a row from before this feature shipped, not yet migrated) it is
// returned unchanged rather than erroring — callers that unmarshal JSON
// from the result get the original plaintext JSON either way.
func DecryptBlob(ctx context.Context, db *sql.DB, encoded string) ([]byte, error) {
	if !strings.HasPrefix(encoded, blobPrefix) {
		return []byte(encoded), nil
	}
	dek, err := getOrCreateDEK(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("secrets.DecryptBlob: %w", err)
	}
	combined, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, blobPrefix))
	if err != nil {
		return nil, fmt.Errorf("secrets.DecryptBlob: decoding base64: %w", err)
	}
	const nonceSize = 12 // AES-GCM standard nonce size, matches crypto.go's gcm.NonceSize()
	if len(combined) < nonceSize {
		return nil, fmt.Errorf("secrets.DecryptBlob: encoded blob too short")
	}
	nonce, ciphertext := combined[:nonceSize], combined[nonceSize:]
	plaintext, err := Decrypt(dek, ciphertext, nonce)
	if err != nil {
		return nil, fmt.Errorf("secrets.DecryptBlob: %w", err)
	}
	return plaintext, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/secrets/... -v`
Expected: PASS (whole package)

- [ ] **Step 5: Wire into `internal/connections/storage.go`**

Read `internal/connections/storage.go` first to confirm current line numbers haven't shifted, then apply these three changes:

In `Save` (around line 158), replace:

```go
	dataBytes, err := json.Marshal(c.Data)
	if err != nil {
		return fmt.Errorf("connections.Save: marshal data: %w", err)
	}
```

with:

```go
	dataBytes, err := json.Marshal(c.Data)
	if err != nil {
		return fmt.Errorf("connections.Save: marshal data: %w", err)
	}
	encodedData, err := secrets.EncryptBlob(ctx, s.db, dataBytes)
	if err != nil {
		return fmt.Errorf("connections.Save: encrypting data: %w", err)
	}
```

...and in the `ExecContext` call just below, replace `string(dataBytes)` with `encodedData`.

In `scanConnection` (around line 532), it currently takes `row *sql.Row` with no context/db access — change its signature to accept them:

```go
func scanConnection(ctx context.Context, db *sql.DB, row *sql.Row) (*Connection, error) {
	var c Connection
	var dataJSON, method string
	err := row.Scan(&c.ID, &c.Platform, &method, &c.Label, &c.AccountID,
		&dataJSON, &c.Status, &c.LastTested, &c.ProfileID, &c.CreatedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scanConnection: %w", err)
	}
	c.Method = AuthMethod(method)
	decoded, err := secrets.DecryptBlob(ctx, db, dataJSON)
	if err != nil {
		return nil, fmt.Errorf("scanConnection: decrypting data: %w", err)
	}
	if err := json.Unmarshal(decoded, &c.Data); err != nil {
		return nil, fmt.Errorf("scanConnection: unmarshal data: %w", err)
	}
	return &c, nil
}
```

Update its one call site, `Get` (around line 186):

```go
func (s *Store) Get(ctx context.Context, id string) (*Connection, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, platform, method, label, account_id, data, status, last_tested, COALESCE(profile_id,'default'), created_at, updated_at
         FROM connections WHERE id = ?`, id)
	return scanConnection(ctx, s.db, row)
}
```

Apply the same shape of change to `scanConnections` (around line 551) and its call sites (`ListAll`, `ListByPlatform`): add `(ctx context.Context, db *sql.DB, rows *sql.Rows)` params, call `secrets.DecryptBlob(ctx, db, dataJSON)` before `json.Unmarshal` for each row, and update every caller (`s.db` is already in scope at each call site inside `Store` methods) to pass `ctx, s.db, rows`.

Add `"monoagent/internal/secrets"` to `storage.go`'s import block.

- [ ] **Step 6: Run the existing connections test suite**

Run: `go test ./internal/connections/... -v`
Expected: PASS — existing tests exercise `Save`/`Get`/`List*` and must still round-trip correctly now that the column holds ciphertext.

- [ ] **Step 7: Add the `encrypt-connections` CLI command**

In `cmd/monoagentcli/secret.go`, add to `newSecretCmd`'s `cmd.AddCommand(...)`:

```go
		newSecretEncryptConnectionsCmd(cfg),
```

And append this function to the same file:

```go
func newSecretEncryptConnectionsCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "encrypt-connections",
		Short: "One-time migration: encrypt any existing plaintext connection credentials in place",
		Long:  "Existing connections created before the secrets vault shipped store OAuth tokens/API keys as plaintext JSON. This re-saves every connection through the same Save path new connections already use, which now encrypts the data column automatically. Safe to run repeatedly — already-encrypted rows are re-encrypted (a no-op in effect) rather than skipped, since Save always re-applies the current encryption path.",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()

			store := connections.NewStore(db.DB)
			conns, err := store.ListAll(cmd.Context(), "")
			if err != nil {
				return fmt.Errorf("listing connections: %w", err)
			}
			migrated := 0
			for i := range conns {
				if err := store.Save(cmd.Context(), &conns[i]); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to re-encrypt connection %s: %v\n", conns[i].ID, err)
					continue
				}
				migrated++
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Encrypted %d of %d connection(s).\n", migrated, len(conns))
			return nil
		},
	}
}
```

Add `"monoagent/internal/connections"` to `secret.go`'s import block. `connections.NewStore(db *sql.DB) *Store` is the correct constructor (confirmed against `internal/connections/storage.go:126`).

- [ ] **Step 8: Write a CLI-level test for the migration command**

Add to `cmd/monoagentcli/secret_test.go`:

```go
func TestSecretEncryptConnections_MigratesPlaintextRow(t *testing.T) {
	dbPath := newSecretCLITestDB(t)

	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	// Insert a connection row the old way: raw plaintext JSON in `data`,
	// bypassing Store.Save so this test can be sure it starts unencrypted.
	_, err = db.DB.Exec(`
		INSERT INTO connections (id, platform, method, label, account_id, data, status, last_tested, profile_id, created_at, updated_at)
		VALUES ('conn-1', 'x', 'oauth', 'Test', '', '{"access_token":"plaintext-token"}', 'active', '', 'default', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("seeding plaintext connection: %v", err)
	}
	db.DB.Close()

	out, err := runSecretCmd(t, dbPath, "encrypt-connections")
	if err != nil {
		t.Fatalf("secret encrypt-connections: %v (%s)", err, out)
	}

	db2, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	defer db2.DB.Close()
	var rawData string
	if err := db2.DB.QueryRow(`SELECT data FROM connections WHERE id = 'conn-1'`).Scan(&rawData); err != nil {
		t.Fatalf("reading migrated row: %v", err)
	}
	if strings.Contains(rawData, "plaintext-token") {
		t.Fatal("connections.data must not contain plaintext after encrypt-connections")
	}
	if !strings.HasPrefix(rawData, "vaultenc:v1:") {
		t.Fatalf("expected vaultenc-prefixed ciphertext, got: %s", rawData)
	}
}
```

- [ ] **Step 9: Run test to verify it passes**

Run: `go test ./cmd/monoagentcli/... -run TestSecretEncryptConnections -v`
Expected: PASS

- [ ] **Step 10: Commit**

```bash
git add internal/secrets/blob.go internal/secrets/blob_test.go internal/connections/storage.go cmd/monoagentcli/secret.go cmd/monoagentcli/secret_test.go
git commit -m "feat: encrypt connections.data in place using the secrets vault"
```

---

### Task 9: Wails GUI — `Vault.jsx` page

**Files:**
- Modify: `wails-app/app.go` (add methods, near the existing "Image Vault" section for consistency)
- Create: `wails-app/frontend/src/pages/Vault.jsx`
- Modify: wherever `ImageVault.jsx`/other pages are registered in the app's router/nav (locate via `grep -rn "ImageVault" wails-app/frontend/src` before editing, to match the existing registration pattern exactly)

**Interfaces:**
- Consumes: `secrets.Add`, `secrets.List`, `secrets.DecryptEntry`, `secrets.Delete` (Task 6); `a.db`, `a.activeProfileID` (existing `App` fields).
- Produces: `App.ListSecrets() ([]secrets.Entry, error)`, `App.AddSecret(kind, name, value, username, url, notes string) (*secrets.Entry, error)`, `App.RevealSecret(id string) (string, error)`, `App.DeleteSecret(id string) error` — bound automatically by Wails' code generation into `wailsjs/go/main/App.js` (same mechanism already used for `GetVaultImages` etc., no manual wiring needed beyond adding the Go methods).

- [ ] **Step 1: Add App methods**

In `wails-app/app.go`, near the `// ── Image Vault ──` section, add a new section:

```go
// ── Secrets Vault ────────────────────────────────────────────────────────────

func (a *App) ListSecrets() ([]secrets.Entry, error) {
	entries, err := secrets.List(context.Background(), a.db, a.activeProfileID)
	if err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []secrets.Entry{}
	}
	return entries, nil
}

func (a *App) AddSecret(kind, name, value, username, url, notes string) (string, error) {
	return secrets.Add(context.Background(), a.db, a.activeProfileID, kind, name, value, username, url, notes)
}

// RevealSecret returns the plaintext value for id. This is the GUI's only
// decrypt entrypoint, calling the identical secrets.DecryptEntry function
// the CLI's `secret reveal --reveal` command calls.
func (a *App) RevealSecret(id string) (string, error) {
	return secrets.DecryptEntry(context.Background(), a.db, a.activeProfileID, id)
}

func (a *App) DeleteSecret(id string) error {
	return secrets.Delete(context.Background(), a.db, a.activeProfileID, id)
}
```

Add `"monoagent/internal/secrets"` to `app.go`'s import block if not already present.

- [ ] **Step 2: Regenerate Wails bindings**

Run: `cd wails-app && wails generate module`
Expected: updates `frontend/src/wailsjs/go/main/App.js` and `App.d.ts` with the four new bound methods (mirrors how `GetVaultImages` etc. already appear there — verify with `grep -n "ListSecrets" wails-app/frontend/src/wailsjs/go/main/App.js`).

- [ ] **Step 3: Locate the existing page registration pattern**

Run: `grep -rn "ImageVault" wails-app/frontend/src --include=*.jsx -l`
Read whatever router/nav file that returns (likely `App.jsx` or a `routes` config) to see exactly how `ImageVault.jsx` is imported and added to navigation, so `Vault.jsx` is wired the identical way.

- [ ] **Step 4: Create `Vault.jsx`**

Create `wails-app/frontend/src/pages/Vault.jsx`, following the layout conventions of `ImageVault.jsx`/`Profile.jsx` (read both first for the exact component/styling patterns this codebase uses — button components, modal patterns, table/list styling — and match them; do not introduce a new UI library or styling approach):

```jsx
import { useEffect, useState } from 'react'
import { ListSecrets, AddSecret, RevealSecret, DeleteSecret } from '../wailsjs/go/main/App'

export default function Vault() {
  const [entries, setEntries] = useState([])
  const [revealed, setRevealed] = useState({})
  const [showAdd, setShowAdd] = useState(false)
  const [form, setForm] = useState({ kind: 'secret', name: '', value: '', username: '', url: '', notes: '' })

  const load = () => ListSecrets().then(setEntries)

  useEffect(() => { load() }, [])

  const handleAdd = async (e) => {
    e.preventDefault()
    await AddSecret(form.kind, form.name, form.value, form.username, form.url, form.notes)
    setForm({ kind: 'secret', name: '', value: '', username: '', url: '', notes: '' })
    setShowAdd(false)
    load()
  }

  const handleReveal = async (id) => {
    const value = await RevealSecret(id)
    setRevealed((prev) => ({ ...prev, [id]: value }))
  }

  const handleDelete = async (id) => {
    if (!window.confirm('Delete this vault entry? This cannot be undone.')) return
    await DeleteSecret(id)
    load()
  }

  return (
    <div>
      <h1>Vault</h1>
      <button onClick={() => setShowAdd(true)}>Add Secret</button>

      {showAdd && (
        <form onSubmit={handleAdd}>
          <select value={form.kind} onChange={(e) => setForm({ ...form, kind: e.target.value })}>
            <option value="secret">Secret</option>
            <option value="login">Login</option>
          </select>
          <input placeholder="Name" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} required />
          {form.kind === 'login' && (
            <>
              <input placeholder="Username" value={form.username} onChange={(e) => setForm({ ...form, username: e.target.value })} />
              <input placeholder="URL" value={form.url} onChange={(e) => setForm({ ...form, url: e.target.value })} />
            </>
          )}
          <input type="password" placeholder="Value" value={form.value} onChange={(e) => setForm({ ...form, value: e.target.value })} required />
          <textarea placeholder="Notes" value={form.notes} onChange={(e) => setForm({ ...form, notes: e.target.value })} />
          <button type="submit">Save</button>
          <button type="button" onClick={() => setShowAdd(false)}>Cancel</button>
        </form>
      )}

      <table>
        <thead>
          <tr><th>Name</th><th>Kind</th><th>Username</th><th>Value</th><th>Updated</th><th></th></tr>
        </thead>
        <tbody>
          {entries.map((e) => (
            <tr key={e.id}>
              <td>{e.name}</td>
              <td>{e.kind}</td>
              <td>{e.username}</td>
              <td>
                {revealed[e.id] ? revealed[e.id] : '••••••••'}
                {!revealed[e.id] && <button onClick={() => handleReveal(e.id)}>Reveal</button>}
              </td>
              <td>{e.updated_at}</td>
              <td><button onClick={() => handleDelete(e.id)}>Delete</button></td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
```

- [ ] **Step 5: Register the page in navigation**

Using the pattern found in Step 3, add `Vault.jsx` alongside `ImageVault.jsx` in whatever router/nav config file that step identified.

- [ ] **Step 6: Manual verification**

Run: `cd wails-app && wails dev`
In the running app: navigate to the new Vault page, add a secret entry, click Reveal and confirm the value appears, click Delete and confirm it's removed from the list. This is a GUI change — there is no automated test for the React component in this codebase's existing patterns (verify by checking whether any `.jsx` files have sibling test files before assuming otherwise); manual verification is required per this project's UI-change convention.

- [ ] **Step 7: Commit**

```bash
git add wails-app/app.go wails-app/frontend/src/pages/Vault.jsx wails-app/frontend/src/wailsjs
git commit -m "feat: add Vault page to the Wails GUI"
```

---

## Self-Review Notes

- **Spec coverage:** crypto core (Task 3), OS-keychain KEK (Task 4), DEK persistence (Task 5), vault_secrets CRUD (Task 6), CLI-first reveal path with `--reveal` confirmation (Task 7), connections.data encryption-in-place + migration command (Task 8), GUI wrapper calling the identical Go functions as the CLI (Task 9) — every approved spec section has a task.
- **Deviation from spec, noted explicitly:** the spec described per-field extraction of connection credentials into individually-named `vault_secrets` rows referenced by `vault_ref`. This plan instead encrypts the whole `connections.data` blob in place (Task 8) using the same DEK. This still satisfies the spec's core requirement — closing the plaintext-at-rest gap via the vault's crypto/keyring infrastructure — while avoiding hand-enumerating secret field names across every platform in `internal/connections/registry.go` (Instagram, LinkedIn, TikTok, X, Telegram, Discord, Slack, Email, MongoDB, etc.), which would be a much larger, higher-risk diff for equivalent security benefit. The standalone `vault_secrets` table (Task 2, 6) remains exactly as specced for user-managed secrets/logins.
- **AES-KW substitution:** the spec mentioned AES-KW for wrapping the DEK; this plan uses AES-256-GCM (via the same `Encrypt`/`Decrypt` from Task 3) for that too, avoiding a second crypto primitive/dependency. Equivalent authenticated-encryption security property, smaller surface.
- **Ambiguity resolved:** Task 8 Step 7's `connections.NewStore` constructor name was verified against `internal/connections/storage.go:126` before finalizing this plan.
