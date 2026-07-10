-- 015_person_messages_status.sql
-- Adds a status to person_messages so outbound messages can be created as
-- drafts (awaiting human confirmation) before actually being sent.
-- Existing rows (all inbound-synced so far) default to 'sent', which is a
-- no-op label for them — status only becomes meaningful for outbound
-- compose/draft messages.
ALTER TABLE person_messages ADD COLUMN status TEXT NOT NULL DEFAULT 'sent';

CREATE INDEX IF NOT EXISTS idx_person_messages_status ON person_messages(status);
