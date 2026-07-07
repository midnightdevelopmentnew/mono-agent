-- 012_person_messages.sql
-- Per-person message/interaction history, ingested from any external source
-- (Outlook, Gmail, Instagram DMs, LinkedIn, X, Telegram, manual notes, ...).

CREATE TABLE IF NOT EXISTS person_messages (
    id           TEXT PRIMARY KEY,
    person_id    TEXT NOT NULL REFERENCES people(id) ON DELETE CASCADE,
    source       TEXT NOT NULL,              -- e.g. "outlook", "gmail", "instagram", "linkedin", "x", "telegram", "manual"
    external_id  TEXT NOT NULL DEFAULT '',   -- source-native message/thread id, for idempotent re-import
    direction    TEXT NOT NULL DEFAULT 'inbound', -- inbound | outbound
    sender       TEXT,
    subject      TEXT,
    body         TEXT,
    metadata     TEXT,                       -- free-form JSON blob for source-specific fields
    sent_at      TIMESTAMP,
    profile_id   TEXT NOT NULL DEFAULT 'default',
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(person_id, source, external_id)
);

CREATE INDEX IF NOT EXISTS idx_person_messages_person  ON person_messages(person_id);
CREATE INDEX IF NOT EXISTS idx_person_messages_profile ON person_messages(profile_id);
