# Secrets Vault — Design Spec
**Date:** 2026-07-13
**Status:** Approved

---

## Overview

An encrypted password/API-key vault for the Mono Agent app, CLI-first with a GUI wrapper (per this project's AI-CLI-discoverability convention). It serves two audiences at once:

1. **Humans** managing arbitrary secrets (generic API keys, website logins) through the CLI or the `Vault` GUI page, with an explicit reveal step to view plaintext.
2. **Connectors/workflows**, which resolve a secret by reference (`@secret:<name>`) at the point of use and never see or log the raw value in configs, `--json` output, or execution history.

It also becomes the backing store for existing `internal/connections` credential material (OAuth tokens, API keys), which today is stored in plaintext in the `connections.data` JSON column — closing that gap via a one-time migration.

Package name: `internal/secrets` (the name `internal/vault` is already taken by the unrelated image-asset vault used for workflow-generated images).

---

## Cryptography

- **Algorithm:** AES-256-GCM, one random 96-bit nonce per encryption operation (stored alongside ciphertext, never reused).
- **Key hierarchy:**
  - **DEK (Data Encryption Key):** one random 256-bit key per installation, generated on first use. Encrypts all `vault_secrets` rows.
  - **KEK (Key Encryption Key):** lives only in the OS keychain (see below); wraps the DEK using AES-KW. Only the wrapped DEK is persisted to disk (in the SQLite DB); the KEK itself never touches disk.
- **No passphrase / unlock step.** The OS account gate (keychain access) is the vault's unlock gate — consistent with the "OS Keychain" choice: no PIN or master-password prompt for CLI or GUI use.

### Key storage: `internal/secrets/keyring.go`

Uses `github.com/zalando/go-keyring` — pure syscall/exec based (no cgo), matching this repo's `CGO_ENABLED=0` cross-compilation for darwin/linux/windows in the Makefile:
- **macOS:** shells to `/usr/bin/security` (Keychain Access).
- **Linux:** talks to the Secret Service over D-Bus.
- **Windows:** uses the Windows Credential Manager via syscall.

Service name: `monoagent-vault`, account: `dek-wrap`. On first access with no existing entry, `keyring.go` generates a fresh KEK, wraps a newly generated DEK, and stores the wrapped DEK in the DB (table below) — the KEK is regenerated and re-stored in the keychain, never reused across reinstalls unless the keychain entry survives.

---

## Data Layer

### Migration: `data/migrations/017_secrets_vault.sql`

```sql
CREATE TABLE vault_secrets (
    id               TEXT PRIMARY KEY,      -- "sec-001", "sec-002", ...
    seq              INTEGER NOT NULL UNIQUE,
    profile_id       TEXT NOT NULL DEFAULT 'default',
    kind             TEXT NOT NULL,          -- 'secret' | 'login'
    name             TEXT NOT NULL,
    username         TEXT,                   -- 'login' kind only
    url              TEXT,                   -- 'login' kind only
    ciphertext       BLOB NOT NULL,          -- AES-256-GCM(secret/password value)
    nonce            BLOB NOT NULL,
    notes_ciphertext BLOB,                   -- nullable, same nonce reused is NOT allowed; own nonce below
    notes_nonce      BLOB,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_vault_secrets_profile_name ON vault_secrets(profile_id, name);
CREATE INDEX idx_vault_secrets_seq ON vault_secrets(seq DESC);

CREATE TABLE vault_keys (
    id          INTEGER PRIMARY KEY CHECK (id = 1), -- singleton row
    wrapped_dek BLOB NOT NULL,
    created_at  TEXT NOT NULL
);
```

ID generation follows the existing `img-%03d` pattern in `internal/vault`: `sec-%03d` from `MAX(seq)+1`, allocated under `BEGIN IMMEDIATE` to avoid the same cross-process race the image vault's `Register` already guards against.

### `internal/connections` change

`Connection.Data` keeps non-secret metadata (platform, scopes, expiry) but any field carrying raw credential material (`access_token`, `refresh_token`, `api_key`, `password`, etc.) is replaced with a `vault_ref` string (e.g. `"vault_ref": "sec-014"`). A resolver function expands these at the point of use — mirroring `internal/vault.ResolveConfig`'s `@img-` walk — so plaintext exists only transiently in memory during an actual API call, never in the stored config, logs, or `--json` output.

---

## `internal/secrets` Package

```go
// Add creates a new secret or login entry, encrypting the value(s) before storage.
func Add(ctx context.Context, db *sql.DB, kind, name, value, username, url, notes string) (id string, err error)

// Resolve returns a workflow-safe reference (not the plaintext) for use in configs.
func Resolve(ctx context.Context, db *sql.DB, ref string) (Reference, error)

// Decrypt returns the plaintext value — the ONLY function that ever does. Called
// exclusively by: (a) the CLI `vault reveal` command, and (b) internal connector
// call sites resolving a Reference immediately before an outbound API call.
func Decrypt(ctx context.Context, db *sql.DB, id string) (value string, err error)

// List returns names/metadata only — never decrypts.
func List(ctx context.Context, db *sql.DB, profileID string) ([]Entry, error)

// Delete removes an entry.
func Delete(ctx context.Context, db *sql.DB, id string) error

// MigrateConnections is idempotent: scans connections.data for known secret-bearing
// fields per platform, moves each into vault_secrets, and rewrites the row to hold
// a vault_ref. Rows already migrated (vault_ref present) are skipped. Runs inside
// one transaction per connection row — a failure leaves that row untouched and logs
// a warning, it does not abort the whole migration.
func MigrateConnections(ctx context.Context, db *sql.DB) (migrated int, err error)
```

---

## CLI — `cmd/monoagentcli/secret.go`

This is the **only** decrypt entrypoint in the whole app — the GUI calls the same underlying Go functions, never a separate code path.

| Command | Behavior |
|---|---|
| `monoagentcli secret add --kind secret\|login --name X [--username U] [--url URL] [--notes N]` | Prompts for the secret/password value on stdin (never accepted as a flag, to keep it out of shell history and `ps`). |
| `monoagentcli secret list [--profile P]` | Names/metadata only. |
| `monoagentcli secret get <name>` | Returns the resolved `@secret:<name>` reference token for use in workflow configs — never plaintext. |
| `monoagentcli secret reveal <name> --reveal` | The human-only decrypt path. Requires the redundant `--reveal` flag as an explicit confirmation gate (a bare `secret reveal foo` without the flag errors, so plaintext is never one accidental Enter away). Prints plaintext to stdout. |
| `monoagentcli secret rm <name>` | Deletes. |
| `monoagentcli secret migrate-connections` | Runs `MigrateConnections`; also invoked once automatically on first startup after upgrade (tracked via a `schema_migrations`-style marker, consistent with how other migrations in `data/migrations/` are tracked). |

---

## GUI — `wails-app/frontend/src/pages/Vault.jsx` (new; naming distinct from the existing `ImageVault.jsx`)

- List view: name, kind, username/URL (for logins), last updated — no secret values rendered by default.
- Reveal: eye icon per row calls a Wails-bound `App.RevealSecret(id)` method, which internally calls the same `secrets.Decrypt` the CLI's `secret reveal` command calls — not a separate implementation.
- Add/Edit modal: kind selector (secret/login), name, conditionally username/URL, secret value input (masked), notes.
- Delete: confirm dialog → `App.DeleteSecret(id)`.
- Follows the existing page pattern (e.g. `ImageVault.jsx`, `Profile.jsx`) for layout/style consistency — no new UI framework or component library introduced.

---

## Error Handling

- `Decrypt`/`Resolve` on a missing ID: explicit "not found" error, no silent fallback to a placeholder value (unlike `internal/vault.ResolveConfig`, which intentionally leaves `@img-` refs unresolved with a warning — secrets must fail loudly since a missing credential silently proceeding could mean an unauthenticated API call).
- Keychain unavailable (e.g. headless Linux CI with no Secret Service running): `Add`/`Decrypt` return a clear error identifying the missing keychain backend; no plaintext fallback storage is ever written to disk.
- Migration failure on one connection row: logged, that row is left as plaintext and unmigrated, migration continues with the next row, and the final count/skip list is surfaced to the CLI caller.

---

## Testing

- `internal/secrets/crypto_test.go`: encrypt/decrypt round-trip, wrong-key decrypt fails, nonce uniqueness across repeated calls.
- `internal/secrets/keyring_test.go`: wrap/unwrap DEK round-trip (using the real OS keychain in CI where available; skipped with a clear message where it's not).
- `internal/secrets/migrate_test.go`: migration moves secret fields out of `connections.data`, running it twice is a no-op (idempotency), a corrupt/malformed row doesn't abort the batch.
- CLI-level test (mirroring the existing `people status set/get/history` CLI coverage): asserts `secret get` never returns plaintext in its output while `secret reveal --reveal` does, and that `secret list`/`--json` anywhere in the app never includes ciphertext or plaintext.

---

## Out of Scope (this spec)

- Cross-machine passphrase-based vault portability (OS keychain ties the vault to one machine/OS account by design, per the approved key-management choice).
- Secret sharing between profiles/users.
- Automatic secret rotation/expiry policies.
