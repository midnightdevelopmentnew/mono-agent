# Person Status Updates Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a manually-written, append-only "status update" per person — a quote-box on the GUI Profile page showing the latest one with a history modal, plus first-class `people status set|get|history` CLI commands, both backed by the same shared `internal/storage` repository functions.

**Architecture:** One new SQLite table (`person_status_updates`, profile-scoped) plus three repository functions in `internal/storage`. `cmd/monoagentcli` and `wails-app/app.go` both call those same functions directly (no duplicated SQL) — the established pattern in this codebase for `PersonMessage`. The frontend adds a quote-box section to `Profile.jsx` and a read-only history modal modeled on the existing `MessageDetailModal.jsx`.

**Tech Stack:** Go 1.25, `modernc.org/sqlite`, Cobra CLI, Wails v2, React (no test framework on the frontend — verified by `vite build` + manual run).

## Global Constraints

- Append-only: no edit or delete of a status-update entry (explicit user decision — YAGNI).
- No character-length cap on the status text (matches `people.introduction` / `person_messages.body`, neither of which are capped).
- Every read/write is scoped to the active profile — a status update belonging to one profile's person must be invisible to and unwritable from another profile (the exact class of bug fixed for `person_messages`/`crawler_sessions`/`social_list_items` in the 2026-07-12 full-app review).
- `cmd/monoagentcli` and `wails-app` must call the same `internal/storage` functions — no SQL duplicated in the GUI layer (per this repo's CLI-first / no-duplicated-logic convention).
- Follow existing code style exactly: repository functions in `internal/storage/repository.go` do not take `context.Context` (none of the neighboring `PersonMessage` functions do — match the file's existing convention, don't introduce a new one).

---

### Task 1: Data model + repository layer

**Files:**
- Create: `data/migrations/016_person_status_updates.sql`
- Modify: `internal/storage/models.go` (add `PersonStatusUpdate` struct, after the `PersonMessage` struct)
- Modify: `internal/storage/repository.go` (add three functions, inserted between `DeletePersonMessage` and the `// Social Lists` section header)
- Create: `internal/storage/person_status_test.go`

**Interfaces:**
- Produces (used by Tasks 2 and 3):
  - `type PersonStatusUpdate struct { ID, PersonID, Text string; CreatedAt time.Time }` with JSON tags `id`, `person_id`, `text`, `created_at`.
  - `func (d *Database) AddPersonStatusUpdate(personID, profileID, text string) (*PersonStatusUpdate, error)`
  - `func (d *Database) GetLatestPersonStatusUpdate(personID, profileID string) (*PersonStatusUpdate, error)` — returns `(nil, nil)` when none exist.
  - `func (d *Database) ListPersonStatusUpdates(personID, profileID string, limit int) ([]*PersonStatusUpdate, error)` — newest first; `limit <= 0` means no cap.

- [ ] **Step 1: Write the migration**

Create `data/migrations/016_person_status_updates.sql`:

```sql
-- 016_person_status_updates.sql
-- Append-only log of manually-written status updates for a person (e.g.
-- "Just closed the Q1 deal") -- a personal-CRM-style note, unrelated to
-- person_messages.status (draft/sent/failed for outbound messages).
-- profile_id is denormalized onto the row (not just inherited via a join)
-- to match the tags/people_tags precedent, and to keep the write-path
-- scoping check (AddPersonStatusUpdate) independent of read-path joins.

CREATE TABLE IF NOT EXISTS person_status_updates (
    id         TEXT PRIMARY KEY,
    person_id  TEXT NOT NULL REFERENCES people(id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL DEFAULT 'default',
    text       TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_status_updates_person ON person_status_updates(person_id, created_at DESC);
```

- [ ] **Step 2: Add the `PersonStatusUpdate` model**

In `internal/storage/models.go`, find this exact block:

```go
	Status     string    `json:"status,omitempty"` // draft | sent | failed; meaningful for outbound compose messages
	SentAt     time.Time `json:"sent_at,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// SocialList represents a row in the social_lists table.
```

Replace with (adding the new struct between the two):

```go
	Status     string    `json:"status,omitempty"` // draft | sent | failed; meaningful for outbound compose messages
	SentAt     time.Time `json:"sent_at,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// PersonStatusUpdate represents a single manually-written status/note entry
// about a person — a personal-CRM-style update (e.g. "Just closed the Q1
// deal"), entirely unrelated to PersonMessage.Status (which tracks
// draft/sent/failed for outbound messages). Append-only: no edit, no delete.
type PersonStatusUpdate struct {
	ID        string    `json:"id"`
	PersonID  string    `json:"person_id"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

// SocialList represents a row in the social_lists table.
```

- [ ] **Step 3: Add the repository functions**

In `internal/storage/repository.go`, find this exact block:

```go
// DeletePersonMessage removes a single message by ID.
func (d *Database) DeletePersonMessage(id string) error {
	result, err := d.DB.Exec("DELETE FROM person_messages WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("deleting person message %s: %w", id, err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("message %s not found", id)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Social Lists
// ---------------------------------------------------------------------------
```

Replace it with (adding the new section between the two):

```go
// DeletePersonMessage removes a single message by ID.
func (d *Database) DeletePersonMessage(id string) error {
	result, err := d.DB.Exec("DELETE FROM person_messages WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("deleting person message %s: %w", id, err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("message %s not found", id)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Person Status Updates
// ---------------------------------------------------------------------------

// AddPersonStatusUpdate appends a new status-update entry for a person — a
// manually-written personal note, unrelated to PersonMessage.Status.
// profileID scopes the write; the update is rejected if personID does not
// belong to that profile.
func (d *Database) AddPersonStatusUpdate(personID, profileID, text string) (*PersonStatusUpdate, error) {
	if profileID == "" {
		profileID = "default"
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("status text must not be empty")
	}

	var exists int
	err := d.DB.QueryRow(`SELECT 1 FROM people WHERE id = ? AND COALESCE(profile_id,'default') = ?`, personID, profileID).Scan(&exists)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("person %s not found", personID)
	}
	if err != nil {
		return nil, fmt.Errorf("checking person %s: %w", personID, err)
	}

	u := &PersonStatusUpdate{
		ID:        NewID(),
		PersonID:  personID,
		Text:      text,
		CreatedAt: time.Now().UTC(),
	}
	_, err = d.DB.Exec(
		`INSERT INTO person_status_updates (id, person_id, profile_id, text, created_at) VALUES (?,?,?,?,?)`,
		u.ID, u.PersonID, profileID, u.Text, u.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("saving status update for person %s: %w", personID, err)
	}
	return u, nil
}

// GetLatestPersonStatusUpdate returns the most recent status update for a
// person within profileID, or (nil, nil) if none exists yet.
func (d *Database) GetLatestPersonStatusUpdate(personID, profileID string) (*PersonStatusUpdate, error) {
	if profileID == "" {
		profileID = "default"
	}
	u := &PersonStatusUpdate{}
	err := d.DB.QueryRow(`
		SELECT su.id, su.person_id, su.text, su.created_at
		FROM person_status_updates su
		JOIN people p ON p.id = su.person_id
		WHERE su.person_id = ? AND COALESCE(p.profile_id,'default') = ?
		ORDER BY su.created_at DESC LIMIT 1`, personID, profileID,
	).Scan(&u.ID, &u.PersonID, &u.Text, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting latest status update for person %s: %w", personID, err)
	}
	return u, nil
}

// ListPersonStatusUpdates returns every status update for a person within
// profileID, newest first. limit <= 0 means no cap.
func (d *Database) ListPersonStatusUpdates(personID, profileID string, limit int) ([]*PersonStatusUpdate, error) {
	if profileID == "" {
		profileID = "default"
	}
	query := `
		SELECT su.id, su.person_id, su.text, su.created_at
		FROM person_status_updates su
		JOIN people p ON p.id = su.person_id
		WHERE su.person_id = ? AND COALESCE(p.profile_id,'default') = ?
		ORDER BY su.created_at DESC`
	args := []interface{}{personID, profileID}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := d.DB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing status updates for person %s: %w", personID, err)
	}
	defer rows.Close()

	var updates []*PersonStatusUpdate
	for rows.Next() {
		u := &PersonStatusUpdate{}
		if err := rows.Scan(&u.ID, &u.PersonID, &u.Text, &u.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning status update row: %w", err)
		}
		updates = append(updates, u)
	}
	return updates, rows.Err()
}

// ---------------------------------------------------------------------------
// Social Lists
// ---------------------------------------------------------------------------
```

- [ ] **Step 4: Write the failing tests**

Create `internal/storage/person_status_test.go`:

```go
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
```

- [ ] **Step 5: Run the tests to verify they fail first (functions don't exist yet if Step 3 was skipped) — then apply Steps 1-3 and re-run**

Run: `cd /Volumes/media/projects/monoes/mono-agent && go test ./internal/storage/... -run PersonStatus -v`
Expected after Steps 1-3 are in place: all 5 tests `PASS`, e.g.:
```
--- PASS: TestAddPersonStatusUpdate
--- PASS: TestAddPersonStatusUpdateRejectsEmpty
--- PASS: TestAddPersonStatusUpdateRejectsWrongProfile
--- PASS: TestListAndGetLatestPersonStatusUpdates
PASS
ok  	monoagent/internal/storage
```

- [ ] **Step 6: Run the full package build/vet/test to make sure nothing else broke**

Run: `cd /Volumes/media/projects/monoes/mono-agent && go build ./internal/storage/... && go vet ./internal/storage/... && go test ./internal/storage/...`
Expected: `ok  	monoagent/internal/storage`

- [ ] **Step 7: Commit**

```bash
cd /Volumes/media/projects/monoes/mono-agent
git add data/migrations/016_person_status_updates.sql internal/storage/models.go internal/storage/repository.go internal/storage/person_status_test.go
git commit -m "feat: add person status updates data model and repository layer

Co-Authored-By: nokhodian <nokhodian@gmail.com>"
```

---

### Task 2: CLI commands (`people status set|get|history`)

**Files:**
- Create: `cmd/monoagentcli/people_status.go`
- Modify: `cmd/monoagentcli/people.go` (register the new subcommand)
- Modify: `README.md` (CLI examples)

**Interfaces:**
- Consumes: `storage.PersonStatusUpdate`, `db.AddPersonStatusUpdate(personID, profileID, text)`, `db.GetLatestPersonStatusUpdate(personID, profileID)`, `db.ListPersonStatusUpdates(personID, profileID, limit)` from Task 1; `initDB(cfg)`, `globalConfig`, `truncateStr` already in `cmd/monoagentcli`.
- Produces: `newPeopleStatusCmd(cfg *globalConfig) *cobra.Command`, consumed by Task 2's own registration step (no other task depends on this).

- [ ] **Step 1: Write the CLI command file**

Create `cmd/monoagentcli/people_status.go`:

```go
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

// newPeopleStatusCmd returns the `people status` subcommand group: a
// manually-written, freeform status update per person (e.g. "Just closed the
// Q1 deal") — an append-only personal-CRM-style log, entirely unrelated to
// the draft/sent/failed status tracked by `people messages`.
func newPeopleStatusCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Manage a person's status updates (freeform notes, e.g. \"Just closed the deal\")",
		Long:  "Post and read manually-written status updates for a person — an append-only personal log, unrelated to message send/draft status.",
	}

	cmd.AddCommand(
		newPeopleStatusSetCmd(cfg),
		newPeopleStatusGetCmd(cfg),
		newPeopleStatusHistoryCmd(cfg),
	)

	return cmd
}

func newPeopleStatusSetCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:     "set <person-id> <text>",
		Short:   "Post a new status update for a person",
		Args:    cobra.ExactArgs(2),
		Example: `  monoagentcli people status set abc123 "Just closed the Q1 deal"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			update, err := db.AddPersonStatusUpdate(args[0], cfg.ProfileID, args[1])
			if err != nil {
				return fmt.Errorf("saving status update: %w", err)
			}

			if cfg.JSONOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(update)
			}
			fmt.Fprintf(os.Stdout, "Posted status update %s for person %s.\n", update.ID, update.PersonID)
			return nil
		},
	}
}

func newPeopleStatusGetCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:     "get <person-id>",
		Short:   "Show a person's latest status update",
		Args:    cobra.ExactArgs(1),
		Example: `  monoagentcli people status get abc123`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			update, err := db.GetLatestPersonStatusUpdate(args[0], cfg.ProfileID)
			if err != nil {
				return fmt.Errorf("getting status update: %w", err)
			}

			if cfg.JSONOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(update)
			}
			if update == nil {
				fmt.Println("No status set for this person.")
				return nil
			}
			fmt.Fprintf(os.Stdout, "%s\n(%s)\n", update.Text, update.CreatedAt.Format("2006-01-02 15:04:05"))
			return nil
		},
	}
}

func newPeopleStatusHistoryCmd(cfg *globalConfig) *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "history <person-id>",
		Short: "List every status update ever posted for a person, newest first",
		Args:  cobra.ExactArgs(1),
		Example: `  monoagentcli people status history abc123
  monoagentcli people status history abc123 --limit 10 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			updates, err := db.ListPersonStatusUpdates(args[0], cfg.ProfileID, limit)
			if err != nil {
				return fmt.Errorf("listing status updates: %w", err)
			}

			if cfg.JSONOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(updates)
			}

			if len(updates) == 0 {
				fmt.Println("No status updates found.")
				return nil
			}

			table := tablewriter.NewWriter(os.Stdout)
			table.SetHeader([]string{"ID", "Text", "Posted At"})
			table.SetBorder(false)
			table.SetAutoWrapText(false)
			for _, u := range updates {
				shortID := u.ID
				if len(shortID) > 8 {
					shortID = shortID[:8]
				}
				table.Append([]string{shortID, truncateStr(u.Text, 60), u.CreatedAt.Format("2006-01-02 15:04:05")})
			}
			table.Render()
			fmt.Fprintf(os.Stderr, "\nTotal: %d update(s)\n", len(updates))
			return nil
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of results (0 = unlimited)")

	return cmd
}
```

- [ ] **Step 2: Register the subcommand**

In `cmd/monoagentcli/people.go`, find:

```go
	cmd.AddCommand(
		newPeopleListCmd(cfg),
		newPeopleGetCmd(cfg),
		newPeopleDeleteCmd(cfg),
		newPeopleImportCmd(cfg),
		newPeopleMessagesCmd(cfg),
	)
