# Changelog

All notable changes to noo-noo are documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.7.0] — 2026-10-02

The "invisible guardian" release — born from a second real machine: an 8 GB
MacBook Air at load average 46 with 8 GB of swap in use, 185 leaked Chrome
code-sign clones pinned by a headless browser stuck for 37 hours, 16,603
test-harness temp directories (43 GB) in $TMPDIR, a 23 GB agent scratch dir
protected by a 7-day age gate, and 21 GB of data from apps uninstalled in
2024. A human reclaimed 180 GB by hand; this release teaches noo-noo to find
all of it, tell the truth about sizes, and stay out of the way while doing so.

### Added
- **temp-family leak signature** — direct children of `$TMPDIR` and
  `/private/tmp` grouped by prefix after stripping the random suffix that
  `os.MkdirTemp`/`mktemp` append. A family of ≥ 5 siblings, newest mtime
  ≥ 12 h, no member held open (one batched `lsof` per scan) becomes one
  truth-sized item; apply re-proves each member before deleting it.
- **Session-liveness gate for agent scratch** — `private-tmp-agent-scratch`
  now asks whether the session is alive (`--session-id <uuid>` in a process,
  or its transcript written in the last 30 min) instead of waiting 7 days.
  Dead sessions are stale after 1 h; live sessions keep their protection but
  offer `scratchpad/` entries idle ≥ 24 h and lsof-empty one by one.
- **`noo-noo apps [list|clean]`** — orphaned app data: children of
  `~/Library/{Application Support,Containers,Caches}` plus `~/DevKinsta`,
  `~/Local Sites`, `~/.docker` whose owning app no longer resolves (bundle id
  via one `mdfind`, name or vendor token under the app folders), idle
  ≥ 30 days and ≥ 50 MB real. Apple ids and toolchain caches are never
  flagged. Sparse files (Docker.raw) report real bytes. Included in `report`.
- **Leak-storm alert** — ≥ 10 new instances of one leak signature within an
  hour (APFS birth time, so a daemon restart cannot fake it) sends one
  notification naming the signature, the count, and the signature's permanent
  fix (for Chrome: `--disable-features=MacAppCodeSignClone`), at most once
  per 6 h per signature. Fresh clones are live and were invisible to the
  suggestion path; the storm is the alert-worthy event.
- **Memory posture in `noo-noo status`** — physical RAM, swap used/total,
  compressor share, a fine/tight/thrashing verdict citing its numbers, and
  the top 3 processes by compressed memory (`internal/core/memory.go`).
- **Caches**: `~/Library/Developer/CoreSimulator/Caches/dyld` target;
  `~/.Trash` sized and shown with "Empty Trash", never cleaned by noo-noo.
- **Finished-worktree sweeper** (`noo-noo worktrees [list|scan|clean|judge]`)
  — obvious tier (clean, branch pushed or preserved) plus an AI-judged tier
  with `--limit`; descends through nested repos.
- Leaks: the redirected-TMPDIR convention (`<volume>/dev-caches/tmp`),
  hardlink-alias liveness, leaks auto-clean on the daily tick.

### Fixed
- **Daemon single instance** — a second `noo-nood` on the same socket exits
  with `noo-nood already running (pid N)` and touches nothing on disk. Start
  takes a `flock` on `<socket>.lock`, removes a pre-existing socket only when
  nobody answers on it, and Stop unlinks only the socket this process bound.
  Previously a stray second daemon deleted the launchd daemon's socket on
  exit and every CLI call that needed the daemon failed until a kickstart.
  `daemon status` now prints the pid and the unreachable error names the
  `launchctl kickstart -k gui/$(id -u)/io.noo-noo.d` recovery.
- **Pressure loop** — the memory-pressure trigger fired a full scan every
  5 minutes forever on a machine whose memory is always high (113 ticks in a
  day). Trigger is disk-only, cooldown 60 min, ticks are light, daemon runs
  at background QoS. Unmeasured free-disk no longer reads as 0 GB.
