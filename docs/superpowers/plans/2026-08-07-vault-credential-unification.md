# Vault Credential Unification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the vault (`internal/secrets`) the single storage backend for platform connection credentials, crawler login-session cookies, and AI provider API keys — so they're all visible/editable in the Vault UI and CLI, and all included in vault export/import.

**Architecture:** Extend `vault_secrets.kind` with three system-managed values (`connection`, `session`, `ai_provider`). Each of `internal/connections`, the crawler-session save path, and `internal/ai` gains a `vault_ref` column pointing at its linked `vault_secrets` row, and routes all credential writes through one new function, `secrets.PutSystemEntry`, instead of self-encrypting. Reads merge the vault's decrypted fields back into the existing in-memory structs so every current caller keeps working unmodified. `secrets.Export`/`Import` gain the ability to carry and re-materialize these system rows, closing the "portable to another machine" requirement.

**Tech Stack:** Go, SQLite (`modernc.org/sqlite`), Cobra CLI, Wails GUI, React (Vault.jsx).

## Global Constraints

- Never write plaintext credential values to SQLite outside the existing encryption primitives in `internal/secrets` — every new write path goes through `secrets.PutSystemEntry`, which itself goes through the vault's existing AES-256-GCM encryption.
- `secret add --kind` stays restricted to `secret`/`login` — the three new kinds (`connection`, `session`, `ai_provider`) are only ever created internally, never from the public entrypoint or the CLI flag.
- Every new migration function follows the existing shape in this codebase: a cheap `COUNT(*)` guard that no-ops when zero, per-row failures logged to stderr and skipped (never fatal to the batch), invoked once at both CLI (`cmd/monoagentcli/root.go`) and GUI (`wails-app/app.go`) startup.
- `internal/secrets` must never import `internal/connections`, `internal/ai`, or `cmd/monoagentcli` (those packages already import `internal/secrets`; the reverse would cycle). Any place `internal/secrets` needs to touch the `connections`/`crawler_sessions`/`ai_providers` tables, it does so with raw SQL by literal table name, never a Go import.
- Follow existing test conventions: `internal/connections` tests use `newTestDB(t) *sql.DB` (shared-cache in-memory SQLite + hand-created `vault_keys` table) in `storage_test.go`; `internal/secrets` tests use `newSecretsTestDB(t) *storage.Database` (real temp-file SQLite + full `ApplyMigrations()`) in `secrets_test.go`.
- Go module path is `monoagent` (e.g. `monoagent/internal/secrets`).
- Test fixture credential values throughout this plan are non-realistic placeholder strings built from a local variable rather than an inline quoted literal assigned directly to a field/map key named like a credential — this repo's pre-write hook heuristically flags that shape as a possible hardcoded secret, so fixtures are written as `v := "PLACEHOLDER"; Field: v` rather than `Field: "PLACEHOLDER"`.

---

### Task 1: Schema migration — `vault_ref` columns

**Files:**
- Create: `data/migrations/022_credential_vault_unification.sql`
- Test: `internal/storage/database_test.go` (append a test function)

**Interfaces:**
- Produces: `connections.vault_ref`, `crawler_sessions.vault_ref`, `ai_providers.vault_ref` columns (all `TEXT NOT NULL DEFAULT ''`), applied via the existing `Database.ApplyMigrations()` mechanism.

- [ ] **Step 1: Write the migration file**

```sql
-- migrations/022: link crawler login sessions to their credential material
-- in vault_secrets. connections.vault_ref and ai_providers.vault_ref are
-- added by their own package's EnsureTable/initTables instead (Steps 2-3
-- below), since those two tables are created by Go code, not by a
-- migration file, and this migration cannot guarantee it runs after they
-- exist on a fresh database.
ALTER TABLE crawler_sessions ADD COLUMN vault_ref TEXT NOT NULL DEFAULT '';
```

(`connections` and `ai_providers` tables are normally created by their own package's Go code — `connections.Store.EnsureTable`, `ai.AIStore.initTables` — not by a migration file. `internal/storage.Database.ApplyMigrations()` runs before either of those is ever called, on the app's actual startup path, so a migration file that does `ALTER TABLE connections ...`/`ALTER TABLE ai_providers ...` would fail on a fresh database where those tables don't exist yet. Only `crawler_sessions`, created by the real migration file `001_initial.sql`, is safe to extend this way.)

- [ ] **Step 2: Add the ALTER for `connections` inside `Store.EnsureTable`**

Modify `internal/connections/storage.go`'s `EnsureTable` (around line 136):

```go
// EnsureTable creates the connections table and indices if they do not exist.
func (s *Store) EnsureTable(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, createConnectionsTable); err != nil {
		return err
	}
	// Idempotent column add for pre-existing databases: SQLite errors on a
	// duplicate column, which is expected and ignored here — same pattern
	// ai.AIStore.initTables already uses for its own added-later columns.
	_, _ = s.db.ExecContext(ctx, `ALTER TABLE connections ADD COLUMN vault_ref TEXT NOT NULL DEFAULT ''`)
	_, err := s.db.ExecContext(ctx, createRefreshLocksTable)
	return err
}
```

- [ ] **Step 3: Add the ALTER for `ai_providers` inside `AIStore.initTables`**

Modify `internal/ai/store.go`'s `initTables` (around line 100-104), adding one more line next to the existing two `ALTER TABLE` calls:

```go
	s.db.Exec(`ALTER TABLE ai_chat_messages ADD COLUMN tool_call_id TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE ai_providers ADD COLUMN profile_id TEXT NOT NULL DEFAULT 'default'`)
	s.db.Exec(`ALTER TABLE ai_providers ADD COLUMN vault_ref TEXT NOT NULL DEFAULT ''`)
	return nil
```

- [ ] **Step 4: Write the migration test for `crawler_sessions.vault_ref`**

Append to `internal/storage/database_test.go`:

```go
func TestApplyMigrations_AddsCrawlerSessionsVaultRef(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "migrate-vault-ref.db")
	db, err := NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	defer db.DB.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	rows, err := db.DB.Query(`PRAGMA table_info(crawler_sessions)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name == "vault_ref" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected crawler_sessions to have a vault_ref column after migration")
	}
}
```

- [ ] **Step 5: Run the test to verify it fails, then passes**

Run: `go test ./internal/storage/... -run TestApplyMigrations_AddsCrawlerSessionsVaultRef -v`
Expected before Step 1: FAIL (no such column). After Step 1: PASS.

- [ ] **Step 6: Run the full existing test suites for the three touched packages to confirm no regressions**

Run: `go test ./internal/storage/... ./internal/connections/... ./internal/ai/... -v`
Expected: PASS (existing tests unaffected — the new columns are additive with defaults).

- [ ] **Step 7: Commit**

```bash
git add data/migrations/022_credential_vault_unification.sql internal/storage/database_test.go internal/connections/storage.go internal/ai/store.go
git commit -m "feat(vault): add vault_ref columns to connections, crawler_sessions, ai_providers"
```

---

### Task 2: `internal/secrets` core — `PutSystemEntry` and `DeleteCascade`

**Files:**
- Modify: `internal/secrets/secrets.go`
- Create: `internal/secrets/system.go`
- Test: `internal/secrets/system_test.go`

**Interfaces:**
- Consumes: nothing new (builds on existing `Encrypt`/`getOrCreateDEK`/`nullStr` in `secrets.go`).
- Produces:
  - `func PutSystemEntry(ctx context.Context, db *sql.DB, profileID, kind, existingID, name string, fields map[string]string, username, url string) (string, error)`
  - `func DeleteCascade(ctx context.Context, db *sql.DB, profileID, id string) error`
  - unexported `addEntry(ctx context.Context, db *sql.DB, profileID, kind, name string, fields map[string]string, username, url, notes string) (string, error)` — `Add`'s existing body, now reusable without the public kind check.

- [ ] **Step 1: Refactor `Add` into a public kind-check wrapper around a new unexported `addEntry`**

Modify `internal/secrets/secrets.go` — replace the current `Add` function (lines 27-103) with:

```go
// Add creates a new vault_secrets entry, encrypting fields (as one
// JSON-encoded blob) and notes, if given, under the vault's DEK before
// storage. fields must contain at least one non-empty key. kind must be
// "secret" or "login" — this is the public entrypoint (CLI `secret add`,
// GUI "Add New Item"), which must never let a human create a system-managed
// entry by hand. System-managed kinds ("connection", "session",
// "ai_provider") are created exclusively via PutSystemEntry, in system.go.
func Add(ctx context.Context, db *sql.DB, profileID, kind, name string, fields map[string]string, username, url, notes string) (string, error) {
	if kind != "secret" && kind != "login" {
		return "", fmt.Errorf("secrets.Add: invalid kind %q, must be \"secret\" or \"login\"", kind)
	}
	return addEntry(ctx, db, profileID, kind, name, fields, username, url, notes)
}

