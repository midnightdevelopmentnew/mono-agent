# Vault: Key-Value Fields, Full-CLI, Encrypted Import/Export — Design Spec
**Date:** 2026-08-06
**Status:** Approved

---

## Overview

Extends the secrets vault ([2026-07-13-secrets-vault-design.md](./2026-07-13-secrets-vault-design.md)) in four ways:

1. Each vault entry holds an arbitrary **set of key-value fields** instead of one single value.
2. Clicking an entry in the GUI opens an **edit view** for all its fields (rename, add, remove) and metadata.
3. The **CLI gets full functionality** — nothing is GUI-only, including editing.
4. The **GUI becomes a thin wrapper over the CLI** for every vault operation (shells out to `monoagentcli`, mirroring the existing `ExportData`/`ExecuteAction` pattern in `wails-app/app.go`), rather than calling `internal/secrets` in-process.
5. **Encrypted export/import** of the whole vault to a portable file, protected by a freshly-generated random password shown once at export time.

This revises two decisions from the prior spec:
- §"GUI — ... calls the same underlying Go functions, never a separate code path" → now the GUI calls the CLI binary as a subprocess; the CLI is the single implementation surface.
- §"Out of Scope: Cross-machine passphrase-based vault portability" → now in scope, via a distinct password-encrypted export/import bridge format that does **not** change the at-rest OS-keychain model (see Import/Export below). The vault is still unlocked by the OS keychain day-to-day; the export file is the only thing a passphrase ever protects.

---

## Data Model

### Migration: `data/migrations/021_vault_kv_fields.sql`

```sql
ALTER TABLE vault_secrets ADD COLUMN kv INTEGER NOT NULL DEFAULT 0;
ALTER TABLE vault_secrets ADD COLUMN field_count INTEGER NOT NULL DEFAULT 1;
```

