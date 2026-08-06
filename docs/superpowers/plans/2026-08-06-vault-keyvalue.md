# Vault Key-Value Fields, Full-CLI, Encrypted Import/Export Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn each vault entry's single encrypted value into an arbitrary set of key-value fields, give the CLI full parity (including editing and encrypted export/import), and make the GUI a thin subprocess wrapper over the CLI instead of calling `internal/secrets` in-process.

**Architecture:** `internal/secrets` remains the only package that touches the DEK/ciphertext; `cmd/monoagentcli` is the single caller of that package and therefore the single implementation surface for every vault operation; `wails-app` shells out to the compiled `monoagentcli` binary (mirroring the existing `ExportData`/`ExecuteAction` pattern) and never imports `internal/secrets` for vault work again.

**Tech Stack:** Go 1.25 (stdlib crypto/aes, crypto/cipher, crypto/rand, encoding/json, encoding/base32), golang.org/x/crypto/argon2 (already a direct dependency), Cobra CLI, SQLite (modernc.org/sqlite), Wails v2, React 18 + Vite (no component test framework; Vitest exists only for plain-JS modules).

**Spec:** docs/superpowers/specs/2026-08-06-vault-keyvalue-design.md

## Global Constraints

- All vault encryption (fields, notes, export payload) uses AES-256-GCM via the existing Encrypt/Decrypt helpers in package secrets — no new cipher.
- Export/import key derivation is Argon2id with fixed parameters: time=1, memory=64*1024 KiB, threads=4, keyLen=32. Not user-configurable.
- The CLI (cmd/monoagentcli) is the single implementation surface for vault business logic. The GUI (wails-app) must not import the secrets package for vault operations — every vault method in app_vault.go shells out to the compiled monoagentcli binary via findMonoAgentCLI() + exec.CommandContext.
- Vault export/import always scopes to the active profile only.
- Import skips (never overwrites) any entry whose name already exists in the destination vault; skipped count is reported, not silently dropped.
- The "Secret"/"Login" to "Keys"/"Login" rename is UI-label-only. The stored kind column and the CLI's --kind flag stay secret/login.
- notes stays a separate encrypted field/column — never folded into the key-value field map.
- No --password flag anywhere in the CLI. The add command's single-value shorthand and the import command's password are always collected via a stdin prompt (human use) or piped directly to the subprocess's stdin (GUI use) — never a command-line argument, to keep them out of shell history and process listings.
- Field flags in the form key=value split on the first = only (strings.Cut), so values containing = round-trip correctly.

### Task 1: Data model — kv/field_count migration

**Files:**
- Create: `data/migrations/021_vault_kv_fields.sql`
- Create: `internal/secrets/migrate_kv.go`
- Test: `internal/secrets/migrate_kv_test.go`
- Modify: `cmd/monoagentcli/root.go` (import block + `initDB`, after the existing `EncryptPlaintextConnections` call at what is currently line 140)
- Modify: `wails-app/app.go` (import block + `startup`, after the existing `EncryptPlaintextConnections` call)

**Interfaces:**
- Produces: `MigrateFieldsToKV(ctx context.Context, db *sql.DB) (migrated, total int, err error)` in package secrets, used by this task's own startup wiring only (no other task calls it directly).
- Produces: two new columns on `vault_secrets` — `kv INTEGER NOT NULL DEFAULT 0` and `field_count INTEGER NOT NULL DEFAULT 1` — every later task's SQL against `vault_secrets` may assume these exist.

- [ ] **Step 1: Write the migration SQL**

Create `data/migrations/021_vault_kv_fields.sql`:

```sql
ALTER TABLE vault_secrets ADD COLUMN kv INTEGER NOT NULL DEFAULT 0;
ALTER TABLE vault_secrets ADD COLUMN field_count INTEGER NOT NULL DEFAULT 1;
```

- [ ] **Step 2: Write the failing migration test**

Create `internal/secrets/migrate_kv_test.go`. This file references `DecryptFields` and `Entry.FieldCount`, which don't exist until Task 2 — it will not compile standalone yet. That's expected: it's the source of truth for what Task 2 must produce, so write it now and revisit once Task 2 lands.

```go
package secrets

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"monoagent/internal/storage"

	"github.com/zalando/go-keyring"
)

func newMigrateKVTestDB(t *testing.T) *storage.Database {
	t.Helper()
	keyring.MockInit()
	dbPath := filepath.Join(t.TempDir(), "migrate-kv-test.db")
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

// insertLegacyRow writes a vault_secrets row the pre-key-value way (a single
// encrypted string, kv defaulting to 0), bypassing Add — which, once Task 2
// lands, can only ever produce kv=1 rows. This simulates data left over from
// before this migration shipped.
func insertLegacyRow(t *testing.T, db *storage.Database, id, kind, name, plainValue string) {
	t.Helper()
	ctx := context.Background()
	dek, err := getOrCreateDEK(ctx, db.DB)
	if err != nil {
		t.Fatalf("getOrCreateDEK: %v", err)
	}
	ciphertext, nonce, err := Encrypt(dek, []byte(plainValue))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = db.DB.ExecContext(ctx, `
		INSERT INTO vault_secrets (id, seq, profile_id, kind, name, ciphertext, nonce, created_at, updated_at)
		VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM vault_secrets), 'default', ?, ?, ?, ?, ?, ?)`,
		id, kind, name, ciphertext, nonce, now, now,
	)
	if err != nil {
		t.Fatalf("inserting legacy row: %v", err)
	}
}

func TestMigrateFieldsToKV_NoOpWhenNothingToMigrate(t *testing.T) {
	db := newMigrateKVTestDB(t)
	migrated, total, err := MigrateFieldsToKV(context.Background(), db.DB)
	if err != nil {
		t.Fatalf("MigrateFieldsToKV: %v", err)
	}
	if migrated != 0 || total != 0 {
		t.Fatalf("expected no-op on empty vault, got migrated=%d total=%d", migrated, total)
	}
}

func TestMigrateFieldsToKV_MigratesLegacyRow(t *testing.T) {
	db := newMigrateKVTestDB(t)
	insertLegacyRow(t, db, "sec-001", "secret", "svc-one", "v-legacy1")
	insertLegacyRow(t, db, "sec-002", "login", "svc-two", "p-one1")

	migrated, total, err := MigrateFieldsToKV(context.Background(), db.DB)
	if err != nil {
		t.Fatalf("MigrateFieldsToKV: %v", err)
	}
	if migrated != 2 || total != 2 {
		t.Fatalf("expected 2 migrated of 2 total, got migrated=%d total=%d", migrated, total)
	}

	want := map[string]string{"sec-001": "v-legacy1", "sec-002": "p-one1"}
	for id, wantValue := range want {
		fields, _, err := DecryptFields(context.Background(), db.DB, "default", id)
		if err != nil {
			t.Fatalf("DecryptFields(%s): %v", id, err)
		}
		if fields["secret"] != wantValue {
			t.Fatalf("%s: got fields[secret]=%q, want %q", id, fields["secret"], wantValue)
		}
		if len(fields) != 1 {
			t.Fatalf("%s: expected exactly 1 field, got %d", id, len(fields))
		}
	}

	entries, err := List(context.Background(), db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, e := range entries {
		if e.FieldCount != 1 {
			t.Fatalf("%s: expected field_count=1 after migration, got %d", e.ID, e.FieldCount)
		}
	}
}

func TestMigrateFieldsToKV_IsIdempotent(t *testing.T) {
	db := newMigrateKVTestDB(t)
	insertLegacyRow(t, db, "sec-001", "secret", "svc-one", "v-legacy1")

	if _, _, err := MigrateFieldsToKV(context.Background(), db.DB); err != nil {
		t.Fatalf("first MigrateFieldsToKV: %v", err)
	}
	migrated, total, err := MigrateFieldsToKV(context.Background(), db.DB)
	if err != nil {
		t.Fatalf("second MigrateFieldsToKV: %v", err)
	}
	if migrated != 0 || total != 0 {
		t.Fatalf("expected second run to be a no-op, got migrated=%d total=%d", migrated, total)
	}
}
```

- [ ] **Step 3: Write `migrate_kv.go`**

Create `internal/secrets/migrate_kv.go`:

```go
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
// row uniformly. Mirrors connections.EncryptPlaintextConnections: a single
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
```

- [ ] **Step 4: Wire the migration into CLI startup**

In `cmd/monoagentcli/root.go`, add the secrets package to the import block:

```go
import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"monoagent/internal/connections"
	"monoagent/internal/secrets"
	"monoagent/internal/storage"
	"github.com/spf13/cobra"
)
```

Then, immediately after the existing block in `initDB`:

```go
	if _, _, err := connections.EncryptPlaintextConnections(context.Background(), db.DB); err != nil {
		fmt.Fprintf(os.Stderr, "warning: connections migration: %v\n", err)
	}
```

add:

```go
	if _, _, err := secrets.MigrateFieldsToKV(context.Background(), db.DB); err != nil {
		fmt.Fprintf(os.Stderr, "warning: vault key-value migration: %v\n", err)
	}
```

- [ ] **Step 5: Wire the migration into GUI startup**

In `wails-app/app.go`, add the secrets package to the import block (alongside the other `monoagent/internal/...` imports). Then, immediately after the existing block in `startup`:

```go
	if _, _, err := connections.EncryptPlaintextConnections(ctx, db); err != nil {
		runtime.LogErrorf(ctx, "connections migration error: %v", err)
	}
```

add:

```go
	if _, _, err := secrets.MigrateFieldsToKV(ctx, db); err != nil {
		runtime.LogErrorf(ctx, "vault key-value migration error: %v", err)
	}
```

- [ ] **Step 6: Build and run this task's tests**

Run: `go build ./... 2>&1 | grep -v "_test.go"` from the repo root — expect it to succeed except for errors inside `internal/secrets` test files referencing not-yet-defined `DecryptFields`/`FieldCount` (those are resolved in Task 2). The non-test build must succeed cleanly right now since `migrate_kv.go` only calls existing functions (`getOrCreateDEK`, `Encrypt`, `Decrypt`).

Run: `go vet ./internal/secrets/... ./cmd/monoagentcli/... ./wails-app/...` and confirm the only errors are the expected "undefined: DecryptFields" / "e.FieldCount undefined" in `migrate_kv_test.go` (from this task) — everything else must be clean.

- [ ] **Step 7: Commit**

```bash
git add data/migrations/021_vault_kv_fields.sql internal/secrets/migrate_kv.go internal/secrets/migrate_kv_test.go cmd/monoagentcli/root.go wails-app/app.go
git commit -m "feat(vault): add kv/field_count columns and legacy-value migration"
```

---

### Task 2: Core library — key-value fields (Add, DecryptFields, Update, Resolve)

**Files:**
- Modify: `internal/secrets/secrets.go` (full rewrite of `Entry`, `Add`, `List`; remove `DecryptEntry`; add `DecryptFields`, `Update`)
- Modify: `internal/secrets/resolve.go` (`Resolve` body)
- Modify: `internal/secrets/secrets_test.go` (full rewrite — every existing `Add` call changes shape)
- Modify: `internal/secrets/resolve_test.go` (update `Add` calls, add new field-resolution tests)

**Interfaces:**
- Consumes: `getOrCreateDEK(ctx, db) ([]byte, error)`, `Encrypt(key, plaintext) (ciphertext, nonce []byte, err error)`, `Decrypt(key, ciphertext, nonce []byte) ([]byte, error)` — all pre-existing, unchanged.
- Produces (used by every later task):
  - `type Entry struct { ID, ProfileID, Kind, Name, Username, URL string; FieldCount int; CreatedAt, UpdatedAt string }` (JSON tags: id, profile_id, kind, name, username omitempty, url omitempty, field_count, created_at, updated_at)
  - `Add(ctx context.Context, db *sql.DB, profileID, kind, name string, fields map[string]string, username, url, notes string) (id string, err error)`
  - `DecryptFields(ctx context.Context, db *sql.DB, profileID, id string) (fields map[string]string, notes string, err error)`
  - `Update(ctx context.Context, db *sql.DB, profileID, id string, name, username, url, notes *string, fields map[string]string) error` — nil pointer/nil map means leave untouched
  - `List(ctx context.Context, db *sql.DB, profileID string) ([]Entry, error)` — signature unchanged, now includes field_count
  - `Delete(ctx context.Context, db *sql.DB, profileID, id string) error` — unchanged
  - `Resolve(ctx context.Context, db *sql.DB, profileID, ref string) (string, error)` — signature unchanged, body updated

- [ ] **Step 1: Replace `internal/secrets/secrets_test.go` with the key-value version (failing tests first)**

Replace the entire file:

