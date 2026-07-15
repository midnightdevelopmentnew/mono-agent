-- 018_crawler_sessions_profile_scoped_unique.sql
-- crawler_sessions.username+platform was globally unique across ALL profiles
-- (not scoped per profile) even though a profile_id column existed since
-- 011_profiles.sql — so logging in to the same account from a second profile
-- (whose browser session cookie already satisfied IsLoggedIn) hit the global
-- UNIQUE constraint on insert instead of creating a profile-scoped row.
-- Same class of bug as 014_people_profile_scoped_unique.sql; apply the same
-- rebuild-the-table fix here.

CREATE TABLE crawler_sessions_v2 (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT NOT NULL,
    platform      TEXT NOT NULL,
    cookies_json  TEXT NOT NULL,
    expiry        TIMESTAMP NOT NULL,
    when_added    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    profile_photo BLOB,
    profile_id    TEXT NOT NULL DEFAULT 'default',
    UNIQUE(username, platform, profile_id)
);

INSERT INTO crawler_sessions_v2 (
    id, username, platform, cookies_json, expiry, when_added, profile_photo, profile_id
)
SELECT
    id, username, platform, cookies_json, expiry, when_added, profile_photo, profile_id
FROM crawler_sessions;

DROP TABLE crawler_sessions;

ALTER TABLE crawler_sessions_v2 RENAME TO crawler_sessions;

CREATE INDEX IF NOT EXISTS idx_sessions_platform ON crawler_sessions(platform);
CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON crawler_sessions(expiry);
CREATE INDEX IF NOT EXISTS idx_sessions_profile ON crawler_sessions(profile_id);
