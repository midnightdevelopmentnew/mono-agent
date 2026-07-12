package storage

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"monoagent/data"
)

// TestApplyMigrationsFreshDatabase is a regression test: migration
// 013_oauth_credentials_per_profile.sql referenced platform_oauth_credentials
// in a SELECT guarded by "WHERE EXISTS (SELECT 1 FROM sqlite_master ...)",
// but SQLite resolves table references in a statement at prepare time
// regardless of runtime WHERE conditions — so on a completely fresh database
// (one that never had that table), ApplyMigrations failed outright with
// "no such table: platform_oauth_credentials", blocking first-time setup
// entirely.
func TestApplyMigrationsFreshDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fresh.db")

	db, err := NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	defer db.DB.Close()

	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations on a fresh database: %v", err)
	}

	var name string
	err = db.DB.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'platform_oauth_credentials'`,
	).Scan(&name)
	if err != nil {
		t.Fatalf("expected platform_oauth_credentials table to exist after migration: %v", err)
	}
}

// applyMigrationsBelow applies every embedded migration with version < maxVer
// directly onto d (recording each in schema_migrations), so a later
// db.ApplyMigrations() only runs migrations from maxVer onward. This lets a
// test populate the pre-014 schema with real rows before migration 014 runs.
func applyMigrationsBelow(t *testing.T, d *Database, maxVer int) {
	t.Helper()

	if _, err := d.DB.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("creating schema_migrations: %v", err)
	}

	entries, err := data.MigrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("reading migrations dir: %v", err)
	}
	type mig struct {
		version  int
		filename string
	}
	var migs []mig
	for _, e := range entries {
		nm := e.Name()
		if strings.HasPrefix(nm, "._") || e.IsDir() || !strings.HasSuffix(nm, ".sql") {
			continue
		}
		parts := strings.SplitN(nm, "_", 2)
		ver, err := strconv.Atoi(parts[0])
		if err != nil || ver >= maxVer {
			continue
		}
		migs = append(migs, mig{version: ver, filename: nm})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })

	for _, m := range migs {
		content, err := data.MigrationsFS.ReadFile("migrations/" + m.filename)
		if err != nil {
			t.Fatalf("reading %s: %v", m.filename, err)
		}
		for _, stmt := range splitStatements(string(content)) {
			if strings.TrimSpace(stmt) == "" {
				continue
			}
			if _, err := d.DB.Exec(stmt); err != nil {
				t.Fatalf("applying %s: %v", m.filename, err)
			}
		}
		if _, err := d.DB.Exec("INSERT INTO schema_migrations (version) VALUES (?)", m.version); err != nil {
			t.Fatalf("recording %s: %v", m.filename, err)
		}
	}
}

// TestApplyMigration014PreservesChildRows is a regression test for the critical
// data-loss bug in migration 014: its table-rebuild (dropping the old people) fired an
// implicit DELETE that, with foreign_keys=ON (baked into the DSN), cascade-
// deleted person_messages/people_tags and failed outright on action_targets/
// posts NO-ACTION references. The migration runner now disables FK enforcement
// on a dedicated connection, so child rows must survive the rebuild.
func TestApplyMigration014PreservesChildRows(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "upgrade.db")

	db, err := NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	defer db.DB.Close()

	// Build the pre-014 schema.
	applyMigrationsBelow(t, db, 14)

	// Populate a person plus a child row in each referencing table.
	exec := func(query string, args ...interface{}) {
		if _, err := db.DB.Exec(query, args...); err != nil {
			t.Fatalf("seeding %q: %v", query, err)
		}
	}
	exec(`INSERT INTO people (id, platform_username, platform) VALUES ('p1', 'alice', 'x')`)
	exec(`INSERT INTO person_messages (id, person_id, source) VALUES ('m1', 'p1', 'gmail')`)
	exec(`INSERT INTO tags (id, name) VALUES ('t1', 'friend')`)
	exec(`INSERT INTO people_tags (person_id, tag_id) VALUES ('p1', 't1')`)
	exec(`INSERT INTO actions (id, created_at, title, type, target_platform) VALUES ('a1', 0, 'x', 'y', 'x')`)
	exec(`INSERT INTO action_targets (id, action_id, person_id, platform) VALUES ('at1', 'a1', 'p1', 'x')`)
	exec(`INSERT INTO posts (id, person_id, platform, shortcode, url, scraped_at) VALUES ('po1', 'p1', 'x', 'sc', 'u', '')`)

	// Run migration 014 (and any later ones).
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations (014 rebuild): %v", err)
	}

	count := func(query string) int {
		var n int
		if err := db.DB.QueryRow(query).Scan(&n); err != nil {
			t.Fatalf("counting %q: %v", query, err)
		}
		return n
	}
	if n := count(`SELECT COUNT(*) FROM people WHERE id = 'p1'`); n != 1 {
		t.Fatalf("person row lost: got %d, want 1", n)
	}
	if n := count(`SELECT COUNT(*) FROM person_messages WHERE person_id = 'p1'`); n != 1 {
		t.Fatalf("person_messages cascade-deleted by migration 014: got %d, want 1", n)
	}
	if n := count(`SELECT COUNT(*) FROM people_tags WHERE person_id = 'p1'`); n != 1 {
		t.Fatalf("people_tags cascade-deleted by migration 014: got %d, want 1", n)
	}
	if n := count(`SELECT COUNT(*) FROM action_targets WHERE person_id = 'p1'`); n != 1 {
		t.Fatalf("action_targets lost by migration 014: got %d, want 1", n)
	}
	if n := count(`SELECT COUNT(*) FROM posts WHERE person_id = 'p1'`); n != 1 {
		t.Fatalf("posts lost by migration 014: got %d, want 1", n)
	}
}
