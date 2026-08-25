package connections

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"monoagent/internal/secrets"

	"github.com/google/uuid"
)

// Connection represents a stored platform connection.
type Connection struct {
	ID         string                 `json:"id"`
	Platform   string                 `json:"platform"`
	Method     AuthMethod             `json:"method"`
	Label      string                 `json:"label"`
	AccountID  string                 `json:"account_id"`
	Data       map[string]interface{} `json:"data"`
	Status     string                 `json:"status"`      // "active" | "expired" | "error"
	LastTested string                 `json:"last_tested,omitempty"`
	ProfileID  string                 `json:"profile_id,omitempty"`
	VaultRef   string                 `json:"vault_ref,omitempty"`
	CreatedAt  string                 `json:"created_at"`
	UpdatedAt  string                 `json:"updated_at"`
}

// SafeConnection is the credential-free projection of a Connection for any
// output boundary — CLI --json, GUI IPC responses — that must never leak
// stored token/secret material (Connection.Data holds access_token,
// refresh_token, api_key, etc. in cleartext).
type SafeConnection struct {
	ID         string     `json:"id"`
	Platform   string     `json:"platform"`
	Method     AuthMethod `json:"method"`
	Label      string     `json:"label"`
	AccountID  string     `json:"account_id"`
	Status     string     `json:"status"`
	LastTested string     `json:"last_tested,omitempty"`
	ProfileID  string     `json:"profile_id,omitempty"`
	VaultRef   string     `json:"vault_ref,omitempty"`
	CreatedAt  string     `json:"created_at"`
	UpdatedAt  string     `json:"updated_at"`
}

// Redact strips credential material, returning the safe-to-serialize view.
func (c Connection) Redact() SafeConnection {
	return SafeConnection{
		ID:         c.ID,
		Platform:   c.Platform,
		Method:     c.Method,
		Label:      c.Label,
		AccountID:  c.AccountID,
		Status:     c.Status,
		LastTested: c.LastTested,
		ProfileID:  c.ProfileID,
		VaultRef:   c.VaultRef,
		CreatedAt:  c.CreatedAt,
		UpdatedAt:  c.UpdatedAt,
	}
}

// RedactAll maps a slice of Connections to their safe projections.
func RedactAll(conns []Connection) []SafeConnection {
	out := make([]SafeConnection, len(conns))
	for i, c := range conns {
		out[i] = c.Redact()
	}
	return out
}

const createConnectionsTable = `
CREATE TABLE IF NOT EXISTS connections (
    id          TEXT PRIMARY KEY,
    platform    TEXT NOT NULL,
    method      TEXT NOT NULL,
    label       TEXT NOT NULL,
    account_id  TEXT NOT NULL DEFAULT '',
    data        TEXT NOT NULL DEFAULT '{}',
    status      TEXT NOT NULL DEFAULT 'active',
    last_tested TEXT NOT NULL DEFAULT '',
    profile_id  TEXT NOT NULL DEFAULT 'default',
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_connections_platform ON connections(platform);
CREATE INDEX IF NOT EXISTS idx_connections_status   ON connections(status);
CREATE INDEX IF NOT EXISTS idx_connections_profile  ON connections(profile_id);
`

// createRefreshLocksTable backs RefreshToken's cross-process lock. The
// connections DB is shared (by file path) across the CLI, the daemon, and
// the GUI, each their own OS process with their own in-memory Store — a
// Go-level mutex only ever serializes goroutines inside one of them. Two
// processes resolving the same stale connection at once would otherwise both
// exchange the same stored refresh_token; providers that rotate single-use
// refresh tokens invalidate the loser's exchange, permanently breaking the
// connection. Coordinating through this table (which every process reads
// and writes through the same SQLite file) closes that gap.
const createRefreshLocksTable = `
CREATE TABLE IF NOT EXISTS oauth_refresh_locks (
    connection_id TEXT PRIMARY KEY,
    locked_at     TEXT NOT NULL
);
`