- `kv`: 0 = legacy row (ciphertext is a raw string), 1 = migrated (ciphertext is a JSON object). Backfilled by the Go migration below, not by this SQL file, since decrypting requires the DEK.
- `field_count`: plaintext count of keys in the (encrypted) fields map, denormalized so `List` can show "N keys" **without decrypting** — preserves the existing invariant that `List`/`Entry` never touch plaintext (see `secrets.Entry`'s doc comment today: "It never carries the secret value").

### Field storage: reinterpret the existing ciphertext column

No new table. The existing `ciphertext`/`nonce` columns on `vault_secrets` (today: `AES-256-GCM(single value string)`) become `AES-256-GCM(json.Marshal(map[string]string))`. This reuses `Encrypt`/`Decrypt` untouched and makes "all key-values in one shot" trivial — it's already one blob per entry.

*Rejected alternative:* a normalized `vault_secret_fields` child table (one encrypted row per key). More "correct" relationally, but needs a join, N encrypt calls per save, and a larger migration/API surface, for no benefit at the scale of a personal vault (a handful of fields per item). The chosen approach's cost — updating one field re-encrypts the whole field set — is irrelevant at this scale.

### Migration function: `internal/secrets/migrate_kv.go`

Same shape as `internal/connections.EncryptPlaintextConnections`: a cheap `SELECT COUNT(*) FROM vault_secrets WHERE kv = 0` first, no-op if zero. Otherwise, for each unmigrated row: decrypt existing ciphertext → plaintext string `v` → re-encrypt `json.Marshal({"secret": v})` → `UPDATE ... SET ciphertext=?, nonce=?, kv=1, field_count=1`. Applies uniformly to `secret`- and `login`-kind rows alike. Auto-invoked once at CLI (`root.go`) and GUI (`wails-app/app.go`) startup, right alongside the existing `EncryptPlaintextConnections` call. Idempotent; a per-row failure is logged and skipped, not fatal to the batch (matches the connections migration's error handling).

---

## `internal/secrets` Package Changes

```go
// Entry gains a plaintext field count — never the field names or values.
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

// Add's single `value string` becomes a field map.
func Add(ctx context.Context, db *sql.DB, profileID, kind, name string, fields map[string]string, username, url, notes string) (id string, err error)

// DecryptFields is DecryptEntry's multi-field successor — decrypts both
// blobs (fields and notes) in one DEK fetch. Called exclusively by
// `secret reveal --reveal`, which surfaces fields always and notes only
// under --json (see CLI table below), and by the GUI edit flow, which needs
// both to prefill its form.
func DecryptFields(ctx context.Context, db *sql.DB, profileID, id string) (fields map[string]string, notes string, err error)

// Update applies a partial update: a nil pointer leaves that column
// untouched, a non-nil pointer sets it (including to the empty string).
// fields, if non-nil, REPLACES the entire field map; nil leaves the fields
// blob untouched. This lets the CLI implement "only touch what --flags were
// passed" without ever needing to decrypt current notes/fields just to
// preserve them unchanged, and lets the GUI (which always has the full
// current state loaded) simply pass every value non-nil.
func Update(ctx context.Context, db *sql.DB, profileID, id string, name, username, url, notes *string, fields map[string]string) error
```

`List`/`Delete` are unchanged in signature. A field key must be non-empty; `Add`/`Update` reject an empty `fields` map (an entry always has at least one field).

### Workflow reference resolution (`resolve.go`)

`internal/secrets/resolve.go`'s `Resolve` function today calls `DecryptEntry` to get the single plaintext value a workflow config reference expands to. `DecryptEntry` is removed (superseded by `DecryptFields`), so `Resolve` picks a value out of the decrypted fields map instead: the field named literally `secret`, if the map has one; otherwise the map's only entry, if it has exactly one field total; otherwise an error explaining the entry has multiple fields and a single-value workflow reference can't disambiguate between them. This keeps every existing/migrated entry (always single-field, keyed `secret`) resolving exactly as before. A multi-field entry simply cannot be the target of that reference form — no new reference syntax is introduced to select one field of many (see Out of Scope).

---

## CLI — `cmd/monoagentcli/secret.go` (+ new `secret_export.go`)

Full functionality lives here; the GUI has no capability the CLI lacks.

| Command | Behavior |
|---|---|
| `secret add --kind secret\|login --name X [--username U] [--url URL] [--notes N] --field key=value [repeatable]` | `--value V` is shorthand for `--field secret=V` (kept for today's scripts/muscle memory); giving both `--value` and `--field` is an error. Neither given → prompts on stdin for one value, stored as field `secret` — identical UX to today's single-secret case. `key=value` splits on the **first** `=` only, so values containing `=` (e.g. a connection string) round-trip correctly. |
| `secret list` | Metadata + `field_count` only, never plaintext. |
| `secret get <name>` | Unchanged — resolves the `@secret:<name>` reference token. |
| `secret reveal <name> --reveal [--json]` | Decrypts fields (and, under `--json` only, notes too — see below). Text mode: bare value if the entry has exactly one field (preserves today's output for the common case), `key: value` lines if it has several; notes is not printed in text mode, unchanged from today's reveal never showing notes. `--json`: `{"fields": {"key":"value",...}, "notes": "..."}` — a nested object rather than a flat one, so fields and notes (two independently-encrypted blobs) stay visibly distinct; this is what the GUI's edit-open flow parses to prefill its form. |
| `secret update <name> [--name NEW] [--username U] [--url URL] [--notes N] [--field key=value ...]` | Same `key=value` first-`=`-only parsing as `add`. Metadata flags (`--name`/`--username`/`--url`/`--notes`) apply only if explicitly passed (Cobra `Flags().Changed`, mapped to `Update`'s nil-means-untouched pointer params) — omitting one leaves it unchanged, with no need to decrypt/re-supply the current value. `--field` is different: passing **any** `--field` flags replaces the *entire* field set, not a merge. Trade-off: a human tweaking one field by hand needs to `reveal --json` first and re-supply the rest; in exchange the semantics stay unambiguous and match exactly what the GUI does (always holds and re-sends the complete edited state). |
| `secret rm <name>` | Unchanged. |
| `secret export [--output FILE] [--json]` | See Import/Export below. Default filename `vault-export-<YYYYMMDD-HHMMSS>.json.enc` in the cwd if `--output` omitted. The generated password is always printed to stderr with a "save this now, it will not be shown again" warning, and additionally included in the JSON payload under `--json` (so the GUI subprocess call can read it programmatically). |
| `secret import <file> [--json]` | Always prompts for the password on stdin — no `--password` flag, to keep it out of shell history and `ps` (matches `secret add`'s existing value-prompt convention). Reports imported/skipped counts. |

---

## Import / Export

- **Password:** 16 random bytes (`crypto/rand`), Crockford base32-encoded (~26 chars, no ambiguous characters), dash-grouped for readability, e.g. `XPQR-7M2K-DN4J-...`. Freshly generated per export; never persisted anywhere.
- **Key derivation:** Argon2id (already-vendored `golang.org/x/crypto/argon2`, no new dependency) with a random 16-byte salt, fixed parameters (time=1, memory=64 MiB, threads=4), producing a 32-byte key. Parameters are not user-configurable.
- **Cipher:** AES-256-GCM (same primitive as everywhere else in the vault), random 12-byte nonce.
- **File format** (JSON envelope, binary fields base64):
  ```json
  {
    "format": "monoagent-vault-export",
    "version": 1,
    "kdf": "argon2id",
    "salt": "<base64>",
    "nonce": "<base64>",
    "ciphertext": "<base64>"
  }
  ```
- **Encrypted payload** (the plaintext that gets sealed into `ciphertext` above) — metadata is encrypted too, not just values, so nothing is readable without the password:
  ```json
  {
    "exported_at": "2026-08-06T12:00:00Z",
    "profile_id": "default",
    "entries": [
      {
        "kind": "secret", "name": "openai-key",
        "username": "", "url": "", "notes": "",
        "fields": {"secret": "sk-..."},
        "created_at": "...", "updated_at": "..."
      }
    ]
  }
  ```
  `id`/`seq` are deliberately omitted — import always allocates fresh ids in the destination vault.
- **Scope:** active profile only, per prior confirmation.
- **Export flow:** generate password → build payload from the active profile's entries (each decrypted via `DecryptFields`) → encrypt → write file → surface the password once (CLI: stderr; GUI: modal with copy-to-clipboard and an explicit "won't be shown again" warning).
- **Import flow:** read file → prompt/collect password → derive key from the file's own embedded salt → decrypt (wrong password surfaces as a clear "incorrect password or corrupted file" error via AES-GCM's built-in auth check, not a generic crash) → parse entries → for each: if `name` already exists in the destination profile, **skip** (counted, reported); otherwise `Add` it fresh, re-encrypted under the *destination* machine's own DEK — the export password never becomes a long-term key, it only unlocks the bridge file.

---

## GUI Architecture: shells out to the CLI

Every `wails-app/app_vault.go` vault method drops its direct `secrets.X(...)` calls and instead spawns `monoagentcli`, mirroring `ExportData` exactly: `findMonoAgentCLI()` → `exec.CommandContext(a.ctx, cliBin, "--profile", a.getActiveProfileID(), "--json", "secret", "<subcommand>", ...)` → `cmd.Output()` → JSON-unmarshal → on failure, surface `stderr` via the same `exec.ExitError` handling `ExportData` already uses.

| Wails method | Shells to |
|---|---|
| `ListSecrets()` | `secret list --json` |
| `AddSecret(kind, name, username, url, notes, fields)` | `secret add ... --field k=v [...] --json` |
| `RevealSecretFields(name)` returning fields map + notes | `secret reveal <name> --reveal --json`, parsing the `{"fields":..., "notes":...}` shape |
| `UpdateSecret(name, newName, username, url, notes, fields)` | `secret update <name> ... --field k=v [...] --json` |
| `DeleteSecret(name)` | `secret rm <name> --json` |
| `ExportVaultAll()` | native Save dialog for the path, then `secret export --output <path> --json` |
| `ImportVaultAll(path, password)` | `secret import <path> --json`, with `password` written directly to the subprocess's **stdin pipe** (`cmd.Stdin = strings.NewReader(password+"\n")`) — never a CLI flag, so it never touches shell history or `ps` |

**Ripple effect:** the CLI's identity surface is entry **name**, not the internal `sec-NNN` id. `Reveal`/`Update`/`Delete` all switch from operating by `id` to operating by `name` on the GUI side — the frontend already has `entry.name` from the loaded list, so this is contained to `Vault.jsx`'s call sites.

**Consequence:** `internal/secrets` now has exactly one caller in the whole codebase — the CLI. `wails-app` no longer imports it for vault operations at all. Named trade-off: every GUI vault action now pays a subprocess spawn instead of an in-process call — imperceptible for a human clicking through a handful of secrets, but a real behavioral difference from today.

---

## GUI UI/UX — `Vault.jsx` + new components

- **`wails-app/frontend/src/components/KeyValueFields.jsx`** (new, shared): a dynamic list of key/value row inputs — add row, remove row, per-row show/hide toggle for the value. Used by both the add-item form and the edit modal so the row-editor isn't built twice.
- **`wails-app/frontend/src/components/VaultItemModal.jsx`** (new): opened by clicking a row. Editable name, username/url (login kind), notes, and a `KeyValueFields` block seeded from `RevealSecretFields(name)`. Save calls `UpdateSecret`. Kind is fixed at creation — no convert-in-place (not requested).
- **`Vault.jsx` changes:**
  - Kind `<select>` options relabel to **"Keys"** / **"Login"** (submitted value stays `secret`/`login` — label-only, no CLI/data break).
  - **"+ Add Secret"** → **"+ Add New Item"**; its form's single password input is replaced by a `KeyValueFields` block (starts with one empty row).
  - New **"Export All"** / **"Import"** buttons in the header. Export shows the returned password in a copy-to-clipboard modal with a "save this now" warning. Import opens a file picker then a password prompt, then shows an "Imported N, skipped M duplicates" summary.
  - List rows: the "Value" column (today's masked value + inline reveal) becomes a "N keys" badge sourced from `field_count` — no plaintext in the list view. Clicking a row opens `VaultItemModal`; the row's trash icon still deletes directly (not duplicated inside the modal).

---

## Error Handling

- Wrong import password / corrupted export file: AES-GCM authentication failure surfaces as one clear message, not a stack trace or silent garbage data.
- `secret update`/`reveal`/`rm` on an unknown name: same "no secret named %q found" pattern already used by `reveal`/`rm` today.
- Partial import failure on one entry (e.g. a malformed field): logged and skipped, does not abort the rest of the batch — same posture as the existing connections migration.
- GUI subprocess spawn failure (`monoagentcli` not found): surfaced via the existing `findMonoAgentCLI` error message, unchanged.

---

## Testing

- `internal/secrets`: JSON-blob field encrypt/decrypt round-trip; `kv` migration idempotency (second run no-ops) and correctness (both kinds → key `secret`); `Update` full-replace-vs-untouched-metadata semantics; export→import round-trip including the wrong-password-fails case; skip-duplicate import logic.
- CLI-level: `secret reveal` bare-value-if-one-field vs. `key: value`-if-many vs. `--json` always-an-object; `secret list`/`--json` never includes ciphertext, plaintext, or field values anywhere in its output.
- GUI: this is a Wails desktop app, not a browser page — verification means actually running it (`wails dev` or the `run` skill) and exercising add/edit/export/import through the real UI, not an automated browser check.

---

## Out of Scope

- CLI piecemeal `--add-field`/`--remove-field` merge flags — `update --field` is full-replace only.
- Cross-*profile* export/import in one shot (always the active profile; cross-*machine* portability is now in scope via the password-encrypted file, per the revision noted in Overview).
- Changing an entry's `kind` after creation.
- Any plaintext-peekable metadata in the export file — the whole payload is encrypted, including names/usernames/URLs.
- Folding `notes` into the key-value field set — it stays the existing separate encrypted field.
- User-configurable Argon2id parameters.
- A field-qualified workflow reference syntax (e.g. selecting one field of a multi-field entry by name) — bare `@secret:name` references continue to resolve only the `secret`-keyed or sole field, per the Workflow reference resolution note above.