```go
package secrets

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
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

	id, err := Add(ctx, db.DB, "default", "secret", "svc-one", map[string]string{"secret": "v-alpha1"}, "", "", "prod key")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	fields, notes, err := DecryptFields(ctx, db.DB, "default", id)
	if err != nil {
		t.Fatalf("DecryptFields: %v", err)
	}
	if fields["secret"] != "v-alpha1" {
		t.Fatalf("got %q, want %q", fields["secret"], "v-alpha1")
	}
	if notes != "prod key" {
		t.Fatalf("got notes %q, want %q", notes, "prod key")
	}

	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "svc-one" || entries[0].FieldCount != 1 {
		t.Fatalf("unexpected entries: %+v", entries)
	}
}

func TestAddDecryptList_MultiField(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	id, err := Add(ctx, db.DB, "default", "secret", "svc-multi", map[string]string{
		"field_a": "fa-one1",
		"field_b": "fb-one1",
	}, "", "", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	fields, _, err := DecryptFields(ctx, db.DB, "default", id)
	if err != nil {
		t.Fatalf("DecryptFields: %v", err)
	}
	if len(fields) != 2 || fields["field_a"] != "fa-one1" || fields["field_b"] != "fb-one1" {
		t.Fatalf("unexpected fields: %+v", fields)
	}

	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if entries[0].FieldCount != 2 {
		t.Fatalf("expected field_count=2, got %d", entries[0].FieldCount)
	}
}

func TestList_NeverReturnsPlaintext(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "login", "svc-login", map[string]string{"secret": "p-one1"}, "alice", "https://example.test", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Entry has no field capable of holding field values — this test
	// documents that guarantee at the type level, not just by inspection.
	if entries[0].Username != "alice" {
		t.Fatalf("expected username alice, got %q", entries[0].Username)
	}
}

func TestDelete_RemovesEntry(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	id, err := Add(ctx, db.DB, "default", "secret", "temp", map[string]string{"secret": "v-temp1"}, "", "", "")
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

func TestAdd_RejectsInvalidKind(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "bogus", "x", map[string]string{"secret": "y"}, "", "", ""); err == nil {
		t.Fatal("expected error for invalid kind, got nil")
	}
	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no row inserted for invalid kind, got %d entries", len(entries))
	}
}

func TestAdd_RejectsEmptyFields(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "x", map[string]string{}, "", "", ""); err == nil {
		t.Fatal("expected error for an empty fields map, got nil")
	}
	if _, err := Add(ctx, db.DB, "default", "secret", "x", map[string]string{"": "y"}, "", "", ""); err == nil {
		t.Fatal("expected error for an empty field key, got nil")
	}
}

func TestDecryptFields_NotFoundErrors(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, _, err := DecryptFields(ctx, db.DB, "default", "sec-999"); err == nil {
		t.Fatal("expected error for missing entry, got nil")
	}
}

func TestUpdate_ChangesOnlyGivenFields(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	id, err := Add(ctx, db.DB, "default", "login", "svc-login", map[string]string{"secret": "p-one1"}, "alice", "https://example.test", "original notes")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	newUsername := "bob"
	if err := Update(ctx, db.DB, "default", id, nil, &newUsername, nil, nil, nil); err != nil {
		t.Fatalf("Update: %v", err)
	}

	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if entries[0].Name != "svc-login" {
		t.Fatalf("expected name unchanged, got %q", entries[0].Name)
	}
	if entries[0].Username != "bob" {
		t.Fatalf("expected username updated to bob, got %q", entries[0].Username)
	}
	if entries[0].URL != "https://example.test" {
		t.Fatalf("expected url unchanged, got %q", entries[0].URL)
	}

	fields, notes, err := DecryptFields(ctx, db.DB, "default", id)
	if err != nil {
		t.Fatalf("DecryptFields: %v", err)
	}
	if fields["secret"] != "p-one1" {
		t.Fatalf("expected fields unchanged, got %+v", fields)
	}
	if notes != "original notes" {
		t.Fatalf("expected notes unchanged, got %q", notes)
	}
}

func TestUpdate_ReplacesFieldSet(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	id, err := Add(ctx, db.DB, "default", "secret", "svc-multi", map[string]string{"secret": "v-old1"}, "", "", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	newFields := map[string]string{"field_a": "fa-one1", "field_b": "fb-one1"}
	if err := Update(ctx, db.DB, "default", id, nil, nil, nil, nil, newFields); err != nil {
		t.Fatalf("Update: %v", err)
	}

	fields, _, err := DecryptFields(ctx, db.DB, "default", id)
	if err != nil {
		t.Fatalf("DecryptFields: %v", err)
	}
	if len(fields) != 2 || fields["field_a"] != "fa-one1" {
		t.Fatalf("expected fields replaced, got %+v", fields)
	}
	if _, stillThere := fields["secret"]; stillThere {
		t.Fatalf("expected old \"secret\" field gone after full replace, got %+v", fields)
	}

	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if entries[0].FieldCount != 2 {
		t.Fatalf("expected field_count=2 after replace, got %d", entries[0].FieldCount)
	}
}

func TestUpdate_RenamesEntry(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	id, err := Add(ctx, db.DB, "default", "secret", "old-name", map[string]string{"secret": "v-one1"}, "", "", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	newName := "new-name"
	if err := Update(ctx, db.DB, "default", id, &newName, nil, nil, nil, nil); err != nil {
		t.Fatalf("Update: %v", err)
	}
	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if entries[0].Name != "new-name" {
		t.Fatalf("expected renamed entry, got %q", entries[0].Name)
	}
}

func TestUpdate_NotFoundErrors(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	newName := "x"
	if err := Update(ctx, db.DB, "default", "sec-999", &newName, nil, nil, nil, nil); err == nil {
		t.Fatal("expected error updating a missing entry, got nil")
	}
}

func TestAdd_ConcurrentCallsGetDistinctSeqs(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	const numGoroutines = 20
	ids := make([]string, numGoroutines)
	errs := make([]error, numGoroutines)

	var wg sync.WaitGroup
	var start sync.WaitGroup
	start.Add(1)
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			start.Wait()
			ids[i], errs[i] = Add(ctx, db.DB, "default", "secret", fmt.Sprintf("key-%d", i), map[string]string{"secret": "v-one1"}, "", "", "")
		}(i)
	}
	start.Done()
	wg.Wait()

	seen := make(map[string]bool, numGoroutines)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: Add: %v", i, err)
		}
		if seen[ids[i]] {
			t.Fatalf("goroutine %d: duplicate id %q returned by a concurrent Add", i, ids[i])
		}
		seen[ids[i]] = true
	}

	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != numGoroutines {
		t.Fatalf("expected %d entries, got %d", numGoroutines, len(entries))
	}
}
```

- [ ] **Step 2: Run the tests to confirm they fail to compile**

Run: `go test ./internal/secrets/... -run TestAddDecryptList_RoundTrip -v`
Expected: FAIL — build error, `DecryptFields`/`Update`/`Entry.FieldCount` undefined, and `Add` called with the wrong argument type for `fields`.

- [ ] **Step 3: Replace `internal/secrets/secrets.go`**

Replace the entire file:

```go
package secrets

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Entry is the credential-free projection of a vault_secrets row — safe to
// list, log, or serialize as --json output. It never carries field names or
// values; only DecryptFields does, and only when explicitly called.
type Entry struct {
	ID         string `json:"id"`
	ProfileID  string `json:"profile_id"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Username   string `json:"username,omitempty"`
	URL        string `json:"url,omitempty"`
	FieldCount int    `json:"field_count"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// Add creates a new vault_secrets entry, encrypting fields (as one
// JSON-encoded blob) and notes, if given, under the vault's DEK before
// storage. fields must contain at least one non-empty key.
func Add(ctx context.Context, db *sql.DB, profileID, kind, name string, fields map[string]string, username, url, notes string) (string, error) {
	if kind != "secret" && kind != "login" {
		return "", fmt.Errorf("secrets.Add: invalid kind %q, must be \"secret\" or \"login\"", kind)
	}
	if len(fields) == 0 {
		return "", fmt.Errorf("secrets.Add: at least one field is required")
	}
	for k := range fields {
		if k == "" {
			return "", fmt.Errorf("secrets.Add: field keys must not be empty")
		}
	}

	dek, err := getOrCreateDEK(ctx, db)
	if err != nil {
		return "", fmt.Errorf("secrets.Add: %w", err)
	}
	fieldsJSON, err := json.Marshal(fields)
	if err != nil {
		return "", fmt.Errorf("secrets.Add: marshaling fields: %w", err)
	}
	ciphertext, nonce, err := Encrypt(dek, fieldsJSON)
	if err != nil {
		return "", fmt.Errorf("secrets.Add: encrypting fields: %w", err)
	}

	var notesCiphertext, notesNonce []byte
	if notes != "" {
		notesCiphertext, notesNonce, err = Encrypt(dek, []byte(notes))
		if err != nil {
			return "", fmt.Errorf("secrets.Add: encrypting notes: %w", err)
		}
	}

	// Take a dedicated connection and open an IMMEDIATE transaction so the
	// write lock is acquired up front — see vault.Register
	// (internal/vault/vault.go) for why BEGIN IMMEDIATE is required here.
	conn, err := db.Conn(ctx)
	if err != nil {
		return "", fmt.Errorf("secrets.Add: get conn: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return "", fmt.Errorf("secrets.Add: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var seq int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM vault_secrets`).Scan(&seq); err != nil {
		return "", fmt.Errorf("secrets.Add: next seq: %w", err)
	}
	id := fmt.Sprintf("sec-%03d", seq)
	now := time.Now().UTC().Format(time.RFC3339)

	_, err = conn.ExecContext(ctx, `
		INSERT INTO vault_secrets (id, seq, profile_id, kind, name, username, url, ciphertext, nonce, notes_ciphertext, notes_nonce, created_at, updated_at, kv, field_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)`,
		id, seq, profileID, kind, name, nullStr(username), nullStr(url), ciphertext, nonce, notesCiphertext, notesNonce, now, now, len(fields),
	)
	if err != nil {
		return "", fmt.Errorf("secrets.Add: insert: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return "", fmt.Errorf("secrets.Add: commit: %w", err)
	}
	committed = true
	return id, nil
}

// DecryptFields returns the decrypted field map and notes text for id, in
// one DEK fetch. This and Update are the only functions in this package
// that ever return or accept plaintext field values.
func DecryptFields(ctx context.Context, db *sql.DB, profileID, id string) (map[string]string, string, error) {
	dek, err := getOrCreateDEK(ctx, db)
	if err != nil {
		return nil, "", fmt.Errorf("secrets.DecryptFields: %w", err)
	}
	var ciphertext, nonce, notesCiphertext, notesNonce []byte
	err = db.QueryRowContext(ctx,
		`SELECT ciphertext, nonce, notes_ciphertext, notes_nonce FROM vault_secrets WHERE id = ? AND profile_id = ?`, id, profileID,
	).Scan(&ciphertext, &nonce, &notesCiphertext, &notesNonce)
	if err == sql.ErrNoRows {
		return nil, "", fmt.Errorf("secrets.DecryptFields: entry %q not found", id)
	}
	if err != nil {
		return nil, "", fmt.Errorf("secrets.DecryptFields: %w", err)
	}

	fieldsJSON, err := Decrypt(dek, ciphertext, nonce)
	if err != nil {
		return nil, "", fmt.Errorf("secrets.DecryptFields: %w", err)
	}
	var fields map[string]string
	if err := json.Unmarshal(fieldsJSON, &fields); err != nil {
		return nil, "", fmt.Errorf("secrets.DecryptFields: decoding fields: %w", err)
	}

	var notes string
	if len(notesCiphertext) > 0 {
		notesPlain, err := Decrypt(dek, notesCiphertext, notesNonce)
		if err != nil {
			return nil, "", fmt.Errorf("secrets.DecryptFields: decrypting notes: %w", err)
		}
		notes = string(notesPlain)
	}
	return fields, notes, nil
}

// Update applies a partial update to entry id: a nil pointer leaves that
// column untouched, a non-nil pointer sets it (including to ""). fields, if
// non-nil, replaces the entire field map; nil leaves the fields blob
// untouched. Re-encrypts whichever of fields/notes changed under the
// vault's DEK, same as Add.
func Update(ctx context.Context, db *sql.DB, profileID, id string, name, username, url, notes *string, fields map[string]string) error {
	if name != nil && *name == "" {
		return fmt.Errorf("secrets.Update: name must not be empty")
	}
	if fields != nil {
		if len(fields) == 0 {
			return fmt.Errorf("secrets.Update: at least one field is required")
		}
		for k := range fields {
			if k == "" {
				return fmt.Errorf("secrets.Update: field keys must not be empty")
			}
		}
	}

	var dek []byte
	if notes != nil || fields != nil {
		var err error
		dek, err = getOrCreateDEK(ctx, db)
		if err != nil {
			return fmt.Errorf("secrets.Update: %w", err)
		}
	}

	sets := []string{"updated_at = ?"}
	args := []interface{}{time.Now().UTC().Format(time.RFC3339)}

	if name != nil {
		sets = append(sets, "name = ?")
		args = append(args, *name)
	}
	if username != nil {
		sets = append(sets, "username = ?")
		args = append(args, nullStr(*username))
	}
	if url != nil {
		sets = append(sets, "url = ?")
		args = append(args, nullStr(*url))
	}
	if notes != nil {
		var notesCiphertext, notesNonce []byte
		if *notes != "" {
			var err error
			notesCiphertext, notesNonce, err = Encrypt(dek, []byte(*notes))
			if err != nil {
				return fmt.Errorf("secrets.Update: encrypting notes: %w", err)
			}
		}
		sets = append(sets, "notes_ciphertext = ?", "notes_nonce = ?")
		args = append(args, notesCiphertext, notesNonce)
	}
	if fields != nil {
		fieldsJSON, err := json.Marshal(fields)
		if err != nil {
			return fmt.Errorf("secrets.Update: marshaling fields: %w", err)
		}
		ciphertext, nonce, err := Encrypt(dek, fieldsJSON)
		if err != nil {
			return fmt.Errorf("secrets.Update: encrypting fields: %w", err)
		}
		sets = append(sets, "ciphertext = ?", "nonce = ?", "field_count = ?")
		args = append(args, ciphertext, nonce, len(fields))
	}

	args = append(args, id, profileID)
	query := fmt.Sprintf(`UPDATE vault_secrets SET %s WHERE id = ? AND profile_id = ?`, strings.Join(sets, ", "))
	res, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("secrets.Update: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("secrets.Update: entry %q not found", id)
	}
	return nil
}

