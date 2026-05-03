-- 0005: auto-clean event log (Phase 0.5).
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