```

Replace with:

```go
	cmd.AddCommand(
		newPeopleListCmd(cfg),
		newPeopleGetCmd(cfg),
		newPeopleDeleteCmd(cfg),
		newPeopleImportCmd(cfg),
		newPeopleMessagesCmd(cfg),
		newPeopleStatusCmd(cfg),
	)
```

- [ ] **Step 3: Build the CLI**

Run: `cd /Volumes/media/projects/monoes/mono-agent && go build -o /tmp/monoagentcli-status-test ./cmd/monoagentcli`
Expected: exits 0, no output.

- [ ] **Step 4: Manually verify the commands end-to-end against a scratch DB**

There is no existing test harness for `cmd/monoagentcli` cobra commands in this repo (zero `*_test.go` files under that package) — introducing one from scratch for a single feature is out of proportion, so this task is verified by actually running the built binary, not an automated test.

Run:

```bash
rm -f /tmp/status-test.db
sqlite3 /tmp/status-test.db "CREATE TABLE IF NOT EXISTS people (id TEXT PRIMARY KEY, platform_username TEXT, platform TEXT, profile_id TEXT DEFAULT 'default'); INSERT INTO people (id, platform_username, platform) VALUES ('p1','alice','x');"
/tmp/monoagentcli-status-test --db-path /tmp/status-test.db people status get p1
```
Expected: `No status set for this person.` (the CLI's own `ApplyMigrations` call creates every other table, including `person_status_updates`, on first connect — only `people` was seeded by hand above since a real `people` row normally comes from a search/import action).

```bash
/tmp/monoagentcli-status-test --db-path /tmp/status-test.db people status set p1 "Just closed the Q1 deal"
/tmp/monoagentcli-status-test --db-path /tmp/status-test.db people status get p1
```
Expected: `Posted status update <uuid> for person p1.` then `Just closed the Q1 deal` followed by a timestamp line.

```bash
/tmp/monoagentcli-status-test --db-path /tmp/status-test.db people status set p1 "Following up next week"
/tmp/monoagentcli-status-test --db-path /tmp/status-test.db people status history p1
```
Expected: a table with 2 rows, "Following up next week" listed above "Just closed the Q1 deal" (newest first).

```bash
/tmp/monoagentcli-status-test --db-path /tmp/status-test.db people status history p1 --json
```
Expected: a JSON array of 2 objects with `id`, `person_id`, `text`, `created_at` fields.

```bash
rm -f /tmp/status-test.db /tmp/monoagentcli-status-test
```

- [ ] **Step 5: Add CLI examples to README**

In `README.md`, find:

```
monoes people messages import <id> --file thread.json --source gmail          # Bulk-import history
```

Replace with:

```
monoes people messages import <id> --file thread.json --source gmail          # Bulk-import history
monoes people status set <id> "Just closed the Q1 deal"    # Post a status update
monoes people status get <id>                               # Show the latest status
monoes people status history <id>                            # Show every status ever posted
```

- [ ] **Step 6: Commit**

```bash
cd /Volumes/media/projects/monoes/mono-agent
git add cmd/monoagentcli/people_status.go cmd/monoagentcli/people.go README.md
git commit -m "feat: add people status set/get/history CLI commands

