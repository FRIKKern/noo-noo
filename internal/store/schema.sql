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


-- v3a: session-safe deferred relocation queue (wave 2, charter D24).
--
-- When `offload apply` is refused by a stop-gate (the asset's owning process
-- is running — the ~/.claude case: you cannot move the config dir of the app
-- you are running), the user may EXPLICITLY consent to queue the relocation
-- instead of losing it. This table carries that queued intent through the
-- two-phase pattern proven by auto_clean_events: an attempt is marked on the
-- row BEFORE Apply runs (the crash pivot), and the outcome is patched in
-- after. The row is the STATE; the JSONL audit stays the append-only trail
-- of actual apply attempts (it structurally cannot carry queued->resolved).
--
-- status values:
--   'queued'    : consented, waiting for a gate re-check
--   'blocked'   : last re-check failed; blocked_reason says why. STILL
--                 pending — the next `offload run-pending` or daemon daily
--                 tick re-checks it fresh. The source is never touched by a
--                 blocked attempt (offload Apply's restore law).
--   'applied'   : terminal; freed_bytes + resolved_at_unix populated
--   'cancelled' : terminal; user withdrew consent (`offload cancel <id>`)
--
-- The dest pin (dest_root + dest_volume_uuid) is captured AT QUEUE TIME:
-- consent was for THAT destination. A re-check refuses (blocks) an entry
-- whose pin no longer matches the live [offload] config — a changed
-- destination needs fresh consent, never a silent redirect. Every re-check
-- runs the full fresh gate set (stop gate, volume guard, destination
-- collision) — queueing never weakens a single gate.
CREATE TABLE IF NOT EXISTS relocation_queue (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    queued_at_unix       INTEGER NOT NULL,
    target_path          TEXT    NOT NULL,
    playbook_asset_id    TEXT    NOT NULL,
    dest_root            TEXT    NOT NULL,
    dest_volume_uuid     TEXT    NOT NULL,
    gate_reason          TEXT    NOT NULL DEFAULT '',  -- why apply was deferred at queue time
    status               TEXT    NOT NULL DEFAULT 'queued',  -- 'queued' | 'blocked' | 'applied' | 'cancelled'
    blocked_reason       TEXT,
    attempts             INTEGER NOT NULL DEFAULT 0,
    last_attempt_at_unix INTEGER,
    resolved_at_unix     INTEGER,
    freed_bytes          INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_reloc_queue_pending
    ON relocation_queue(status) WHERE status IN ('queued', 'blocked');

-- v3b (wave-2): disk-space time-series (posture data layer).
--
-- One row per (volume, sample): a point-in-time capacity reading for the boot
-- volume and each mounted /Volumes/* volume. scan.ScanRoots records these,
-- throttled to at most one row per volume per hour (pressure-triggered scans
-- fire ~every 5 min; unthrottled this would grow ~300 rows/day/volume). It is
-- the source series for `noo-noo status`/`trends`: the days-until-full forecast
-- day-buckets these samples (charter D18), and cumulative-fill patterns read
-- the free_bytes trend per volume_uuid.
--
-- Appended as CREATE TABLE IF NOT EXISTS ONLY (charter D16): migrate() re-execs
-- this whole file on every Open, so a bare ALTER TABLE would error on the
-- second daemon start and brick the store. Never ALTER; new tables only.
CREATE TABLE IF NOT EXISTS disk_space_history (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    volume_uuid  TEXT     NOT NULL,
    mount_point  TEXT     NOT NULL,
    total_bytes  INTEGER  NOT NULL,
    free_bytes   INTEGER  NOT NULL,
    recorded_at  DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_disk_history_volume_time
    ON disk_space_history(volume_uuid, recorded_at);