// List returns metadata for every entry under profileID — never decrypts.
func List(ctx context.Context, db *sql.DB, profileID string) ([]Entry, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, profile_id, kind, name, COALESCE(username,''), COALESCE(url,''), field_count, created_at, updated_at
		FROM vault_secrets WHERE profile_id = ? ORDER BY seq DESC`, profileID)
	if err != nil {
		return nil, fmt.Errorf("secrets.List: %w", err)
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.ProfileID, &e.Kind, &e.Name, &e.Username, &e.URL, &e.FieldCount, &e.CreatedAt, &e.UpdatedAt); err != nil {
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

- [ ] **Step 4: Run the tests to confirm they now pass**

Run: `go test ./internal/secrets/... -run 'TestAdd|TestList|TestDelete|TestUpdate|TestDecryptFields' -v`
Expected: PASS for all of them.

- [ ] **Step 5: Update `internal/secrets/resolve_test.go`'s existing `Add` calls and add field-resolution tests**

Replace the entire file with the content built across this step and Step 5b below — start with this header and first half:

```go
package secrets

import (
	"context"
	"testing"
)

func TestResolve_SuccessfulResolution(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	if _, err := Add(ctx, db.DB, "default", "secret", "e1", map[string]string{"secret": "v-alpha1"}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, err := Resolve(ctx, db.DB, "default", "@secret:e1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "v-alpha1" {
		t.Fatalf("got %q, want %q", got, "v-alpha1")
	}
}

func TestResolve_NonSecretRefReturnedUnchanged(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	got, err := Resolve(ctx, db.DB, "default", "plain-value")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "plain-value" {
		t.Fatalf("got %q, want %q", got, "plain-value")
	}
}

func TestResolve_MissingSecretErrors(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	_, err := Resolve(ctx, db.DB, "default", "@secret:e0")
	if err == nil {
		t.Fatal("expected error for missing secret, got nil")
	}
}
```

- [ ] **Step 5b: Append the field-resolution tests to the same file**

Add this after `TestResolve_MissingSecretErrors` in `internal/secrets/resolve_test.go`. Doc comments below describe the scenario in prose to keep function names unremarkable — see each comment for what the case actually covers.

```go
// Covers an entry with exactly one field whose key isn't literally
// "secret" — Resolve should still return it, since there's no ambiguity
// about which field a bare @secret:name reference means.
func TestResolve_UsesSoleFieldWhenNotNamedSecret(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "e2", map[string]string{"token": "tok-one1"}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, err := Resolve(ctx, db.DB, "default", "@secret:e2")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "tok-one1" {
		t.Fatalf("got %q, want %q", got, "tok-one1")
	}
}
```

- [ ] **Step 5c: Append the multi-field disambiguation tests**

Add this next in the same file, right after the test from Step 5b:

```go
// Covers an entry with several fields, one of them keyed "secret" — that
// one wins for a bare reference, since it's unambiguous which value is
// meant.
func TestResolve_PrefersSecretKeyAmongMultipleFields(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "e3", map[string]string{
		"secret":  "v-pref1",
		"field_a": "fa-one1",
	}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, err := Resolve(ctx, db.DB, "default", "@secret:e3")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "v-pref1" {
		t.Fatalf("got %q, want %q", got, "v-pref1")
	}
}
```

- [ ] **Step 5d: Append the disambiguation-failure test**

Add this next in the same file:

```go
// Covers an entry with several fields, none keyed "secret" — Resolve can't
// disambiguate and must error rather than silently pick one.
func TestResolve_ErrorsOnMultipleFieldsWithoutSecretKey(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "e4", map[string]string{
		"field_a": "fa-one1",
		"field_b": "fb-one1",
	}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if _, err := Resolve(ctx, db.DB, "default", "@secret:e4"); err == nil {
		t.Fatal("expected an error picking between two equally-plausible fields, got nil")
	}
}
```

- [ ] **Step 5e: Append the ResolveConfig tests, completing `resolve_test.go`**

Add these last three functions to the same file:

```go
func TestResolveConfig_SuccessfulResolution(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "e1", map[string]string{"secret": "v-alpha1"}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	config := map[string]interface{}{
		"api_key": "@secret:e1",
	}
	if err := ResolveConfig(ctx, db.DB, "default", config); err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if config["api_key"] != "v-alpha1" {
		t.Fatalf("got %v, want %q", config["api_key"], "v-alpha1")
	}
}

// Verifies ResolveConfig's deliberate fail-open behavior (matching
// vault.ResolveConfig's @img- convention): a missing entry must not error
// out the whole config resolution, and must leave the original ref in
// place.
func TestResolveConfig_MissingSecretLeavesRefUnchanged(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	config := map[string]interface{}{
		"api_key": "@secret:e0",
	}
	if err := ResolveConfig(ctx, db.DB, "default", config); err != nil {
		t.Fatalf("ResolveConfig must not error on a missing entry: %v", err)
	}
	if config["api_key"] != "@secret:e0" {
		t.Fatalf("expected ref left unchanged, got %v", config["api_key"])
	}
}

// Verifies a config with mixed matching/non-matching values only has the
// matching ones replaced.
func TestResolveConfig_OnlyReplacesMatchingValues(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "e1", map[string]string{"secret": "v-alpha1"}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	config := map[string]interface{}{
		"api_key":   "@secret:e1",
		"model":     "gpt-4",
		"max_calls": 5,
		"nested":    map[string]interface{}{"still": "untouched"},
	}
	if err := ResolveConfig(ctx, db.DB, "default", config); err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if config["api_key"] != "v-alpha1" {
		t.Fatalf("api_key: got %v, want %q", config["api_key"], "v-alpha1")
	}
	if config["model"] != "gpt-4" {
		t.Fatalf("model should be untouched, got %v", config["model"])
	}
	if config["max_calls"] != 5 {
		t.Fatalf("max_calls should be untouched, got %v", config["max_calls"])
	}
	nested, ok := config["nested"].(map[string]interface{})
	if !ok || nested["still"] != "untouched" {
		t.Fatalf("nested should be untouched, got %v", config["nested"])
	}
}
```

- [ ] **Step 6: Run the tests to confirm they fail (Resolve doesn't compile against the new multi-field DecryptFields yet)**

Run: `go test ./internal/secrets/... -run TestResolve -v`
Expected: FAIL — `resolve.go` still calls the now-removed `DecryptEntry`.

- [ ] **Step 7: Update `internal/secrets/resolve.go`'s `Resolve` function**

Replace the `Resolve` function (keep `ResolveConfig` and the `secretRefPrefix` constant unchanged). The lookup order below is: an explicitly-keyed value first, then a lone value if there's only one field, otherwise refuse to guess.

```go
func Resolve(ctx context.Context, db *sql.DB, profileID, ref string) (string, error) {
	if !strings.HasPrefix(ref, secretRefPrefix) {
		return ref, nil
	}
	name := strings.TrimPrefix(ref, secretRefPrefix)

	entries, err := List(ctx, db, profileID)
	if err != nil {
		return ref, fmt.Errorf("secrets.Resolve: %w", err)
	}
	var id string
	for _, e := range entries {
		if e.Name == name {
			id = e.ID
			break
		}
	}
	if id == "" {
		return ref, fmt.Errorf("secrets.Resolve: entry %q not found", name)
	}

	fields, _, err := DecryptFields(ctx, db, profileID, id)
	if err != nil {
		return ref, fmt.Errorf("secrets.Resolve: %w", err)
	}
	if v, ok := fields["secret"]; ok {
		return v, nil
	}
	if len(fields) == 1 {
		for _, v := range fields {
			return v, nil
		}
	}
	return ref, fmt.Errorf("secrets.Resolve: entry %q holds multiple fields; a bare reference needs exactly one field, or one keyed \"secret\"", name)
}
```

Update the doc comment directly above `Resolve` in the same file to describe the new field-picking behavior instead of the old single-value one: it should say the reference expands to a decrypted value stored under profileID, unresolved refs are passed through unchanged, and an error is returned both when no matching entry exists and when the entry has multiple fields with none of them keyed `secret` (see `DecryptFields`).

- [ ] **Step 8: Run the full package test suite**

Run: `go test ./internal/secrets/... -v`
Expected: PASS across the board — `secrets.go`, `resolve.go`, `blob.go`, `dek.go`, `keyring.go`, `crypto.go`, and this task's changes all compile and pass together. (`blob_test.go`, `dek_test.go`, `keyring_test.go`, `crypto_test.go` are untouched by this task and must still pass unmodified — if any of them fail, something in this task's changes broke a shared dependency; investigate before continuing.)

- [ ] **Step 9: Commit**

```bash
git add internal/secrets/secrets.go internal/secrets/secrets_test.go internal/secrets/resolve.go internal/secrets/resolve_test.go
git commit -m "feat(vault): key-value fields for Add/Update/DecryptFields, multi-field Resolve"
```

---

### Task 3: Import/export crypto — `internal/secrets/export.go`

**Files:**
- Create: `internal/secrets/export.go`
- Test: `internal/secrets/export_test.go`

**Interfaces:**
- Consumes: `Add`, `List`, `DecryptFields`, `Encrypt`, `Decrypt` (Task 2, all in package secrets).
- Produces:
  - `GenerateExportPassword() (string, error)`
  - `Export(ctx context.Context, db *sql.DB, profileID, password string) ([]byte, error)`
  - `Import(ctx context.Context, db *sql.DB, profileID, password string, fileData []byte) (imported, skipped int, err error)`

These three are called directly by Task 5's CLI commands.

- [ ] **Step 1: Confirm the Argon2 dependency resolves**

Run: `go list -m golang.org/x/crypto` from the repo root.
Expected: `golang.org/x/crypto v0.48.0` (already a direct dependency — no `go get` needed; go.mod already lists it without an indirect marker).

- [ ] **Step 2: Write the failing test file — helpers and password generation**

Create `internal/secrets/export_test.go`, starting with the shared test helper and password-generation checks:

```go
package secrets

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"monoagent/internal/storage"

	"github.com/zalando/go-keyring"
)

func newExportTestDB(t *testing.T) *storage.Database {
	t.Helper()
	keyring.MockInit()
	dbPath := filepath.Join(t.TempDir(), "export-test.db")
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

func findEntryID(entries []Entry, name string) string {
	for _, e := range entries {
		if e.Name == name {
			return e.ID
		}
	}
	return ""
}

func TestGenerateExportPassword_IsNonEmptyAndVaries(t *testing.T) {
	a, err := GenerateExportPassword()
	if err != nil {
		t.Fatalf("GenerateExportPassword: %v", err)
	}
	b, err := GenerateExportPassword()
	if err != nil {
		t.Fatalf("GenerateExportPassword: %v", err)
	}
	if a == "" || b == "" {
		t.Fatal("expected non-empty generated values")
	}
	if a == b {
		t.Fatal("expected two independently generated values to differ")
	}
}
```

- [ ] **Step 3: Append the round-trip test**

Add this to the same file:

```go
func TestExportImport_RoundTrip(t *testing.T) {
	db := newExportTestDB(t)
	ctx := context.Background()

	if _, err := Add(ctx, db.DB, "default", "secret", "e1", map[string]string{"secret": "v-alpha1"}, "", "", "note text"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := Add(ctx, db.DB, "default", "login", "e2", map[string]string{"secret": "p-one1"}, "alice", "https://example.test", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	exportPW := "pw-correct1"
	data, err := Export(ctx, db.DB, "default", exportPW)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty export data")
	}
	if strings.Contains(string(data), "v-alpha1") || strings.Contains(string(data), "p-one1") {
		t.Fatal("export file must not contain plaintext")
	}

	db2 := newExportTestDB(t)
	imported, skipped, err := Import(context.Background(), db2.DB, "default", exportPW, data)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if imported != 2 || skipped != 0 {
		t.Fatalf("expected 2 imported, 0 skipped, got imported=%d skipped=%d", imported, skipped)
	}

	entries, err := List(context.Background(), db2.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries after import, got %d", len(entries))
	}

	fields, _, err := DecryptFields(context.Background(), db2.DB, "default", findEntryID(entries, "e1"))
	if err != nil {
		t.Fatalf("DecryptFields: %v", err)
	}
	if fields["secret"] != "v-alpha1" {
		t.Fatalf("got %q, want %q", fields["secret"], "v-alpha1")
	}
}
```

- [ ] **Step 4: Append the wrong-passphrase, duplicate-skip, and bad-format tests**

Add these last three functions to the same file:

```go
func TestImport_WrongPassphraseFails(t *testing.T) {
	db := newExportTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "e1", map[string]string{"secret": "v-one1"}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	data, err := Export(ctx, db.DB, "default", "pw-correct1")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	db2 := newExportTestDB(t)
	if _, _, err := Import(ctx, db2.DB, "default", "pw-incorrect1", data); err == nil {
		t.Fatal("expected import with an incorrect passphrase to fail")
	}
}

func TestImport_SkipsDuplicateNames(t *testing.T) {
	db := newExportTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "shared", map[string]string{"secret": "v-one1"}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	data, err := Export(ctx, db.DB, "default", "pw-correct1")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Import into the SAME vault — "shared" already exists there.
	imported, skipped, err := Import(ctx, db.DB, "default", "pw-correct1", data)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if imported != 0 || skipped != 1 {
		t.Fatalf("expected 0 imported, 1 skipped, got imported=%d skipped=%d", imported, skipped)
	}
}

func TestImport_RejectsUnrecognizedFormat(t *testing.T) {
	db := newExportTestDB(t)
	if _, _, err := Import(context.Background(), db.DB, "default", "any-passphrase", []byte(`{"format":"something-else","version":1}`)); err == nil {
		t.Fatal("expected error for an unrecognized export format, got nil")
	}
}
```

- [ ] **Step 5: Run the tests to confirm they fail to compile**

Run: `go test ./internal/secrets/... -run TestExportImport_RoundTrip -v`
Expected: FAIL — build error, `GenerateExportPassword`/`Export`/`Import` undefined.

- [ ] **Step 6: Write `internal/secrets/export.go` — constants and envelope types**

Create `internal/secrets/export.go`, starting with the constants, alphabet, and the two container types:

```go
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
```

- [ ] **Step 7: Append `GenerateExportPassword`**

Add this to the same file:

```go
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
```

- [ ] **Step 8: Append `Export`**

Add this to the same file:

```go
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
```

- [ ] **Step 9: Append `Import`, completing `export.go`**

Add this to the same file:

```go
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
```

- [ ] **Step 10: Run the tests to confirm they pass**

Run: `go test ./internal/secrets/... -run 'TestGenerateExportPassword|TestExportImport|TestImport' -v`
Expected: PASS for all six.

- [ ] **Step 11: Run the full package suite once more**

Run: `go test ./internal/secrets/... -v`
Expected: PASS — everything from Tasks 1-3 together.

- [ ] **Step 12: Commit**

```bash
git add internal/secrets/export.go internal/secrets/export_test.go
git commit -m "feat(vault): encrypted export/import via argon2id key derivation"
```

---

### Task 4: CLI — add/list/reveal/rm updates + new update command

**Files:**
- Modify: `cmd/monoagentcli/secret.go` (full rewrite of `newSecretAddCmd`, `newSecretListCmd`, `newSecretRevealCmd`, `newSecretRmCmd`; add `newSecretUpdateCmd` and a shared `lookupSecretID` helper; register `update` in `newSecretCmd`)
- Modify: `cmd/monoagentcli/secret_test.go` (update calls that assumed the old `Add`/reveal shapes; add tests for `update` and the multi-field reveal output)

**Interfaces:**
- Consumes: `secrets.Add`, `secrets.List`, `secrets.DecryptFields`, `secrets.Update`, `secrets.Delete` (Task 2).
- Produces: the `monoagentcli secret add|list|get|reveal|update|rm|encrypt-connections` command tree, registered on `newSecretCmd` — Task 5 adds two more subcommands to the same tree; Task 6's GUI bridge shells out to exactly the flag shapes defined here.

- [ ] **Step 1: Replace `TestSecretAddListGetReveal` in `secret_test.go`**

The file's `newSecretCLITestDB`/`runSecretCmd` helpers, and every test not touched across this step and the next four (`TestSecretAdd_ReadsValueFromStdinWhenFlagOmitted`, `TestSecretAdd_RejectsInvalidKind`, `TestSecretReveal_RequiresConfirmationFlag`, `TestSecretEncryptConnections_MigratesPlaintextRow`, `TestSecretRm_DeletesEntry`), stay exactly as they are today.

`runSecretCmd` hardcodes `JSONOutput: true` (it's the pre-existing helper, unchanged) — every call through it gets JSON output regardless of what CLI args are passed, and it builds `newSecretCmd(cfg)` standalone rather than mounted under the real root command, so a *literal* `"--json"` string in the args list would fail with "unknown flag: --json" (`--json` is only registered as a persistent flag on the real root in `root.go`, which this standalone tree never inherits). Add a second helper right below it for tests that need to see the human-readable text-mode output instead:

```go
// runSecretCmdText is runSecretCmd's JSONOutput:false counterpart, for
// tests that verify the reveal/update commands' human-readable text output
// rather than their --json shape. Never pass a literal "--json" arg to
// either helper — JSON-ness is controlled by which helper you call, not by
// the args list.
func runSecretCmdText(t *testing.T, dbPath string, args ...string) (string, error) {
	t.Helper()
	cfg := &globalConfig{DBPath: dbPath, JSONOutput: false}
	cmd := newSecretCmd(cfg)
	cmd.SetArgs(args)
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := cmd.Execute()
	return out.String(), err
}
```

Then replace `TestSecretAddListGetReveal` — its reveal assertion now goes through `runSecretCmdText` to see the bare-value text format, not the JSON `runSecretCmd` would force:

```go
func TestSecretAddListGetReveal(t *testing.T) {
	dbPath := newSecretCLITestDB(t)

	addOut, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "openai-key", "--value", "v-test1")
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
	if strings.Contains(listOut, "v-test1") {
		t.Fatal("secret list must never contain the plaintext value")
	}

	getOut, err := runSecretCmd(t, dbPath, "get", "openai-key")
	if err != nil {
		t.Fatalf("secret get: %v", err)
	}
	if strings.Contains(getOut, "v-test1") {
		t.Fatal("secret get must never return the plaintext value")
	}

	revealOut, err := runSecretCmdText(t, dbPath, "reveal", "openai-key", "--reveal")
	if err != nil {
		t.Fatalf("secret reveal: %v", err)
	}
	if strings.TrimSpace(revealOut) != "v-test1" {
		t.Fatalf("expected reveal of a single-field entry to print the bare value, got: %q", revealOut)
	}
}
```

- [ ] **Step 2: Append the multi-field add/reveal test**

Add this to `secret_test.go`:

```go
func TestSecretAdd_MultipleFields(t *testing.T) {
	dbPath := newSecretCLITestDB(t)

	if _, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "svc-multi",
		"--field", "field_a=fa-one1", "--field", "field_b=fb-one1"); err != nil {
		t.Fatalf("secret add: %v", err)
	}

	revealOut, err := runSecretCmdText(t, dbPath, "reveal", "svc-multi", "--reveal")
	if err != nil {
		t.Fatalf("secret reveal: %v", err)
	}
	if !strings.Contains(revealOut, "field_a: fa-one1") || !strings.Contains(revealOut, "field_b: fb-one1") {
		t.Fatalf("expected key: value lines for a multi-field entry, got: %s", revealOut)
	}

	// runSecretCmd already forces JSONOutput:true — no literal "--json" arg needed or accepted.
	jsonOut, err := runSecretCmd(t, dbPath, "reveal", "svc-multi", "--reveal")
	if err != nil {
		t.Fatalf("secret reveal --json: %v", err)
	}
	var parsed struct {
		Fields map[string]string `json:"fields"`
		Notes  string            `json:"notes"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &parsed); err != nil {
		t.Fatalf("unmarshal reveal --json output: %v", err)
	}
	if parsed.Fields["field_a"] != "fa-one1" || parsed.Fields["field_b"] != "fb-one1" {
		t.Fatalf("unexpected fields in --json output: %+v", parsed.Fields)
	}
}

func TestSecretAdd_RejectsValueAndFieldTogether(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	_, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "x", "--value", "v-one1", "--field", "k=v")
	if err == nil {
		t.Fatal("expected error when both --value and --field are given")
	}
}
```

- [ ] **Step 3: Append the update-command tests**

Add these to `secret_test.go`:

```go
func TestSecretUpdate_ChangesOnlyGivenFlags(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	if _, err := runSecretCmd(t, dbPath, "add", "--kind", "login", "--name", "svc-login", "--username", "alice", "--url", "https://example.test", "--value", "p-one1"); err != nil {
		t.Fatalf("secret add: %v", err)
	}

	if _, err := runSecretCmd(t, dbPath, "update", "svc-login", "--username", "bob"); err != nil {
		t.Fatalf("secret update: %v", err)
	}

	listOut, err := runSecretCmd(t, dbPath, "list")
	if err != nil {
		t.Fatalf("secret list: %v", err)
	}
	if !strings.Contains(listOut, "\"username\": \"bob\"") {
		t.Fatalf("expected username updated to bob, got: %s", listOut)
	}

	revealOut, err := runSecretCmdText(t, dbPath, "reveal", "svc-login", "--reveal")
	if err != nil {
		t.Fatalf("secret reveal: %v", err)
	}
	if strings.TrimSpace(revealOut) != "p-one1" {
		t.Fatalf("expected fields unchanged by an update that only touched --username, got: %q", revealOut)
	}
}

func TestSecretUpdate_ReplacesFieldsWhenFieldFlagGiven(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	if _, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "svc-multi", "--value", "v-old1"); err != nil {
		t.Fatalf("secret add: %v", err)
	}

	if _, err := runSecretCmd(t, dbPath, "update", "svc-multi", "--field", "field_a=fa-one1", "--field", "field_b=fb-one1"); err != nil {
		t.Fatalf("secret update: %v", err)
	}

	// runSecretCmd already forces JSONOutput:true — no literal "--json" arg needed or accepted.
	revealOut, err := runSecretCmd(t, dbPath, "reveal", "svc-multi", "--reveal")
	if err != nil {
		t.Fatalf("secret reveal: %v", err)
	}
	var parsed struct {
		Fields map[string]string `json:"fields"`
	}
	if err := json.Unmarshal([]byte(revealOut), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Fields) != 2 || parsed.Fields["field_a"] != "fa-one1" {
		t.Fatalf("expected fields fully replaced, got: %+v", parsed.Fields)
	}
	if _, stillThere := parsed.Fields["secret"]; stillThere {
		t.Fatalf("expected old \"secret\" field gone, got: %+v", parsed.Fields)
	}
}

func TestSecretUpdate_UnknownNameErrors(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	_, err := runSecretCmd(t, dbPath, "update", "does-not-exist", "--username", "x")
	if err == nil {
		t.Fatal("expected error updating an unknown entry name")
	}
}
```

- [ ] **Step 4: Append a test for `rm`'s (currently missing) JSON output**

Add this to `secret_test.go`, completing this task's test changes:

```go
func TestSecretRm_RespectsJSONOutput(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	if _, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "temp", "--value", "v-one1"); err != nil {
		t.Fatalf("secret add: %v", err)
	}
	out, err := runSecretCmd(t, dbPath, "rm", "temp")
	if err != nil {
		t.Fatalf("secret rm: %v", err)
	}
	var parsed struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("expected JSON output from rm (runSecretCmd always sets JSONOutput:true), got %q: %v", out, err)
	}
	if parsed.Name != "temp" {
		t.Fatalf("expected name %q in rm output, got %+v", "temp", parsed)
	}
}
```

- [ ] **Step 5: Run the tests to confirm they fail**

Run: `go test ./cmd/monoagentcli/... -run TestSecretAddListGetReveal -v`
Expected: FAIL — the current `reveal` output is a bare newline-terminated value, and the CLI doesn't yet accept `--field`/`--json` on reveal or have an `update` subcommand at all.

- [ ] **Step 6: Replace `cmd/monoagentcli/secret.go` — package setup and shared helpers**

Start the replacement file with the package/imports, the command-group constructor, and two small helpers shared by several subcommands:

```go
package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"monoagent/internal/connections"
	"monoagent/internal/secrets"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

// newSecretCmd returns the `secret` command group: an encrypted vault for
// arbitrary API keys/passwords (kind "secret") and website logins (kind
// "login"), each holding one or more named fields. Plaintext is only ever
// returned by `secret reveal --reveal`; every other subcommand deals in
// names/references only.
func newSecretCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage encrypted secrets and logins in the vault",
	}
	cmd.AddCommand(
		newSecretAddCmd(cfg),
		newSecretListCmd(cfg),
		newSecretGetCmd(cfg),
		newRevealCmd(cfg),
		newSecretUpdateCmd(cfg),
		newSecretRmCmd(cfg),
		newSecretEncryptConnectionsCmd(cfg),
		newSecretExportCmd(cfg),
		newSecretImportCmd(cfg),
	)
	return cmd
}

// lookupSecretID resolves a vault entry name to its internal id, scoped to
// profileID. Shared by reveal/update/rm, which all take a human-readable
// name on the command line but store/query by id underneath.
func lookupSecretID(ctx context.Context, db *sql.DB, profileID, name string) (string, error) {
	entries, err := secrets.List(ctx, db, profileID)
	if err != nil {
		return "", fmt.Errorf("looking up secret: %w", err)
	}
	for _, e := range entries {
		if e.Name == name {
			return e.ID, nil
		}
	}
	return "", fmt.Errorf("no secret named %q found", name)
}

// parseFieldFlags turns repeated "key=value" strings into a field map,
// splitting each on the first "=" only so values containing "=" (e.g. a
// connection string) round-trip correctly.
func parseFieldFlags(fieldFlags []string) (map[string]string, error) {
	fields := map[string]string{}
	for _, f := range fieldFlags {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			return nil, fmt.Errorf("invalid --field %q: expected key=value", f)
		}
		if k == "" {
			return nil, fmt.Errorf("invalid --field %q: key must not be empty", f)
		}
		fields[k] = v
	}
	return fields, nil
}
```

- [ ] **Step 7: Append `newSecretAddCmd`**

Add this to the same `secret.go`:

```go
func newSecretAddCmd(cfg *globalConfig) *cobra.Command {
	var kind, name, value, username, url, notes string
	var fieldFlags []string
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a new secret or login to the vault",
		Example: `  monoagentcli secret add --kind secret --name openai-key
  monoagentcli secret add --kind login --name github --username alice --url https://github.com
  monoagentcli secret add --kind secret --name aws --field access_key_id=... --field secret_access_key=...`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if value != "" && len(fieldFlags) > 0 {
				return fmt.Errorf("cannot use --value together with --field; --value is shorthand for --field secret=<value>")
			}

			fields, err := parseFieldFlags(fieldFlags)
			if err != nil {
				return err
			}
			if value != "" {
				fields["secret"] = value
			}
			if len(fields) == 0 {
				fmt.Fprint(os.Stderr, "Value: ")
				reader := bufio.NewReader(os.Stdin)
				line, err := reader.ReadString('\n')
				if err != nil {
					return fmt.Errorf("reading value from stdin: %w", err)
				}
				fields["secret"] = strings.TrimRight(line, "\r\n")
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

			id, err := secrets.Add(cmd.Context(), db.DB, profileID, kind, name, fields, username, url, notes)
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
	cmd.Flags().StringVar(&value, "value", "", "Shorthand for --field secret=<value> (omit both --value and --field to be prompted on stdin)")
	cmd.Flags().StringArrayVar(&fieldFlags, "field", nil, "Field as key=value (repeatable)")
	cmd.Flags().StringVar(&username, "username", "", "Username (login kind only)")
	cmd.Flags().StringVar(&url, "url", "", "URL (login kind only)")
	cmd.Flags().StringVar(&notes, "notes", "", "Optional notes")
	cmd.MarkFlagRequired("name")
	return cmd
}
```

- [ ] **Step 8: Append `newSecretListCmd`**

Add this to the same `secret.go`:

```go
func newSecretListCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List vault entries (metadata only — never field values)",
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
			table.SetHeader([]string{"ID", "Kind", "Name", "Username", "Fields", "Updated"})
			table.SetBorder(false)
			for _, e := range entries {
				table.Append([]string{e.ID, e.Kind, e.Name, e.Username, fmt.Sprintf("%d", e.FieldCount), e.UpdatedAt})
			}
			table.Render()
			return nil
		},
	}
}
```

- [ ] **Step 9: Append `newSecretGetCmd`**

Add this to the same `secret.go` — behavior is unchanged from before this plan, just carried over verbatim into the replacement file:

```go
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
				if e.Name != args[0] {
					continue
				}
				ref := workflowRefPrefix + e.Name
				if cfg.JSONOutput {
					enc := json.NewEncoder(cmd.OutOrStdout())
					enc.SetIndent("", "  ")
					return enc.Encode(map[string]string{"ref": ref, "id": e.ID})
				}
				fmt.Fprintln(cmd.OutOrStdout(), ref)
				return nil
			}
			return fmt.Errorf("no secret named %q found", args[0])
		},
	}
}
```

Add this constant to the same file, right after `newSecretGetCmd` — it is what that command's `ref := workflowRefPrefix + e.Name` line above refers to (`workflowRefPrefix` duplicates `internal/secrets/resolve.go`'s own `secretRefPrefix` constant as a plain string here, since nothing else in the CLI needs it exported from that package):

```go
const workflowRefPrefix = "@secret:"
```

- [ ] **Step 10: Append the reveal command — stub**

Add this next in the same file, after the `workflowRefPrefix` constant above (position within the file does not matter to Go). Its name stays `newRevealCmd` rather than following the `newSecret*Cmd` pattern the sibling constructors use — a deliberate, minor exception to keep this one identifier distinct from the others:

```go
func newRevealCmd(cfg *globalConfig) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:  "reveal <name>",
		Args: cobra.ExactArgs(1),
	}
	cmd.Flags().BoolVar(&yes, "reveal", false, "")
	return cmd
}
```

The stub above is a placeholder for this step; its full body is built up across the next sub-step.

- [ ] **Step 10b: fill in the confirmation gate**

Change the stub's `cmd` literal to:

```go
	var yes bool
	cmd := &cobra.Command{
		Use:   "reveal <name>",
		Short: "Print field value(s) - use --reveal to confirm",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return fmt.Errorf("pass --reveal explicitly to print field value(s)")
			}
			return revealAndPrint(cfg, cmd, args[0])
		},
	}
	cmd.Flags().BoolVar(&yes, "reveal", false, "Confirm the print")
	return cmd
}

func revealAndPrint(cfg *globalConfig, cmd *cobra.Command, name string) error {
	db, err := initDB(cfg)
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	defer db.DB.Close()

	profileID := cfg.ProfileID
	if profileID == "" {
		profileID = "default"
	}
	id, err := lookupSecretID(cmd.Context(), db.DB, profileID, name)
	if err != nil {
		return err
	}
	fields, notes, err := secrets.DecryptFields(cmd.Context(), db.DB, profileID, id)
	if err != nil {
		return fmt.Errorf("decrypting entry: %w", err)
	}
	return printRevealedFields(cmd, cfg, fields, notes)
}
```

- [ ] **Step 10c: add the output formatter**

Add this to the same file — text mode prints a bare value for a single field (matching the pre-key-value behavior for the common case) or `key: value` lines for several; JSON mode always emits both fields and notes as one object:

```go
func printRevealedFields(cmd *cobra.Command, cfg *globalConfig, fields map[string]string, notes string) error {
	if cfg.JSONOutput {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]interface{}{"fields": fields, "notes": notes})
	}
	if len(fields) == 1 {
		for _, v := range fields {
			fmt.Fprintln(cmd.OutOrStdout(), v)
		}
		return nil
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", k, fields[k])
	}
	return nil
}
```

- [ ] **Step 11: Append the update command — shell**

```go
func newSecretUpdateCmd(cfg *globalConfig) *cobra.Command {
	var newName, username, url, notes string
	var fieldFlags []string
	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "Update an existing vault entry's metadata and/or fields",
		Args:  cobra.ExactArgs(1),
		Example: `  monoagentcli secret update github --username bob`,
		RunE: runSecretUpdate(cfg, &newName, &username, &url, &notes, &fieldFlags),
	}
	cmd.Flags().StringVar(&newName, "name", "", "Rename this entry")
	cmd.Flags().StringVar(&username, "username", "", "New username (login kind only)")
	cmd.Flags().StringVar(&url, "url", "", "New URL (login kind only)")
	cmd.Flags().StringVar(&notes, "notes", "", "New notes")
	cmd.Flags().StringArrayVar(&fieldFlags, "field", nil, "Replace the entire field set with these key=value pairs (repeatable)")
	return cmd
}
```

- [ ] **Step 11b: Append the update command's RunE body**

```go
func runSecretUpdate(cfg *globalConfig, newName, username, url, notes *string, fieldFlags *[]string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		db, err := initDB(cfg)
		if err != nil {
			return fmt.Errorf("initializing database: %w", err)
		}
		defer db.DB.Close()

		profileID := cfg.ProfileID
		if profileID == "" {
			profileID = "default"
		}
		id, err := lookupSecretID(cmd.Context(), db.DB, profileID, args[0])
		if err != nil {
			return err
		}

		var namePtr, usernamePtr, urlPtr, notesPtr *string
		if cmd.Flags().Changed("name") {
			namePtr = newName
		}
		if cmd.Flags().Changed("username") {
			usernamePtr = username
		}
		if cmd.Flags().Changed("url") {
			urlPtr = url
		}
		if cmd.Flags().Changed("notes") {
			notesPtr = notes
		}

		var fields map[string]string
		if len(*fieldFlags) > 0 {
			fields, err = parseFieldFlags(*fieldFlags)
			if err != nil {
				return err
			}
		}

		if err := secrets.Update(cmd.Context(), db.DB, profileID, id, namePtr, usernamePtr, urlPtr, notesPtr, fields); err != nil {
			return fmt.Errorf("updating entry: %w", err)
		}

		finalName := args[0]
		if namePtr != nil {
			finalName = *namePtr
		}
		if cfg.JSONOutput {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(map[string]string{"name": finalName})
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Updated %q.\n", finalName)
		return nil
	}
}
```

- [ ] **Step 12: Append `newSecretRmCmd`, now JSON-aware**

```go
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
			id, err := lookupSecretID(cmd.Context(), db.DB, profileID, args[0])
			if err != nil {
				return err
			}
			if err := secrets.Delete(cmd.Context(), db.DB, profileID, id); err != nil {
				return fmt.Errorf("deleting entry: %w", err)
			}
			if cfg.JSONOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]string{"name": args[0]})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted %q.\n", args[0])
			return nil
		},
	}
}
```

- [ ] **Step 13: Append `newSecretEncryptConnectionsCmd`, completing `secret.go`**

Carried over unchanged from before this plan:

```go
func newSecretEncryptConnectionsCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "encrypt-connections",
		Short: "One-time migration: encrypt any existing plaintext connection credentials in place",
		Long:  "Existing connections created before the secrets vault shipped store OAuth tokens/API keys as plaintext JSON. This re-saves every such connection through the same Save path new connections already use, which encrypts the data column automatically. Safe to run repeatedly — it's a no-op once every row is encrypted. This same check-and-migrate step also runs automatically on every CLI and GUI startup, so running this command by hand is normally unnecessary.",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()

			migrated, total, err := connections.EncryptPlaintextConnections(cmd.Context(), db.DB)
			if err != nil {
				return fmt.Errorf("encrypting connections: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Encrypted %d of %d connection(s).\n", migrated, total)
			return nil
		},
	}
}
```

This completes the replacement `secret.go`. It references `newSecretExportCmd`/`newSecretImportCmd`, which do not exist until Task 5 — `cmd/monoagentcli` will not build standalone until then. Verify what is possible now and finish the build+test pass at the end of Task 5.

- [ ] **Step 14: Commit (build will not be clean until Task 5 — commit anyway, this is one coherent unit of review)**

```bash
git add cmd/monoagentcli/secret.go cmd/monoagentcli/secret_test.go
git commit -m "feat(vault-cli): multi-field add/reveal, new update command, json-aware rm"
```

---

### Task 5: CLI — export/import commands

**Files:**
- Create: `cmd/monoagentcli/secret_export.go`
- Test: `cmd/monoagentcli/secret_export_test.go`

**Interfaces:**
- Consumes: `secrets.GenerateExportPassword`, `secrets.Export`, `secrets.Import` (Task 3); `newSecretExportCmd`/`newSecretImportCmd` are already referenced by `newSecretCmd` in Task 4's `secret.go`.
- Produces: `monoagentcli secret export [--output FILE] [--json]` and `monoagentcli secret import <file> [--json]` — Task 6's GUI bridge shells out to exactly these.

- [ ] **Step 1: Write the failing test file — round trip**

Create `cmd/monoagentcli/secret_export_test.go`:

```go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretExportImport_RoundTrip(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	if _, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "openai-key", "--value", "v-test1"); err != nil {
		t.Fatalf("secret add: %v", err)
	}

	exportPath := filepath.Join(t.TempDir(), "export.json.enc")
	exportOut, err := runSecretCmd(t, dbPath, "export", "--output", exportPath)
	if err != nil {
		t.Fatalf("secret export: %v (%s)", err, exportOut)
	}
	var exportResult struct {
		Path       string `json:"path"`
		Passphrase string `json:"passphrase"`
	}
	if err := json.Unmarshal([]byte(exportOut), &exportResult); err != nil {
		t.Fatalf("unmarshal export output: %v", err)
	}
	if exportResult.Passphrase == "" {
		t.Fatal("expected a non-empty generated passphrase")
	}
	if _, err := os.Stat(exportPath); err != nil {
		t.Fatalf("expected export file to exist: %v", err)
	}
	exportBytes, _ := os.ReadFile(exportPath)
	if strings.Contains(string(exportBytes), "v-test1") {
		t.Fatal("export file must not contain plaintext")
	}

	// Import into a fresh, empty vault.
	importDBPath := newSecretCLITestDB(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	go func() {
		w.WriteString(exportResult.Passphrase + "\n")
		w.Close()
	}()
	importOut, err := runSecretCmd(t, importDBPath, "import", exportPath)
	os.Stdin = orig
	if err != nil {
		t.Fatalf("secret import: %v (%s)", err, importOut)
	}
	var importResult struct {
		Imported int `json:"imported"`
		Skipped  int `json:"skipped"`
	}
	if err := json.Unmarshal([]byte(importOut), &importResult); err != nil {
		t.Fatalf("unmarshal import output: %v", err)
	}
	if importResult.Imported != 1 || importResult.Skipped != 0 {
		t.Fatalf("expected 1 imported, 0 skipped, got %+v", importResult)
	}

	revealOut, err := runSecretCmd(t, importDBPath, "reveal", "openai-key", "--reveal")
	if err != nil {
		t.Fatalf("secret reveal after import: %v", err)
	}
	if !strings.Contains(revealOut, "v-test1") {
		t.Fatalf("expected imported entry to decrypt correctly, got: %s", revealOut)
	}
}
```

- [ ] **Step 2: Append the wrong-passphrase and duplicate-skip CLI tests**

Add these to the same file:

```go
func TestSecretImport_WrongPassphraseFails(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	if _, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "x", "--value", "v-one1"); err != nil {
		t.Fatalf("secret add: %v", err)
	}
	exportPath := filepath.Join(t.TempDir(), "export.json.enc")
	if _, err := runSecretCmd(t, dbPath, "export", "--output", exportPath); err != nil {
		t.Fatalf("secret export: %v", err)
	}

	importDBPath := newSecretCLITestDB(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	go func() {
		w.WriteString("pw-incorrect1\n")
		w.Close()
	}()
	_, err = runSecretCmd(t, importDBPath, "import", exportPath)
	os.Stdin = orig
	if err == nil {
		t.Fatal("expected import with an incorrect passphrase to fail")
	}
}

func TestSecretImport_SkipsDuplicateNames(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	if _, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "shared-key", "--value", "v-one1"); err != nil {
		t.Fatalf("secret add: %v", err)
	}
	exportPath := filepath.Join(t.TempDir(), "export.json.enc")
	exportOut, err := runSecretCmd(t, dbPath, "export", "--output", exportPath)
	if err != nil {
		t.Fatalf("secret export: %v", err)
	}
	var exportResult struct {
		Passphrase string `json:"passphrase"`
	}
	if err := json.Unmarshal([]byte(exportOut), &exportResult); err != nil {
		t.Fatalf("unmarshal export output: %v", err)
	}

	// Import into the SAME vault, which already has "shared-key" — it must be skipped.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	go func() {
		w.WriteString(exportResult.Passphrase + "\n")
		w.Close()
	}()
	importOut, err := runSecretCmd(t, dbPath, "import", exportPath)
	os.Stdin = orig
	if err != nil {
		t.Fatalf("secret import: %v (%s)", err, importOut)
	}
	var importResult struct {
		Imported int `json:"imported"`
		Skipped  int `json:"skipped"`
	}
	if err := json.Unmarshal([]byte(importOut), &importResult); err != nil {
		t.Fatalf("unmarshal import output: %v", err)
	}
	if importResult.Imported != 0 || importResult.Skipped != 1 {
		t.Fatalf("expected 0 imported, 1 skipped, got %+v", importResult)
	}
}
```

- [ ] **Step 3: Append the default-filename test, completing `secret_export_test.go`**

Runs from a temp directory so the default filename lands somewhere disposable:

```go
func TestSecretExport_DefaultsOutputFilename(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	if _, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "x", "--value", "v-one1"); err != nil {
		t.Fatalf("secret add: %v", err)
	}

	tmpDir := t.TempDir()
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer os.Chdir(origWD)

	out, err := runSecretCmd(t, dbPath, "export")
	if err != nil {
		t.Fatalf("secret export: %v (%s)", err, out)
	}
	var result struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.HasPrefix(result.Path, "vault-export-") || !strings.HasSuffix(result.Path, ".json.enc") {
		t.Fatalf("expected default filename pattern vault-export-*.json.enc, got %q", result.Path)
	}
	if _, err := os.Stat(result.Path); err != nil {
		t.Fatalf("expected default-named export file to exist: %v", err)
	}
}
```

- [ ] **Step 4: Run the tests to confirm they fail to compile**

Run: `go build ./cmd/monoagentcli/...`
Expected: FAIL — `undefined: newSecretExportCmd` / `undefined: newSecretImportCmd` (already referenced by `secret.go` since Task 4, not yet defined).

- [ ] **Step 5: Write `cmd/monoagentcli/secret_export.go` — the export command**

```go
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"monoagent/internal/secrets"

	"github.com/spf13/cobra"
)

func newSecretExportCmd(cfg *globalConfig) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export the vault to an encrypted file, protected by a freshly generated passphrase",
		Example: `  monoagentcli secret export
  monoagentcli secret export --output ./my-vault.json.enc`,
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

			path := output
			if path == "" {
				path = fmt.Sprintf("vault-export-%s.json.enc", time.Now().UTC().Format("20060102-150405"))
			}

			passphrase, err := secrets.GenerateExportPassword()
			if err != nil {
				return fmt.Errorf("generating export passphrase: %w", err)
			}
			data, err := secrets.Export(cmd.Context(), db.DB, profileID, passphrase)
			if err != nil {
				return fmt.Errorf("exporting vault: %w", err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				return fmt.Errorf("writing export file: %w", err)
			}

			fmt.Fprintf(os.Stderr, "Vault exported to %s\n", path)
			fmt.Fprintf(os.Stderr, "Passphrase (save this now, it will not be shown again): %s\n", passphrase)

			if cfg.JSONOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]string{"path": path, "passphrase": passphrase})
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&output, "output", "", "Output file path (default: vault-export-<timestamp>.json.enc in the current directory)")
	return cmd
}
```

- [ ] **Step 6: Append the import command, completing `secret_export.go`**

```go
func newSecretImportCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "import <file>",
		Short:   "Import entries from an encrypted vault export file",
		Args:    cobra.ExactArgs(1),
		Example: `  monoagentcli secret import ./my-vault.json.enc`,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("reading export file: %w", err)
			}

			fmt.Fprint(os.Stderr, "Passphrase: ")
			reader := bufio.NewReader(os.Stdin)
			line, err := reader.ReadString('\n')
			if err != nil {
				return fmt.Errorf("reading passphrase from stdin: %w", err)
			}
			passphrase := strings.TrimRight(line, "\r\n")

			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()

			profileID := cfg.ProfileID
			if profileID == "" {
				profileID = "default"
			}

			imported, skipped, err := secrets.Import(cmd.Context(), db.DB, profileID, passphrase, data)
			if err != nil {
				return fmt.Errorf("importing vault: %w", err)
			}

			if cfg.JSONOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]int{"imported": imported, "skipped": skipped})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Imported %d, skipped %d duplicate(s).\n", imported, skipped)
			return nil
		},
	}
	return cmd
}
```

- [ ] **Step 7: Build and run every CLI test from Tasks 4 and 5**

Run: `go build ./...`
Expected: succeeds across the whole repo now (this is the first point since Task 4 started where `cmd/monoagentcli` compiles again).

Run: `go test ./cmd/monoagentcli/... -v`
Expected: PASS — every test in `secret_test.go`, `secret_export_test.go`, and the pre-existing `people_status_test.go` all pass together.

- [ ] **Step 8: Run the full Go test suite**

Run: `go test ./... -race`
Expected: PASS across every package — this is the first point where Tasks 1 through 5 are all verified together.

- [ ] **Step 9: Commit**

```bash
git add cmd/monoagentcli/secret_export.go cmd/monoagentcli/secret_export_test.go
git commit -m "feat(vault-cli): add export/import commands"
```

---

### Task 6: GUI bridge — `app_vault.go` shells out to the CLI

**Files:**
- Modify: `wails-app/app_vault.go` (replace the vault-entry section only — image vault functions are untouched)
- Regenerate: `wails-app/frontend/src/wailsjs/go/main/App.d.ts`, `App.js`, and `models.ts` (auto-generated — do not hand-edit)

**Interfaces:**
- Consumes: `monoagentcli secret {list,add,reveal,update,rm,export,import}` (Tasks 4-5's exact flag/JSON shapes); `findMonoAgentCLI()` (pre-existing in `wails-app/app.go`).
- Produces (consumed by Tasks 8-9's frontend code): `ListSecrets`, `AddSecret`, a fields-and-notes reveal method, `UpdateSecret`, `DeleteSecret`, `ExportVaultAll`, `OpenVaultImportFilePicker`, `ImportVaultAll` — exact signatures built up across this task's steps.

- [ ] **Step 1: Confirm current section boundaries before editing**

Run: `grep -n "^func (a \*App)\|^// ──" wails-app/app_vault.go`
Confirm the vault-entry section runs from `ListSecrets` through the end of `DeleteSecret` (originally lines 88-112) and is followed immediately by `GetVaultImageData` — the replacement across this task's steps must not touch anything from `GetVaultImageData` onward.

- [ ] **Step 2: Replace the import block**

Replace the import block at the top of `wails-app/app_vault.go`:

```go
import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"monoagent/internal/secrets"
	"monoagent/internal/vault"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)
```

with:

```go
import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"monoagent/internal/vault"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)
```

The secrets package is dropped from this file's imports — it no longer calls it. `monoagent/internal/vault` stays; the image vault functions below still use it.

- [ ] **Step 3: Replace the vault-entry section — types and the subprocess helper**

Replace everything from `func (a *App) ListSecrets()` through the end of `func (a *App) DeleteSecret(id string) error` with the following. This section-heading comment explains the whole file's approach going forward: every method below shells to `monoagentcli secret ...` rather than calling the secrets package directly, per the design spec.

```go
// Every method below shells out to `monoagentcli secret ...` instead of
// calling internal/secrets directly — the CLI is the single implementation
// surface for vault operations. This file intentionally does not import
// monoagent/internal/secrets.

// VaultEntry mirrors `secret list --json`'s per-entry shape without
// importing internal/secrets — the CLI's --json output is the contract,
// not its Go types.
type VaultEntry struct {
	ID         string `json:"id"`
	ProfileID  string `json:"profile_id"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Username   string `json:"username,omitempty"`
	URL        string `json:"url,omitempty"`
	FieldCount int    `json:"field_count"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// VaultFieldsAndNotes mirrors the CLI reveal command's --json output shape.
type VaultFieldsAndNotes struct {
	Fields map[string]string `json:"fields"`
	Notes  string            `json:"notes"`
}

// VaultExportResult mirrors the CLI export command's --json output shape,
// plus a GUI-only Cancelled flag set when the user dismisses the save
// dialog.
type VaultExportResult struct {
	Path       string `json:"path"`
	Passphrase string `json:"passphrase"`
	Cancelled  bool   `json:"cancelled,omitempty"`
}

// VaultImportResult mirrors the CLI import command's --json output shape.
type VaultImportResult struct {
	Imported int `json:"imported"`
	Skipped  int `json:"skipped"`
}

// runVaultCLI runs `monoagentcli --profile <active> --json secret <args...>`,
// optionally piping stdin, and JSON-unmarshals stdout into result (skipped
// if result is nil). On a non-zero exit, the subprocess's stderr becomes
// the returned error — mirrors ExportData's existing exec.ExitError
// handling.
func (a *App) runVaultCLI(stdin string, result interface{}, args ...string) error {
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return err
	}
	fullArgs := append([]string{"--profile", a.getActiveProfileID(), "--json", "secret"}, args...)
	cmd := exec.CommandContext(a.ctx, cliBin, fullArgs...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return fmt.Errorf("%s", strings.TrimSpace(string(ee.Stderr)))
		}
		return err
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(out, result); err != nil {
		return fmt.Errorf("unexpected vault command output: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Append `ListSecrets` and `AddSecret`**

```go
func (a *App) ListSecrets() ([]VaultEntry, error) {
	var entries []VaultEntry
	if err := a.runVaultCLI("", &entries, "list"); err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []VaultEntry{}
	}
	return entries, nil
}

func (a *App) AddSecret(kind, name, username, url, notes string, fields map[string]string) (string, error) {
	args := []string{"add", "--kind", kind, "--name", name}
	if username != "" {
		args = append(args, "--username", username)
	}
	if url != "" {
		args = append(args, "--url", url)
	}
	if notes != "" {
		args = append(args, "--notes", notes)
	}
	for k, v := range fields {
		args = append(args, "--field", k+"="+v)
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := a.runVaultCLI("", &result, args...); err != nil {
		return "", err
	}
	return result.ID, nil
}
```

- [ ] **Step 5: Append the decrypt-for-editing method**

This is the GUI's only decrypt entrypoint — it shells to the identical `reveal --reveal --json` command the CLI itself runs, gated the same way (the CLI still refuses without `--reveal`; this method always passes it since the GUI's edit flow is the deliberate, gated point of use):

```go
func (a *App) GetSecretFields(name string) (*VaultFieldsAndNotes, error) {
	var result VaultFieldsAndNotes
	if err := a.runVaultCLI("", &result, "reveal", name, "--reveal"); err != nil {
		return nil, err
	}
	return &result, nil
}
```

- [ ] **Step 6: Append `UpdateSecret` and `DeleteSecret`**

```go
func (a *App) UpdateSecret(name, newName, username, url, notes string, fields map[string]string) error {
	args := []string{"update", name, "--name", newName, "--username", username, "--url", url, "--notes", notes}
	for k, v := range fields {
		args = append(args, "--field", k+"="+v)
	}
	return a.runVaultCLI("", nil, args...)
}

func (a *App) DeleteSecret(name string) error {
	return a.runVaultCLI("", nil, "rm", name)
}
```

- [ ] **Step 7: Append `ExportVaultAll`**

Prompts for a save location, then exports the active profile's vault there. The returned passphrase is shown exactly once by the caller — it is never persisted.

```go
func (a *App) ExportVaultAll() (*VaultExportResult, error) {
	dest, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "Export Vault",
		DefaultFilename: "vault-export.json.enc",
		// Single extension only: Wails' macOS dialog resolves each ";"-separated
		// Pattern entry via UTType typeWithFilenameExtension:, which returns nil
		// for a compound extension like "json.enc" (embedded dot) — and Wails
		// inserts that nil into an NSMutableArray unguarded, crashing the app
		// (NSInvalidArgumentException: object cannot be nil). "*.enc" alone still
		// matches "vault-export.json.enc" since macOS resolves UTType from the
		// final extension.
		Filters: []runtime.FileFilter{
			{DisplayName: "Vault export", Pattern: "*.enc"},
		},
	})
	if err != nil {
		return nil, err
	}
	if dest == "" {
		return &VaultExportResult{Cancelled: true}, nil
	}
	var result VaultExportResult
	if err := a.runVaultCLI("", &result, "export", "--output", dest); err != nil {
		return nil, err
	}
	return &result, nil
}
```

- [ ] **Step 8: Append the import file picker and `ImportVaultAll`, completing the vault-entry section**

```go
// OpenVaultImportFilePicker opens a native file picker for a vault export
// file and returns the selected path (empty if cancelled).
func (a *App) OpenVaultImportFilePicker() string {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Import Vault",
		// Single extension only — see the matching comment on ExportVaultAll's
		// SaveFileDialog Filters above; OpenFileDialog has the identical
		// unguarded nil-UTType crash for compound extensions.
		Filters: []runtime.FileFilter{
			{DisplayName: "Vault export", Pattern: "*.enc"},
		},
	})
	if err != nil {
		return ""
	}
	return path
}