Co-Authored-By: nokhodian <nokhodian@gmail.com>"
```

---

### Task 3: Wails App methods + regenerate bindings

**Files:**
- Modify: `wails-app/app.go` (add three methods, after `RejectDraftPersonMessage`)
- Regenerate: `wails-app/frontend/src/wailsjs/go/main/App.js`, `wails-app/frontend/src/wailsjs/go/main/App.d.ts` (generated files, git-tracked in this repo — regenerated via `wails generate module`, not hand-edited)

**Interfaces:**
- Consumes: `storage.PersonStatusUpdate`, `db.AddPersonStatusUpdate`, `db.GetLatestPersonStatusUpdate`, `db.ListPersonStatusUpdates` from Task 1; `a.db *sql.DB`, `a.activeProfileID string` already on the `App` struct.
- Produces (consumed by Task 4): generated JS bindings `GoApp.GetLatestPersonStatus(personId)`, `GoApp.AddPersonStatus(personId, text)`, `GoApp.GetPersonStatusHistory(personId, limit)`.

- [ ] **Step 1: Add the App methods**

In `wails-app/app.go`, find this exact block:

```go
	if connectionID, err := draftMessageConnectionID(msg); err == nil && msg.ExternalID != "" {
		a.RunNode(NodeRunRequest{
			NodeType: "service.outlook_mail",
			Config: map[string]interface{}{
				"credential_id": connectionID,
				"operation":     "delete_message",
				"message_id":    msg.ExternalID,
			},
		})
	}
	return db.DeletePersonMessage(personMessageID)
}