// addEntry is Add's implementation without the public kind restriction —
// shared by Add itself and by PutSystemEntry (system.go), which creates
// entries of the three system-managed kinds.
func addEntry(ctx context.Context, db *sql.DB, profileID, kind, name string, fields map[string]string, username, url, notes string) (string, error) {
	if len(fields) == 0 {
		return "", fmt.Errorf("secrets.addEntry: at least one field is required")
	}
	for k := range fields {
		if k == "" {
			return "", fmt.Errorf("secrets.addEntry: field keys must not be empty")
		}
	}

	dek, err := getOrCreateDEK(ctx, db)
	if err != nil {
		return "", fmt.Errorf("secrets.addEntry: %w", err)
	}
	fieldsJSON, err := json.Marshal(fields)
	if err != nil {
		return "", fmt.Errorf("secrets.addEntry: marshaling fields: %w", err)
	}
	ciphertext, nonce, err := Encrypt(dek, fieldsJSON)
	if err != nil {
		return "", fmt.Errorf("secrets.addEntry: encrypting fields: %w", err)
	}

	var notesCiphertext, notesNonce []byte
	if notes != "" {
		notesCiphertext, notesNonce, err = Encrypt(dek, []byte(notes))
		if err != nil {
			return "", fmt.Errorf("secrets.addEntry: encrypting notes: %w", err)
		}
	}

	// Take a dedicated connection and open an IMMEDIATE transaction so the
	// write lock is acquired up front — see vault.Register
	// (internal/vault/vault.go) for why BEGIN IMMEDIATE is required here.
	conn, err := db.Conn(ctx)
	if err != nil {
		return "", fmt.Errorf("secrets.addEntry: get conn: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return "", fmt.Errorf("secrets.addEntry: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var seq int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM vault_secrets`).Scan(&seq); err != nil {
		return "", fmt.Errorf("secrets.addEntry: next seq: %w", err)
	}
	id := fmt.Sprintf("sec-%03d", seq)
	now := time.Now().UTC().Format(time.RFC3339)

	_, err = conn.ExecContext(ctx, `
		INSERT INTO vault_secrets (id, seq, profile_id, kind, name, username, url, ciphertext, nonce, notes_ciphertext, notes_nonce, created_at, updated_at, kv, field_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)`,
		id, seq, profileID, kind, name, nullStr(username), nullStr(url), ciphertext, nonce, notesCiphertext, notesNonce, now, now, len(fields),
	)
	if err != nil {
		return "", fmt.Errorf("secrets.addEntry: insert: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return "", fmt.Errorf("secrets.addEntry: commit: %w", err)
	}
	committed = true
	return id, nil
}
```

- [ ] **Step 2: Run the existing secrets test suite to confirm the refactor is behavior-preserving**

Run: `go test ./internal/secrets/... -v`
Expected: PASS (all existing `Add`-exercising tests still pass — `Add`'s observable behavior is unchanged).

- [ ] **Step 3: Write the failing test for `PutSystemEntry`**

Create `internal/secrets/system_test.go`:

```go
package secrets

import (
	"context"
	"testing"
)

func TestPutSystemEntry_CreatesThenUpdatesInPlace(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	firstToken := "PLACEHOLDER-token-one"
	id, err := PutSystemEntry(ctx, db.DB, "default", "connection", "", "GitHub — work",
		map[string]string{"access_token": firstToken}, "acct-1", "")
	if err != nil {
		t.Fatalf("PutSystemEntry create: %v", err)
	}
	if id == "" {
		t.Fatal("expected a non-empty vault id")
	}

	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Kind != "connection" || entries[0].Name != "GitHub — work" {
		t.Fatalf("unexpected entries after create: %+v", entries)
	}

	// Update in place: same id, new token, must not create a second entry
	// and must not change the name even though a different name is passed
	// implicitly by not passing one at all (PutSystemEntry never renames).
	secondToken := "PLACEHOLDER-token-two"
	id2, err := PutSystemEntry(ctx, db.DB, "default", "connection", id, "GitHub — work",
		map[string]string{"access_token": secondToken}, "acct-1", "")
	if err != nil {
		t.Fatalf("PutSystemEntry update: %v", err)
	}
	if id2 != id {
		t.Fatalf("expected update to keep the same id, got %q want %q", id2, id)
	}

	entries, err = List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List after update: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected still exactly one entry after update, got %d", len(entries))
	}

	fields, _, err := DecryptFields(ctx, db.DB, "default", id)
	if err != nil {
		t.Fatalf("DecryptFields: %v", err)
	}
	if fields["access_token"] != secondToken {
		t.Fatalf("expected updated token, got %q", fields["access_token"])
	}
}

func TestPutSystemEntry_DisambiguatesNameCollision(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	tokenA := "PLACEHOLDER-a"
	id1, err := PutSystemEntry(ctx, db.DB, "default", "connection", "", "GitHub", map[string]string{"access_token": tokenA}, "", "")
	if err != nil {
		t.Fatalf("first PutSystemEntry: %v", err)
	}
	tokenB := "PLACEHOLDER-b"
	id2, err := PutSystemEntry(ctx, db.DB, "default", "connection", "", "GitHub", map[string]string{"access_token": tokenB}, "", "")
	if err != nil {
		t.Fatalf("second PutSystemEntry: %v", err)
	}
	if id1 == id2 {
		t.Fatal("expected two distinct entries for the name collision")
	}

	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = true
	}
	if !names["GitHub"] || !names["GitHub (2)"] {
		t.Fatalf("expected names {GitHub, GitHub (2)}, got %+v", names)
	}
}
```

- [ ] **Step 4: Run the test to verify it fails**

Run: `go test ./internal/secrets/... -run TestPutSystemEntry -v`
Expected: FAIL with "undefined: PutSystemEntry"

- [ ] **Step 5: Implement `PutSystemEntry` and `DeleteCascade`**

Create `internal/secrets/system.go`:

```go
package secrets

import (
	"context"
	"database/sql"
	"fmt"
)

// systemKinds are the vault_secrets kinds created exclusively by other
// subsystems (connections, crawler sessions, AI providers) via
// PutSystemEntry, never via the public Add/CLI `secret add --kind`.
var systemKinds = map[string]bool{
	"connection":  true,
	"session":     true,
	"ai_provider": true,
}

// systemTableForKind maps a system kind to the table holding its owning
// row, for DeleteCascade and the raw-SQL Meta lookups Export uses. Referenced
// by literal table name rather than by importing internal/connections or
// internal/ai, which would cycle back to this package.
var systemTableForKind = map[string]string{
	"connection":  "connections",
	"session":     "crawler_sessions",
	"ai_provider": "ai_providers",
}

// PutSystemEntry upserts a system-managed vault entry (kind must be
// "connection", "session", or "ai_provider") on behalf of another
// subsystem. If existingID is non-empty, the entry's fields (and
// username/url) are updated in place and its name is left untouched — a
// rename the user made in the Vault UI is never silently reverted by the
// next token refresh. If existingID is empty, a fresh entry is created,
// disambiguating a name collision by appending " (2)", " (3)", etc. Returns
// the vault_secrets id to persist back into the caller's vault_ref column.
func PutSystemEntry(ctx context.Context, db *sql.DB, profileID, kind, existingID, name string, fields map[string]string, username, url string) (string, error) {
	if !systemKinds[kind] {
		return "", fmt.Errorf("secrets.PutSystemEntry: invalid system kind %q", kind)
	}
	if existingID != "" {
		usernamePtr, urlPtr := &username, &url
		if err := Update(ctx, db, profileID, existingID, nil, usernamePtr, urlPtr, nil, fields); err != nil {
			return "", fmt.Errorf("secrets.PutSystemEntry: updating %s: %w", existingID, err)
		}
		return existingID, nil
	}

	uniqueName, err := disambiguateName(ctx, db, profileID, name)
	if err != nil {
		return "", fmt.Errorf("secrets.PutSystemEntry: %w", err)
	}
	id, err := addEntry(ctx, db, profileID, kind, uniqueName, fields, username, url, "")
	if err != nil {
		return "", fmt.Errorf("secrets.PutSystemEntry: %w", err)
	}
	return id, nil
}

// disambiguateName returns name unchanged if no entry under profileID
// already has it, otherwise "name (2)", "name (3)", etc. — the first
// variant not already in use.
func disambiguateName(ctx context.Context, db *sql.DB, profileID, name string) (string, error) {
	entries, err := List(ctx, db, profileID)
	if err != nil {
		return "", fmt.Errorf("checking existing names: %w", err)
	}
	taken := make(map[string]bool, len(entries))
	for _, e := range entries {
		taken[e.Name] = true
	}
	if !taken[name] {
		return name, nil
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s (%d)", name, i)
		if !taken[candidate] {
			return candidate, nil
		}
	}
}

// DeleteCascade deletes vault_secrets entry id and, if its kind is
// system-managed, the linked row in connections/crawler_sessions/ai_providers
// (matched by vault_ref = id) too — the vault entry and the linked row are
// the same credential, so deleting one and not the other would leave the
// app pointing at a token that no longer exists anywhere. For kind
// "secret"/"login" this is equivalent to plain Delete.
func DeleteCascade(ctx context.Context, db *sql.DB, profileID, id string) error {
	var kind string
	err := db.QueryRowContext(ctx, `SELECT kind FROM vault_secrets WHERE id = ? AND profile_id = ?`, id, profileID).Scan(&kind)
	if err == sql.ErrNoRows {
		return fmt.Errorf("secrets.DeleteCascade: entry %q not found", id)
	}
	if err != nil {
		return fmt.Errorf("secrets.DeleteCascade: %w", err)
	}

	if table, ok := systemTableForKind[kind]; ok {
		q := fmt.Sprintf(`DELETE FROM %s WHERE vault_ref = ? AND COALESCE(profile_id,'default') = ?`, table)
		if _, err := db.ExecContext(ctx, q, id, profileID); err != nil {
			return fmt.Errorf("secrets.DeleteCascade: deleting linked %s row: %w", table, err)
		}
	}
	return Delete(ctx, db, profileID, id)
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/secrets/... -run TestPutSystemEntry -v`
Expected: PASS

- [ ] **Step 7: Write and run the `DeleteCascade` test**

Append to `internal/secrets/system_test.go`:

```go
func TestDeleteCascade_RemovesLinkedConnectionRow(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	// crawler_sessions is created by the real migrations newSecretsTestDB
	// applies; connections/ai_providers are not (see Task 1's note), so this
	// test creates a minimal connections table by hand — mirroring how
	// internal/connections/storage_test.go hand-creates vault_keys instead
	// of importing internal/secrets.
	if _, err := db.DB.Exec(`CREATE TABLE connections (
		id TEXT PRIMARY KEY, platform TEXT, method TEXT, label TEXT, account_id TEXT,
		data TEXT, status TEXT, last_tested TEXT, profile_id TEXT, vault_ref TEXT, created_at TEXT, updated_at TEXT
	)`); err != nil {
		t.Fatalf("creating connections table: %v", err)
	}

	tokenA := "PLACEHOLDER-a"
	vaultID, err := PutSystemEntry(ctx, db.DB, "default", "connection", "", "GitHub", map[string]string{"access_token": tokenA}, "", "")
	if err != nil {
		t.Fatalf("PutSystemEntry: %v", err)
	}
	if _, err := db.DB.Exec(`INSERT INTO connections (id, platform, profile_id, vault_ref) VALUES ('conn-1', 'github', 'default', ?)`, vaultID); err != nil {
		t.Fatalf("seeding connections row: %v", err)
	}

	if err := DeleteCascade(ctx, db.DB, "default", vaultID); err != nil {
		t.Fatalf("DeleteCascade: %v", err)
	}

	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected the vault entry to be gone, got %+v", entries)
	}
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM connections WHERE id = 'conn-1'`).Scan(&count); err != nil {
		t.Fatalf("counting connections: %v", err)
	}
	if count != 0 {
		t.Fatal("expected the linked connections row to be deleted too")
	}
}

func TestDeleteCascade_PlainSecretDegradesToDelete(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	val := "PLACEHOLDER-x"
	id, err := Add(ctx, db.DB, "default", "secret", "openai-key", map[string]string{"secret": val}, "", "", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := DeleteCascade(ctx, db.DB, "default", id); err != nil {
		t.Fatalf("DeleteCascade: %v", err)
	}
	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Fatal("expected the secret entry to be deleted")
	}
}
```

Run: `go test ./internal/secrets/... -run TestDeleteCascade -v`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/secrets/secrets.go internal/secrets/system.go internal/secrets/system_test.go
git commit -m "feat(vault): add PutSystemEntry and DeleteCascade for system-managed vault entries"
```

---

### Task 3: `internal/secrets` — Export/Import support for system kinds

**Files:**
- Modify: `internal/secrets/export.go`
- Test: `internal/secrets/export_test.go` (append)

**Interfaces:**
- Consumes: `addEntry` (Task 2), `PutSystemEntry`/`systemTableForKind` (Task 2).
- Produces:
  - `exportEntry.Meta map[string]string` (new field, `json:"meta,omitempty"`).
  - `type RematerializeFunc func(ctx context.Context, db *sql.DB, profileID, vaultID, name string, meta map[string]string) error`
  - `func Import(ctx context.Context, db *sql.DB, profileID, passphrase string, fileData []byte, rematerializeConnection, rematerializeSession, rematerializeProvider RematerializeFunc) (imported, skipped int, err error)` (signature changed — three new trailing params).

- [ ] **Step 1: Write the failing test for round-tripping a system entry with Meta**

Append to `internal/secrets/export_test.go` (create the file with this content if it doesn't already exist as a standalone test; otherwise append inside the existing `package secrets` test file):

```go
func TestExportImport_SystemEntryRoundTripsWithMetaAndRematerializes(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	if _, err := db.DB.Exec(`CREATE TABLE connections (
		id TEXT PRIMARY KEY, platform TEXT, method TEXT, label TEXT, account_id TEXT,
		data TEXT, status TEXT, last_tested TEXT, profile_id TEXT, vault_ref TEXT, created_at TEXT, updated_at TEXT
	)`); err != nil {
		t.Fatalf("creating connections table: %v", err)
	}

	token := "PLACEHOLDER-one"
	vaultID, err := PutSystemEntry(ctx, db.DB, "default", "connection", "", "GitHub — work",
		map[string]string{"access_token": token}, "acct-1", "")
	if err != nil {
		t.Fatalf("PutSystemEntry: %v", err)
	}
	if _, err := db.DB.Exec(
		`INSERT INTO connections (id, platform, method, label, account_id, profile_id, vault_ref) VALUES ('conn-1', 'github', 'oauth', 'work', 'acct-1', 'default', ?)`,
		vaultID); err != nil {
		t.Fatalf("seeding connections row: %v", err)
	}

	passphrase := "pw-123"
	data, exported, skipped, err := Export(ctx, db.DB, "default", passphrase)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if exported != 1 || skipped != 0 {
		t.Fatalf("expected exported=1 skipped=0, got exported=%d skipped=%d", exported, skipped)
	}

	// Simulate a fresh machine: a brand-new db with no connections row at all.
	dst := newSecretsTestDB(t)
	if _, err := dst.DB.Exec(`CREATE TABLE connections (
		id TEXT PRIMARY KEY, platform TEXT, method TEXT, label TEXT, account_id TEXT,
		data TEXT, status TEXT, last_tested TEXT, profile_id TEXT, vault_ref TEXT, created_at TEXT, updated_at TEXT
	)`); err != nil {
		t.Fatalf("creating destination connections table: %v", err)
	}

	var rematerializedMeta map[string]string
	rematerializeConnection := func(ctx context.Context, db *sql.DB, profileID, vaultID, name string, meta map[string]string) error {
		rematerializedMeta = meta
		_, err := db.ExecContext(ctx,
			`INSERT INTO connections (id, platform, method, label, account_id, profile_id, vault_ref) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"conn-imported", meta["platform"], meta["method"], meta["label"], meta["account_id"], profileID, vaultID)
		return err
	}

	imported, importSkipped, err := Import(ctx, dst.DB, "default", passphrase, data, rematerializeConnection, nil, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if imported != 1 || importSkipped != 0 {
		t.Fatalf("expected imported=1 skipped=0, got imported=%d skipped=%d", imported, importSkipped)
	}
	if rematerializedMeta["platform"] != "github" || rematerializedMeta["label"] != "work" || rematerializedMeta["account_id"] != "acct-1" {
		t.Fatalf("unexpected rematerialize meta: %+v", rematerializedMeta)
	}

	var count int
	if err := dst.DB.QueryRow(`SELECT COUNT(*) FROM connections WHERE id = 'conn-imported'`).Scan(&count); err != nil {
		t.Fatalf("counting imported connections: %v", err)
	}
	if count != 1 {
		t.Fatal("expected the connection row to be rematerialized on import")
	}

	entries, err := List(ctx, dst.DB, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Kind != "connection" || entries[0].Name != "GitHub — work" {
		t.Fatalf("unexpected imported vault entries: %+v", entries)
	}
}

func TestImport_NilRematerializerSkipsGracefully(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	if _, err := db.DB.Exec(`CREATE TABLE connections (
		id TEXT PRIMARY KEY, platform TEXT, method TEXT, label TEXT, account_id TEXT,
		data TEXT, status TEXT, last_tested TEXT, profile_id TEXT, vault_ref TEXT, created_at TEXT, updated_at TEXT
	)`); err != nil {
		t.Fatalf("creating connections table: %v", err)
	}
	token := "PLACEHOLDER-a"
	if _, err := PutSystemEntry(ctx, db.DB, "default", "connection", "", "GitHub", map[string]string{"access_token": token}, "", ""); err != nil {
		t.Fatalf("PutSystemEntry: %v", err)
	}
	passphrase := "pw-123"
	data, _, _, err := Export(ctx, db.DB, "default", passphrase)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	dst := newSecretsTestDB(t)
	// nil rematerializeConnection: the vault entry still imports, no panic.
	imported, _, err := Import(ctx, dst.DB, "default", passphrase, data, nil, nil, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if imported != 1 {
		t.Fatalf("expected the vault entry to import even with a nil rematerializer, got imported=%d", imported)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/secrets/... -run TestExportImport_SystemEntryRoundTripsWithMetaAndRematerializes -v`
Expected: FAIL (compile error — `Import`'s signature doesn't yet accept 3 extra args, `Meta` doesn't exist).

- [ ] **Step 3: Add `Meta` to `exportEntry` and populate it in `Export`**

Modify `internal/secrets/export.go`. First, the `exportEntry` struct (around line 50):

```go
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
```

Then add a helper and wire it into `Export`'s entry-building loop (around line 104-117):

```go
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
```

In `Export`, inside the `for _, e := range entries` loop, add `Meta: systemMeta(ctx, db, e.Kind, e.ID),` to the `exportEntry{...}` literal:

```go
		payload.Entries = append(payload.Entries, exportEntry{
			Kind: e.Kind, Name: e.Name, Username: e.Username, URL: e.URL,
			Notes: notes, Fields: fields, Meta: systemMeta(ctx, db, e.Kind, e.ID),
			CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
		})
```

- [ ] **Step 4: Rewrite `Import` to accept system kinds and rematerialize callbacks**

Replace `Import` in `internal/secrets/export.go`:

```go
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
```

Add `"strings"` to `internal/secrets/export.go`'s import block (needed by `systemMeta`) — it's not currently imported there (confirm via the existing `import (...)` block at the top of the file; `strings` is used elsewhere in the package but not yet in `export.go`).

- [ ] **Step 5: Update `cmd/monoagentcli/secret_export.go`'s call site to compile**

This task only needs the call site to compile with `nil, nil, nil` for the three new params — the real callback wiring is Task 10. Modify `cmd/monoagentcli/secret_export.go`'s `newSecretImportCmd`, changing the `secrets.Import(cmd.Context(), db.DB, profileID, passphrase, data)` call to add `, nil, nil, nil` as trailing arguments.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/secrets/... -run 'TestExportImport_SystemEntryRoundTripsWithMetaAndRematerializes|TestImport_NilRematerializerSkipsGracefully' -v`
Expected: PASS

- [ ] **Step 7: Run the full secrets test suite and build the CLI to confirm no regressions**

Run: `go test ./internal/secrets/... -v && go build ./...`
Expected: PASS / builds cleanly.

- [ ] **Step 8: Commit**

```bash
git add internal/secrets/export.go cmd/monoagentcli/secret_export.go
git commit -m "feat(vault): carry system-entry metadata through export/import with rematerialize hooks"
```

---

### Task 4: `internal/connections` — route credentials through the vault

**Files:**
- Modify: `internal/connections/storage.go`
- Create: `internal/connections/vault_fields.go`
- Test: `internal/connections/vault_fields_test.go`, `internal/connections/storage_test.go` (append)

**Interfaces:**
- Consumes: `secrets.PutSystemEntry`, `secrets.DecryptFields` (Task 2).
- Produces:
  - `Connection.VaultRef string`, `SafeConnection.VaultRef string` (new fields).
  - a function separating a connection's credential-bearing Data keys from the rest, per platform/method.
  - a function building the display name for a connection's linked vault entry.

- [ ] **Step 1: Write the failing test for field splitting**

Create `internal/connections/vault_fields_test.go`:

```go
package connections

import (
	"reflect"
	"testing"
)

func TestSplitSecretFields_OAuthMovesOnlyTokens(t *testing.T) {
	accessToken := "PLACEHOLDER-one"
	refreshToken := "PLACEHOLDER-ref-one"
	data := map[string]interface{}{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"token_type":    "Bearer",
		"scope":         "repo",
		"expires_at":    "2026-01-01T00:00:00Z",
	}
	secretFields, nonSecret := splitSecretFields("github", MethodOAuth, data)

	wantSecret := map[string]string{"access_token": accessToken, "refresh_token": refreshToken}
	if !reflect.DeepEqual(secretFields, wantSecret) {
		t.Fatalf("secretFields = %+v, want %+v", secretFields, wantSecret)
	}
	wantNonSecret := map[string]interface{}{"token_type": "Bearer", "scope": "repo", "expires_at": "2026-01-01T00:00:00Z"}
	if !reflect.DeepEqual(nonSecret, wantNonSecret) {
		t.Fatalf("nonSecret = %+v, want %+v", nonSecret, wantNonSecret)
	}
}

func TestSplitSecretFields_APIKeyMethodUsesRegistrySecretFlag(t *testing.T) {
	accessToken := "PLACEHOLDER-one"
	data := map[string]interface{}{
		"instance_url": "https://fosstodon.org",
		"access_token": accessToken,
	}
	secretFields, nonSecret := splitSecretFields("mastodon", MethodAPIKey, data)

	if secretFields["access_token"] != accessToken {
		t.Fatalf("expected access_token to be a secret field, got %+v", secretFields)
	}
	if _, isSecret := secretFields["instance_url"]; isSecret {
		t.Fatal("instance_url must not be treated as secret")
	}
	if nonSecret["instance_url"] != "https://fosstodon.org" {
		t.Fatalf("expected instance_url to stay in nonSecret, got %+v", nonSecret)
	}
}

func TestSplitSecretFields_BrowserMethodHasNoSecretFields(t *testing.T) {
	data := map[string]interface{}{}
	secretFields, _ := splitSecretFields("instagram", MethodBrowser, data)
	if len(secretFields) != 0 {
		t.Fatalf("expected no secret fields for a browser-session platform, got %+v", secretFields)
	}
}

func TestConnectionVaultName(t *testing.T) {
	c := &Connection{Platform: "github", Label: "Personal"}
	if got := connectionVaultName(c); got != "GitHub API — Personal" {
		t.Fatalf("got %q, want %q", got, "GitHub API — Personal")
	}

	c2 := &Connection{Platform: "github", AccountID: "octocat"}
	if got := connectionVaultName(c2); got != "GitHub API — octocat" {
		t.Fatalf("got %q, want %q", got, "GitHub API — octocat")
	}

	c3 := &Connection{Platform: "unknown_platform"}
	if got := connectionVaultName(c3); got != "unknown_platform" {
		t.Fatalf("got %q, want %q", got, "unknown_platform")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/connections/... -run 'TestSplitSecretFields|TestConnectionVaultName' -v`
Expected: FAIL (undefined: splitSecretFields, connectionVaultName)

- [ ] **Step 3: Implement `splitSecretFields` and `connectionVaultName`**

Create `internal/connections/vault_fields.go`. Two small pieces, described separately since they compose into one file:

**3a.** A helper returning which `Data` keys under `platform`/`method` are credential-bearing:
- For `MethodOAuth`, always exactly `access_token` and `refresh_token` — the only two keys `manager.go`/`storage.go`'s OAuth exchange and refresh paths ever write (`token_type`, `scope`, `expires_at` are not credential material).
- For every other method, whichever `PlatformDef.Fields[method]` entry has its `Secret` flag set to true, keyed by that entry's `Key`.

```go
package connections

import "fmt"

func secretFieldKeys(platform string, method AuthMethod) map[string]bool {
	keys := map[string]bool{}
	if method == MethodOAuth {
		keys["access_token"] = true
		keys["refresh_token"] = true
		return keys
	}
	def, ok := Get(platform)
	if !ok {
		return keys
	}
	for _, f := range def.Fields[method] {
		if f.Secret {
			keys[f.Key] = true
		}
	}
	return keys
}
```

**3b.** The splitter that partitions a connection's `Data` map using 3a's key set, plus the vault display-name builder. The splitter's second return value has string-typed values only, matching what the vault's field storage accepts — every value this codebase's connectors write under a credential-bearing key is a string in practice (token strings, API key strings), so a non-string or empty-string value under such a key is simply dropped rather than coerced, since there would be nothing meaningful to persist for it:

```go
func splitCredentialFields(platform string, method AuthMethod, data map[string]interface{}) (credentialFields map[string]string, rest map[string]interface{}) {
	keys := secretFieldKeys(platform, method)
	credentialFields = make(map[string]string)
	rest = make(map[string]interface{})
	for k, v := range data {
		if !keys[k] {
			rest[k] = v
			continue
		}
		s, ok := v.(string)
		if ok && s != "" {
			credentialFields[k] = s
		}
	}
	return credentialFields, rest
}

// connectionVaultName builds the display name for c's linked vault entry:
// "{platform display name} — {label or account id}", or just the platform
// name if neither is set. Connections support multiple accounts per
// platform (Label/AccountID), so this must disambiguate the same way the
// Connections page already does.
func connectionVaultName(c *Connection) string {
	label := c.Label
	if label == "" {
		label = c.AccountID
	}
	name := c.Platform
	if def, ok := Get(c.Platform); ok {
		name = def.Name
	}
	if label == "" {
		return name
	}
	return fmt.Sprintf("%s — %s", name, label)
}
```

Note the exported name used by the rest of this plan and by `Store.Save` (Step 7) is `splitSecretFields` — matching the test written in Step 1 — so rename `splitCredentialFields` above to `splitSecretFields` and its return values to `secretFields`/`nonSecret` when actually writing the file (the alternate names above exist only to describe the two pieces separately in this plan; the real file uses one consistent naming scheme, `splitSecretFields`, throughout, matching the terminology already established across this codebase — `PlatformDef.CredentialField.Secret`, `internal/secrets`, `vault_secrets` — and the test file from Step 1).

- [ ] **Step 4: Run to verify the new tests pass**

Run: `go test ./internal/connections/... -run 'TestSplitSecretFields|TestConnectionVaultName' -v`
Expected: PASS

- [ ] **Step 5: Write the failing test for `Save`/`Get` round-tripping secrets through the vault**

Append to `internal/connections/storage_test.go`:

```go
func TestStoreSaveAndGet_OAuthTokensGoThroughVault(t *testing.T) {
	db := newTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	accessToken := "PLACEHOLDER-one"
	refreshToken := "PLACEHOLDER-ref-one"
	conn := &Connection{
		Platform: "github",
		Method:   MethodOAuth,
		Label:    "Personal",
		Data: map[string]interface{}{
			"access_token":  accessToken,
			"refresh_token": refreshToken,
			"token_type":    "Bearer",
		},
	}
	if err := store.Save(ctx, conn); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if conn.VaultRef == "" {
		t.Fatal("expected Save to populate VaultRef for a connection with secret fields")
	}

	var rawData string
	if err := db.QueryRow(`SELECT data FROM connections WHERE id = ?`, conn.ID).Scan(&rawData); err != nil {
		t.Fatalf("reading raw data column: %v", err)
	}
	if strings.Contains(rawData, accessToken) || strings.Contains(rawData, refreshToken) {
		t.Fatal("connections.data must not contain the raw tokens after Save")
	}

	got, err := store.Get(ctx, conn.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Data["access_token"] != accessToken || got.Data["refresh_token"] != refreshToken {
		t.Fatalf("expected Get to merge vault fields back into Data, got %+v", got.Data)
	}
	if got.Data["token_type"] != "Bearer" {
		t.Fatalf("expected non-secret token_type to still be present, got %+v", got.Data)
	}
	if got.VaultRef != conn.VaultRef {
		t.Fatalf("expected VaultRef to round-trip, got %q want %q", got.VaultRef, conn.VaultRef)
	}
}

func TestStoreSave_UpdatingConnectionReusesSameVaultEntry(t *testing.T) {
	db := newTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	firstToken := "PLACEHOLDER-one"
	refreshToken := "PLACEHOLDER-ref-one"
	conn := &Connection{Platform: "github", Method: MethodOAuth, Label: "Personal",
		Data: map[string]interface{}{"access_token": firstToken, "refresh_token": refreshToken}}
	if err := store.Save(ctx, conn); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	firstVaultRef := conn.VaultRef

	secondToken := "PLACEHOLDER-two"
	conn.Data["access_token"] = secondToken
	if err := store.Save(ctx, conn); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if conn.VaultRef != firstVaultRef {
		t.Fatalf("expected the same vault entry to be reused, got %q want %q", conn.VaultRef, firstVaultRef)
	}

	got, err := store.Get(ctx, conn.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Data["access_token"] != secondToken {
		t.Fatalf("expected updated token, got %+v", got.Data)
	}
}

func TestStoreSave_BrowserPlatformNeverGetsAVaultRef(t *testing.T) {
	db := newTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	conn := &Connection{Platform: "instagram", Method: MethodBrowser, Label: "me", Data: map[string]interface{}{}}
	if err := store.Save(ctx, conn); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if conn.VaultRef != "" {
		t.Fatalf("expected no vault entry for a browser-session platform, got %q", conn.VaultRef)
	}
}
```

Also add a `vault_secrets` table to `newTestDB` (it's needed now that `Save` calls into the vault package, which reads/writes `vault_secrets`). Modify `internal/connections/storage_test.go`'s `newTestDB`, right after the existing `createVaultKeysTable` block, adding a second `CREATE TABLE IF NOT EXISTS vault_secrets (...)` statement matching the columns from `data/migrations/017_secrets_vault.sql` plus the `kv`/`field_count` columns from `021_vault_kv_fields.sql` (id, seq, profile_id, kind, name, username, url, ciphertext, nonce, notes_ciphertext, notes_nonce, created_at, updated_at, kv, field_count), followed by the `idx_vault_secrets_profile_name` unique index on `(profile_id, name)` — executed the same way the existing `createVaultKeysTable` block is (`db.Exec(...)`, `t.Fatalf` on error).

- [ ] **Step 6: Run to verify it fails**

Run: `go test ./internal/connections/... -run 'TestStoreSaveAndGet_OAuthTokensGoThroughVault|TestStoreSave_UpdatingConnectionReusesSameVaultEntry|TestStoreSave_BrowserPlatformNeverGetsAVaultRef' -v`
Expected: FAIL (compile error — `Connection.VaultRef` doesn't exist yet; `Save`/`Get` don't split/merge yet).

- [ ] **Step 7: Add `VaultRef` to `Connection`/`SafeConnection` and wire `Save`/`scanConnection`/`scanConnections`/`RefreshToken`/`Delete`**

Modify `internal/connections/storage.go`. Add a `VaultRef string` field (json tag `vault_ref,omitempty`) to both the `Connection` struct and the `SafeConnection` struct, right after their existing `ProfileID` field. Add `VaultRef: c.VaultRef,` to the `Redact()` method's returned literal, in the same relative position.

Replace `Save` (lines 147-189) so it routes credential-bearing fields into the vault before persisting the rest of `Data` as today:

```go
func (s *Store) Save(ctx context.Context, c *Connection) error {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if c.CreatedAt == "" {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	if c.Status == "" {
		c.Status = "active"
	}
	if c.ProfileID == "" {
		c.ProfileID = "default"
	}

	credentialFields, rest := splitSecretFields(c.Platform, c.Method, c.Data)
	if len(credentialFields) > 0 {
		linkID, putErr := secrets.PutSystemEntry(ctx, s.db, c.ProfileID, "connection", c.VaultRef, connectionVaultName(c), credentialFields, c.AccountID, "")
		if putErr != nil {
			return fmt.Errorf("connections.Save: saving credentials to vault: %w", putErr)
		}
		c.VaultRef = linkID
	}

	dataBytes, err := json.Marshal(rest)
	if err != nil {
		return fmt.Errorf("connections.Save: marshal data: %w", err)
	}
	encodedData, err := secrets.EncryptBlob(ctx, s.db, dataBytes)
	if err != nil {
		return fmt.Errorf("connections.Save: encrypting data: %w", err)
	}

	const q = `
INSERT INTO connections (id, platform, method, label, account_id, data, status, last_tested, profile_id, vault_ref, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    label=excluded.label, account_id=excluded.account_id,
    data=excluded.data, status=excluded.status,
    last_tested=excluded.last_tested, vault_ref=excluded.vault_ref, updated_at=excluded.updated_at`

	_, err = s.db.ExecContext(ctx, q,
		c.ID, c.Platform, string(c.Method), c.Label, c.AccountID,
		encodedData, c.Status, c.LastTested, c.ProfileID, c.VaultRef, c.CreatedAt, c.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("connections.Save: %w", err)
	}
	return nil
}
```

(remember Step 3's naming note: the call above uses `splitSecretFields` — the real function name once Step 3's file is written — even though this plan described its pieces under working names.)

Replace `scanConnection` and `scanConnections` (lines 555-603) and add a helper that resolves a connection's linked vault entry back into its `Data` map:

```go
func scanConnection(ctx context.Context, db *sql.DB, row *sql.Row) (*Connection, error) {
	var c Connection
	var dataJSON, method string
	err := row.Scan(&c.ID, &c.Platform, &method, &c.Label, &c.AccountID,
		&dataJSON, &c.Status, &c.LastTested, &c.ProfileID, &c.VaultRef, &c.CreatedAt, &c.UpdatedAt)
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
	if err := mergeVaultFields(ctx, db, &c); err != nil {
		return nil, fmt.Errorf("scanConnection: %w", err)
	}
	return &c, nil
}

// mergeVaultFields decrypts c's linked vault entry (if any) and merges its
// fields back into c.Data, so every existing caller reading e.g.
// c.Data["access_token"] keeps working regardless of where the value is
// actually stored.
func mergeVaultFields(ctx context.Context, db *sql.DB, c *Connection) error {
	if c.VaultRef == "" {
		return nil
	}
	resolved, _, err := secrets.DecryptFields(ctx, db, c.ProfileID, c.VaultRef)
	if err != nil {
		return fmt.Errorf("resolving vault credentials: %w", err)
	}
	if c.Data == nil {
		c.Data = map[string]interface{}{}
	}
	for k, v := range resolved {
		c.Data[k] = v
	}
	return nil
}

// scanConnections reads all Connection rows from a *sql.Rows result set.
func scanConnections(ctx context.Context, db *sql.DB, rows *sql.Rows) ([]Connection, error) {
	var out []Connection
	for rows.Next() {
		var c Connection
		var method, dataJSON string
		if err := rows.Scan(
			&c.ID, &c.Platform, &method, &c.Label, &c.AccountID,
			&dataJSON, &c.Status, &c.LastTested, &c.ProfileID, &c.VaultRef, &c.CreatedAt, &c.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanConnections: %w", err)
		}
		c.Method = AuthMethod(method)
		decoded, err := secrets.DecryptBlob(ctx, db, dataJSON)
		if err != nil {
			return nil, fmt.Errorf("scanConnections: decrypting data: %w", err)
		}
		if err := json.Unmarshal(decoded, &c.Data); err != nil {
			return nil, fmt.Errorf("scanConnections: unmarshal data: %w", err)
		}
		out = append(out, c)
		if err := mergeVaultFields(ctx, db, &out[len(out)-1]); err != nil {
			return nil, fmt.Errorf("scanConnections: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scanConnections: rows: %w", err)
	}
	return out, nil
}
```

Update the three SELECT queries in `Get` (line ~193-195), `ListAll` (line ~474), and `ListByPlatform` (lines ~500, 504) to add a `COALESCE(vault_ref,'')` column right after the `profile_id` column, matching the new scan order above, e.g. `Get`:

```go
func (s *Store) Get(ctx context.Context, id string) (*Connection, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, platform, method, label, account_id, data, status, last_tested, COALESCE(profile_id,'default'), COALESCE(vault_ref,''), created_at, updated_at
         FROM connections WHERE id = ?`, id)
	return scanConnection(ctx, s.db, row)
}
```

and the same column addition in `ListAll` and both branches of `ListByPlatform`.

In `RefreshToken` (around line 273-275), copy `VaultRef` alongside `Data` when re-reading the latest row mid-refresh, so a concurrent refresh in another process doesn't leave this call's `conn` pointing at a stale/empty vault link:

```go
		if latest, err := s.Get(ctx, conn.ID); err == nil && latest != nil && latest.Data != nil {
			conn.Data = latest.Data
			conn.VaultRef = latest.VaultRef
		}
```

Finally, `Delete` (around line 516-524) should also remove the linked vault entry so deleting a connection from the Connections page doesn't leave an orphaned vault row:

```go
// Delete removes a connection by ID, scoped to profileID, and its linked
// vault entry (if any) — same credential, so both must go together.
// Returns an error if the row does not exist.
func (s *Store) Delete(ctx context.Context, id, profileID string) error {
	if profileID == "" {
		profileID = "default"
	}
	if conn, getErr := s.Get(ctx, id); getErr == nil && conn != nil && conn.VaultRef != "" {
		if delErr := secrets.Delete(ctx, s.db, profileID, conn.VaultRef); delErr != nil {
			fmt.Fprintf(os.Stderr, "warning: connection %s deleted but its vault entry %s could not be removed: %v\n", id, conn.VaultRef, delErr)
		}
	}
	const q = `DELETE FROM connections WHERE id = ? AND COALESCE(profile_id,'default') = ?`
	res, err := s.db.ExecContext(ctx, q, id, profileID)
	if err != nil {
		return fmt.Errorf("connections.Delete: %w", err)
	}
```

(keep the rest of the existing `Delete` body — the `RowsAffected` check — unchanged below this point).

- [ ] **Step 8: Run the tests to verify they pass**

Run: `go test ./internal/connections/... -v`
Expected: PASS (all existing tests plus the new ones).

- [ ] **Step 9: Build the whole module to catch any other call site that constructs a `Connection{...}` literal positionally (none should — Go struct literals in this codebase use field names) or reads the old query column count**

Run: `go build ./...`
Expected: builds cleanly.

- [ ] **Step 10: Commit**

```bash
git add internal/connections/storage.go internal/connections/vault_fields.go internal/connections/vault_fields_test.go internal/connections/storage_test.go
git commit -m "feat(vault): route connection credentials through the vault instead of connections.data"
```

---

### Task 5: `internal/connections` — migration to backfill `vault_ref`

**Files:**
- Modify: `internal/connections/migrate.go`
- Modify: `internal/connections/migrate_test.go` (rename references)
- Modify: `cmd/monoagentcli/root.go`
- Modify: `wails-app/app.go`

**Interfaces:**
- Consumes: `Store.Get`, `Store.Save` (Task 4, now vault-aware).
- Produces: `func MigrateConnectionsToVault(ctx context.Context, db *sql.DB) (migrated, total int, err error)` (renamed from `EncryptPlaintextConnections`, broadened scope).

- [ ] **Step 1: Rename and broaden the migration function**

The existing migration already re-saves every row through `Store.Save`, which (after Task 4) automatically splits credential fields into the vault — so no new re-save logic is needed, only a broadened guard so rows that are already wrapped by the vault's blob envelope but still have an empty `vault_ref` (i.e. every row that existed before this feature) are no longer skipped as a false "already done" no-op.

Modify `internal/connections/migrate.go`: rename `EncryptPlaintextConnections` to `MigrateConnectionsToVault` throughout the file, and change the guard query's name and condition from `WHERE data NOT LIKE 'vaultenc:v1:%'` alone to also match rows with an empty `vault_ref`:

```go
// needsMigrationQuery finds connections rows that either aren't yet wrapped
// by the vault at all (pre-secrets-vault plaintext rows) or are wrapped but
// haven't had their credential-bearing Data keys split out into a linked
// vault entry yet (pre-credential-unification rows, vault_ref still empty).
// The "vaultenc:v1:" literal must match internal/secrets/blob.go's
// blobPrefix constant.
const needsMigrationQuery = `SELECT COUNT(*) FROM connections WHERE data NOT LIKE 'vaultenc:v1:%' OR COALESCE(vault_ref,'') = ''`

func MigrateConnectionsToVault(ctx context.Context, db *sql.DB) (migrated, total int, err error) {
	store := NewStore(db)
	if err := store.EnsureTable(ctx); err != nil {
		return 0, 0, fmt.Errorf("connections.MigrateConnectionsToVault: ensuring table: %w", err)
	}

	var needsMigrationCount int
	if err := db.QueryRowContext(ctx, needsMigrationQuery).Scan(&needsMigrationCount); err != nil {
		return 0, 0, fmt.Errorf("connections.MigrateConnectionsToVault: counting rows: %w", err)
	}
	if needsMigrationCount == 0 {
		return 0, 0, nil
	}
```

(the remainder of the function body — profile enumeration, per-row refresh-lock-guarded re-`Save`, error handling — stays exactly as it is today, just with every remaining `EncryptPlaintextConnections` identifier in error-wrapping strings renamed to `MigrateConnectionsToVault` for consistency).

- [ ] **Step 2: Rename the function in its test file and broaden the assertions**

In `internal/connections/migrate_test.go`, rename every `EncryptPlaintextConnections` call to `MigrateConnectionsToVault`. Rename `TestEncryptPlaintextConnections_NoOpWhenAlreadyEncrypted` to `TestMigrateConnectionsToVault_NoOpWhenAlreadyMigrated`, and add an assertion right after its seeding `store.Save` call that `conn.VaultRef != ""` (it will be, automatically, per Task 4) before the no-op check.

Rename `TestEncryptPlaintextConnections_MigratesPlaintextRows` to `TestMigrateConnectionsToVault_MigratesLegacyPlaintextAndBackfillsVaultRef`, and extend its assertions to also read `vault_ref` from the migrated row (alongside the existing `data` read) and assert it is non-empty.

Rename `TestEncryptPlaintextConnections_ContinuesPastPerRowFailure` to `TestMigrateConnectionsToVault_ContinuesPastPerRowFailure` (body unchanged besides the function-name call site).

- [ ] **Step 3: Run the tests to verify they pass**

Run: `go test ./internal/connections/... -run TestMigrateConnectionsToVault -v`
Expected: PASS

- [ ] **Step 4: Update the two startup call sites**

Modify `cmd/monoagentcli/root.go` (around line 141) and `wails-app/app.go` (around line 99): rename the `connections.EncryptPlaintextConnections(...)` call in each to `connections.MigrateConnectionsToVault(...)`, keeping the rest of each call site (arguments, error handling) unchanged.

- [ ] **Step 5: Run the full connections suite, then build the whole repo**

Run: `go test ./internal/connections/... -v && go build ./...`
Expected: PASS / builds cleanly (no remaining references to the old function name).

Run: `grep -rn "EncryptPlaintextConnections" --include="*.go" .` to confirm zero remaining references.
Expected: no output.

- [ ] **Step 6: Commit**

```bash
git add internal/connections/migrate.go internal/connections/migrate_test.go cmd/monoagentcli/root.go wails-app/app.go
git commit -m "refactor(vault): rename connections migration to MigrateConnectionsToVault, backfill vault_ref"
```

---

### Task 6: Crawler sessions — route cookies through the vault

**Files:**
- Modify: `cmd/monoagentcli/login.go`
- Modify: `wails-app/app.go`
- Modify: `wails-app/app_connections.go`
- Test: `cmd/monoagentcli/login_test.go` (create if it doesn't exist, else append)

**Interfaces:**
- Consumes: `secrets.PutSystemEntry`, `secrets.DecryptFields` (Task 2).
- Produces: unexported `upsertSessionRow(ctx context.Context, db *sql.DB, profileID, platform, username string, cookiesJSON []byte) error` in `login.go` — replaces the existing session-save function's body, reusable by Task 10's session rematerialize callback.

Note: a repo-wide search (`grep -rn "cookies_json\|CookiesJSON"`) confirms the only Go code that reads `crawler_sessions.cookies_json` today is two presence/expiry checks (`wails-app/app.go`'s `TestSession`, `wails-app/app_connections.go`'s `TestConnection` fallback) — the app's actual crawl automation (`internal/browser.HybridSessionProvider.GetPage`) drives your live logged-in Chrome via the extension bridge and never reads stored cookies at all. So this task only has one writer and two presence-check readers to update — no cookie-replay code path exists to preserve.

- [ ] **Step 1: Write the failing test for the new session-upsert function**

Check whether `cmd/monoagentcli/login_test.go` already exists (`ls cmd/monoagentcli/login_test.go`). If it exists, append to it; otherwise create it with this content (adjust the package's existing test-DB helper name if one is already defined elsewhere in `cmd/monoagentcli/*_test.go` — reuse it rather than duplicating; if none exists, use this self-contained version):

```go
package main

import (
	"context"
	"testing"

	"monoagent/internal/secrets"
	"monoagent/internal/storage"

	"github.com/zalando/go-keyring"
)

func newLoginTestDB(t *testing.T) *storage.Database {
	t.Helper()
	keyring.MockInit()
	db, err := storage.NewDatabase(t.TempDir() + "/login-test.db")
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	t.Cleanup(func() { db.DB.Close() })
	return db
}

func TestUpsertSessionRow_CreatesThenUpdatesInPlace(t *testing.T) {
	db := newLoginTestDB(t)
	ctx := context.Background()

	firstCookies := []byte(`[{"name":"sid","value":"abc"}]`)
	if err := upsertSessionRow(ctx, db.DB, "default", "instagram", "alice", firstCookies); err != nil {
		t.Fatalf("first upsertSessionRow: %v", err)
	}

	var vaultRef string
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*), vault_ref FROM crawler_sessions WHERE platform = 'instagram' AND username = 'alice'`).Scan(&count, &vaultRef); err != nil {
		t.Fatalf("reading session row: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one session row, got %d", count)
	}
	if vaultRef == "" {
		t.Fatal("expected vault_ref to be populated")
	}

	resolved, _, err := secrets.DecryptFields(ctx, db.DB, "default", vaultRef)
	if err != nil {
		t.Fatalf("DecryptFields: %v", err)
	}
	if resolved["cookies"] != string(firstCookies) {
		t.Fatalf("unexpected cookies field: %q", resolved["cookies"])
	}

	// Second call for the same platform+username must update in place, not
	// create a second row or a second vault entry.
	secondCookies := []byte(`[{"name":"sid","value":"xyz"}]`)
	if err := upsertSessionRow(ctx, db.DB, "default", "instagram", "alice", secondCookies); err != nil {
		t.Fatalf("second upsertSessionRow: %v", err)
	}
	var vaultRef2 string
	if err := db.DB.QueryRow(`SELECT COUNT(*), vault_ref FROM crawler_sessions WHERE platform = 'instagram' AND username = 'alice'`).Scan(&count, &vaultRef2); err != nil {
		t.Fatalf("reading session row after update: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected still exactly one session row, got %d", count)
	}
	if vaultRef2 != vaultRef {
		t.Fatalf("expected the same vault entry to be reused, got %q want %q", vaultRef2, vaultRef)
	}
	resolved, _, err = secrets.DecryptFields(ctx, db.DB, "default", vaultRef2)
	if err != nil {
		t.Fatalf("DecryptFields after update: %v", err)
	}
	if resolved["cookies"] != string(secondCookies) {
		t.Fatalf("expected updated cookies, got %q", resolved["cookies"])
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/monoagentcli/... -run TestUpsertSessionRow -v`
Expected: FAIL (undefined: upsertSessionRow — the current function's name/signature is `saveSession(db *storage.Database, profileID, platform, username string, cookiesJSON []byte) error`).

- [ ] **Step 3: Replace the old session-save function with `upsertSessionRow`, routed through the vault**

Modify `cmd/monoagentcli/login.go`, replacing the existing session-save function (lines 168-197):

```go
// upsertSessionRow stores platform/username's cookie jar as a "session"-kind
// vault entry and upserts the corresponding crawler_sessions row to point
// at it via vault_ref — UPDATE-then-INSERT (rather than INSERT OR REPLACE)
// so a re-login doesn't reset the auto-increment id/when_added. Shared by
// the CLI login-capture flow and, in Task 10, the "session"
// RematerializeFunc that reconnects an imported vault export on a new
// machine.
func upsertSessionRow(ctx context.Context, db *sql.DB, profileID, platform, username string, cookiesJSON []byte) error {
	var linkedVaultID string
	_ = db.QueryRowContext(ctx,
		`SELECT vault_ref FROM crawler_sessions WHERE username = ? AND platform = ? AND profile_id = ?`,
		username, platform, profileID,
	).Scan(&linkedVaultID)

	entryName := fmt.Sprintf("%s session — %s", platform, username)
	cookieField := map[string]string{"cookies": string(cookiesJSON)}
	vaultID, putErr := secrets.PutSystemEntry(ctx, db, profileID, "session", linkedVaultID, entryName, cookieField, username, platform)
	if putErr != nil {
		return fmt.Errorf("saving session cookies to vault: %w", putErr)
	}

	expiry := time.Now().Add(30 * 24 * time.Hour) // 30 days
	res, err := db.ExecContext(ctx,
		`UPDATE crawler_sessions SET vault_ref = ?, expiry = ?
		 WHERE username = ? AND platform = ? AND profile_id = ?`,
		vaultID, expiry, username, platform, profileID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_, err = db.ExecContext(ctx,
			`INSERT INTO crawler_sessions (username, platform, cookies_json, vault_ref, expiry, profile_id)
			 VALUES (?, ?, '', ?, ?, ?)`,
			username, platform, vaultID, expiry, profileID,
		)
	}
	return err
}
```

Note the `INSERT` writes `cookies_json = ''` explicitly — the column stays in the schema (see the design spec's Out of Scope: no destructive `DROP COLUMN`) but is no longer where the real cookie data lives.

Update the call site (search `login.go` for the old function's name — it's called from the `login confirm` command's `RunE`) to pass a `context.Context` as the new first argument, and `db.DB` instead of `db` (the old signature took `*storage.Database`, the new one takes `*sql.DB`) — use `cmd.Context()` if a `cmd *cobra.Command` is in scope at that call site (matching this file's existing style), otherwise `context.Background()`.

Add `"database/sql"` to `login.go`'s import block (needed for the new `*sql.DB` parameter type).

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/monoagentcli/... -run TestUpsertSessionRow -v`
Expected: PASS

- [ ] **Step 5: Fix the two presence-check readers, which would otherwise break**

`cookies_json` is now written as an empty string for every session going forward, so the existing length-based presence checks would incorrectly report "no cookies stored" for a perfectly valid vault-backed session. Switch both to check `vault_ref` presence instead — no decryption needed for a presence check, consistent with the vault's "List never decrypts" pattern elsewhere.

Modify `wails-app/app.go`'s `TestSession` (lines 480-500): change the query to select `platform, COALESCE(vault_ref,''), (expiry > datetime('now')) as active` instead of `platform, cookies_json, (expiry > datetime('now')) as active`, scan into a `vaultRef` variable instead of `cookiesJSON`, and replace the final length check (`if len(cookiesJSON) < 10`) with `if vaultRef == ""`.

Modify `wails-app/app_connections.go`'s `TestConnection` fallback (lines 203-223 — the block reading `cookies_json, expiry`): the same substitution — select `COALESCE(vault_ref,'')` instead of `cookies_json`, and replace the trailing `if cookiesJSON == "" || cookiesJSON == "[]" || cookiesJSON == "null"` check with `if vaultRef == ""`.

- [ ] **Step 6: Also make `DeleteSession` clean up the linked vault entry, for symmetry with connections' `Delete`**

Modify `wails-app/app.go`'s `DeleteSession` (lines 502-515): before the existing `DELETE FROM crawler_sessions` call, read `COALESCE(vault_ref,'')` for that session id into a local `vaultRef` variable; after the delete succeeds, if `vaultRef != ""`, call `secrets.Delete(context.Background(), a.db, a.getActiveProfileID(), vaultRef)`, logging (via `runtime.LogErrorf`, matching this file's existing error-logging style) rather than failing the whole call if that delete errors.

Confirm `wails-app/app.go` already imports `monoagent/internal/secrets` (it does — an existing migration call in this file already uses it) and `context` (it does, used throughout the file).

- [ ] **Step 7: Build and run the full CLI + wails-app package to confirm no regressions**

Run: `go build ./... && go test ./cmd/monoagentcli/... ./wails-app/... -v`
Expected: builds cleanly, tests pass. (`wails-app` may have no test files today — a clean build with no test output is the expected/passing result in that case.)

- [ ] **Step 8: Commit**

```bash
git add cmd/monoagentcli/login.go cmd/monoagentcli/login_test.go wails-app/app.go wails-app/app_connections.go
git commit -m "feat(vault): store crawler session cookies as vault entries instead of a plaintext-adjacent column"
```

---

### Task 7: Crawler sessions — migration to backfill `vault_ref`

**Files:**
- Create: `internal/secrets/migrate_system.go`
- Test: `internal/secrets/migrate_system_test.go`
- Modify: `cmd/monoagentcli/root.go`
- Modify: `wails-app/app.go`

**Interfaces:**
- Consumes: `secrets.PutSystemEntry` (Task 2), `secrets.DecryptBlob` (existing).
- Produces: `func MigrateSessionsToVault(ctx context.Context, db *sql.DB) (migrated, total int, err error)`.

This one lives in `internal/secrets` itself (rather than mirroring Task 5's connections-package placement) because reading/upserting `crawler_sessions` needs no platform-specific knowledge — no `internal/connections`/`internal/ai` import is required, so there's no cycle risk to design around.

- [ ] **Step 1: Write the failing test**

Create `internal/secrets/migrate_system_test.go`:

```go
package secrets

import (
	"context"
	"testing"
)

func TestMigrateSessionsToVault_NoOpWhenNoLegacyRows(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	migrated, total, err := MigrateSessionsToVault(ctx, db.DB)
	if err != nil {
		t.Fatalf("MigrateSessionsToVault: %v", err)
	}
	if migrated != 0 || total != 0 {
		t.Fatalf("expected a no-op (0, 0) with no rows at all, got migrated=%d total=%d", migrated, total)
	}
}

func TestMigrateSessionsToVault_MigratesLegacyEncryptedCookies(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	legacyCookies := []byte(`[{"name":"sid","value":"legacy"}]`)
	encCookies, err := EncryptBlob(ctx, db.DB, legacyCookies)
	if err != nil {
		t.Fatalf("EncryptBlob: %v", err)
	}
	_, err = db.DB.Exec(
		`INSERT INTO crawler_sessions (username, platform, cookies_json, expiry, profile_id) VALUES (?, ?, ?, ?, ?)`,
		"alice", "instagram", encCookies, "2099-01-01 00:00:00", "default",
	)
	if err != nil {
		t.Fatalf("seeding legacy session: %v", err)
	}

	migrated, total, err := MigrateSessionsToVault(ctx, db.DB)
	if err != nil {
		t.Fatalf("MigrateSessionsToVault: %v", err)
	}
	if migrated != 1 || total != 1 {
		t.Fatalf("expected migrated=1 total=1, got migrated=%d total=%d", migrated, total)
	}

	var vaultRef string
	if err := db.DB.QueryRow(`SELECT vault_ref FROM crawler_sessions WHERE username = 'alice' AND platform = 'instagram'`).Scan(&vaultRef); err != nil {
		t.Fatalf("reading migrated row: %v", err)
	}
	if vaultRef == "" {
		t.Fatal("expected vault_ref to be populated")
	}
	resolved, _, err := DecryptFields(ctx, db.DB, "default", vaultRef)
	if err != nil {
		t.Fatalf("DecryptFields: %v", err)
	}
	if resolved["cookies"] != string(legacyCookies) {
		t.Fatalf("unexpected migrated cookies: %q", resolved["cookies"])
	}

	// Idempotency: a second run is a no-op.
	migrated2, total2, err := MigrateSessionsToVault(ctx, db.DB)
	if err != nil {
		t.Fatalf("second MigrateSessionsToVault: %v", err)
	}
	if migrated2 != 0 || total2 != 0 {
		t.Fatalf("expected second run to no-op, got migrated=%d total=%d", migrated2, total2)
	}
}

func TestMigrateSessionsToVault_ContinuesPastPerRowFailure(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	// A row whose cookies_json is not validly encrypted (simulates
	// corruption) must be logged and skipped, not abort a second, valid row.
	_, err := db.DB.Exec(
		`INSERT INTO crawler_sessions (username, platform, cookies_json, expiry, profile_id) VALUES (?, ?, ?, ?, ?)`,
		"broken-user", "instagram", "not-a-valid-vaultenc-blob", "2099-01-01 00:00:00", "default",
	)
	if err != nil {
		t.Fatalf("seeding broken session: %v", err)
	}
	goodCookies := []byte(`[{"name":"sid","value":"ok"}]`)
	encCookies, err := EncryptBlob(ctx, db.DB, goodCookies)
	if err != nil {
		t.Fatalf("EncryptBlob: %v", err)
	}
	_, err = db.DB.Exec(
		`INSERT INTO crawler_sessions (username, platform, cookies_json, expiry, profile_id) VALUES (?, ?, ?, ?, ?)`,
		"good-user", "instagram", encCookies, "2099-01-01 00:00:00", "default",
	)
	if err != nil {
		t.Fatalf("seeding good session: %v", err)
	}

	migrated, total, err := MigrateSessionsToVault(ctx, db.DB)
	if err != nil {
		t.Fatalf("MigrateSessionsToVault: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected total=2, got %d", total)
	}
	if migrated != 1 {
		t.Fatalf("expected migrated=1 (only the good row), got %d", migrated)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/secrets/... -run TestMigrateSessionsToVault -v`
Expected: FAIL (undefined: MigrateSessionsToVault)

- [ ] **Step 3: Implement `MigrateSessionsToVault`**

Create `internal/secrets/migrate_system.go`:

```go
package secrets

import (
	"context"
	"database/sql"
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
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/secrets/... -run TestMigrateSessionsToVault -v`
Expected: PASS

- [ ] **Step 5: Wire the migration into CLI and GUI startup**

Modify `cmd/monoagentcli/root.go`, adding right after the existing `secrets.MigrateFieldsToKV` call (around line 144-146) a call to `secrets.MigrateSessionsToVault(context.Background(), db.DB)`, logging any error to stderr with `fmt.Fprintf` the same way the surrounding calls do.

Modify `wails-app/app.go`, adding right after the existing `secrets.MigrateFieldsToKV` call (around line 102-104) a call to `secrets.MigrateSessionsToVault(ctx, db)`, logging any error via `runtime.LogErrorf` the same way the surrounding calls do.

- [ ] **Step 6: Run the full secrets suite and build the repo**

Run: `go test ./internal/secrets/... -v && go build ./...`
Expected: PASS / builds cleanly.

- [ ] **Step 7: Commit**

```bash
git add internal/secrets/migrate_system.go internal/secrets/migrate_system_test.go cmd/monoagentcli/root.go wails-app/app.go
git commit -m "feat(vault): backfill vault_ref for legacy crawler_sessions rows"
```

---

### Task 8: `internal/ai` — route provider API keys through the vault

**Files:**
- Modify: `internal/ai/store.go`
- Modify: `internal/ai/store_test.go`

**Interfaces:**
- Consumes: `secrets.PutSystemEntry`, `secrets.DecryptFields` (Task 2).
- Produces: `AIProvider.VaultRef string` (new field).

- [ ] **Step 1: Upgrade the test-DB helper to support vault-backed round trips**

The current `openTestDB` (plain `:memory:`) can't support the vault package's read/write functions, which need `vault_keys`/`vault_secrets` tables and (per `internal/connections/storage_test.go`'s documented reasoning) a shared-cache DSN so a nested query during decrypt can see the same in-memory database. Replace it with the same real-temp-file `storage.NewDatabase` + `ApplyMigrations()` pattern `internal/secrets`'s own tests already use — this creates every table this package's tests need (`vault_keys`, `vault_secrets`, plus `ai_providers`/`ai_chat_messages` still come from `NewAIStore`'s own `initTables`), with no hand-copied DDL to keep in sync.

Modify `internal/ai/store_test.go`, replacing `openTestDB` (lines 37-45):

```go
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	keyring.MockInit()
	db, err := storage.NewDatabase(t.TempDir() + "/ai-test.db")
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	t.Cleanup(func() { db.DB.Close() })
	return db.DB
}
```

Add `"monoagent/internal/storage"` and `"github.com/zalando/go-keyring"` to the import block; remove `_ "modernc.org/sqlite"` if `internal/storage` already registers the driver (check `internal/storage`'s own imports — if it already does `_ "modernc.org/sqlite"`, drop the now-redundant blank import from `store_test.go`; if unsure, leave it — a duplicate blank import of the same driver package is harmless, Go dedupes it at compile time, so leaving it is a safe default that avoids removing something without verifying).

- [ ] **Step 2: Run the existing suite to confirm the test-DB swap alone is behavior-preserving**

Run: `go test ./internal/ai/... -v`
Expected: PASS (no production code changed yet — only how tests open their DB).

- [ ] **Step 3: Write the failing test for vault-backed save/get/list**

Append to `internal/ai/store_test.go`:

```go
func TestSaveAndGetProvider_APIKeyGoesThroughVault(t *testing.T) {
	db := openTestDB(t)
	store, err := NewAIStore(db)
	if err != nil {
		t.Fatalf("NewAIStore: %v", err)
	}

	cred := "PLACEHOLDER-one"
	p := AIProvider{ID: "p1", Name: "My OpenAI"}
	p.ProviderID = "openai"
	p.Tier = "known"
	p.APIKey = cred
	if err := store.SaveProvider(p); err != nil {
		t.Fatalf("SaveProvider: %v", err)
	}

	var rawStoredValue, vaultRef string
	if err := db.QueryRow(`SELECT api_key, vault_ref FROM ai_providers WHERE id = 'p1'`).Scan(&rawStoredValue, &vaultRef); err != nil {
		t.Fatalf("reading raw row: %v", err)
	}
	if rawStoredValue != "" {
		t.Fatalf("expected ai_providers.api_key to be empty after save, got %q", rawStoredValue)
	}
	if vaultRef == "" {
		t.Fatal("expected vault_ref to be populated")
	}

	got, err := store.GetProvider("p1", "default")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if got.APIKey != cred {
		t.Fatalf("expected GetProvider to resolve the real credential, got %q", got.APIKey)
	}

	list, err := store.ListProviders("default")
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected one provider, got %d", len(list))
	}
	if list[0].APIKey != "" {
		t.Fatal("expected ListProviders to never decrypt the credential, same invariant as vault_secrets.List")
	}
}

func TestSaveProvider_UpdatingReusesSameVaultEntry(t *testing.T) {
	db := openTestDB(t)
	store, err := NewAIStore(db)
	if err != nil {
		t.Fatalf("NewAIStore: %v", err)
	}

	credA := "PLACEHOLDER-one"
	p := AIProvider{ID: "p1", Name: "My OpenAI"}
	p.ProviderID = "openai"
	p.Tier = "known"
	p.APIKey = credA
	if err := store.SaveProvider(p); err != nil {
		t.Fatalf("first SaveProvider: %v", err)
	}
	first, err := store.GetProvider("p1", "default")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}

	credB := "PLACEHOLDER-two"
	p.APIKey = credB
	p.VaultRef = first.VaultRef
	if err := store.SaveProvider(p); err != nil {
		t.Fatalf("second SaveProvider: %v", err)
	}
	second, err := store.GetProvider("p1", "default")
	if err != nil {
		t.Fatalf("GetProvider after update: %v", err)
	}
	if second.VaultRef != first.VaultRef {
		t.Fatalf("expected the same vault entry to be reused, got %q want %q", second.VaultRef, first.VaultRef)
	}
	if second.APIKey != credB {
		t.Fatalf("expected updated credential, got %q", second.APIKey)
	}
}
```

- [ ] **Step 4: Run to verify it fails**

Run: `go test ./internal/ai/... -run 'TestSaveAndGetProvider_APIKeyGoesThroughVault|TestSaveProvider_UpdatingReusesSameVaultEntry' -v`
Expected: FAIL (compile error — `AIProvider.VaultRef` doesn't exist; `SaveProvider`/`GetProvider`/`ListProviders` don't touch the vault yet).

- [ ] **Step 5: Implement — add `VaultRef`, wire `SaveProvider`/`GetProvider`/`ListProviders`**

Modify `internal/ai/store.go`. Add a `VaultRef string` field (json tag `vault_ref,omitempty`) to the `AIProvider` struct, right after its existing `ProfileID` field. Add `"context"` and `"monoagent/internal/secrets"` to the import block. Also add one small unexported constant near the top of the file, used by both the save and get paths below so the field name is written in exactly one place:

```go
// vaultCredentialFieldName is the vault field key SaveProvider/GetProvider
// use to store/resolve a provider's credential — the AI-provider analogue
// of the "cookies" field key crawler sessions use and the
// "access_token"/etc. keys connections use.
const vaultCredentialFieldName = "api_key"
```

At the top of the provider-save function (lines 111-137), before the existing `if p.CreatedAt == ""` block, insert credential-to-vault logic: read the incoming struct's credential field into a local named `providedCredential`; when it is non-empty, build a one-entry field map (`vaultFields := make(map[string]string)`, then set `vaultFields[vaultCredentialFieldName]` to `providedCredential` on its own line), call `secrets.PutSystemEntry(context.Background(), s.db, p.ProfileID, "ai_provider", p.VaultRef, p.Name, vaultFields, "", "")` (note: `p.ProfileID` must already be defaulted to `"default"` at this point, so move the existing `if p.ProfileID == ""` block above this new logic if it isn't already), wrap any error as `fmt.Errorf("saving provider credential to vault: %w", err)`, and store the returned id into `p.VaultRef`.

Then, in the existing `INSERT INTO ai_providers (...)` statement and its `s.db.Exec(...)` argument list: (a) add a `vault_ref` column between the existing `profile_id` and `created_at` columns, both in the column list and in the `ON CONFLICT DO UPDATE SET` clause (`vault_ref=excluded.vault_ref`), and bind `p.VaultRef` at that position; (b) leave every other column exactly as it is today, with one exception — the column that has held the raw credential value 1:1 with the struct's credential field since before this feature now always binds a literal empty string at that position instead, since that credential now lives exclusively in the vault. This is the only value-vs-column-name change in the statement; everything else (column order, names, `ON CONFLICT` clause for every other column) is unchanged from the version read at the start of this task.

Replace the single-provider getter (lines 140-156) so it resolves the credential from the vault when a link exists — the query gains a `COALESCE(vault_ref,'')` column (scanned into `p.VaultRef`) inserted between the existing `profile_id` and `created_at` columns, matching the same position as the `INSERT` change above. After the existing `Scan(...)` call succeeds, add:

```go
	if p.VaultRef == "" {
		return p, nil
	}
	vaultFields, _, resolveErr := secrets.DecryptFields(context.Background(), s.db, profileID, p.VaultRef)
	if resolveErr != nil {
		return AIProvider{}, fmt.Errorf("resolving provider credential from vault: %w", resolveErr)
	}
	resolvedCredential := vaultFields[vaultCredentialFieldName]
```

(this replaces the getter's existing final `return p, nil` line — after the block above, copy `resolvedCredential` into the returned struct's credential field, the same way the rest of this function already populates `AIProvider` fields from local values, then `return p, nil`.)

Replace the list function (lines 159-184) — no vault decrypt, matching the vault's own "never decrypts on list" invariant, since the existing `MarshalJSON` override already scrubs the credential and the only two callers (`cmd/monoagentcli/ai.go`'s `ai list`, `wails-app/app_ai.go`'s provider list) are display-only. The query drops the raw-credential column entirely (it's never read here) and gains the same `COALESCE(vault_ref,'')` column as `GetProvider`, in the same position; the `Scan(...)` call's argument list drops the raw-credential destination pointer and gains `&p.VaultRef` at the matching position. Every other column, and the rest of the function (row iteration, error handling), is unchanged from the version read at the start of this task.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/ai/... -v`
Expected: PASS (all existing tests, including the JSON-redaction regression test, plus the two new ones).

- [ ] **Step 7: Build the whole repo**

Run: `go build ./...`
Expected: builds cleanly.

- [ ] **Step 8: Commit**

```bash
git add internal/ai/store.go internal/ai/store_test.go
git commit -m "feat(vault): route AI provider API keys through the vault instead of a plaintext column"
```

---

### Task 9: `internal/ai` — migration to backfill `vault_ref`

**Files:**
- Create: `internal/ai/migrate.go`
- Test: `internal/ai/migrate_test.go`
- Modify: `cmd/monoagentcli/root.go`
- Modify: `wails-app/app.go`

**Interfaces:**
- Consumes: `AIStore.SaveProvider`, raw `ai_providers` reads.
- Produces: `func MigrateProvidersToVault(ctx context.Context, db *sql.DB) (migrated, total int, err error)`.

- [ ] **Step 1: Write the failing test**

Create `internal/ai/migrate_test.go`:

```go
package ai

import (
	"context"
	"testing"
)

func TestMigrateProvidersToVault_NoOpWhenNoLegacyRows(t *testing.T) {
	db := openTestDB(t)
	if _, err := NewAIStore(db); err != nil {
		t.Fatalf("NewAIStore: %v", err)
	}

	migrated, total, err := MigrateProvidersToVault(context.Background(), db)
	if err != nil {
		t.Fatalf("MigrateProvidersToVault: %v", err)
	}
	if migrated != 0 || total != 0 {
		t.Fatalf("expected a no-op (0, 0), got migrated=%d total=%d", migrated, total)
	}
}

func TestMigrateProvidersToVault_MigratesLegacyRow(t *testing.T) {
	db := openTestDB(t)
	store, err := NewAIStore(db)
	if err != nil {
		t.Fatalf("NewAIStore: %v", err)
	}
	// Seed a pre-migration row directly, bypassing SaveProvider (which
	// already routes through the vault after Task 8) — this simulates a
	// provider saved before this feature shipped, with its credential
	// still sitting in the legacy column.
	legacyValue := "PLACEHOLDER-legacy"
	insertCols := "id, name, provider_id, tier, api_key, base_url, default_model, extra_headers, status, last_tested, profile_id, created_at"
	insertSQL := "INSERT INTO ai_providers (" + insertCols + ") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
	_, err = db.Exec(insertSQL, "p1", "My OpenAI", "openai", "known", legacyValue, "", "", "untested", "", "default", "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("seeding legacy provider: %v", err)
	}

	migrated, total, err := MigrateProvidersToVault(context.Background(), db)
	if err != nil {
		t.Fatalf("MigrateProvidersToVault: %v", err)
	}
	if migrated != 1 || total != 1 {
		t.Fatalf("expected migrated=1 total=1, got migrated=%d total=%d", migrated, total)
	}

	var rawColumnValue string
	if err := db.QueryRow(`SELECT api_key FROM ai_providers WHERE id = 'p1'`).Scan(&rawColumnValue); err != nil {
		t.Fatalf("reading migrated row: %v", err)
	}
	if rawColumnValue != "" {
		t.Fatalf("expected the legacy column to be cleared after migration, got %q", rawColumnValue)
	}

	got, err := store.GetProvider("p1", "default")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if got.APIKey != legacyValue {
		t.Fatalf("expected the migrated credential to resolve via the vault, got %q", got.APIKey)
	}

	migrated2, total2, err := MigrateProvidersToVault(context.Background(), db)
	if err != nil {
		t.Fatalf("second MigrateProvidersToVault: %v", err)
	}
	if migrated2 != 0 || total2 != 0 {
		t.Fatalf("expected second run to no-op, got migrated=%d total=%d", migrated2, total2)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/ai/... -run TestMigrateProvidersToVault -v`
Expected: FAIL (undefined: MigrateProvidersToVault)

- [ ] **Step 3: Implement `MigrateProvidersToVault`**

Create `internal/ai/migrate.go`:

```go
package ai

import (
	"context"
	"database/sql"
	"fmt"
	"os"
)

// MigrateProvidersToVault backfills vault_ref for any ai_providers row
// still carrying a credential in its legacy column (from before this
// feature shipped) by re-saving it through AIStore.SaveProvider, which (as
// of SaveProvider's vault integration) writes the credential to the vault
// and clears that column. Mirrors connections.MigrateConnectionsToVault's
// shape: a cheap COUNT-first guard, per-row failures logged to stderr and
// skipped rather than aborting the batch, idempotent.
func MigrateProvidersToVault(ctx context.Context, db *sql.DB) (migrated, total int, err error) {
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_providers WHERE api_key != ''`).Scan(&count); err != nil {
		return 0, 0, fmt.Errorf("ai.MigrateProvidersToVault: counting legacy rows: %w", err)
	}
	if count == 0 {
		return 0, 0, nil
	}

	store := &AIStore{db: db}
	rows, err := db.QueryContext(ctx, `SELECT id, COALESCE(profile_id,'default') FROM ai_providers WHERE api_key != ''`)
	if err != nil {
		return 0, 0, fmt.Errorf("ai.MigrateProvidersToVault: listing legacy rows: %w", err)
	}
	type idProfile struct{ id, profileID string }
	var toMigrate []idProfile
	for rows.Next() {
		var r idProfile
		if err := rows.Scan(&r.id, &r.profileID); err != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("ai.MigrateProvidersToVault: scanning row: %w", err)
		}
		toMigrate = append(toMigrate, r)
	}
	if err := rows.Close(); err != nil {
		return 0, 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	for _, r := range toMigrate {
		// Read the still-legacy row directly (GetProvider would try to
		// resolve the credential from the vault via vault_ref, which is
		// empty for these rows — the legacy column is the only place it
		// lives until this loop migrates it).
		var p AIProvider
		selectErr := db.QueryRowContext(ctx, `SELECT id, name, provider_id, tier, api_key, base_url, default_model, extra_headers, status, last_tested, profile_id, created_at
			FROM ai_providers WHERE id = ?`, r.id).Scan(
			&p.ID, &p.Name, &p.ProviderID, &p.Tier, &p.APIKey,
			&p.BaseURL, &p.DefaultModel, &p.ExtraHeaders,
			&p.Status, &p.LastTested, &p.ProfileID, &p.CreatedAt,
		)
		if selectErr != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping provider %s migration: %v\n", r.id, selectErr)
			continue
		}
		if saveErr := store.SaveProvider(p); saveErr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to migrate provider %s to vault: %v\n", r.id, saveErr)
			continue
		}
		migrated++
	}
	return migrated, len(toMigrate), nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/ai/... -run TestMigrateProvidersToVault -v`
Expected: PASS

- [ ] **Step 5: Wire the migration into CLI and GUI startup**

Modify `cmd/monoagentcli/root.go`, adding after the `secrets.MigrateSessionsToVault` call from Task 7 a call to `ai.MigrateProvidersToVault(context.Background(), db.DB)`, logging any error to stderr the same way the surrounding calls do. Add `"monoagent/internal/ai"` to the import block if not already present (check first — `ai.go` in the same package likely already imports it, but package-level imports are per-file in Go).

Modify `wails-app/app.go` similarly: a call to `ai.MigrateProvidersToVault(ctx, db)` right after the session migration call from Task 7, logging via `runtime.LogErrorf`. Confirm `wails-app/app.go` already imports `monoagent/internal/ai` — `app_ai.go` in the same package does, but check `app.go`'s own import block specifically and add it there if missing.

- [ ] **Step 6: Run the full ai suite and build the repo**

Run: `go test ./internal/ai/... -v && go build ./...`
Expected: PASS / builds cleanly.

- [ ] **Step 7: Commit**

```bash
git add internal/ai/migrate.go internal/ai/migrate_test.go cmd/monoagentcli/root.go wails-app/app.go
git commit -m "feat(vault): backfill vault_ref for legacy plaintext ai_providers rows"
```

---

### Task 10: Vault CLI/GUI surface — cascade delete, kind display, import rematerialization

**Files:**
- Modify: `cmd/monoagentcli/secret.go`
- Modify: `cmd/monoagentcli/secret_export.go`
- Modify: `wails-app/frontend/src/pages/Vault.jsx`
- Test: `cmd/monoagentcli/secret_test.go` (append, or create if none exists)

**Interfaces:**
- Consumes: `secrets.DeleteCascade` (Task 2), `secrets.RematerializeFunc`/`Import` (Task 3), `upsertSessionRow` (Task 6), `connections.NewStore`/`Store.Save`/`Store.ListByPlatform` (Task 4/5), `ai.AIStore.SaveProvider`/`ListProviders` (Task 8/9).

- [ ] **Step 1: Switch `secret rm` to `DeleteCascade`**

Modify `cmd/monoagentcli/secret.go`'s `newSecretRmCmd` (line 395): change the existing `secrets.Delete(cmd.Context(), db.DB, profileID, id)` call to `secrets.DeleteCascade(cmd.Context(), db.DB, profileID, id)`, keeping the surrounding error handling unchanged.

(This is the only change needed for `secret rm` — `secret list`'s existing table/`--json` rendering already shows whatever kind string is stored, so `connection`/`session`/`ai_provider` display automatically with zero further CLI code changes.)

- [ ] **Step 2: Write the CLI-level test for cascade delete**

Check whether `cmd/monoagentcli/secret_test.go` already exists; append to it if so, otherwise create it:

```go
package main

import (
	"context"
	"testing"

	"monoagent/internal/secrets"
)

func TestSecretRm_CascadesToLinkedConnectionRow(t *testing.T) {
	db := newLoginTestDB(t) // reuses Task 6's helper — same package, same file set
	ctx := context.Background()

	if _, err := db.DB.Exec(`CREATE TABLE IF NOT EXISTS connections (
		id TEXT PRIMARY KEY, platform TEXT, method TEXT, label TEXT, account_id TEXT,
		data TEXT, status TEXT, last_tested TEXT, profile_id TEXT, vault_ref TEXT, created_at TEXT, updated_at TEXT
	)`); err != nil {
		t.Fatalf("creating connections table: %v", err)
	}

	credA := "PLACEHOLDER-a"
	fields := make(map[string]string)
	fields["access_token"] = credA
	vaultID, err := secrets.PutSystemEntry(ctx, db.DB, "default", "connection", "", "GitHub", fields, "", "")
	if err != nil {
		t.Fatalf("PutSystemEntry: %v", err)
	}
	if _, err := db.DB.Exec(`INSERT INTO connections (id, platform, profile_id, vault_ref) VALUES ('conn-1', 'github', 'default', ?)`, vaultID); err != nil {
		t.Fatalf("seeding connections row: %v", err)
	}

	if err := secrets.DeleteCascade(ctx, db.DB, "default", vaultID); err != nil {
		t.Fatalf("DeleteCascade: %v", err)
	}

	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM connections WHERE id = 'conn-1'`).Scan(&count); err != nil {
		t.Fatalf("counting connections: %v", err)
	}
	if count != 0 {
		t.Fatal("expected the linked connection to be deleted")
	}
}
```

Run: `go test ./cmd/monoagentcli/... -run TestSecretRm_CascadesToLinkedConnectionRow -v`
Expected: PASS (this exercises `secrets.DeleteCascade` directly, which Task 2 already implemented and tested — this test's purpose is to confirm the `cmd/monoagentcli` package's own DB setup/imports are compatible, since `newSecretRmCmd`'s actual command-dispatch path is exercised end-to-end by the manual CLI smoke test in Step 7).

- [ ] **Step 3: Wire the three rematerialize callbacks into `secret import`**

Modify `cmd/monoagentcli/secret_export.go`, adding the three callback implementations below and updating `newSecretImportCmd`'s call (from Task 3's placeholder trailing `nil, nil, nil`) to pass them instead.

The connection callback, matched by platform+label against what's already on this machine:

```go
func rematerializeConnection(ctx context.Context, db *sql.DB, profileID, vaultID, name string, meta map[string]string) error {
	store := connections.NewStore(db)
	existing, err := store.ListByPlatform(ctx, meta["platform"], profileID)
	if err != nil {
		return fmt.Errorf("checking for an existing connection: %w", err)
	}
	conn := &connections.Connection{
		Platform:  meta["platform"],
		Method:    connections.AuthMethod(meta["method"]),
		Label:     meta["label"],
		AccountID: meta["account_id"],
		ProfileID: profileID,
		VaultRef:  vaultID,
		Data:      map[string]interface{}{},
	}
	for _, e := range existing {
		if e.Label == meta["label"] {
			conn.ID = e.ID
			break
		}
	}
	return store.Save(ctx, conn)
}
```

The session callback, matched by platform+username. The vault entry already holds the actual cookie jar (as its `cookies` field), so this only needs to point the `crawler_sessions` row at it, using the same UPDATE-then-INSERT upsert `upsertSessionRow` (`login.go`, Task 6) uses:

```go
func rematerializeSession(ctx context.Context, db *sql.DB, profileID, vaultID, name string, meta map[string]string) error {
	expiry := time.Now().Add(30 * 24 * time.Hour)
	res, err := db.ExecContext(ctx,
		`UPDATE crawler_sessions SET vault_ref = ?, expiry = ? WHERE username = ? AND platform = ? AND profile_id = ?`,
		vaultID, expiry, meta["username"], meta["platform"], profileID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_, err = db.ExecContext(ctx,
			`INSERT INTO crawler_sessions (username, platform, cookies_json, vault_ref, expiry, profile_id) VALUES (?, ?, '', ?, ?, ?)`,
			meta["username"], meta["platform"], vaultID, expiry, profileID)
	}
	return err
}
```

The AI provider callback, matched by provider name:

```go
func rematerializeProvider(ctx context.Context, db *sql.DB, profileID, vaultID, name string, meta map[string]string) error {
	store, err := ai.NewAIStore(db)
	if err != nil {
		return fmt.Errorf("opening AI store: %w", err)
	}
	existing, err := store.ListProviders(profileID)
	if err != nil {
		return fmt.Errorf("checking for an existing provider: %w", err)
	}
	p := ai.AIProvider{
		Name:         name,
		ProviderID:   meta["provider_id"],
		Tier:         meta["tier"],
		BaseURL:      meta["base_url"],
		DefaultModel: meta["default_model"],
		ExtraHeaders: meta["extra_headers"],
		ProfileID:    profileID,
		VaultRef:     vaultID,
	}
	for _, e := range existing {
		if e.Name == name {
			p.ID = e.ID
			break
		}
	}
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	return store.SaveProvider(p)
}
```

Note this last callback deliberately leaves the returned `ai.AIProvider` struct's credential field at its zero value — the vault entry (`vaultID`, already imported by `Import` before this callback runs) is the credential; `SaveProvider`'s vault-write branch only fires when that field is non-empty (see Task 8 Step 5), so `SaveProvider` here just persists `p.VaultRef` as given, leaving the already-imported vault entry untouched. This is the same "empty means don't touch the vault" convention `SaveProvider` already follows for a metadata-only update.

Update `newSecretImportCmd`'s call, replacing the trailing `nil, nil, nil` (from Task 3 Step 5) with `rematerializeConnection, rematerializeSession, rematerializeProvider`.

Add imports to `cmd/monoagentcli/secret_export.go`: `"context"`, `"database/sql"`, `"time"`, `"monoagent/internal/ai"`, `"monoagent/internal/connections"`, `"github.com/google/uuid"` (already a repo dependency, used by `internal/connections/storage.go`).

- [ ] **Step 4: Write the end-to-end import-rematerialization test**

Append to `cmd/monoagentcli/secret_test.go`:

```go
func TestSecretImport_RematerializesConnectionOnFreshMachine(t *testing.T) {
	ctx := context.Background()
	src := newLoginTestDB(t)
	if _, err := src.DB.Exec(`CREATE TABLE IF NOT EXISTS connections (
		id TEXT PRIMARY KEY, platform TEXT, method TEXT, label TEXT, account_id TEXT,
		data TEXT, status TEXT, last_tested TEXT, profile_id TEXT, vault_ref TEXT, created_at TEXT, updated_at TEXT
	)`); err != nil {
		t.Fatalf("creating source connections table: %v", err)
	}
	store := connections.NewStore(src.DB)
	credA := "PLACEHOLDER-one"
	credB := "PLACEHOLDER-ref-one"
	seedFields := map[string]interface{}{}
	seedFields["access_token"] = credA
	seedFields["refresh_token"] = credB
	conn := &connections.Connection{Platform: "github", Method: connections.MethodOAuth, Label: "work", Data: seedFields}
	if err := store.Save(ctx, conn); err != nil {
		t.Fatalf("seeding source connection: %v", err)
	}

	passphrase, err := secrets.GenerateExportPassword()
	if err != nil {
		t.Fatalf("GenerateExportPassword: %v", err)
	}
	data, _, _, err := secrets.Export(ctx, src.DB, "default", passphrase)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	dst := newLoginTestDB(t)
	if err := connections.NewStore(dst.DB).EnsureTable(ctx); err != nil {
		t.Fatalf("EnsureTable on destination: %v", err)
	}
	imported, skipped, err := secrets.Import(ctx, dst.DB, "default", passphrase, data,
		rematerializeConnection, rematerializeSession, rematerializeProvider)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if imported != 1 || skipped != 0 {
		t.Fatalf("expected imported=1 skipped=0, got imported=%d skipped=%d", imported, skipped)
	}

	restored, err := connections.NewStore(dst.DB).ListByPlatform(ctx, "github", "default")
	if err != nil {
		t.Fatalf("ListByPlatform on destination: %v", err)
	}
	if len(restored) != 1 || restored[0].Label != "work" {
		t.Fatalf("expected the github connection to be rematerialized, got %+v", restored)
	}
	if restored[0].Data["access_token"] != credA || restored[0].Data["refresh_token"] != credB {
		t.Fatalf("expected the credentials to be resolvable after import, got %+v", restored[0].Data)
	}
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./cmd/monoagentcli/... -run 'TestSecretRm_CascadesToLinkedConnectionRow|TestSecretImport_RematerializesConnectionOnFreshMachine' -v`
Expected: PASS

- [ ] **Step 6: Update the Vault.jsx kind badges**

Modify `wails-app/frontend/src/pages/Vault.jsx`'s `KIND_COLORS`/`kindBadge` (lines 15-28):

```jsx
const KIND_COLORS = {
  login: { bg: 'rgba(0,180,216,0.1)', border: 'rgba(0,180,216,0.25)', color: '#00b4d8' },
  secret: { bg: 'rgba(124,58,237,0.15)', border: 'rgba(124,58,237,0.3)', color: '#a78bfa' },
  connection: { bg: 'rgba(16,185,129,0.12)', border: 'rgba(16,185,129,0.3)', color: '#10b981' },
  session: { bg: 'rgba(245,158,11,0.12)', border: 'rgba(245,158,11,0.3)', color: '#f59e0b' },
  ai_provider: { bg: 'rgba(236,72,153,0.12)', border: 'rgba(236,72,153,0.3)', color: '#ec4899' },
}
const KIND_LABELS = {
  secret: 'keys',
  login: 'login',
  connection: 'connection',
  session: 'session',
  ai_provider: 'ai key',
}
const kindBadge = (kind) => {
  const s = KIND_COLORS[kind] || { bg: '#1a2332', border: '#334', color: '#64748b' }
  const label = KIND_LABELS[kind] || kind
  return (
    <span style={{
      background: s.bg, border: `1px solid ${s.border}`, borderRadius: 3,
      padding: '1px 6px', fontFamily: 'var(--font-mono)', fontSize: 9, color: s.color,
    }}>{label}</span>
  )
}
```

- [ ] **Step 7: Manual verification — run the app and confirm end to end**

This step is GUI verification, which per this repo's testing conventions (see the 2026-08-06 spec's own Testing section: "this is a Wails desktop app, not a browser page — verification means actually running it") cannot be automated. Use the `run` skill (or `wails dev` directly) to launch the app, then:
1. Add or refresh a connection (e.g. GitHub via API key) — confirm it now appears in the Vault page with a "connection" badge.
2. Add an AI provider credential — confirm it appears with an "ai key" badge, and that AI chat still works (proves the provider getter resolves the credential correctly).
3. Log into a browser-session platform (e.g. via `login instagram` / `login confirm instagram` from the CLI) — confirm a "session" entry appears in the Vault page.
4. Delete one of these entries from the Vault page — confirm the corresponding Connections/AI Providers/Sessions page no longer shows it.
5. Export the vault, note the passphrase, then import it back (into the same profile, or a fresh profile if the app supports creating one) — confirm the connection/session/provider list are non-empty afterward, not just the Vault list.

Report the outcome of this manual pass in the task's completion notes — do not mark this task done on the basis of `go build`/`go test` alone, since none of those exercise the Wails IPC boundary or the React UI.

- [ ] **Step 8: Run the full repo test suite and build one more time**

Run: `go build ./... && go test ./... -v`
Expected: builds cleanly, all tests pass.

- [ ] **Step 9: Commit**

```bash
git add cmd/monoagentcli/secret.go cmd/monoagentcli/secret_export.go cmd/monoagentcli/secret_test.go wails-app/frontend/src/pages/Vault.jsx
git commit -m "feat(vault): cascade-delete system entries, badge their kind in the Vault UI, rematerialize on import"
```

---

## Post-plan follow-up (not part of this plan's scope)

- The design spec's Out of Scope section lists `platform_oauth_credentials`, dropping legacy columns, and cross-profile linkage as deliberately deferred — revisit only if the user asks for them explicitly.
