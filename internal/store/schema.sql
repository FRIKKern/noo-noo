-- v1: initial schema
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    applied_at DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS cache_size_history (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    target_path TEXT    NOT NULL,
    bytes       INTEGER NOT NULL,
    recorded_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_cache_history_target_time
    ON cache_size_history(target_path, recorded_at);

CREATE TABLE IF NOT EXISTS repo_idleness (
    path                TEXT PRIMARY KEY,
    last_commit_at      DATETIME,
    node_modules_bytes  INTEGER NOT NULL DEFAULT 0,
    last_scan_at        DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS actions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    ts            DATETIME NOT NULL,
    module        TEXT     NOT NULL,
    op            TEXT     NOT NULL,
    target        TEXT     NOT NULL,
    size_bytes    INTEGER  NOT NULL DEFAULT 0,
    evidence_json TEXT     NOT NULL DEFAULT '{}',
    outcome       TEXT     NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_actions_ts ON actions(ts);

CREATE TABLE IF NOT EXISTS suggestions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    ts            DATETIME NOT NULL,
    module        TEXT     NOT NULL,
    target        TEXT     NOT NULL,
    reason        TEXT     NOT NULL,
    evidence_json TEXT     NOT NULL DEFAULT '{}',
    severity      TEXT     NOT NULL DEFAULT 'medium',
    dismissed_at  DATETIME
);
CREATE INDEX IF NOT EXISTS idx_suggestions_open
    ON suggestions(dismissed_at) WHERE dismissed_at IS NULL;