// GetPersonPosts returns all scraped posts for a person, with we_liked/we_commented flags.
```

Replace with (adding the new methods between the two):

```go
	if connectionID, err := draftMessageConnectionID(msg); err == nil && msg.ExternalID != "" {
		a.RunNode(NodeRunRequest{
			NodeType: "service.outlook_mail",
			Config: map[string]interface{}{
				"credential_id": connectionID,
				"operation":     "delete_message",
				"message_id":    msg.ExternalID,
			},
		})
	}
	return db.DeletePersonMessage(personMessageID)
}

// GetLatestPersonStatus returns the most recent status update for a person,
// or nil if none exists yet — the GUI equivalent of `people status get`.
func (a *App) GetLatestPersonStatus(personId string) *storage.PersonStatusUpdate {
	if a.db == nil {
		return nil
	}
	u, err := (&storage.Database{DB: a.db}).GetLatestPersonStatusUpdate(personId, a.activeProfileID)
	if err != nil {
		return nil
	}
	return u
}

// AddPersonStatus appends a new status update for a person, delegating to
// the same storage.PersonStatusUpdate repo used by `monoagentcli people status set`.
func (a *App) AddPersonStatus(personId, text string) (*storage.PersonStatusUpdate, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	return (&storage.Database{DB: a.db}).AddPersonStatusUpdate(personId, a.activeProfileID, text)
}

