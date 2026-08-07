package connections

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	_ "modernc.org/sqlite"
)

// newTestDB opens an in-memory SQLite database and ensures the connections
// table exists, plus the vault_keys table that secrets.EncryptBlob/DecryptBlob
// (used by Save/scanConnection/scanConnections) needs to store the DEK.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	keyring.MockInit()
	// cache=shared: a plain ":memory:" DSN gives each pooled connection its
	// own separate database, which broke the moment scanConnections started
	// issuing a nested query (secrets.DecryptBlob's vault_keys lookup) while
	// the outer *sql.Rows was still open — that nested query needs a second
	// connection to see the same in-memory database as the first. The DSN is
	// keyed by test name so distinct tests don't share the same underlying
	// shared-cache database.
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=foreign_keys(ON)", t.Name())
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("newTestDB: open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store := NewStore(db)
	if err := store.EnsureTable(context.Background()); err != nil {
		t.Fatalf("newTestDB: EnsureTable: %v", err)
	}
	const createVaultKeysTable = `
CREATE TABLE IF NOT EXISTS vault_keys (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    wrapped_dek   BLOB NOT NULL,
    wrapped_nonce BLOB NOT NULL,
    created_at    TEXT NOT NULL
);`
	if _, err := db.Exec(createVaultKeysTable); err != nil {
		t.Fatalf("newTestDB: create vault_keys: %v", err)
	}
	const createVaultSecretsTable = `
CREATE TABLE IF NOT EXISTS vault_secrets (
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
    updated_at       TEXT NOT NULL,
    kv               INTEGER NOT NULL DEFAULT 0,
    field_count      INTEGER NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_vault_secrets_profile_name ON vault_secrets(profile_id, name);`
	if _, err := db.Exec(createVaultSecretsTable); err != nil {
		t.Fatalf("newTestDB: create vault_secrets: %v", err)
	}
	return db
}

// TestStoreSaveAndGet verifies that a connection saved with an empty ID gets
// an auto-generated UUID, and that Get retrieves the same Label and Data.
func TestStoreSaveAndGet(t *testing.T) {
	db := newTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	conn := &Connection{
		Platform: "github",
		Method:   MethodAPIKey,
		Label:    "my github token",
		Data:     map[string]interface{}{"token": "ghp_test123"},
	}

	if err := store.Save(ctx, conn); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if conn.ID == "" {
		t.Fatal("Save did not assign an ID")
	}

	got, err := store.Get(ctx, conn.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil for an existing connection")
	}
	if got.Label != conn.Label {
		t.Errorf("Label: got %q, want %q", got.Label, conn.Label)
	}
	if got.Data["token"] != "ghp_test123" {
		t.Errorf("Data[token]: got %v, want %q", got.Data["token"], "ghp_test123")
	}
}

// TestStoreDelete verifies that after deleting a connection, Get returns nil.
func TestStoreDelete(t *testing.T) {
	db := newTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	conn := &Connection{
		Platform: "stripe",
		Method:   MethodAPIKey,
		Label:    "stripe test key",
		Data:     map[string]interface{}{"secret_key": "sk_test_abc"},
	}

	if err := store.Save(ctx, conn); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := store.Delete(ctx, conn.ID, ""); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, err := store.Get(ctx, conn.ID)
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if got != nil {
		t.Errorf("Get after delete: expected nil, got %+v", got)
	}
}