- Daemon: `RunTick` serialized with `tickMu`; reserved daily-tick lane so the
  03:00 auto-clean is never starved by pressure ticks.
- Brew cask: strips `com.apple.quarantine` before the postflight runs
  `noo-noo install` (macOS Sequoia SIGKILLs the ad-hoc signed binary
  otherwise and the install rolled back); `depends_on macos` symbol form.

## [0.6.0] — 2026-07-16

The "disk guardian" release: truth-sized measurement, leak signatures, offload
to external volumes, and visibility surfaces — born from a real 228 GB Mac that
kept filling itself (65 leaked Chrome code-sign clones, a du that over-reported
APFS clones by 40×, and a Docker VM that died silently on a full datadisk).

### Added
- `internal/sizer` — APFS-truth sizing: `Blocks` (allocated), `UniqueAllocated`
  (F_LOG2PHYS_EXT extent union — clone- and sparse-aware, so suggestions report
  REAL reclaimable bytes, never naive-du fiction), `FreedByDelete` (statfs
  ground truth).
- `noo-noo leaks scan|list|clean` — data-driven leak-signature registry.
  Signature #1: Chrome `code_sign_clone` (leaks one ~2 GB self-clone per
  crash/force-kill; staleness proven via lsof, never mtime). Signature #2:
  unpurged agent scratch in `/private/tmp`. Apply re-checks staleness at
  delete time and reports real freed bytes.
- `noo-noo offload scan|plan|apply|pending|run-pending|cancel` — policy-driven
  relocation of bulky assets to a UUID-pinned external volume (live write-probe
  guard; copy → verify → swap → symlink → only then drop). Playbooks are data,
  templated on your configured `dest_root` — nothing machine-specific. Assets
  blocked by a running app can be queued with `--defer` (explicit consent) and
  executed later by `run-pending` or the armed daemon tick.
- `noo-noo status` — machine posture in one honest statement: disk fill rate,
  known growers, external volumes with health verdicts (including hardware
  write-locked media, where repair is impossible and rescue-copy is the only
  correct move), offload readiness, and pressure posture.
- `noo-noo trends` — per-asset-class growth sparklines, day-bucketed
  days-until-full forecasting, and recurrence detection (a leak class that
  keeps coming back surfaces its permanent remediation, e.g. Chrome's
  `--disable-features=MacAppCodeSignClone`).
- `noo-noo orphans list|scan|kill` — detects orphaned automation browsers
  (headless Chromes whose user-data-dir lives in agent/temp scratch and whose
  parent job is gone) and terminates them safely (own-uid, reparented-to-init,
  fresh TOCTOU re-verification before any signal). These orphans are the
  primary *source* of code-sign-clone leaks.
- `internal/vmdisk` — VM inner-disk fullness detection for colima/lima: spots
  a 100 %-full datadisk (silently dead dockerd), suggests a grow sized to
  demand, gated on proven host-volume headroom.
- Daemon leak alerts: the leaks registry now runs on scheduler tick and
  pressure triggers with macOS notifications naming the leak class, real GB,
  and the one fixing command.
- `disk_space_history` sampling + readable auto-clean audit trail
  (`auto_clean events` wired end-to-end).
- ShipIt (Squirrel.Mac) updater-leftover caches added to the caches module.

### Changed
- CLI flag law: flags parse AFTER the verb across all subcommands
  (`noo-noo offload apply -y`); a flag before the verb now errors loudly
  instead of silently dropping your `-y`.
- `noo-noo report` includes leaks and offload sections.
- Scan roots are symlink-resolved (a symlinked root previously never walked).

### Fixed
- Volume guard resolves the destination's mount point via statfs before the
  diskutil identity check — subdirectory dest_roots no longer refused.
- Suggestion sizes are first-class bytes (no more string-typed size=0 holes).