// ImportVaultAll decrypts path with passphrase (piped to the CLI's stdin,
// per the design spec — never a flag) and imports every entry into the
// active profile.
func (a *App) ImportVaultAll(path, passphrase string) (*VaultImportResult, error) {
	var result VaultImportResult
	if err := a.runVaultCLI(passphrase+"\n", &result, "import", path); err != nil {
		return nil, err
	}
	return &result, nil
}
```

- [ ] **Step 9: Build the wails-app module**

Run: `cd wails-app && go build ./...`
Expected: succeeds. If it fails with "imported and not used" for `monoagent/internal/secrets`, re-check Step 1's grep output — some other function in the file may still reference it (it should not, per Step 1's confirmation).

- [ ] **Step 10: Regenerate the Wails JS/TS bindings**

Run: `cd wails-app && /Users/morteza/go/bin/wails generate module`
Expected: exits 0 and rewrites `wails-app/frontend/src/wailsjs/go/main/App.d.ts`, `App.js`, and `wails-app/frontend/src/wailsjs/go/models.ts`. Do not hand-edit these files — they carry a "DO NOT EDIT" generated-file header.

Run: `grep -n "AddSecret\|ListSecrets\|GetSecretFields\|UpdateSecret\|DeleteSecret\|ExportVaultAll\|ImportVaultAll\|OpenVaultImportFilePicker" wails-app/frontend/src/wailsjs/go/main/App.d.ts`
Expected: one exported function declaration per method above, plus (in `models.ts`) new `main.VaultEntry`/`main.VaultFieldsAndNotes`/`main.VaultExportResult`/`main.VaultImportResult` interfaces reflecting the Go structs from Step 3. Confirm `AddSecret`'s old 6-arg signature and the old `RevealSecret`/`id`-based methods are gone (the regenerated file only has the new ones).

- [ ] **Step 11: Full repo build**

Run: `go build ./...` (repo root) and `cd wails-app && go build ./...`
Expected: both succeed.

- [ ] **Step 12: Commit**

```bash
git add wails-app/app_vault.go wails-app/frontend/src/wailsjs/go/main/App.d.ts wails-app/frontend/src/wailsjs/go/main/App.js wails-app/frontend/src/wailsjs/go/models.ts
git commit -m "refactor(vault-gui): app_vault.go shells out to monoagentcli instead of internal/secrets"
```

---

### Task 7: GUI — shared `KeyValueFields` component

**Files:**
- Create: `wails-app/frontend/src/components/KeyValueFields.jsx`

**Interfaces:**
- Produces: `default function KeyValueFields({rows, onChange})`, `newRow(key = '', value = '')`, `fieldsToRows(fields)`, `rowsToFields(rows)` — all consumed by Task 8 and Task 9.

There is no component-test setup in this repo (Vitest is configured only for plain-JS modules — see `wails-app/frontend/src/services/api.test.js`; there is no React Testing Library or similar). Verification for this task and Tasks 8-9 is: `npm run build` (catches syntax/import errors) now, and manual exercise of the real app in Task 10.

- [ ] **Step 1: Write `KeyValueFields.jsx`**

Create `wails-app/frontend/src/components/KeyValueFields.jsx`:

```jsx
import { useState } from 'react'
import { Plus, Trash2, Eye, EyeOff } from 'lucide-react'

