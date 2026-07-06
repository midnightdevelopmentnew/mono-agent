package connections

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// newManagerDB opens an in-memory SQLite database and returns a Manager backed by it.
func newManagerDB(t *testing.T) (*Manager, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatalf("newManagerDB: open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	mgr, err := NewManager(db)
	if err != nil {
		t.Fatalf("newManagerDB: NewManager: %v", err)
	}
	return mgr, db
}

// TestManagerListEmpty verifies that List on an empty DB returns an empty (non-nil) slice.
func TestManagerListEmpty(t *testing.T) {
	mgr, _ := newManagerDB(t)
	ctx := context.Background()

	conns, err := mgr.List(ctx, "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if conns == nil {
		t.Fatal("List returned nil, expected empty slice")
	}
	if len(conns) != 0 {
		t.Errorf("List returned %d connections, expected 0", len(conns))
	}
}

// TestManagerRemoveNotFound verifies that Remove returns an error for a non-existent ID.
func TestManagerRemoveNotFound(t *testing.T) {
	mgr, _ := newManagerDB(t)
	ctx := context.Background()

	err := mgr.Remove(ctx, "nonexistent-id", "")
	if err == nil {
		t.Fatal("expected error when removing non-existent ID, got nil")
	}
}

// TestManagerConnectUnknownPlatform verifies that Connect returns an error for an unknown platform.
func TestManagerConnectUnknownPlatform(t *testing.T) {
	mgr, _ := newManagerDB(t)
	ctx := context.Background()

	_, err := mgr.Connect(ctx, "notaplatform", ConnectOptions{})
	if err == nil {
		t.Fatal("expected error when connecting to unknown platform, got nil")
	}
}

// TestManagerConnectSavesProfileID is a regression test: Connect previously never
// set ProfileID on the created Connection, so every connection made via
// `monoes connect` (regardless of the active profile) was silently saved under
// "default" and became invisible to other profiles.
func TestManagerConnectSavesProfileID(t *testing.T) {
	mgr, _ := newManagerDB(t)
	ctx := context.Background()

	conn, err := mgr.Connect(ctx, "postgresql", ConnectOptions{
		Method:      MethodConnStr,
		ProfileID:   "work",
		FieldValues: map[string]string{"connection_string": "postgres://u:p@localhost:5432/db"},
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if conn.ProfileID != "work" {
		t.Fatalf("ProfileID = %q, want %q", conn.ProfileID, "work")
	}

	saved, err := mgr.Get(ctx, conn.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if saved.ProfileID != "work" {
		t.Fatalf("saved ProfileID = %q, want %q", saved.ProfileID, "work")
	}
}