## [0.5.0] — 2026-05-03
### Added
- Pressure-triggered scans: daemon samples `vm_stat` + free disk every 15 s,
  fires an out-of-band scan when sustained-high pressure is detected
  (`internal/pressure`).
- Filesystem scan-collector (`internal/scan`) called from CLI, scheduler tick,
  and pressure trigger — closes the Phase 0.2 stale-data bug.
- Opt-in auto-clean engine (`internal/autoclean`) with six-gate safety design,
  default-disabled, multi-step opt-in via `--i-understand-the-risks` flag.
- New `auto_clean_events` audit table (migration 0005).
- IPC: `AutoClean.Status` and `AutoClean.Toggle` methods.
- CLI: `noo-noo auto-clean enable|disable|status|history` subcommand.
- Config: `[pressure]` and `[auto_clean]` sections with safe defaults.
- e2e tests: pressure-triggered scan in < 60 s; auto-clean gate cascade.

### Changed
- Daemon scheduler tick now: scan -> heuristics -> autoclean (if enabled)
  -> notify. Previously: just heuristics -> notify.

### Security
- Auto-clean is default-disabled; first enable requires
  `noo-noo auto-clean enable --i-understand-the-risks`.
- Auto-clean only acts on `dev`-module suggestions by default;
  caches/startup require explicit allowlist edit.
- Per-tick size cap (10 GB default) bounds worst-case damage.
- Pressure-triggered scans never auto-clean; only the daily tick may.

## [0.4.0] — 2026-05-03
### Added
- GitHub Actions release workflow (`.github/workflows/release.yml`) that
  builds, signs, and publishes universal Mach-O binaries on tag push.
- `Noo-Noo.app.zip` and `Noo-Noo-vX.X.X.dmg` release artifacts (ad-hoc
  signed; right-click → Open required on first launch).
- Homebrew tap at `FRIKKern/homebrew-tap` with both Cask (GUI app) and
  Formula (CLI-only, headless servers).
- `build/release/build-binaries.sh`, `build-app.sh`, `build-dmg.sh`,
  `checksums.sh` — modular CI build scripts.
- `build/brew/noo-noo.rb` and `noo-noo-formula.rb` templates with
  `__VERSION__` + `__SHA256_*__` placeholders.
- `scripts/release.sh --dry-run` local convenience driver.
- This `CHANGELOG.md`.

### Changed
- README install instructions: `brew install FRIKKern/tap/noo-noo` is
  now the primary path; `make app-package` survives as "Build from
  source" documentation.

## [0.3.0] — 2026-05-03
### Added
- `Noo-Noo.app` Wails v3 + Svelte 5 menubar app with `LSUIElement` set
  (no dock icon).
- Status badge showing the open-suggestion count.
- Suggestions submenu with the top three open suggestions inline.
- "Run Scan Now" menu item via the new `Daemon.TriggerScan` IPC.
- Settings window: daily scan hour, heuristic toggles & thresholds,
  notification toggle.
- `make app`, `make app-dev`, `make app-package` Makefile targets.
- New `internal/menubar` package (icon, menu, click, poller).

## [0.2.0] — 2026-05-03
### Added
- `noo-nood` background daemon launched via launchd user agent.
- Unix-socket JSON-RPC server at `~/Library/Application Support/noo-noo/noo-noo.sock`.
- IPC services: `Daemon.Status`, `Suggestions.List/Dismiss`,
  `Clean.Execute`, `Report.Full`.
- Daily scheduled scans at 03:00 (configurable).
- macOS user notifications on new suggestions.

## [0.1.0] — 2026-05-02
### Added
- Initial CLI MVP: `noo-noo scan`, `clean`, `suggestions`, `report`,
  `install`.
- Heuristics: idle-repos (Git repos untouched for N days with large
  `node_modules/`), cache-velocity (caches growing faster than expected).
- TOML config at `~/.config/noo-noo/config.toml`.
- Cobra CLI scaffold + structured logging.