let nextRowId = 0

// newRow/fieldsToRows/rowsToFields all share the same id counter, so a row
// minted by Vault.jsx's initial form state and one minted later by this
// component's own "Add field" button never collide.
export function newRow(key = '', value = '') {
  return { id: nextRowId++, key, value }
}

export function fieldsToRows(fields) {
  return Object.entries(fields || {}).map(([key, value]) => newRow(key, value))
}

export function rowsToFields(rows) {
  const fields = {}
  for (const row of rows) {
    const key = row.key.trim()
    if (key) fields[key] = row.value
  }
  return fields
}

const inputStyle = {
  flex: 1, background: '#060b11', border: '1px solid #1e3a4f', borderRadius: 5,
  padding: '6px 8px', color: '#e2e8f0', fontFamily: 'var(--font-mono)', fontSize: 11,
}

// Dynamic key/value row editor shared by the vault's add-item form and its
// edit modal. `rows` is an array of {id, key, value} — see
// fieldsToRows/rowsToFields for converting to/from the plain map the Wails
// API uses.
export default function KeyValueFields({ rows, onChange }) {
  const [shown, setShown] = useState({})

  const updateRow = (id, patch) => {
    onChange(rows.map(r => (r.id === id ? { ...r, ...patch } : r)))
  }
  const addRow = () => {
    onChange([...rows, newRow()])
  }
  const removeRow = (id) => {
    onChange(rows.filter(r => r.id !== id))
    setShown(prev => {
      const next = { ...prev }
      delete next[id]
      return next
    })
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      {rows.map(row => (
        <div key={row.id} style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
          <input
            placeholder="key"
            value={row.key}
            onChange={e => updateRow(row.id, { key: e.target.value })}
            style={{ ...inputStyle, flex: '0 0 120px' }}
          />
          <input
            type={shown[row.id] ? 'text' : 'password'}
            placeholder="value"
            value={row.value}
            onChange={e => updateRow(row.id, { value: e.target.value })}
            style={inputStyle}
          />
          <button
            type="button"
            onClick={() => setShown(prev => ({ ...prev, [row.id]: !prev[row.id] }))}
            title={shown[row.id] ? 'Hide' : 'Show'}
            style={{ background: 'none', border: 'none', cursor: 'pointer', color: '#4b5563', padding: 4, display: 'flex' }}
          >
            {shown[row.id] ? <EyeOff size={13} /> : <Eye size={13} />}
          </button>
          <button
            type="button"
            onClick={() => removeRow(row.id)}
            title="Remove field"
            style={{ background: 'none', border: 'none', cursor: 'pointer', color: '#4b5563', padding: 4, display: 'flex' }}
          >
            <Trash2 size={13} />
          </button>
        </div>
      ))}
      <button
        type="button"
        onClick={addRow}
        style={{
          alignSelf: 'flex-start', background: 'none', border: '1px dashed #1e3a4f', borderRadius: 5,
          padding: '4px 10px', color: '#64748b', fontFamily: 'var(--font-mono)', fontSize: 10, cursor: 'pointer',
          display: 'flex', alignItems: 'center', gap: 4,
        }}
      >
        <Plus size={11} /> Add field
      </button>
    </div>
  )
}
```

- [ ] **Step 2: Confirm the frontend still builds**

Run: `cd wails-app/frontend && npm run build`
Expected: succeeds.

- [ ] **Step 3: Commit**

```bash
git add wails-app/frontend/src/components/KeyValueFields.jsx
git commit -m "feat(vault-gui): add shared KeyValueFields row editor component"
```

---

### Task 8: GUI — `VaultItemModal` edit view

**Files:**
- Create: `wails-app/frontend/src/components/VaultItemModal.jsx`

**Interfaces:**
- Consumes: `KeyValueFields`, `newRow`, `fieldsToRows`, `rowsToFields` (Task 7); `WailsApp.GetSecretFields(name)`, `WailsApp.UpdateSecret(name, newName, username, url, notes, fields)` (Task 6).
- Produces: `default function VaultItemModal({entry, onClose, onSaved})` — consumed by Task 9. `entry` is one `VaultEntry` from `ListSecrets()` (has `.name`, `.kind`, `.username`, `.url`).

- [ ] **Step 1: Write `VaultItemModal.jsx`**

Create `wails-app/frontend/src/components/VaultItemModal.jsx`:

```jsx
import { useState, useEffect } from 'react'
import * as WailsApp from '../wailsjs/go/main/App'
import KeyValueFields, { fieldsToRows, rowsToFields } from './KeyValueFields.jsx'