// GetPersonStatusHistory returns every status update for a person, newest
// first — the GUI equivalent of `people status history`. limit <= 0 means
// no cap.
func (a *App) GetPersonStatusHistory(personId string, limit int) []*storage.PersonStatusUpdate {
	if a.db == nil {
		return nil
	}
	updates, err := (&storage.Database{DB: a.db}).ListPersonStatusUpdates(personId, a.activeProfileID, limit)
	if err != nil {
		return nil
	}
	return updates
}

// GetPersonPosts returns all scraped posts for a person, with we_liked/we_commented flags.
```

- [ ] **Step 2: Build the wails-app Go module to catch compile errors**

Run: `cd /Volumes/media/projects/monoes/mono-agent/wails-app && go build ./...`
Expected: exits 0, no output.

- [ ] **Step 3: Regenerate the Wails JS bindings**

Run: `cd /Volumes/media/projects/monoes/mono-agent/wails-app && wails generate module`
Expected: exits 0; `git diff --stat frontend/src/wailsjs/go/main/` shows `App.js` and `App.d.ts` changed, adding `GetLatestPersonStatus`, `AddPersonStatus`, `GetPersonStatusHistory`.

- [ ] **Step 4: Verify the generated bindings**

Run: `grep -n "GetLatestPersonStatus\|AddPersonStatus\|GetPersonStatusHistory" /Volumes/media/projects/monoes/mono-agent/wails-app/frontend/src/wailsjs/go/main/App.d.ts`
Expected output includes three lines, e.g.:
```
export function AddPersonStatus(arg1:string,arg2:string):Promise<main.PersonStatusUpdate>;
export function GetLatestPersonStatus(arg1:string):Promise<main.PersonStatusUpdate>;
export function GetPersonStatusHistory(arg1:string,arg2:number):Promise<Array<main.PersonStatusUpdate>>;
```

- [ ] **Step 5: Commit**

```bash
cd /Volumes/media/projects/monoes/mono-agent
git add wails-app/app.go wails-app/frontend/src/wailsjs/go/main/App.js wails-app/frontend/src/wailsjs/go/main/App.d.ts
git commit -m "feat: expose person status updates through the Wails App API