// TestStoreListByPlatform verifies that ListByPlatform filters correctly.
func TestStoreListByPlatform(t *testing.T) {
	db := newTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	conns := []*Connection{
		{Platform: "github", Method: MethodAPIKey, Label: "github work", Data: map[string]interface{}{}},
		{Platform: "github", Method: MethodOAuth, Label: "github personal", Data: map[string]interface{}{}},
		{Platform: "stripe", Method: MethodAPIKey, Label: "stripe prod", Data: map[string]interface{}{}},
	}
	for _, c := range conns {
		if err := store.Save(ctx, c); err != nil {
			t.Fatalf("Save %q: %v", c.Label, err)
		}
	}

	results, err := store.ListByPlatform(ctx, "github", "")
	if err != nil {
		t.Fatalf("ListByPlatform: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("ListByPlatform(\"github\"): got %d results, want 2", len(results))
	}
	for _, r := range results {
		if r.Platform != "github" {
			t.Errorf("unexpected platform %q in results", r.Platform)
		}
	}
}

// TestStoreMarkTested verifies that MarkTested updates status and last_tested,
// and returns an error for an unknown ID.
func TestStoreMarkTested(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	conn := &Connection{Platform: "github", Method: MethodAPIKey, Label: "G", Data: map[string]interface{}{}}
	_ = s.Save(ctx, conn)

	if err := s.MarkTested(ctx, conn.ID, "error"); err != nil {
		t.Fatalf("MarkTested: %v", err)
	}
	got, _ := s.Get(ctx, conn.ID)
	if got.Status != "error" {
		t.Errorf("expected status 'error', got %q", got.Status)
	}
	if got.LastTested == "" {
		t.Error("expected LastTested to be set")
	}

	// MarkTested on unknown ID should error
	if err := s.MarkTested(ctx, "nonexistent", "active"); err == nil {
		t.Error("expected error for unknown ID")
	}
}

// TestConnectionRedactStripsCredentialData is a regression test: `monoagentcli
// connect list --json` and the Wails GUI's ListConnections/GetConnectionsForPlatform
// previously serialized the full Connection struct, leaking access_token/
// refresh_token/api_key in cleartext via Data. Redact/RedactAll must produce
// output with no trace of Data while preserving every other field.
func TestConnectionRedactStripsCredentialData(t *testing.T) {
	conn := Connection{
		ID:       "conn-1",
		Platform: "github",
		Method:   MethodOAuth,
		Label:    "GitHub – octocat",
		Data: map[string]interface{}{
			"access_token":  "ghp_supersecrettoken",
			"refresh_token": "ghr_supersecretrefresh",
		},
		Status:     "active",
		LastTested: "2026-01-01T00:00:00Z",
		ProfileID:  "work",
		CreatedAt:  "2026-01-01T00:00:00Z",
		UpdatedAt:  "2026-01-01T00:00:00Z",
	}

	safe := conn.Redact()

	b, err := json.Marshal(safe)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	out := string(b)
	if strings.Contains(out, "supersecret") {
		t.Fatalf("Redact() leaked credential material into JSON output: %s", out)
	}
	if strings.Contains(out, "\"data\"") {
		t.Fatalf("Redact() output still contains a data field: %s", out)
	}

	// Non-credential fields must survive.
	if safe.ID != conn.ID || safe.Platform != conn.Platform || safe.Label != conn.Label ||
		safe.AccountID != conn.AccountID || safe.Status != conn.Status ||
		safe.LastTested != conn.LastTested || safe.ProfileID != conn.ProfileID {
		t.Fatalf("Redact() dropped a non-credential field: %+v", safe)
	}

	list := RedactAll([]Connection{conn, conn})
	if len(list) != 2 {
		t.Fatalf("RedactAll: got %d entries, want 2", len(list))
	}
	b2, _ := json.Marshal(list)
	if strings.Contains(string(b2), "supersecret") {
		t.Fatalf("RedactAll() leaked credential material into JSON output: %s", b2)
	}
}

// TestOAuthClientPersistRoundTrip verifies that the OAuth app credentials a
// successful connect persists (SaveOAuthClient) are what the silent-refresh
// path reads back (GetOAuthClient), scoped per profile — the missing link
// behind "connect refresh: missing ClientID" an hour after a working login.
func TestOAuthClientPersistRoundTrip(t *testing.T) {
	db := newTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Nothing stored yet.
	if id, _ := store.GetOAuthClient(ctx, "outlook", "default"); id != "" {
		t.Fatalf("GetOAuthClient on empty store: got %q, want empty", id)
	}

	if err := store.SaveOAuthClient(ctx, "outlook", "", "client-default", "sec1"); err != nil {
		t.Fatalf("SaveOAuthClient: %v", err)
	}
	if err := store.SaveOAuthClient(ctx, "outlook", "work", "client-work", ""); err != nil {
		t.Fatalf("SaveOAuthClient(work): %v", err)
	}

	// Empty profileID normalizes to "default" on both read and write.
	if id, sec := store.GetOAuthClient(ctx, "outlook", ""); id != "client-default" || sec != "sec1" {
		t.Fatalf("GetOAuthClient(default): got %q/%q", id, sec)
	}
	if id, _ := store.GetOAuthClient(ctx, "outlook", "work"); id != "client-work" {
		t.Fatalf("GetOAuthClient(work): got %q", id)
	}

	// Re-connect with a different app registration overwrites in place.
	if err := store.SaveOAuthClient(ctx, "outlook", "work", "client-work2", "s"); err != nil {
		t.Fatalf("SaveOAuthClient(overwrite): %v", err)
	}
	if id, _ := store.GetOAuthClient(ctx, "outlook", "work"); id != "client-work2" {
		t.Fatalf("GetOAuthClient after overwrite: got %q", id)
	}
}


// TestRefreshTokenSerializesAcrossStores verifies the fix for the
// cross-process refresh race: two Store instances sharing one DB (standing
// in for two OS processes — the CLI and a daemon, say — resolving the same
// stale connection at once) must never both exchange the SAME stored
// refresh_token. A provider that rotates single-use refresh tokens rejects
// the second use of an already-consumed one and would otherwise leave one
// caller with a dead token, permanently breaking the connection. It's fine
// for the loser to perform its own subsequent exchange once it acquires the
// lock (it re-reads first, so it uses the winner's already-rotated token,
// which is a legitimate independent refresh) — what must never happen is
// two exchanges submitting the identical refresh_token value.
func TestRefreshTokenSerializesAcrossStores(t *testing.T) {
	var mu sync.Mutex
	var submitted []string
	var exchanges int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&exchanges, 1)
		_ = r.ParseForm()
		rt := r.FormValue("refresh_token")

		mu.Lock()
		submitted = append(submitted, rt)
		mu.Unlock()

		// Hold the "network round trip" open long enough that a concurrent
		// caller reliably arrives while this one still holds the lock.
		time.Sleep(150 * time.Millisecond)

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"access-%d","refresh_token":"refresh-%d","expires_in":3600}`, n, n)
	}))
	defer srv.Close()

	const platformID = "test-refresh-race-platform"
	Registry[platformID] = PlatformDef{
		ID:      platformID,
		Name:    "Test Refresh Race Platform",
		Methods: []AuthMethod{MethodOAuth},
		OAuth:   &OAuthConfig{TokenURL: srv.URL, ClientID: "client", ClientSecret: "secret"},
	}
	t.Cleanup(func() { delete(Registry, platformID) })

	db := newTestDB(t)
	storeA := NewStore(db) // stands in for process A (e.g. the CLI)
	storeB := NewStore(db) // stands in for process B (e.g. a daemon)

	conn := &Connection{
		Platform: platformID,
		Method:   MethodOAuth,
		Data:     map[string]interface{}{"refresh_token": "old-refresh", "access_token": "old-access"},
	}
	if err := storeA.Save(context.Background(), conn); err != nil {
		t.Fatalf("Save: %v", err)
	}

	connA, err := storeA.Get(context.Background(), conn.ID)
	if err != nil || connA == nil {
		t.Fatalf("Get connA: %v", err)
	}
	connB, err := storeB.Get(context.Background(), conn.ID)
	if err != nil || connB == nil {
		t.Fatalf("Get connB: %v", err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs[0] = storeA.RefreshToken(context.Background(), connA)
	}()
	go func() {
		defer wg.Done()
		time.Sleep(20 * time.Millisecond) // let A acquire the lock first
		errs[1] = storeB.RefreshToken(context.Background(), connB)
	}()
	wg.Wait()

	if errs[0] != nil {
		t.Fatalf("storeA.RefreshToken: %v", errs[0])
	}
	if errs[1] != nil {
		t.Fatalf("storeB.RefreshToken: %v", errs[1])
	}

	// The invariant the lock protects: no submitted refresh_token value is
	// ever reused. Whether 1 or 2 exchanges happened depends on scheduling
	// (the loser may acquire the lock after the winner releases and perform
	// its own legitimate follow-up refresh using the now-rotated token), but
	// a repeated value means both callers read the old token before either
	// wrote back — exactly the pre-fix race.
	mu.Lock()
	defer mu.Unlock()
	if len(submitted) < 1 {
		t.Fatalf("token endpoint was never called")
	}
	seen := map[string]bool{}
	for _, rt := range submitted {
		if seen[rt] {
			t.Fatalf("refresh_token %q was submitted more than once — the race was not serialized: %v", rt, submitted)
		}
		seen[rt] = true
	}

	final, err := storeA.Get(context.Background(), conn.ID)
	if err != nil || final == nil {
		t.Fatalf("Get final: %v", err)
	}
	lastAccess := fmt.Sprintf("access-%d", len(submitted))
	if final.Data["access_token"] != lastAccess {
		t.Fatalf("final access_token = %v, want %v (the last exchange's result, not clobbered by an earlier writer)", final.Data["access_token"], lastAccess)
	}
}

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
