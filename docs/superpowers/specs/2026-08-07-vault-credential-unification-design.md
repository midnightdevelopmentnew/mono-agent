# Vault: Single Source of Truth for Connections, Sessions & AI Keys — Design Spec
**Date:** 2026-08-07
**Status:** Approved

---

## Overview

Extends the vault ([2026-07-13-secrets-vault-design.md](./2026-07-13-secrets-vault-design.md), [2026-08-06-vault-keyvalue-design.md](./2026-08-06-vault-keyvalue-design.md)) so it becomes the single storage backend for every credential the app holds, not just user-added secrets/logins:

1. **Platform connections** (`connections.data` — GitHub/Reddit/Discord/Stripe/etc. tokens and API keys).
2. **Crawler login sessions** (`crawler_sessions.cookies_json` — Instagram/LinkedIn/X/TikTok/HackerNews/Gemini browser cookies).
3. **AI provider keys** (`ai_providers.api_key` — OpenAI/Anthropic/Bedrock/Google/etc.).

All three are today either invisible to the vault (connections, sessions — encrypted at rest under the vault's own DEK via `secrets.EncryptBlob`, but as opaque per-row blobs, not vault entries) or fully plaintext (`ai_providers.api_key`, no encryption at all). None of the three are included in the vault's export/import.

After this change: any credential set into a connection, a crawler login, or an AI provider is automatically written through the vault as a real `vault_secrets` row; the Vault page/CLI lists and can edit it like any other entry; deleting it there removes the underlying connection/session/provider too; and exporting the vault and importing it on another machine reconnects everything — not just repopulates a list.

---

## Core Mechanism

### Schema: `data/migrations/022_credential_vault_unification.sql`

```sql
ALTER TABLE connections       ADD COLUMN vault_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE crawler_sessions  ADD COLUMN vault_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE ai_providers      ADD COLUMN vault_ref TEXT NOT NULL DEFAULT '';
```

`vault_ref` holds the linked `vault_secrets.id` (e.g. `sec-014`). Empty means "not yet migrated" (pre-existing row) or, transiently, "being created."

`vault_secrets.kind` gains three system-managed values: `connection`, `session`, `ai_provider` (alongside the existing user-facing `secret`, `login`). Distinguishing them by `kind` — rather than a separate boolean — lets `secret list`/Vault.jsx render a kind badge with no new column.

### `internal/secrets` changes

`Add`'s existing kind allowlist (`"secret"` or `"login"` only) stays as the **public** gate — it's what the CLI's `secret add --kind` flag and the GUI's "Add New Item" form go through, and a human still cannot hand-create a fake `connection`/`session`/`ai_provider` entry. Internally, `Add` becomes a thin wrapper around a new unexported `addEntry` (same body, no kind check) that the system-entry path below calls directly with a system kind.

```go
// PutSystemEntry upserts a system-managed vault entry (kind must be
// "connection", "session", or "ai_provider") on behalf of connections,
// crawler-session login, or the AI provider store. This is the one write
// path all three funnel through — it IS the "credentials are automatically
// saved to the vault" mechanism, not a separate sync step.
//
// If existingID is non-empty, the entry's fields (and username/url, if
// given) are updated in place via Update; name is left untouched so a
// rename the user made in the Vault UI isn't silently reverted by the next
// token refresh. If existingID is empty, a fresh entry is created, and a
// name collision is disambiguated by appending " (2)", " (3)", etc.
// Returns the vault_secrets id to persist back into the caller's vault_ref
// column.
func PutSystemEntry(ctx context.Context, db *sql.DB, profileID, kind, existingID, name string, fields map[string]string, username, url string) (string, error)

// DeleteCascade deletes vault_secrets entry id and, if its kind is
// system-managed, the linked row in connections/crawler_sessions/ai_providers
// (matched by vault_ref = id) in the same transaction — the vault entry and
// the linked row are the same credential, so deleting one and not the other
// would leave the app pointing at a token that no longer exists anywhere.
// For kind "secret"/"login" this is equivalent to plain Delete.
func DeleteCascade(ctx context.Context, db *sql.DB, profileID, id string) error
```

`DeleteCascade` uses raw SQL against the three table names (`DELETE FROM connections WHERE vault_ref = ? AND profile_id = ?`, etc.) rather than importing `internal/connections`/`internal/ai` — those packages already import `internal/secrets`, so the reverse import would cycle. This mirrors the existing convention (`internal/connections/migrate.go`'s comment referencing `secrets/blob.go`'s prefix constant by literal string, not by import).

### Per-subsystem field split

Each subsystem decides which of its own fields are secret — `internal/secrets` stays platform-agnostic and only ever sees a `map[string]string`.

**Connections** (`internal/connections`): for `MethodOAuth`, the secret keys are exactly `access_token` and `refresh_token` (confirmed the only two secret-bearing keys `manager.go`/`storage.go` write into `Data` after a token exchange/refresh — `token_type`, `scope`, `expires_at` stay non-secret). For every other method, the secret keys are whichever `CredentialField.Key` in `PlatformDef.Fields[method]` has `Secret: true` (e.g. `password`, `api_key`, `access_token`, `private_key`, `passphrase`, `bot_token`, `auth_token`, `connection_string`, `app_password`, `secret_key`). Everything else in `Data` (host, port, username, base_url, shop_domain, instance_url, from_number, account_sid, ...) stays exactly where it is today.

**Crawler sessions** (`cmd/monoagentcli/login.go`): the entire cookie jar is one field, `{"cookies": "<cookies_json>"}`. There is no non-secret remainder — cookies are the whole credential.

**AI providers** (`internal/ai`): one field, `{"api_key": "<key>"}`. `base_url`, `default_model`, `extra_headers`, `tier`, `status`, `last_tested` stay in `ai_providers` as today.

### Naming

- Connection: `"{PlatformDef.Name} — {Label or AccountID}"` (connections already support multiple accounts per platform via `Label`/`AccountID`, so the vault name must disambiguate the same way).
- Session: `"{platform} session — {username}"`.
- AI provider: reuses the provider's own user-supplied `Name` field directly (already unique-ish; `PutSystemEntry`'s collision suffixing covers the rest).

