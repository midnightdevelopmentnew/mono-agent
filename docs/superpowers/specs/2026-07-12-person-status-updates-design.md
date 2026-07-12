# Person Status Updates — Design Spec

Date: 2026-07-12

## Summary

Add a manually-written, freeform "status update" per person (contact) — a
short personal-CRM-style note like "Just closed the Q1 deal" or "Following
up next week". Entirely separate from platform data (bios, messages) and
from the existing `PersonMessage.Status` enum (draft/sent/failed), which is
unrelated despite the name collision.

- **GUI**: a quote-box on the Profile page showing the latest status, with
  an inline input to post a new one, and a "History" link opening a modal
  with the full chronological log.
- **CLI**: `people status set|get|history`, first-class and AI-discoverable
  per this repo's CLI-first convention.
- Both surfaces call the same shared Go repository functions — no duplicated
  logic between `cmd/monoagentcli` and `wails-app`.

## Data model

New table, profile-scoped like every other people-adjacent table (`people`,
`tags`, `person_messages`) — closing off the class of cross-profile leakage
found and fixed in the 2026-07-12 full-app review.

Migration `data/migrations/016_person_status_updates.sql`:

```sql
CREATE TABLE person_status_updates (
    id         TEXT PRIMARY KEY,
    person_id  TEXT NOT NULL REFERENCES people(id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL DEFAULT 'default',
    text       TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_status_updates_person ON person_status_updates(person_id, created_at DESC);
```

- Append-only: no edit, no delete (YAGNI — add later only if actually needed).
- `text`: trimmed, rejected if empty after trim. No length cap, matching
  `people.introduction` and `person_messages.body`, which aren't capped either.
- `profile_id` is denormalized onto the status row (not just inherited via
  `person_id` join) so a query can filter directly without always joining
  `people`, matching the `tags`/`people_tags` precedent.

## Repository layer (`internal/storage`)

`models.go`:

```go
// PersonStatusUpdate represents a single manually-written status/note entry
// about a person — a personal-CRM-style update, unrelated to
// PersonMessage.Status (which tracks draft/sent/failed for outbound messages).
type PersonStatusUpdate struct {
    ID        string    `json:"id"`
    PersonID  string    `json:"person_id"`
    Text      string    `json:"text"`
    CreatedAt time.Time `json:"created_at"`
}
```

`repository.go` — three functions, all profile-scoped by verifying the
target person belongs to `profileID` before acting (same guard shape as
`assertMessageInProfile` added in the review-fix pass):

- `AddPersonStatusUpdate(ctx, personID, profileID, text string) (*PersonStatusUpdate, error)`
  Trims `text`, errors if empty, errors if the person doesn't belong to
  `profileID`, inserts, returns the created row.
- `GetLatestPersonStatusUpdate(ctx, personID, profileID string) (*PersonStatusUpdate, error)`
  Returns `(nil, nil)` — not an error — when no status exists yet.
- `ListPersonStatusUpdates(ctx, personID, profileID string, limit int) ([]*PersonStatusUpdate, error)`
  Newest first (`ORDER BY created_at DESC`). `limit <= 0` means no cap.

Both `cmd/monoagentcli` and `wails-app` import `internal/storage` directly
(the existing pattern for `PersonMessage`) and call these same functions —
no SQL duplicated in the GUI layer.

## CLI (`cmd/monoagentcli/people_status.go`)

New `people status` subcommand group, sibling to the existing
`people messages`:

```
monoagentcli people status set <person-id> "Just closed the Q1 deal"
monoagentcli people status get <person-id>
monoagentcli people status history <person-id> [--limit N] [--json]
```

- `set`: requires the text as a positional arg (quoted); errors clearly if
  the person ID doesn't resolve within the active profile.
- `get`: prints the latest status, or a clear "no status set for this
  person" message (not an error) if none exists.
- `history`: table output by default (timestamp + text), `--json` for
  scripting/agent use. `--limit` optional, default unlimited.
- All three resolve the person scoped to `cfg.ProfileID`, matching every
  other `people` subcommand's profile-scoping.

`README.md`'s "Data & People" CLI examples block gets three new lines next
to the existing `people messages` examples.

## GUI (`wails-app`)

**App methods** (`wails-app/app.go`), thin wrappers over the repository
functions above, scoped to `a.activeProfileID`:

```go
func (a *App) GetLatestPersonStatus(personId string) *storage.PersonStatusUpdate
func (a *App) AddPersonStatus(personId, text string) (*storage.PersonStatusUpdate, error)
func (a *App) GetPersonStatusHistory(personId string, limit int) []*storage.PersonStatusUpdate
```

**Frontend (`wails-app/frontend/src`):**

- `services/api.js`: `getLatestPersonStatus`, `addPersonStatus`,
  `getPersonStatusHistory` wrappers around the generated Wails bindings,
  matching the existing wrapper style for `getPersonMessages` etc.
- `pages/Profile.jsx`: new `StatusSection` component placed immediately
  after the bio/introduction block in the hero card (`profile-hero-info`).
  Renders as a blockquote-style card — left accent bar in the platform
  color, italic text — showing the latest status text and a relative
  timestamp, or "No status yet" when `GetLatestPersonStatus` returns nil.
  Below it: a single-line input + "Post" button that calls `AddPersonStatus`
  and refreshes the quote box in place. A "History →" link opens the modal.
- `components/StatusHistoryModal.jsx`: new modal modeled directly on the
  existing `MessageDetailModal.jsx` (same overlay/close-on-backdrop/Escape
  pattern). Lists every status update newest-first with full timestamps.
  Read-only — no delete/edit affordance, per the append-only decision above.

## Testing

- `internal/storage`: repository test covering add → get-latest → list-
  history, plus the profile-scoping guard (a status added under profile A
  must be invisible to and unwritable from profile B) — same shape as the
  existing profile-scoping tests added during the review-fix pass.
- `cmd/monoagentcli`: CLI-level test for `set`/`get`/`history`, including
  the wrong-profile → "not found" case.
- No GUI test framework exists in this repo today (Posts/Messages sections
  on Profile.jsx aren't covered either) — the quote box and modal are
  verified manually by running the app, not by an automated test.

## Explicitly out of scope (YAGNI)

- Editing or deleting a status-update entry.
- A length cap / character limit on the status text.
- Any syncing of status updates from external platforms — this is a
  manual-only, local note.
