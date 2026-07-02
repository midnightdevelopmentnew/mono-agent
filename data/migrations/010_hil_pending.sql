CREATE TABLE IF NOT EXISTS hil_pending (
    id            TEXT PRIMARY KEY,
    execution_id  TEXT NOT NULL,
    workflow_id   TEXT NOT NULL,
    node_id       TEXT NOT NULL,
    node_name     TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'pending',
    readonly_data TEXT NOT NULL DEFAULT '{}',
    editable_data TEXT NOT NULL DEFAULT '{}',
    edited_data   TEXT NOT NULL DEFAULT '{}',
    node_config   TEXT NOT NULL DEFAULT '{}',
    created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_hil_pending_status      ON hil_pending (status);
CREATE INDEX IF NOT EXISTS idx_hil_pending_execution   ON hil_pending (execution_id);