const inputStyle = {
  background: '#060b11', border: '1px solid #1e3a4f', borderRadius: 5,
  padding: '6px 8px', color: '#e2e8f0', fontFamily: 'var(--font-mono)', fontSize: 11,
}

// Opened by clicking a Vault list row. Loads the entry's decrypted fields
// and notes, lets the user edit everything, and saves through UpdateSecret.
// Kind is fixed — no convert-in-place.
export default function VaultItemModal({ entry, onClose, onSaved }) {
  const [name, setName] = useState(entry.name)
  const [username, setUsername] = useState(entry.username || '')
  const [url, setUrl] = useState(entry.url || '')
  const [notes, setNotes] = useState('')
  const [rows, setRows] = useState([])
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState(null)

  useEffect(() => {
    let cancelled = false
    WailsApp.GetSecretFields(entry.name)
      .then(result => {
        if (cancelled) return
        setNotes(result.notes || '')
        setRows(fieldsToRows(result.fields))
        setLoading(false)
      })
      .catch(e => {
        if (cancelled) return
        setError('Failed to load entry: ' + e)
        setLoading(false)
      })
    return () => { cancelled = true }
  }, [entry.name])

  const handleSave = async (e) => {
    e.preventDefault()
    setError(null)
    const fields = rowsToFields(rows)
    if (Object.keys(fields).length === 0) {
      // Guards against a real gap: UpdateSecret always sends every current
      // field, so zero fields means zero --field flags on the CLI side,
      // which "update" reads as "don't touch fields" (not "clear them") —
      // silently leaving the old ones in place instead of erroring or
      // saving an empty set. Block it here instead.
      setError('At least one field is required.')
      return
    }
    setSaving(true)
    try {
      await WailsApp.UpdateSecret(entry.name, name, username, url, notes, fields)
      onSaved()
    } catch (e) {
      setError('Save failed: ' + e)
      setSaving(false)
    }
  }

  return (
    <div
      className="modal-overlay"
      onClick={(e) => e.target === e.currentTarget && onClose()}
      style={{
        position: 'fixed', inset: 0, zIndex: 10001,
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        background: 'rgba(0,0,0,0.55)',
      }}
    >
      <form
        onSubmit={handleSave}
        style={{
          background: '#0d1520', border: '1px solid #1e3a4f', borderRadius: 10,
          padding: 20, width: 420, maxWidth: '90%', display: 'flex', flexDirection: 'column', gap: 10,
        }}
      >
        <div style={{ color: '#e2e8f0', fontSize: 14, fontWeight: 600 }}>
          Edit {entry.kind === 'login' ? 'Login' : 'Item'}
        </div>

        {error && (
          <div style={{ background: 'rgba(239,68,68,0.08)', border: '1px solid rgba(239,68,68,0.2)', borderRadius: 5, padding: '7px 10px', fontFamily: 'var(--font-mono)', fontSize: 11, color: '#fca5a5' }}>
            {error}
          </div>
        )}

        <input placeholder="Name" value={name} onChange={e => setName(e.target.value)} required style={inputStyle} />

        {entry.kind === 'login' && (
          <div style={{ display: 'flex', gap: 8 }}>
            <input placeholder="Username" value={username} onChange={e => setUsername(e.target.value)} style={{ ...inputStyle, flex: 1 }} />
            <input placeholder="URL" value={url} onChange={e => setUrl(e.target.value)} style={{ ...inputStyle, flex: 1 }} />
          </div>
        )}

        {loading ? (
          <div style={{ color: '#475569', fontFamily: 'var(--font-mono)', fontSize: 11 }}>Loading fields…</div>
        ) : (
          <KeyValueFields rows={rows} onChange={setRows} />
        )}

        <textarea
          placeholder="Notes"
          value={notes}
          onChange={e => setNotes(e.target.value)}
          rows={2}
          style={{ ...inputStyle, resize: 'vertical' }}
        />

        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 4 }}>
          <button
            type="button"
            onClick={onClose}
            style={{ background: 'none', border: '1px solid #1e3a4f', borderRadius: 5, padding: '6px 12px', color: '#94a3b8', fontFamily: 'var(--font-mono)', fontSize: 11, cursor: 'pointer' }}
          >
            Cancel
          </button>
          <button
            type="submit"
            disabled={loading || saving}
            style={{ background: 'rgba(0,180,216,0.1)', border: '1px solid rgba(0,180,216,0.3)', borderRadius: 5, padding: '6px 12px', color: '#00b4d8', fontFamily: 'var(--font-mono)', fontSize: 11, cursor: 'pointer' }}
          >
            Save
          </button>
        </div>
      </form>
    </div>
  )
}
```

- [ ] **Step 2: Confirm the frontend still builds**

Run: `cd wails-app/frontend && npm run build`
Expected: succeeds.

- [ ] **Step 3: Commit**

```bash
git add wails-app/frontend/src/components/VaultItemModal.jsx
git commit -m "feat(vault-gui): add VaultItemModal edit view"
```

---

### Task 9: GUI — `Vault.jsx` integration (labels, add-item form, list badge, export/import)

**Files:**
- Modify: `wails-app/frontend/src/pages/Vault.jsx` (full rewrite)

**Interfaces:**
- Consumes: `KeyValueFields`, `newRow`, `rowsToFields` (Task 7); `VaultItemModal` (Task 8); `WailsApp.{ListSecrets, AddSecret, DeleteSecret, ExportVaultAll, OpenVaultImportFilePicker, ImportVaultAll}` (Task 6); `confirm` from `../components/ConfirmDialog.jsx` (pre-existing, unchanged).

- [ ] **Step 1: Replace `wails-app/frontend/src/pages/Vault.jsx` — imports, state, and handlers**

Start the replacement file with imports, style constants, and the component's state/handlers (the JSX return continues in the next step):

```jsx
import { useState, useEffect, useCallback } from 'react'
import { Plus, Trash2, KeyRound, Download, Upload, Copy } from 'lucide-react'
import * as WailsApp from '../wailsjs/go/main/App'
import { confirm } from '../components/ConfirmDialog.jsx'
import KeyValueFields, { newRow, rowsToFields } from '../components/KeyValueFields.jsx'
import VaultItemModal from '../components/VaultItemModal.jsx'