Collisions (e.g. two connections both landing on the same computed name) are resolved by `PutSystemEntry` appending `" (2)"`, `" (3)"`, ... — the same behavior a user hand-adding a duplicately-named secret would hit.

---

## Per-subsystem wiring

### `internal/connections/storage.go`

`Connection` and `SafeConnection` both gain a `VaultRef string` field (mirrors the new `connections.vault_ref` column; included in `SafeConnection` since it's not secret material, just an id — useful for the GUI to deep-link from a connection to its Vault entry).

- `Store.Save`: splits `Data` into `(secretFields, nonSecretData)` per the rule above. Calls `secrets.PutSystemEntry(ctx, db, c.ProfileID, "connection", c.VaultRef, name, secretFields, c.AccountID, "")`, stores the returned id into `c.VaultRef` (new `Connection.VaultRef` field) before persisting; `nonSecretData` is what gets JSON-marshaled and `EncryptBlob`-wrapped into the `data` column, unchanged mechanism, just a smaller payload. If `secretFields` is empty (e.g. a `MethodBrowser` platform with no `Fields` at all — Instagram, LinkedIn, X, TikTok, HackerNews, Gemini), no vault entry is created and `vault_ref` stays empty; those platforms' actual login state lives entirely in `crawler_sessions`, not `connections`.
- `scanConnection`/`scanConnections` (used by `Get`, `GetOrResolve`, `ListAll`, `ListByPlatform`): after decoding the non-secret `Data`, if `vault_ref` is set, call `secrets.DecryptFields` and merge the returned fields into `Data` — every existing caller reading `conn.Data["access_token"]` etc. keeps working with zero call-site changes. This preserves the current "everything returns full `Data`; output boundaries call `Redact()`" invariant rather than introducing a List/Get split.
- `RefreshToken`: writes the new `access_token`/`refresh_token` through the same `Save` path (which routes through `PutSystemEntry` with the existing `vault_ref`, so it's an update, never a duplicate).
- `SaveOAuthClient`/`GetOAuthClient` (`platform_oauth_credentials` table): **left as-is** — see Out of Scope.

### `cmd/monoagentcli/login.go`

- `saveSession` replaces its direct `secrets.EncryptBlob(cookiesJSON)` call with `secrets.PutSystemEntry(ctx, db.DB, profileID, "session", existingVaultRef, name, map[string]string{"cookies": string(cookiesJSON)}, username, platform)`, storing the id into a new `vault_ref` column via the same UPDATE-then-INSERT upsert it already does for the row itself. `existingVaultRef` comes from a `SELECT vault_ref` added right before the existing UPDATE attempt.
- Every reader of `cookies_json` (`wails-app/app.go`'s `TestSession`, `app_connections.go`'s status fallback, and whatever the actual crawl action reads cookies from) switches from reading the column to `secrets.DecryptFields` by `vault_ref`. The `cookies_json` column itself is left in the schema (see Out of Scope) but stops being written.

### `internal/ai/store.go`

`AIProvider` gains a `VaultRef string` field (mirrors the new `ai_providers.vault_ref` column). It's excluded from `MarshalJSON`'s existing custom projection only if it should stay internal; since it's not secret, it's left in the default JSON output.

- `SaveProvider`: no longer writes the real key into the `api_key` column (writes `""` going forward — see Out of Scope for why the column stays). Calls `secrets.PutSystemEntry(ctx, s.db, p.ProfileID, "ai_provider", p.VaultRef, p.Name, map[string]string{"api_key": p.APIKey}, "", "")`, stores the id into a new `AIProvider.VaultRef` field before the INSERT/UPDATE.
- `GetProvider`: after scanning the row, if `vault_ref` is set, `secrets.DecryptFields` and set `p.APIKey` from the `"api_key"` field — this is the one function real API call sites (`internal/ai/chat/service.go`, `internal/ai/nodes/common.go`) go through, so it's the only one that pays a decrypt.
- `ListProviders`: does **not** decrypt — `APIKey` stays empty on every returned struct. `MarshalJSON` already scrubs `APIKey` from any serialized output regardless, and the only two callers of `ListProviders` (`cmd/monoagentcli/ai.go`'s `ai list`, `wails-app/app_ai.go`'s provider list) are display-only — this matches the vault's own existing "`List` never decrypts" invariant instead of introducing a new exception to it.

---

## One-time migration

Three migration functions, each living in the package that already has the domain knowledge to split fields (avoids the `internal/secrets` → `internal/connections` import cycle noted above):

- `internal/connections/migrate_vault_ref.go`: `MigrateConnectionsToVault(ctx, db) (migrated, total int, err error)` — for rows with `vault_ref = ''` and at least one secret-bearing key present in `Data`, splits and calls `PutSystemEntry`, then re-saves the row (same `Store.Save` path, so the row's `data` column shrinks to the non-secret remainder in the same pass).
- `internal/ai/migrate.go`: `MigrateProvidersToVault(ctx, db) (migrated, total int, err error)` — for rows with `vault_ref = ''` and non-empty `api_key`, same pattern.
- `internal/secrets/migrate_system.go`: `MigrateSessionsToVault(ctx, db) (migrated, total int, err error)` — for `crawler_sessions` rows with `vault_ref = ''`, decrypts the legacy `cookies_json` blob via the existing `DecryptBlob` and calls `PutSystemEntry` directly (no connections-specific knowledge needed here, so this one can live in `secrets`).

All three follow the existing `EncryptPlaintextConnections`/`MigrateFieldsToKV` shape: a cheap `COUNT(*)` guard first (no-op when zero), per-row failures logged to stderr and skipped rather than aborting the batch. All three are invoked once at startup — CLI `root.go` and GUI `wails-app/app.go` — alongside the two existing migration calls already there.

---

## Portability (export / import)

Because system-managed entries are now ordinary `vault_secrets` rows, **`secrets.Export` already includes them** — `Export` iterates every entry for the profile regardless of kind, so no export-side change is needed beyond what already exists.

`Import` needs two changes:

1. **Accept system kinds.** `Import` currently calls the public `Add` (rejects anything but `secret`/`login`) — switch it to the unexported `addEntry` so `connection`/`session`/`ai_provider` entries import as real vault rows instead of erroring out.
2. **Re-materialize the linked row.** `exportEntry` gains an optional `Meta map[string]string` field, populated per kind at export time:
   - `connection`: `platform`, `method`, `label`, `account_id`.
   - `session`: `platform`, `username`.
   - `ai_provider`: `provider_id`, `tier`, `base_url`, `default_model`, `extra_headers`.

   `Export` populates `Meta` with a raw SQL `SELECT` against the relevant table by `vault_ref` (`SELECT platform, method, label, account_id FROM connections WHERE vault_ref = ?`, etc.) — the same "reference the table by literal name, not by importing the owning package" approach `DeleteCascade` uses, so `internal/secrets` still never imports `internal/connections`/`internal/ai`.

   Re-materializing the row on import is a *write* into a table whose row shape only `internal/connections`/`internal/ai`/the session-save logic know how to construct correctly (e.g. a connection needs `PlatformDef` lookups, an `id`, a `status`), so `Import` cannot do this itself without the same import-cycle problem. `Import`'s signature grows three optional callback parameters, one per system kind, each `func(ctx context.Context, db *sql.DB, profileID, vaultID string, meta map[string]string) error`:

   ```go
   func Import(ctx context.Context, db *sql.DB, profileID, passphrase string, fileData []byte,
       rematerializeConnection, rematerializeSession, rematerializeProvider func(ctx context.Context, db *sql.DB, profileID, vaultID string, meta map[string]string) error,
   ) (imported, skipped int, err error)
   ```

   A `nil` callback simply skips re-materializing that kind (the vault entry itself still imports) — `internal/secrets`' own package-internal tests pass `nil` for all three and only assert on the vault row. `cmd/monoagentcli/secret_export.go`'s `secret import` command — the only real caller — wires all three to the actual `internal/connections`/`internal/ai`/session-save upsert functions, matched against an existing row by natural key (platform+label for connections, platform+username for sessions, provider name for AI providers) to update in place rather than duplicate on a re-import.

   A name collision on import is a **skip, not an update**: `secrets.Import` checks for an existing entry of the same name before any rematerialize callback runs, and leaves the destination's existing entry (and its credential) untouched when one is found. This is the safer default — an older exported credential (e.g. a since-rotated OAuth refresh token) could otherwise silently replace a working local one — but it does mean re-importing a vault export onto a machine that already has a same-named connection/session/provider is a no-op for that entry, not a refresh.

---

## Vault UI/CLI surface

- `secret list` / `Vault.jsx`: kind badge changes from "Keys"/"Login" to also cover "Connection"/"Session"/"AI Key" for the three system kinds; `field_count` display unchanged.
- Editing: unchanged code path (`secret update --field`, `VaultItemModal`/`KeyValueFields`) — since these are chosen to be fully editable, changing a system entry's field there **is** changing the live credential (same row), no extra propagation.
- Deleting: `secret rm` and the GUI's delete action switch from `secrets.Delete` to `secrets.DeleteCascade` unconditionally (a no-op behavior difference for `secret`/`login` kinds, since `DeleteCascade` degrades to plain `Delete` when there's nothing to cascade to).
- `secret add --kind` stays restricted to `secret`/`login` — unchanged, per the "public gate" note above.

---

## Error Handling

- `vault_ref` set but the linked `vault_secrets` row missing (e.g. deleted out-of-band via raw SQL, or corrupted): the merge-back in `Get`/`GetProvider`/session-read returns a clear "credential not found in vault" error — never silently falls back to an empty-string credential and proceeds with an unauthenticated call.
- Import re-materialization failure for one entry (e.g. a `Rematerialize` callback error): logged and skipped, same posture as every other per-entry failure in this codebase's migration/import code — does not abort the batch.
- Migration per-row failures: logged and skipped, matching `EncryptPlaintextConnections`/`MigrateFieldsToKV`.

---

## Testing

- `internal/secrets`: `PutSystemEntry` create-then-update-in-place (second call with `existingID` doesn't create a duplicate, doesn't touch `name`); `DeleteCascade` removes both the vault row and, via a fake rematerializer/direct table row, the linked row, for each of the three kinds; `Add`'s public kind allowlist still rejects `connection`/`session`/`ai_provider`.
- `internal/connections`: `Save`/`Get` round-trip with a mix of secret and non-secret fields — confirms `Data` merge-back is transparent; `RefreshToken` updates the existing vault entry rather than creating a second one; `MigrateConnectionsToVault` idempotency (second run no-ops) and correctness (secret keys land in vault, non-secret keys stay in `data`).
- `internal/ai`: `SaveProvider`/`GetProvider` round-trip; raw `ai_providers.api_key` column is empty after save; `ListProviders` never decrypts (verify via a DEK-fetch call counter or simply that `APIKey` is `""` on every returned struct).
- `cmd/monoagentcli`: session save/read round-trip through the vault; `secret export`/`secret import` round trip that recreates a `connections` row (not just the vault entry) with matching platform/label/method; re-importing onto the same "machine" upserts the existing connection rather than duplicating it.
- Migration idempotency tests for all three new migration functions, mirroring the existing `migrate_kv_test.go` pattern.

---

## Out of Scope

- `platform_oauth_credentials` (the app's own registered OAuth client id/secret per platform, set via `connect set-oauth-client`) — already encrypted via `secrets.EncryptBlob`, and it's an app-registration secret shared across all connections to a platform rather than a specific account's login credential, which is what this spec is about. Left untouched.
- Dropping the now-partially-unused columns (`ai_providers.api_key`, `crawler_sessions.cookies_json`, the secret keys `connections.data` used to carry) — left in the schema as inert legacy columns rather than a destructive `ALTER TABLE ... DROP COLUMN` in this pass.
- Renaming the unrelated `internal/vault` (image-asset) package.
- Changing the crawl-login capture UX itself (`login.go`'s `login`/`login confirm` flow) — only its storage backend changes.
- Cross-profile linkage — system-managed vault entries stay scoped to the same profile as their connection/session/provider, consistent with existing profile scoping everywhere else in the app.
- Automatic credential rotation/expiry policies.
- Folding `MethodBrowser` platforms (Instagram/LinkedIn/X/TikTok/HackerNews/Gemini) into the `connection` kind — their credential material is entirely the `crawler_sessions` cookie jar, already covered by the `session` kind; their `connections` row (if any) never gets a `vault_ref`.
