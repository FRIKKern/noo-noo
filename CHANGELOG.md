# Changelog

All notable changes to noo-noo are documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