// refreshLockStaleAfter bounds how long a lock row is honored before it's
// considered abandoned (its holder crashed or was killed mid-refresh) and
// safe to steal — a token exchange is a single HTTP round trip and never
// legitimately takes this long.
const refreshLockStaleAfter = 30 * time.Second

// refreshLockWaitTimeout is how long a caller that lost the race waits for
// the current holder to finish before giving up and just using whatever is
// on file — a real exchange completes in well under this.
const refreshLockWaitTimeout = 5 * time.Second

// Store provides CRUD operations for connections.
type Store struct {
	db *sql.DB
}

// NewStore creates a new Store backed by the given database.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// DB returns the underlying *sql.DB.
func (s *Store) DB() *sql.DB { return s.db }

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

// Save upserts a connection. If ID is empty a new UUID is generated. If
// CreatedAt is empty it is set to now. UpdatedAt is always refreshed.
// Status defaults to "active" when empty.
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

	secretFields, nonSecret := splitSecretFields(c.Platform, c.Method, c.Data)
	if len(secretFields) > 0 {
		linkID, putErr := secrets.PutSystemEntry(ctx, s.db, c.ProfileID, "connection", c.VaultRef, connectionVaultName(c), secretFields, c.AccountID, "")
		if putErr != nil {
			return fmt.Errorf("connections.Save: saving credentials to vault: %w", putErr)
		}
		c.VaultRef = linkID
	}

	dataBytes, err := json.Marshal(nonSecret)
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

