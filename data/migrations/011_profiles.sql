-- 011_profiles.sql
-- Multi-profile support: every user-owned entity is scoped to a profile.
-- Running workflows from a non-active profile continue uninterrupted —
-- they are subprocesses; only the UI read-filter changes.

CREATE TABLE IF NOT EXISTS profiles (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Bootstrap the default profile so FK references are satisfied.
INSERT OR IGNORE INTO profiles (id, name) VALUES ('default', 'Default');

-- Persist the active-profile selection across restarts.
INSERT OR IGNORE INTO settings (key, value) VALUES ('active_profile_id', 'default');

-- Add profile_id to every user-scoped table.
ALTER TABLE people              ADD COLUMN IF NOT EXISTS profile_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE crawler_sessions    ADD COLUMN IF NOT EXISTS profile_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE actions             ADD COLUMN IF NOT EXISTS profile_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE social_lists        ADD COLUMN IF NOT EXISTS profile_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE threads             ADD COLUMN IF NOT EXISTS profile_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE workflows           ADD COLUMN IF NOT EXISTS profile_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE workflow_executions ADD COLUMN IF NOT EXISTS profile_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE credentials         ADD COLUMN IF NOT EXISTS profile_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE vault_images        ADD COLUMN IF NOT EXISTS profile_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE hil_pending         ADD COLUMN IF NOT EXISTS profile_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE tags                ADD COLUMN IF NOT EXISTS profile_id TEXT NOT NULL DEFAULT 'default';

-- Performance indexes for profile-scoped queries.
CREATE INDEX IF NOT EXISTS idx_people_profile              ON people(profile_id);
CREATE INDEX IF NOT EXISTS idx_sessions_profile            ON crawler_sessions(profile_id);
CREATE INDEX IF NOT EXISTS idx_actions_profile             ON actions(profile_id);
CREATE INDEX IF NOT EXISTS idx_social_lists_profile        ON social_lists(profile_id);
CREATE INDEX IF NOT EXISTS idx_workflows_profile           ON workflows(profile_id);
CREATE INDEX IF NOT EXISTS idx_workflow_executions_profile ON workflow_executions(profile_id);
CREATE INDEX IF NOT EXISTS idx_credentials_profile         ON credentials(profile_id);
CREATE INDEX IF NOT EXISTS idx_vault_images_profile        ON vault_images(profile_id);
CREATE INDEX IF NOT EXISTS idx_hil_pending_profile         ON hil_pending(profile_id);
CREATE INDEX IF NOT EXISTS idx_tags_profile                ON tags(profile_id);
