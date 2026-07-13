CREATE TABLE vault_secrets (
    id               TEXT PRIMARY KEY,
    seq              INTEGER NOT NULL UNIQUE,
    profile_id       TEXT NOT NULL DEFAULT 'default',
    kind             TEXT NOT NULL,
    name             TEXT NOT NULL,
    username         TEXT,
    url              TEXT,
    ciphertext       BLOB NOT NULL,
    nonce            BLOB NOT NULL,
    notes_ciphertext BLOB,
    notes_nonce      BLOB,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_vault_secrets_profile_name ON vault_secrets(profile_id, name);
CREATE INDEX idx_vault_secrets_seq ON vault_secrets(seq DESC);

CREATE TABLE vault_keys (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    wrapped_dek   BLOB NOT NULL,
    wrapped_nonce BLOB NOT NULL,
    created_at    TEXT NOT NULL
);