// Get returns the connection with the given ID, or nil if not found.
func (s *Store) Get(ctx context.Context, id string) (*Connection, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, platform, method, label, account_id, data, status, last_tested, COALESCE(profile_id,'default'), COALESCE(vault_ref,''), created_at, updated_at
         FROM connections WHERE id = ?`, id)
	return scanConnection(ctx, s.db, row)
}

// GetOrResolve looks up a connection by ID, falling back to platform name lookup
// if the ID isn't found. When only one connection exists for a platform, it's used
// automatically. OAuth tokens are refreshed if expired.
func (s *Store) GetOrResolve(ctx context.Context, idOrPlatform string) (*Connection, error) {
	// Try by ID first.
	conn, err := s.Get(ctx, idOrPlatform)
	if err == nil && conn != nil {
		return s.ensureFreshToken(ctx, conn)
	}

	// Fallback: look up by platform name across all profiles (no profile filter for workflow resolution).
	conns, err := s.ListByPlatform(ctx, idOrPlatform, "")
	if err != nil || len(conns) == 0 {
		return nil, fmt.Errorf("no connection found for %q", idOrPlatform)
	}
	// Prefer active connections.
	for i := range conns {
		if conns[i].Status == "active" {
			return s.ensureFreshToken(ctx, &conns[i])
		}
	}
	return s.ensureFreshToken(ctx, &conns[0])
}

// ensureFreshToken checks if an OAuth token is expired and refreshes it using
// the stored refresh_token. Returns the (possibly refreshed) connection.
// Best-effort: any refresh failure is swallowed and the caller gets back the
// connection as-is (its existing, possibly-stale token) rather than an error,
// since callers on this path just want a usable connection to proceed with.
// Use RefreshToken instead when the caller needs to know whether it worked.
func (s *Store) ensureFreshToken(ctx context.Context, conn *Connection) (*Connection, error) {
	expiresStr, _ := conn.Data["expires_at"].(string)
	if expiresStr == "" {
		return conn, nil // Not an OAuth connection or no expiry — use as-is.
	}
	expiresAt, err := time.Parse(time.RFC3339, expiresStr)
	if err != nil {
		return conn, nil // Can't parse — use as-is.
	}
	// Token still valid for at least 60 more seconds.
	if time.Now().UTC().Before(expiresAt.Add(-60 * time.Second)) {
		return conn, nil
	}
	_ = s.RefreshToken(ctx, conn)
	return conn, nil
}

// RefreshToken performs a refresh_token exchange for conn (regardless of
// whether its access_token has actually expired yet), persists the result,
// and reports exactly why it failed when it does — used by `connect refresh`
// and anywhere else that needs real feedback instead of a silent no-op.
func (s *Store) RefreshToken(ctx context.Context, conn *Connection) error {
	// Serialize refreshes across every process sharing this DB (CLI, daemon,
	// GUI) so overlapping resolvers don't each exchange the same stored
	// refresh_token. A caller that loses the race waits briefly for the
	// winner to finish, then re-reads and uses whatever it produced instead
	// of racing a second exchange of the same (possibly now-rotated) token.
	if conn.ID != "" {
		acquired, err := s.acquireRefreshLock(ctx, conn.ID)
		if err != nil {
			return fmt.Errorf("acquiring refresh lock: %w", err)
		}
		if !acquired {
			if latest, gerr := s.Get(ctx, conn.ID); gerr == nil && latest != nil {
				*conn = *latest
			}
			return nil
		}
		defer s.releaseRefreshLock(context.WithoutCancel(ctx), conn.ID)

		// Re-read the latest tokens: a refresh that completed in another
		// process between our caller's Get and our lock acquisition may have
		// rotated the refresh_token, and exchanging the now-consumed one
		// would permanently break the connection.
		if latest, err := s.Get(ctx, conn.ID); err == nil && latest != nil && latest.Data != nil {
			conn.Data = latest.Data
			conn.VaultRef = latest.VaultRef
		}
	}

	refreshToken, _ := conn.Data["refresh_token"].(string)
	if refreshToken == "" {
		return fmt.Errorf("no refresh_token stored for this connection — reconnect with the full OAuth flow instead")
	}

	p, ok := Get(conn.Platform)
	if !ok || p.OAuth == nil {
		return fmt.Errorf("platform %q has no OAuth config", conn.Platform)
	}

	cfg := *p.OAuth
	cfg.PlatformID = p.ID
	// Load client credentials from platform_oauth_credentials table (scoped
	// per profile — two connections for the same platform under different
	// profiles may need different Azure/OAuth app registrations), then fall
	// back to env vars.
	credProfileID := conn.ProfileID
	if credProfileID == "" {
		credProfileID = "default"
	}
	if dbClientID, dbClientSecret := s.GetOAuthClient(ctx, conn.Platform, credProfileID); dbClientID != "" {
		cfg.ClientID = dbClientID
		cfg.ClientSecret = dbClientSecret
	}
	// Allow env var overrides.
	envPrefix := "MONOAGENT_" + strings.ToUpper(strings.ReplaceAll(p.ID, "-", "_")) + "_"
	if cfg.ClientID == "" {
		cfg.ClientID = os.Getenv(envPrefix + "CLIENT_ID")
	}
	if cfg.ClientSecret == "" {
		cfg.ClientSecret = os.Getenv(envPrefix + "CLIENT_SECRET")
	}
	if cfg.ClientID == "" {
		return fmt.Errorf("missing OAuth client credentials for %q profile %q — run `monoagentcli connect set-oauth-client %s --client-id <id> [--client-secret <secret>]` or set %sCLIENT_ID", p.ID, credProfileID, p.ID, envPrefix)
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", cfg.ClientID)
	form.Set("client_secret", cfg.ClientSecret)

	body, status, err := PostTokenRequestWithAudienceFallback(cfg.PlatformID, cfg.TokenURL, form)
	if err != nil {
		return fmt.Errorf("token request: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("token endpoint returned %d: %s", status, string(body))
	}

	var result OAuthResult
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("decode token response: %w", err)
	}
	if result.AccessToken == "" {
		return fmt.Errorf("token response missing access_token")
	}

	// Update connection data with new tokens.
	conn.Data["access_token"] = result.AccessToken
	conn.Data["token_type"] = result.TokenType
	if result.RefreshToken != "" {
		conn.Data["refresh_token"] = result.RefreshToken
	}
	if result.ExpiresIn > 0 {
		conn.Data["expires_at"] = time.Now().UTC().Add(time.Duration(result.ExpiresIn) * time.Second).Format(time.RFC3339)
	}
	conn.Status = "active"

	return s.Save(ctx, conn)
}

// acquireRefreshLock atomically claims the refresh lock for connID, stealing
// it if the existing holder's lock has gone stale (crashed mid-refresh), and
// otherwise polling briefly for the current holder to release it. Returns
// false (no error) if the lock is still held by someone else after
// refreshLockWaitTimeout — the caller is expected to fall back to reading
// whatever that holder produced instead of blocking forever.
func (s *Store) acquireRefreshLock(ctx context.Context, connID string) (bool, error) {
	deadline := time.Now().Add(refreshLockWaitTimeout)
	for {
		acquired, err := s.tryAcquireRefreshLock(ctx, connID)
		if err != nil {
			return false, err
		}
		if acquired {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// tryAcquireRefreshLock makes a single atomic attempt to claim connID's
// refresh lock. It succeeds if no lock row exists, or if the existing row is
// older than refreshLockStaleAfter (its holder is presumed dead). The
// INSERT...ON CONFLICT...WHERE form is a single statement, so SQLite's own
// locking (already tuned with busy_timeout in the DSN) makes the claim
// atomic across every process sharing this database file.
func (s *Store) tryAcquireRefreshLock(ctx context.Context, connID string) (bool, error) {
	now := time.Now().UTC()
	staleBefore := now.Add(-refreshLockStaleAfter).Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO oauth_refresh_locks (connection_id, locked_at) VALUES (?, ?)
		 ON CONFLICT(connection_id) DO UPDATE SET locked_at = excluded.locked_at
		 WHERE oauth_refresh_locks.locked_at < ?`,
		connID, now.Format(time.RFC3339), staleBefore)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// releaseRefreshLock drops connID's refresh lock so the next caller (in this
