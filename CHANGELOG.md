# Changelog

All notable changes to noo-noo are documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