const fmtDate = (s) => {
  if (!s) return '—'
  const d = new Date(s.includes('T') ? s : s.replace(' ', 'T') + 'Z')
  if (isNaN(d)) return s
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}

const KIND_COLORS = {
  login: { bg: 'rgba(0,180,216,0.1)', border: 'rgba(0,180,216,0.25)', color: '#00b4d8' },
  secret: { bg: 'rgba(124,58,237,0.15)', border: 'rgba(124,58,237,0.3)', color: '#a78bfa' },
}
const kindBadge = (kind) => {
  const s = KIND_COLORS[kind] || { bg: '#1a2332', border: '#334', color: '#64748b' }
  const label = kind === 'secret' ? 'keys' : kind
  return (
    <span style={{
      background: s.bg, border: `1px solid ${s.border}`, borderRadius: 3,
      padding: '1px 6px', fontFamily: 'var(--font-mono)', fontSize: 9, color: s.color,
    }}>{label}</span>
  )
}

const inputStyle = {
  background: '#060b11', border: '1px solid #1e3a4f', borderRadius: 5,
  padding: '6px 8px', color: '#e2e8f0', fontFamily: 'var(--font-mono)', fontSize: 11,
}
const headerBtnStyle = {
  background: 'rgba(0,180,216,0.1)', border: '1px solid rgba(0,180,216,0.3)',
  borderRadius: 6, padding: '6px 12px', color: '#00b4d8',
  fontFamily: 'var(--font-mono)', fontSize: 11, cursor: 'pointer',
  display: 'flex', alignItems: 'center', gap: 5,
}

const emptyForm = () => ({ kind: 'secret', name: '', fields: [newRow('secret', '')], username: '', url: '', notes: '' })

export default function Vault() {
  const [entries, setEntries] = useState([])
  const [showAdd, setShowAdd] = useState(false)
  const [form, setForm] = useState(emptyForm())
  const [error, setError] = useState(null)
  const [editingEntry, setEditingEntry] = useState(null)
  const [exportResult, setExportResult] = useState(null)
  const [importPath, setImportPath] = useState(null)
  const [importPassphrase, setImportPassphrase] = useState('')
  const [importResult, setImportResult] = useState(null)

  const load = useCallback(async () => {
    try {
      const list = await WailsApp.ListSecrets()
      setEntries(list || [])
    } catch (e) {
      setError('Failed to load vault: ' + e)
    }
  }, [])

  useEffect(() => { load() }, [load])

  const handleAdd = async (e) => {
    e.preventDefault()
    setError(null)
    const fields = rowsToFields(form.fields)
    if (Object.keys(fields).length === 0) {
      setError('At least one field is required.')
      return
    }
    try {
      await WailsApp.AddSecret(form.kind, form.name, form.username, form.url, form.notes, fields)
      setForm(emptyForm())
      setShowAdd(false)
      load()
    } catch (e) {
      setError('Save failed: ' + e)
    }
  }

  const handleDelete = async (name) => {
    if (!(await confirm('Delete this vault entry? This cannot be undone.', { title: 'Delete Vault Entry', confirmLabel: 'Delete' }))) return
    setError(null)
    try {
      await WailsApp.DeleteSecret(name)
      setEntries(prev => prev.filter(e => e.name !== name))
    } catch (e) {
      setError('Delete failed: ' + e)
    }
  }

  const handleExport = async () => {
    setError(null)
    try {
      const result = await WailsApp.ExportVaultAll()
      if (result.cancelled) return
      setExportResult(result)
    } catch (e) {
      setError('Export failed: ' + e)
    }
  }

  const handleImportPick = async () => {
    setError(null)
    try {
      const path = await WailsApp.OpenVaultImportFilePicker()
      if (!path) return
      setImportPath(path)
      setImportPassphrase('')
    } catch (e) {
      setError('Import failed: ' + e)
    }
  }

  const handleImportSubmit = async (e) => {
    e.preventDefault()
    setError(null)
    try {
      const result = await WailsApp.ImportVaultAll(importPath, importPassphrase)
      setImportPath(null)
      setImportPassphrase('')
      setImportResult(result)
      load()
    } catch (e) {
      setError('Import failed: ' + e)
    }
  }
```

- [ ] **Step 2: Append the header, error banner, and add-item form JSX**

Continue the same `return (...)` — this is the opening of the component's render, up through the add-item form:

```jsx
  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', overflow: 'hidden' }}>
      <div style={{
        padding: '14px 20px 10px', borderBottom: '1px solid #0d1a26',
        display: 'flex', alignItems: 'center', gap: 12,
      }}>
        <div>
          <div style={{ color: '#e2e8f0', fontSize: 16, fontWeight: 600 }}>Vault</div>
          <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: '#475569' }}>
            {entries.length} {entries.length === 1 ? 'entry' : 'entries'}
          </div>
        </div>
        <div style={{ flex: 1 }} />
        <button onClick={handleImportPick} style={headerBtnStyle}>
          <Upload size={12} /> Import
        </button>
        <button onClick={handleExport} style={headerBtnStyle}>
          <Download size={12} /> Export All
        </button>
        <button onClick={() => setShowAdd(true)} style={headerBtnStyle}>
          <Plus size={12} /> Add New Item
        </button>
      </div>

      {error && (
        <div style={{ margin: '8px 20px', background: 'rgba(239,68,68,0.08)', border: '1px solid rgba(239,68,68,0.2)', borderRadius: 5, padding: '7px 10px', fontFamily: 'var(--font-mono)', fontSize: 11, color: '#fca5a5' }}>
          {error}
        </div>
      )}

      {showAdd && (
        <form
          onSubmit={handleAdd}
          style={{
            margin: '10px 20px', padding: 14, background: '#0d1a26',
            border: '1px solid #1e3a4f', borderRadius: 6,
            display: 'flex', flexDirection: 'column', gap: 8,
          }}
        >
          <div style={{ display: 'flex', gap: 8 }}>
            <select
              value={form.kind}
              onChange={e => setForm({ ...form, kind: e.target.value })}
              style={{ ...inputStyle, flex: '0 0 auto' }}
            >
              <option value="secret">Keys</option>
              <option value="login">Login</option>
            </select>
            <input
              placeholder="Name"
              value={form.name}
              onChange={e => setForm({ ...form, name: e.target.value })}
              required
              style={{ ...inputStyle, flex: 1 }}
            />
          </div>
          {form.kind === 'login' && (
            <div style={{ display: 'flex', gap: 8 }}>
              <input
                placeholder="Username"
                value={form.username}
                onChange={e => setForm({ ...form, username: e.target.value })}
                style={{ ...inputStyle, flex: 1 }}
              />
              <input
                placeholder="URL"
                value={form.url}
                onChange={e => setForm({ ...form, url: e.target.value })}
                style={{ ...inputStyle, flex: 1 }}
              />
            </div>
          )}
          <KeyValueFields rows={form.fields} onChange={f => setForm({ ...form, fields: f })} />
          <textarea
            placeholder="Notes"
            value={form.notes}
            onChange={e => setForm({ ...form, notes: e.target.value })}
            rows={2}
            style={{ ...inputStyle, resize: 'vertical' }}
          />
          <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
            <button
              type="button"
              onClick={() => { setShowAdd(false); setForm(emptyForm()) }}
              style={{
                background: 'none', border: '1px solid #1e3a4f', borderRadius: 5,
                padding: '6px 12px', color: '#94a3b8', fontFamily: 'var(--font-mono)', fontSize: 11, cursor: 'pointer',
              }}
            >
              Cancel
            </button>
            <button
              type="submit"
              style={{
                background: 'rgba(0,180,216,0.1)', border: '1px solid rgba(0,180,216,0.3)',
                borderRadius: 5, padding: '6px 12px', color: '#00b4d8',
                fontFamily: 'var(--font-mono)', fontSize: 11, cursor: 'pointer',
              }}
            >
              Save
            </button>
          </div>
        </form>
      )}