// process or another) doesn't have to wait out refreshLockStaleAfter.
func (s *Store) releaseRefreshLock(ctx context.Context, connID string) {
	_, _ = s.db.ExecContext(ctx, `DELETE FROM oauth_refresh_locks WHERE connection_id = ?`, connID)
}

func envLookup(key string) string {
	return os.Getenv(key)
}

// GetOAuthClient returns the OAuth app client_id/client_secret stored for
// platform under profileID (set via `connect set-oauth-client`, the GUI, or
// auto-persisted by a successful connect). Returns empty strings when none
// are stored (including when the table doesn't exist yet).
func (s *Store) GetOAuthClient(ctx context.Context, platform, profileID string) (clientID, clientSecret string) {
	if profileID == "" {
		profileID = "default"
	}
	var storedSecret string
	_ = s.db.QueryRowContext(ctx,
		`SELECT client_id, client_secret FROM platform_oauth_credentials WHERE platform = ? AND profile_id = ?`,
		platform, profileID,
	).Scan(&clientID, &storedSecret)
	if storedSecret != "" {
		// Decrypt; legacy plaintext rows pass through unchanged.
		if plain, err := secrets.DecryptBlob(ctx, s.db, storedSecret); err == nil {
			clientSecret = string(plain)
		}
	}
	return clientID, clientSecret
}

