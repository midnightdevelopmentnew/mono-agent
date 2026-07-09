-- 014_people_profile_scoped_unique.sql
-- people.platform_username+platform was globally unique across ALL profiles
-- (not scoped per profile) even though a profile_id column existed — so the
-- same external username/email always resolved to one shared row regardless
-- of which profile encountered it first, breaking "full data isolation per
-- profile" for this table specifically. Widen the uniqueness to include
-- profile_id. SQLite can't alter a UNIQUE constraint directly, so rebuild.

CREATE TABLE people_v2 (
    id                TEXT PRIMARY KEY,
    platform_username TEXT NOT NULL,
    platform          TEXT NOT NULL,
    full_name         TEXT,
    image_url         TEXT,
    contact_details   TEXT,
    website           TEXT,
    content_count     INTEGER DEFAULT 0,
    follower_count    TEXT,
    following_count   INTEGER DEFAULT 0,
    introduction      TEXT,
    is_verified       INTEGER DEFAULT 0,
    category          TEXT,
    job_title         TEXT,
    created_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    profile_url       TEXT,
    profile_id        TEXT NOT NULL DEFAULT 'default',
    UNIQUE(platform_username, platform, profile_id)
);

INSERT INTO people_v2 (
    id, platform_username, platform, full_name, image_url, contact_details,
    website, content_count, follower_count, following_count, introduction,
    is_verified, category, job_title, created_at, updated_at, profile_url,
    profile_id
)
SELECT
    id, platform_username, platform, full_name, image_url, contact_details,
    website, content_count, follower_count, following_count, introduction,
    is_verified, category, job_title, created_at, updated_at, profile_url,
    profile_id
FROM people;

DROP TABLE people;

ALTER TABLE people_v2 RENAME TO people;

CREATE INDEX IF NOT EXISTS idx_people_username ON people(platform_username);
CREATE INDEX IF NOT EXISTS idx_people_profile ON people(profile_id);