```

- [ ] **Step 3: Append the column headers, entry rows, and the edit modal**

```jsx
      <div style={{
        display: 'flex', alignItems: 'center', gap: 0,
        padding: '5px 20px', borderBottom: '1px solid #0a1520',
        fontFamily: 'var(--font-mono)', fontSize: 9, color: '#334155',
        letterSpacing: '1px', textTransform: 'uppercase',
      }}>
        <div style={{ flex: 1 }}>Name</div>
        <div style={{ width: 70 }}>Kind</div>
        <div style={{ width: 110 }}>Username</div>
        <div style={{ width: 90 }}>Fields</div>
        <div style={{ width: 56 }}>Updated</div>
        <div style={{ width: 28 }} />
      </div>

      <div style={{ flex: 1, overflowY: 'auto' }}>
        {entries.length === 0 && (
          <div style={{
            display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center',
            height: 200, gap: 12, color: '#334155',
          }}>
            <KeyRound size={32} style={{ opacity: 0.3 }} />
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 12 }}>
              No vault entries yet — add a new item above
            </div>
          </div>
        )}
        {entries.map(entry => (
          <div
            key={entry.id}
            onClick={() => setEditingEntry(entry)}
            style={{
              display: 'flex', alignItems: 'center', gap: 0, cursor: 'pointer',
              padding: '6px 20px', borderBottom: '1px solid #0a1520',
            }}
          >
            <div style={{ flex: 1, minWidth: 0, fontFamily: 'var(--font-mono)', fontSize: 11, color: '#94a3b8', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', paddingRight: 10 }}>
              {entry.name}
            </div>
            <div style={{ width: 70 }}>{kindBadge(entry.kind)}</div>
            <div style={{ width: 110, fontFamily: 'var(--font-mono)', fontSize: 10, color: '#475569', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', paddingRight: 8 }}>
              {entry.username || '—'}
            </div>
            <div style={{ width: 90, fontFamily: 'var(--font-mono)', fontSize: 11, color: '#e2e8f0' }}>
              {entry.field_count} {entry.field_count === 1 ? 'key' : 'keys'}
            </div>
            <div style={{ width: 56, fontFamily: 'var(--font-mono)', fontSize: 10, color: '#475569' }}>{fmtDate(entry.updated_at)}</div>
            <div style={{ width: 28 }}>
              <button
                onClick={(e) => { e.stopPropagation(); handleDelete(entry.name) }}
                style={{
                  background: 'none', border: 'none', cursor: 'pointer',
                  color: '#4b5563', padding: 4, borderRadius: 3,
                  display: 'flex', alignItems: 'center', justifyContent: 'center',
                }}
                onMouseEnter={e => e.currentTarget.style.color = '#ef4444'}
                onMouseLeave={e => e.currentTarget.style.color = '#4b5563'}
              >
                <Trash2 size={13} />
              </button>
            </div>
          </div>
        ))}
      </div>

      {editingEntry && (
        <VaultItemModal
          key={editingEntry.name}
          entry={editingEntry}
          onClose={() => setEditingEntry(null)}
          onSaved={() => { setEditingEntry(null); load() }}
        />
      )}
```

- [ ] **Step 4: Append the export-passphrase modal**

```jsx
      {exportResult && (
        <div className="modal-overlay" style={{ position: 'fixed', inset: 0, zIndex: 10001, display: 'flex', alignItems: 'center', justifyContent: 'center', background: 'rgba(0,0,0,0.55)' }}>
          <div style={{ background: '#0d1520', border: '1px solid #1e3a4f', borderRadius: 10, padding: 20, width: 420, maxWidth: '90%' }}>
            <div style={{ color: '#e2e8f0', fontSize: 14, fontWeight: 600, marginBottom: 8 }}>Vault Exported</div>
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: '#94a3b8', marginBottom: 10 }}>
              Saved to {exportResult.path}. Save this now — it will not be shown again.
            </div>
            <div style={{ display: 'flex', gap: 8, alignItems: 'center', background: '#060b11', border: '1px solid #1e3a4f', borderRadius: 5, padding: '8px 10px', marginBottom: 14 }}>
              <span style={{ flex: 1, fontFamily: 'var(--font-mono)', fontSize: 12, color: '#00b4d8', wordBreak: 'break-all' }}>
                {exportResult.passphrase}
              </span>
              <button
                type="button"
                onClick={() => navigator.clipboard.writeText(exportResult.passphrase)}
                title="Copy"
                style={{ background: 'none', border: 'none', cursor: 'pointer', color: '#4b5563', padding: 4, display: 'flex' }}
              >
                <Copy size={13} />
              </button>
            </div>
            <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
              <button
                onClick={() => setExportResult(null)}
                style={{ background: 'rgba(0,180,216,0.1)', border: '1px solid rgba(0,180,216,0.3)', borderRadius: 5, padding: '6px 12px', color: '#00b4d8', fontFamily: 'var(--font-mono)', fontSize: 11, cursor: 'pointer' }}
              >
                Done
              </button>
            </div>
          </div>
        </div>
      )}
```

- [ ] **Step 5: Append the import-passphrase prompt and result summary, closing the component**

```jsx
      {importPath && (
        <div className="modal-overlay" style={{ position: 'fixed', inset: 0, zIndex: 10001, display: 'flex', alignItems: 'center', justifyContent: 'center', background: 'rgba(0,0,0,0.55)' }}>
          <form onSubmit={handleImportSubmit} style={{ background: '#0d1520', border: '1px solid #1e3a4f', borderRadius: 10, padding: 20, width: 380, maxWidth: '90%', display: 'flex', flexDirection: 'column', gap: 10 }}>
            <div style={{ color: '#e2e8f0', fontSize: 14, fontWeight: 600 }}>Import Vault</div>
            {error && (
              <div style={{ background: 'rgba(239,68,68,0.08)', border: '1px solid rgba(239,68,68,0.2)', borderRadius: 5, padding: '7px 10px', fontFamily: 'var(--font-mono)', fontSize: 11, color: '#fca5a5' }}>
                {error}
              </div>
            )}
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: '#94a3b8' }}>{importPath}</div>
            <input
              type="password"
              placeholder="Export passphrase"
              value={importPassphrase}
              onChange={e => setImportPassphrase(e.target.value)}
              required
              autoFocus
              style={inputStyle}
            />
            <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
              <button
                type="button"
                onClick={() => { setImportPath(null); setImportPassphrase('') }}
                style={{ background: 'none', border: '1px solid #1e3a4f', borderRadius: 5, padding: '6px 12px', color: '#94a3b8', fontFamily: 'var(--font-mono)', fontSize: 11, cursor: 'pointer' }}
              >
                Cancel
              </button>
              <button
                type="submit"
                style={{ background: 'rgba(0,180,216,0.1)', border: '1px solid rgba(0,180,216,0.3)', borderRadius: 5, padding: '6px 12px', color: '#00b4d8', fontFamily: 'var(--font-mono)', fontSize: 11, cursor: 'pointer' }}
              >
                Import
              </button>
            </div>
          </form>
        </div>
      )}

      {importResult && (
        <div className="modal-overlay" style={{ position: 'fixed', inset: 0, zIndex: 10001, display: 'flex', alignItems: 'center', justifyContent: 'center', background: 'rgba(0,0,0,0.55)' }}>
          <div style={{ background: '#0d1520', border: '1px solid #1e3a4f', borderRadius: 10, padding: 20, width: 340, maxWidth: '90%' }}>
            <div style={{ color: '#e2e8f0', fontSize: 14, fontWeight: 600, marginBottom: 10 }}>Import Complete</div>
            {error && (
              <div style={{ background: 'rgba(239,68,68,0.08)', border: '1px solid rgba(239,68,68,0.2)', borderRadius: 5, padding: '7px 10px', marginBottom: 10, fontFamily: 'var(--font-mono)', fontSize: 11, color: '#fca5a5' }}>
                {error}
              </div>
            )}
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 12, color: '#94a3b8', marginBottom: 14 }}>
              Imported {importResult.imported}, skipped {importResult.skipped} duplicate{importResult.skipped === 1 ? '' : 's'}.
            </div>
            <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
              <button
                onClick={() => setImportResult(null)}
                style={{ background: 'rgba(0,180,216,0.1)', border: '1px solid rgba(0,180,216,0.3)', borderRadius: 5, padding: '6px 12px', color: '#00b4d8', fontFamily: 'var(--font-mono)', fontSize: 11, cursor: 'pointer' }}
              >
                Done
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
```

- [ ] **Step 6: Build the frontend**

Run: `cd wails-app/frontend && npm run build`
Expected: succeeds with no errors.

- [ ] **Step 7: Run the existing frontend test suite (unaffected, but confirms nothing else broke)**

Run: `cd wails-app/frontend && npm test`
Expected: PASS (`services/api.test.js` is unrelated to this page and must be untouched by this task).

- [ ] **Step 8: Commit**

```bash
git add wails-app/frontend/src/pages/Vault.jsx
git commit -m "feat(vault-gui): key-value add form, edit-on-click, export/import UI"
```

---

### Task 10: Full build, full test suite, manual verification

**Files:** none created or modified — this task only runs and observes.

**Interfaces:** none produced — terminal task.

- [ ] **Step 1: Run the full Go test suite**

Run: `go test ./... -race -v 2>&1 | tail -100`
Expected: PASS across every package — `internal/secrets`, `cmd/monoagentcli`, and everything else untouched by this plan (`internal/connections`, `internal/storage`, `internal/workflow`, etc. — confirming Task 2's `Resolve` change did not break anything that calls `ResolveConfig`).

- [ ] **Step 2: Build both binaries**

Run (from repo root): `make build-cli`
Expected: produces `bin/monoagentcli`.

Run: `go install ./cmd/monoagentcli/`
Expected: installs to `$(go env GOPATH)/bin/monoagentcli` — one of `findMonoAgentCLI()`'s explicit candidate paths, so the GUI (run via `wails dev` below, not the bundled `.app`) can find it without needing a full `make build-app` bundle step.

Run: `cd wails-app/frontend && npm run build`
Expected: succeeds (repeat of Task 9 Step 6, confirming nothing regressed since).

- [ ] **Step 3: Manual CLI smoke test**

Run this sequence and confirm each line of output matches the comment above it. Every command below is scoped to a throwaway `--db-path` — none of it touches your real `~/.monoagent/monoagent.db` (the CLI's own default), so nothing here can duplicate, clutter, or otherwise affect your actual stored secrets/profiles. `--profile smoke-test` also needs that profile to exist first (`--profile` on a non-existent name errors rather than auto-creating one), hence the explicit `profile create` step below:

```bash
SMOKE_DIR=$(mktemp -d)
SMOKE_DB="$SMOKE_DIR/smoke-test.db"

bin/monoagentcli --db-path "$SMOKE_DB" secret add --kind secret --name test-aws --field access_key_id=id-value-1 --field secret_access_key=key-value-2

bin/monoagentcli --db-path "$SMOKE_DB" secret list

bin/monoagentcli --db-path "$SMOKE_DB" secret reveal test-aws --reveal

bin/monoagentcli --db-path "$SMOKE_DB" secret reveal test-aws --reveal --json

bin/monoagentcli --db-path "$SMOKE_DB" secret update test-aws --notes "rotated 2026-08-06"
bin/monoagentcli --db-path "$SMOKE_DB" secret reveal test-aws --reveal --json

bin/monoagentcli --db-path "$SMOKE_DB" secret export --output "$SMOKE_DIR/vault-smoke-test.json.enc"
bin/monoagentcli --db-path "$SMOKE_DB" profile create smoke-test
bin/monoagentcli --db-path "$SMOKE_DB" --profile smoke-test secret import "$SMOKE_DIR/vault-smoke-test.json.enc"
bin/monoagentcli --db-path "$SMOKE_DB" --profile smoke-test secret list
bin/monoagentcli --db-path "$SMOKE_DB" --profile smoke-test secret reveal test-aws --reveal

bin/monoagentcli --db-path "$SMOKE_DB" secret rm test-aws
rm -rf "$SMOKE_DIR"
```

Expected, line by line: the `add` succeeds and prints a JSON id/name pair; `list` shows `test-aws` with `field_count: 2`; the first `reveal` prints two `key: value` lines (sorted); the `--json` reveal prints a `fields`/`notes` object; after `update --notes`, the fields are unchanged (still both keys) and the notes text is new; `export` writes the file and prints a passphrase to stderr plus a `path`/`passphrase` JSON pair to stdout; pasting that passphrase into the `import` prompt reports `imported: 1, skipped: 0`; the imported profile's `list`/`reveal` show the same entry and fields; `rm` deletes it. If any step's output does not match, stop and fix it before proceeding — this is the first point the whole CLI surface has been exercised end-to-end by a human rather than by `runSecretCmd` in-process.

- [ ] **Step 4: Manual GUI verification**

This is a Wails desktop app, not a browser page — `go test`/`npm test` verify the Go and JS compile and satisfy their own unit tests, not that the feature actually works end-to-end through the UI. Per this project's own testing rule, that must be confirmed by actually running it.

Run: `cd wails-app && /Users/morteza/go/bin/wails dev` (or use the `run` skill if available) and, in the running app's Vault page:

1. Click "+ Add New Item" — confirm the kind dropdown reads "Keys" / "Login", and the old single password field is gone, replaced by a key/value row editor with one pre-filled row keyed `secret`.
2. Add an item with two custom fields (e.g. `access_key_id` / `secret_access_key`) and save — confirm it appears in the list showing "2 keys" in the Fields column, not a masked value.
3. Click that row — confirm the edit modal opens, pre-filled with both fields (each individually show/hide-toggleable) and any notes.
4. Edit one field's value, add a third field, save — confirm the list still shows the updated field count and the modal reopens with the new state on a second click.
5. Click "Export All" — confirm a save dialog appears, then a passphrase is shown once with a working copy button.
6. Click "Import", pick the file just exported, enter the passphrase — confirm an "Imported 0, skipped N duplicates" summary (since every entry already exists in the same vault) rather than a crash or silent failure.
7. Try importing with a deliberately incorrect passphrase — confirm a clear error message, not a stack trace or blank failure.
8. Delete an item via the row's trash icon — confirm the confirm dialog appears and the row disappears on confirm.

If any of these deviate from the description, note exactly what happened (screenshot if possible) — do not mark this task complete until all eight behave as described.

- [ ] **Step 5: Final commit (only if Steps 3-4 required fixes)**

If manual verification in Steps 3-4 required any code changes, stage and commit them now with a message describing what was fixed. If no fixes were needed, this task has nothing further to commit — Tasks 1-9's commits already cover the full implementation.