Co-Authored-By: nokhodian <nokhodian@gmail.com>"
```

---

### Task 4: Frontend quote box (`Profile.jsx` + `api.js` + CSS)

**Files:**
- Modify: `wails-app/frontend/src/services/api.js`
- Modify: `wails-app/frontend/src/pages/Profile.jsx` (add `StatusSection` component + hero-card wiring)
- Modify: `wails-app/frontend/src/index.css` (new `.profile-status-*` classes)

**Interfaces:**
- Consumes: `GoApp.GetLatestPersonStatus`, `GoApp.AddPersonStatus` from Task 3.
- Produces (consumed by Task 5): `api.getLatestPersonStatus(personId)`, `api.addPersonStatus(personId, text)`, `api.getPersonStatusHistory(personId, limit)` in `api.js`; a `StatusSection({ personId, platformColor })` component in `Profile.jsx` that Task 5 will extend to render `StatusHistoryModal`.

- [ ] **Step 1: Add the api.js wrappers**

In `wails-app/frontend/src/services/api.js`, find:

```js
  getPostDetail:    (postId)   => GoApp.GetPostDetail(postId).catch(() => null),
```

Replace with:

```js
  getLatestPersonStatus: (personId) => GoApp.GetLatestPersonStatus(personId).catch(() => null),
  addPersonStatus:       (personId, text) => GoApp.AddPersonStatus(personId, text).catch(() => null),
  getPersonStatusHistory:(personId, limit) => GoApp.GetPersonStatusHistory(personId, limit ?? 0).catch(() => []),
  getPostDetail:    (postId)   => GoApp.GetPostDetail(postId).catch(() => null),
```

- [ ] **Step 2: Add the CSS for the quote box**

In `wails-app/frontend/src/index.css`, find:

```css
.profile-meta-link:hover {
  color: var(--cyan);
  border-color: var(--border);
  background: var(--cyan-glow);
}

/* Stats row */
```

Replace with:

```css
.profile-meta-link:hover {
  color: var(--cyan);
  border-color: var(--border);
  background: var(--cyan-glow);
}

/* Status update quote box */
.profile-status-box {
  margin: 4px 0 10px;
  padding: 10px 14px;
  background: var(--elevated);
  border-left: 3px solid var(--platform-color, var(--cyan));
  border-radius: 0 6px 6px 0;
  max-width: 540px;
}

.profile-status-quote {
  font-size: 13px;
  font-style: italic;
  color: var(--text);
  line-height: 1.5;
  margin: 0 0 4px;
}

.profile-status-empty {
  font-size: 12px;
  color: var(--text-muted);
  font-style: italic;
  margin: 0 0 4px;
}

.profile-status-meta {
  display: flex;
  align-items: center;
  gap: 10px;
  font-family: var(--font-mono);
  font-size: 10px;
  color: var(--text-dim);
}

.profile-status-history-link {
  background: none;
  border: none;
  padding: 0;
  cursor: pointer;
  color: var(--cyan-dim);
  font-family: var(--font-mono);
  font-size: 10px;
}
.profile-status-history-link:hover {
  color: var(--cyan);
  text-decoration: underline;
}

.profile-status-form {
  display: flex;
  gap: 6px;
  margin-top: 8px;
  max-width: 540px;
}

.profile-status-input {
  flex: 1;
  background: var(--elevated);
  border: 1px solid var(--border-dim);
  border-radius: var(--radius);
  padding: 6px 10px;
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--text);
  outline: none;
}
.profile-status-input:focus {
  border-color: var(--cyan-dim);
}

/* Stats row */
```

- [ ] **Step 3: Add the `StatusSection` component to Profile.jsx**

In `wails-app/frontend/src/pages/Profile.jsx`, find this exact block (the end of `MessagesSection` and the start of `stripHTML`):

```jsx
      {openMessage && (
        <MessageDetailModal
          message={openMessage}
          personLabel={personLabel}
          onClose={() => setOpenMessage(null)}
        />
      )}
    </div>
  )
}

// Strips HTML tags for the plain-text preview snippet; full HTML is
// rendered properly in MessageDetailModal via a sandboxed iframe.
function stripHTML(body) {
```

Replace with (adding `StatusSection` between the two — note this task does not yet import or render `StatusHistoryModal`; that wiring is Task 5's job):

```jsx
      {openMessage && (
        <MessageDetailModal
          message={openMessage}
          personLabel={personLabel}
          onClose={() => setOpenMessage(null)}
        />
      )}
    </div>
  )
}