// SaveOAuthClient persists the OAuth app credentials for platform/profileID
// so silent token refresh keeps working in any later process without a
// MONOAGENT_<PLATFORM>_CLIENT_ID env var. Called automatically after every
// successful OAuth connect with whatever client credentials actually worked.
func (s *Store) SaveOAuthClient(ctx context.Context, platform, profileID, clientID, clientSecret string) error {
	if profileID == "" {
		profileID = "default"
	}
	// Same schema as migration 013 — created here too because Store is also
	// used against databases initialized via EnsureTable alone (tests, embedders).
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS platform_oauth_credentials (
    platform      TEXT NOT NULL,
    profile_id    TEXT NOT NULL DEFAULT 'default',
    client_id     TEXT NOT NULL,
    client_secret TEXT NOT NULL DEFAULT '',
    updated_at    TEXT NOT NULL,
    PRIMARY KEY (platform, profile_id)
)`); err != nil {
		return fmt.Errorf("connections.SaveOAuthClient: ensure table: %w", err)
	}
	// Encrypt the client_secret under the vault envelope (same as connection
	// data); an empty secret (PKCE-only platforms) stays empty.
	encSecret := clientSecret
	if clientSecret != "" {
		enc, encErr := secrets.EncryptBlob(ctx, s.db, []byte(clientSecret))
		if encErr != nil {
			return fmt.Errorf("connections.SaveOAuthClient: encrypting secret: %w", encErr)
		}
		encSecret = enc
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO platform_oauth_credentials (platform, profile_id, client_id, client_secret, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		platform, profileID, clientID, encSecret, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("connections.SaveOAuthClient: %w", err)
	}
	return nil
}

// ListAll returns all connections ordered by platform then created_at.
func (s *Store) ListAll(ctx context.Context, profileID string) ([]Connection, error) {
	const q = `SELECT id, platform, method, label, account_id, data, status, last_tested, COALESCE(profile_id,'default'), COALESCE(vault_ref,''), created_at, updated_at
	           FROM connections WHERE COALESCE(profile_id,'default') = ? ORDER BY platform, created_at`
	rows, err := s.db.QueryContext(ctx, q, profileID)
	if err != nil {
		return nil, fmt.Errorf("connections.ListAll: %w", err)
	}
	defer rows.Close()
	out, err := scanConnections(ctx, s.db, rows)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []Connection{}
	}
	return out, nil
}

// ListByPlatform returns connections for a given platform. If profileID is non-empty,
// only that profile's connections are returned; empty profileID returns all profiles.
func (s *Store) ListByPlatform(ctx context.Context, platform, profileID string) ([]Connection, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if profileID == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, platform, method, label, account_id, data, status, last_tested, COALESCE(profile_id,'default'), COALESCE(vault_ref,''), created_at, updated_at
			 FROM connections WHERE platform = ? ORDER BY created_at`, platform)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, platform, method, label, account_id, data, status, last_tested, COALESCE(profile_id,'default'), COALESCE(vault_ref,''), created_at, updated_at
			 FROM connections WHERE platform = ? AND COALESCE(profile_id,'default') = ? ORDER BY created_at`,
			platform, profileID)
	}
	if err != nil {
		return nil, fmt.Errorf("connections.ListByPlatform: %w", err)
	}
	defer rows.Close()
	return scanConnections(ctx, s.db, rows)
}

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
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("connections.Delete: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("connections.Delete: id %q not found", id)
	}
	return nil
}

// MarkTested updates the status, last_tested, and updated_at fields for the
// given connection ID.
func (s *Store) MarkTested(ctx context.Context, id, status string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	const q = `UPDATE connections SET status = ?, last_tested = ?, updated_at = ? WHERE id = ?`
	res, err := s.db.ExecContext(ctx, q, status, now, now, id)
	if err != nil {
		return fmt.Errorf("connections.MarkTested: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("connections.MarkTested: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("connections.MarkTested: id %q not found", id)
	}
	return nil
}

// scanConnection reads a single Connection from a *sql.Row.
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

// scanConnections reads all Connection rows from a *sql.Rows result set. A
// per-row mergeVaultFields failure (e.g. a dangling vault_ref left by an
// out-of-band delete or corruption) is logged and that row is omitted,
// rather than aborting the whole scan — unlike scanConnection's single-row
// path, which does return the error, since a caller looking up one specific
// connection should see it fail clearly instead of silently disappearing.
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
		if err := mergeVaultFields(ctx, db, &c); err != nil {
			fmt.Fprintf(os.Stderr, "warning: connection %s: could not resolve vault credentials, omitting from list: %v\n", c.ID, err)
			continue
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scanConnections: rows: %w", err)
	}
	return out, nil
}
