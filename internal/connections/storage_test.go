package connections

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// newTestDB opens an in-memory SQLite database and ensures the connections
// table exists.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatalf("newTestDB: open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store := NewStore(db)
	if err := store.EnsureTable(context.Background()); err != nil {
		t.Fatalf("newTestDB: EnsureTable: %v", err)
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