// Quote-box showing a person's latest manually-written status update (e.g.
// "Just closed the Q1 deal"), with an inline form to post a new one. The
// "History" link is wired up by StatusHistoryModal in a later change.
function StatusSection({ personId, platformColor }) {
  const [latest, setLatest]   = useState(null)
  const [loading, setLoading] = useState(true)
  const [text, setText]       = useState('')
  const [posting, setPosting] = useState(false)
  const [historyOpen, setHistoryOpen] = useState(false)

  const reload = () => api.getLatestPersonStatus(personId).then(setLatest)

  useEffect(() => {
    setLoading(true)
    reload().catch(() => {}).finally(() => setLoading(false))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [personId])

  const post = async () => {
    const value = text.trim()
    if (!value || posting) return
    setPosting(true)
    try {
      const created = await api.addPersonStatus(personId, value)
      if (created) {
        setLatest(created)
        setText('')
      }
    } finally {
      setPosting(false)
    }
  }

  if (loading) return null

  return (
    <div className="profile-status-box" style={{ '--platform-color': platformColor }}>
      {latest ? (
        <p className="profile-status-quote">&ldquo;{latest.text}&rdquo;</p>
      ) : (
        <p className="profile-status-empty">No status yet</p>
      )}
      <div className="profile-status-meta">
        {latest && <span>{latest.created_at.slice(0, 16).replace('T', ' ')}</span>}
        <button className="profile-status-history-link" onClick={() => setHistoryOpen(true)}>
          History →
        </button>
      </div>
      <div className="profile-status-form">
        <input
          className="profile-status-input"
          placeholder="Post a status update…"
          value={text}
          onChange={e => setText(e.target.value)}
          onKeyDown={e => { if (e.key === 'Enter') post() }}
          disabled={posting}
        />
        <button className="btn btn-secondary btn-sm" onClick={post} disabled={posting || !text.trim()}>
          Post
        </button>
      </div>
    </div>
  )
}

// Strips HTML tags for the plain-text preview snippet; full HTML is
// rendered properly in MessageDetailModal via a sandboxed iframe.
function stripHTML(body) {
```

- [ ] **Step 4: Render `StatusSection` in the hero card**

In the same file, find:

```jsx
              {person.introduction && (
                <p className="profile-bio">{person.introduction}</p>
              )}
              <div className="profile-links">
```

Replace with:

```jsx
              {person.introduction && (
                <p className="profile-bio">{person.introduction}</p>
              )}
              <StatusSection personId={id} platformColor={platformColor} />
              <div className="profile-links">
```

- [ ] **Step 5: Build the frontend to catch JS errors**

Run: `cd /Volumes/media/projects/monoes/mono-agent/wails-app/frontend && npm run build`
Expected: exits 0, ending with a `vite build` success summary (no `historyOpen`/`setHistoryOpen` unused-variable lint failure — Vite's build doesn't run ESLint, so an unused `historyOpen` state at this point in the plan is harmless; Task 5 puts it to use).

- [ ] **Step 6: Commit**

```bash
cd /Volumes/media/projects/monoes/mono-agent
git add wails-app/frontend/src/services/api.js wails-app/frontend/src/pages/Profile.jsx wails-app/frontend/src/index.css
git commit -m "feat: add status-update quote box to the Profile page

Co-Authored-By: nokhodian <nokhodian@gmail.com>"
```

---

### Task 5: History modal + final verification

**Files:**
- Create: `wails-app/frontend/src/components/StatusHistoryModal.jsx`
- Modify: `wails-app/frontend/src/pages/Profile.jsx` (import + render the modal from `StatusSection`)

**Interfaces:**
- Consumes: `api.getPersonStatusHistory(personId, limit)` from Task 4; `historyOpen`/`setHistoryOpen` state already in `StatusSection` from Task 4.
- Produces: nothing further consumed by other tasks — this is the last task.

- [ ] **Step 1: Write the history modal**

Create `wails-app/frontend/src/components/StatusHistoryModal.jsx`:

```jsx
import { useEffect, useState, useRef } from 'react'
import { X } from 'lucide-react'
import { api } from '../services/api.js'

// Read-only chronological log of every status update ever posted for a
// person — no edit/delete affordance, matching the append-only data model.
export default function StatusHistoryModal({ personId, onClose }) {
  const overlayRef = useRef(null)
  const [entries, setEntries] = useState([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    const handler = (e) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [onClose])

  useEffect(() => {
    api.getPersonStatusHistory(personId).then(data => setEntries(data || [])).finally(() => setLoading(false))
  }, [personId])

  return (
    <div
      ref={overlayRef}
      onClick={(e) => { if (e.target === overlayRef.current) onClose() }}
      style={{
        position: 'fixed', inset: 0, zIndex: 1000,
        background: 'rgba(0,0,0,0.75)',
        display: 'flex', alignItems: 'center', justifyContent: 'center',
      }}
    >
      <div style={{
        background: '#0d1a26', border: '1px solid #1e3a4f', borderRadius: 12,
        width: 480, maxWidth: '92vw', maxHeight: '80vh',
        display: 'flex', flexDirection: 'column',
        boxShadow: '0 20px 60px rgba(0,0,0,0.6)',
        overflow: 'hidden',
      }}>
        {/* Header */}
        <div style={{
          display: 'flex', justifyContent: 'space-between', alignItems: 'center',
          padding: '14px 18px', borderBottom: '1px solid #1e3a4f',
        }}>
          <div style={{ fontFamily: 'var(--font-mono)', fontSize: 13, color: 'var(--text)', fontWeight: 600 }}>
            Status History
          </div>
          <button onClick={onClose} style={{ background: 'none', border: 'none', cursor: 'pointer', color: '#475569', padding: 2 }}>
            <X size={18} />
          </button>
        </div>

        {/* Body */}
        <div style={{ flex: 1, overflow: 'auto', padding: '12px 18px' }}>
          {loading ? (
            <div style={{ padding: '20px 0', textAlign: 'center' }}>
              <div className="spinner" style={{ width: 16, height: 16, margin: '0 auto' }} />
            </div>
          ) : entries.length === 0 ? (
            <div style={{ padding: '20px 0', textAlign: 'center', color: 'var(--text-muted)', fontFamily: 'var(--font-mono)', fontSize: 12 }}>
              No status updates yet
            </div>
          ) : (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
              {entries.map(e => (
                <div key={e.id} style={{
                  padding: '8px 10px', borderRadius: 6,
                  background: 'var(--elevated)', border: '1px solid var(--border)',
                }}>
                  <div style={{ fontSize: 12, color: 'var(--text)', lineHeight: 1.5 }}>{e.text}</div>
                  <div style={{ marginTop: 4, fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-dim)' }}>
                    {e.created_at.slice(0, 16).replace('T', ' ')}
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
```

- [ ] **Step 2: Wire the modal into `StatusSection`**

In `wails-app/frontend/src/pages/Profile.jsx`, find the top-of-file import block:

```jsx
import { api, PLATFORM_COLORS, STATE_COLORS } from '../services/api.js'
import MessageDetailModal from '../components/MessageDetailModal.jsx'
```

Replace with:

```jsx
import { api, PLATFORM_COLORS, STATE_COLORS } from '../services/api.js'
import MessageDetailModal from '../components/MessageDetailModal.jsx'
import StatusHistoryModal from '../components/StatusHistoryModal.jsx'
```

Then, in the same file, find the end of `StatusSection` (added in Task 4):

```jsx
        <button className="btn btn-secondary btn-sm" onClick={post} disabled={posting || !text.trim()}>
          Post
        </button>
      </div>
    </div>
  )
}

// Strips HTML tags for the plain-text preview snippet; full HTML is
```

Replace with:

```jsx
        <button className="btn btn-secondary btn-sm" onClick={post} disabled={posting || !text.trim()}>
          Post
        </button>
      </div>

      {historyOpen && (
        <StatusHistoryModal personId={personId} onClose={() => setHistoryOpen(false)} />
      )}
    </div>
  )
}

// Strips HTML tags for the plain-text preview snippet; full HTML is
```

- [ ] **Step 3: Build the frontend**

Run: `cd /Volumes/media/projects/monoes/mono-agent/wails-app/frontend && npm run build`
Expected: exits 0, ending with a `vite build` success summary.

- [ ] **Step 4: Manually verify in the running app**

Run: `cd /Volumes/media/projects/monoes/mono-agent/wails-app && wails dev`

In the opened app window:
1. Navigate to People, click into any existing contact's profile.
2. Confirm the quote box renders under the bio with "No status yet" (or an existing status if one was posted during Task 2's manual CLI verification against the *real* dev database — if so, skip to step 4).
3. Type a status into the input, click Post — the quote box should update immediately to show the new text.
4. Click "History →" — the modal should open showing every status update newest-first, with working Escape-to-close and click-outside-to-close.
5. Close the wails dev process (Ctrl+C) once confirmed.

- [ ] **Step 5: Run the full-repo build/vet/test sweep**

Run:
```bash
cd /Volumes/media/projects/monoes/mono-agent
go build ./... && go vet ./... && go test ./...
cd wails-app && go build ./... && go vet ./... && go test ./...
```
Expected: every line either `ok  	monoagent/...` or `?   	monoagent/...	[no test files]`; no `FAIL`.

- [ ] **Step 6: Commit**

```bash
cd /Volumes/media/projects/monoes/mono-agent
git add wails-app/frontend/src/components/StatusHistoryModal.jsx wails-app/frontend/src/pages/Profile.jsx
git commit -m "feat: add status update history modal to the Profile page

Co-Authored-By: nokhodian <nokhodian@gmail.com>"
```
