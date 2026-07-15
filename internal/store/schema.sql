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

-- v2: auto-clean event log (Phase 0.5).
--
-- The autoclean engine writes one row per delete it considers, and updates
-- it with the outcome. The pre-delete row (outcome='in_progress') is the
-- crash-safety pivot: if the daemon dies between RecordAutoCleanEvent and
-- the os.RemoveAll, recovery sees an in-progress row pointing at a path
-- that may now be partially deleted.
--
-- outcome values:
--   'in_progress' : audit row written, delete not yet attempted
--   'deleted'     : delete completed, freed_bytes populated
--   'skipped'     : a gate (or the safety guard) refused the delete
--   'errored'     : delete attempted but failed; error_msg populated
--
-- trigger values:
--   'daily'       : the regular daemon tick (the only auto trigger)
--   'manual'      : operator invoked `noo-noo auto-clean run`
--
-- Pressure events are intentionally NOT a trigger here: the autoclean
-- engine refuses any trigger != 'daily' so mid-flight dev work is never
-- the moment we choose to delete things.
CREATE TABLE IF NOT EXISTS auto_clean_events (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at_unix        INTEGER NOT NULL,
    ended_at_unix          INTEGER,
    trigger                TEXT NOT NULL,        -- 'daily' | 'manual'
    outcome                TEXT NOT NULL,        -- 'in_progress' | 'deleted' | 'skipped' | 'errored'
    skip_reason            TEXT,                 -- 'module_not_allowed' | 'idle_too_short' | ...
    target_path            TEXT NOT NULL,
    module                 TEXT NOT NULL,
    target_size_bytes      INTEGER NOT NULL,
    freed_bytes            INTEGER NOT NULL DEFAULT 0,
    idle_days_at_decision  INTEGER NOT NULL DEFAULT 0,
    suggestion_id          TEXT NOT NULL,
    error_msg              TEXT
);
CREATE INDEX IF NOT EXISTS idx_auto_clean_started ON auto_clean_events(started_at_unix);
CREATE INDEX IF NOT EXISTS idx_auto_clean_outcome ON auto_clean_events(outcome);
