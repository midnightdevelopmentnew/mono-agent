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
