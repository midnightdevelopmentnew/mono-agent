package storage

import (
	"path/filepath"
	"testing"
	"time"
)

// newStatusTestDB returns a fully-migrated Database backed by a temp file,
// seeded with person "p1" under profile "default" and person "p2" under
// profile "work" — enough to exercise both the happy path and the
// profile-scoping guard.
func newStatusTestDB(t *testing.T) *Database {
	t.Helper()
	db, err := NewDatabase(filepath.Join(t.TempDir(), "status.db"))
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	t.Cleanup(func() { db.DB.Close() })
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	exec := func(query string, args ...interface{}) {
		if _, err := db.DB.Exec(query, args...); err != nil {
			t.Fatalf("seeding %q: %v", query, err)
		}
	}
	exec(`INSERT INTO people (id, platform_username, platform, profile_id) VALUES ('p1', 'alice', 'x', 'default')`)
	exec(`INSERT INTO people (id, platform_username, platform, profile_id) VALUES ('p2', 'bob', 'x', 'work')`)
	return db
}

func TestAddPersonStatusUpdate(t *testing.T) {
	db := newStatusTestDB(t)

	u, err := db.AddPersonStatusUpdate("p1", "default", "  Just closed the Q1 deal  ")
	if err != nil {
		t.Fatalf("AddPersonStatusUpdate: %v", err)
	}
	if u.Text != "Just closed the Q1 deal" {
		t.Errorf("text not trimmed: got %q", u.Text)
	}
	if u.ID == "" {
		t.Error("expected a generated ID")
	}
	if u.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be set")
	}
}

func TestAddPersonStatusUpdateRejectsEmpty(t *testing.T) {
	db := newStatusTestDB(t)

	if _, err := db.AddPersonStatusUpdate("p1", "default", "   "); err == nil {
		t.Error("expected an error for empty/whitespace-only text")
	}
}

func TestAddPersonStatusUpdateRejectsWrongProfile(t *testing.T) {
	db := newStatusTestDB(t)

	// p1 belongs to "default", not "work" — adding under "work" must fail,
	// not silently attach the status to the wrong profile's person.
	if _, err := db.AddPersonStatusUpdate("p1", "work", "hello"); err == nil {
		t.Error("expected an error when person does not belong to the given profile")
	}
}

func TestListAndGetLatestPersonStatusUpdates(t *testing.T) {
	db := newStatusTestDB(t)

	// No status yet: nil, no error.
	u, err := db.GetLatestPersonStatusUpdate("p1", "default")
	if err != nil {
		t.Fatalf("GetLatestPersonStatusUpdate (none yet): %v", err)
	}
	if u != nil {
		t.Fatalf("expected nil when no status exists, got %+v", u)
	}

	// Insert three updates for p1 with explicit, unambiguous timestamps so
	// ordering doesn't depend on wall-clock timing between statements.
	insertAt := func(id, personID, profileID, text string, ts time.Time) {
		if _, err := db.DB.Exec(
			`INSERT INTO person_status_updates (id, person_id, profile_id, text, created_at) VALUES (?,?,?,?,?)`,
			id, personID, profileID, text, ts,
		); err != nil {
			t.Fatalf("seeding status update: %v", err)
		}
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	insertAt("s1", "p1", "default", "first", base)
	insertAt("s2", "p1", "default", "second", base.Add(time.Hour))
	insertAt("s3", "p1", "default", "third", base.Add(2*time.Hour))
	insertAt("s4", "p2", "work", "unrelated", base) // different person/profile

	updates, err := db.ListPersonStatusUpdates("p1", "default", 0)
	if err != nil {
		t.Fatalf("ListPersonStatusUpdates: %v", err)
	}
	if len(updates) != 3 {
		t.Fatalf("got %d updates, want 3", len(updates))
	}
	if updates[0].Text != "third" || updates[1].Text != "second" || updates[2].Text != "first" {
		t.Fatalf("expected newest-first order, got %q, %q, %q", updates[0].Text, updates[1].Text, updates[2].Text)
	}

	latest, err := db.GetLatestPersonStatusUpdate("p1", "default")
	if err != nil {
		t.Fatalf("GetLatestPersonStatusUpdate: %v", err)
	}
	if latest == nil || latest.Text != "third" {
		t.Fatalf("expected latest to be %q, got %+v", "third", latest)
	}

	limited, err := db.ListPersonStatusUpdates("p1", "default", 2)
	if err != nil {
		t.Fatalf("ListPersonStatusUpdates(limit=2): %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("got %d updates with limit=2, want 2", len(limited))
	}

	// Cross-profile scoping: reading p1's history "as work" (the wrong
	// profile for p1) must return nothing, not s1-s3.
	wrongProfile, err := db.ListPersonStatusUpdates("p1", "work", 0)
	if err != nil {
		t.Fatalf("ListPersonStatusUpdates (wrong profile): %v", err)
	}
	if len(wrongProfile) != 0 {
		t.Fatalf("expected 0 updates when profile doesn't match person's profile, got %d", len(wrongProfile))
	}
}
