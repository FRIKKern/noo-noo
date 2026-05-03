# noo-noo

> A lightweight, smart, opt-in cleanup daemon for Mac developers — inspired by [Noo-Noo](https://teletubbies.fandom.com/wiki/Noo-Noo), the Teletubbies vacuum cleaner.

[![CI](https://github.com/FRIKKern/noo-noo/actions/workflows/ci.yml/badge.svg)](https://github.com/FRIKKern/noo-noo/actions/workflows/ci.yml)
[![Status: v0.3](https://img.shields.io/badge/status-v0.3-blue.svg)](#status)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go: 1.23+](https://img.shields.io/badge/go-1.23+-00ADD8.svg)](https://go.dev)

`noo-noo` watches your Mac for the sneaky stuff that piles up while you work — abandoned `node_modules`, runaway caches, idle iOS simulators, dead launchd agents — and quietly suggests cleanups when (and only when) the data says you'll get something back. Nothing destructive runs without your explicit approval.

## Status

**v0.3 — Menubar app shipped.** `Noo-Noo.app` (Wails v3 + Svelte 5) lives in the macOS menubar with a live open-suggestion badge, a top-3 suggestions submenu, an on-demand "Run Scan Now" trigger, and a Settings window for the daily scan hour, heuristic toggles & thresholds, and notification preferences. The Phase 0.2 daemon is unchanged; the app is a thin frontend over the existing JSON-RPC IPC. See [docs/plans/](docs/plans/) for the roadmap.

What's new in v0.3:

- **`Noo-Noo.app`** — macOS menubar app (LSUIElement, no dock icon).
- **Status badge** showing the open-suggestion count (with singular/plural handling).
- **Suggestions submenu** with the top three open suggestions inline + a "See all…" trailer.
- **Run Scan Now** — on-demand scan via the new `Daemon.TriggerScan` IPC.
- **Settings window** (Svelte 5) for the daily scan hour, heuristic toggles & thresholds, and notifications. Atomic write-then-rename to `~/.config/noo-noo/config.toml`.

What ships in v0.2 (still current):

- `noo-nood` daemon supervised by `launchd` (LaunchAgent, no `sudo`).
- Persistent SQLite store at `~/Library/Application Support/noo-noo/store.db`.
- JSON-RPC over a Unix socket (Daemon.Status, Report.Full, Suggestions.List, Suggestions.Dismiss, Clean.Execute, Daemon.TriggerScan).
- Two heuristic engines: idle repos (low risk) and cache velocity (medium risk).
- macOS notifications via `osascript`, gated by a configurable severity threshold.

## What does NOT ship in 0.3

- **Notarization & Apple Developer ID signing** — Phase 0.4. The `.app` is ad-hoc signed; first launch needs a Right-click > Open to bypass Gatekeeper.
- **Homebrew tap** — Phase 0.4.
- **Hot-reloadable config** — Phase 0.3.1. Today, the daemon picks up `config.toml` changes on next restart.

## Why

If you're a developer on a Mac, you probably have:

- Dozens of `node_modules` folders eating GBs in repos you haven't touched in months
- A `pnpm` / `yarn` / `go-build` cache that grew past 10 GB without you noticing
- iOS simulators from runtimes you've since uninstalled
- LaunchAgents from apps you forgot you had

Existing cleanup tools brute-force this with cron jobs that delete everything, then break your "I just need to run that one project from six months ago" moment. `noo-noo` instead **watches** activity, **scores** what's safe to clean, and **asks** before removing anything.

## How it works

```
┌─────────────────────────────────────┐
│ noo-nood — pure Go daemon (~12 MB)  │
│   • Lightweight daily scan          │
│   • System pressure watcher         │
│   • SQLite state log                │
│   • Sends macOS notifications       │
└────────────────┬────────────────────┘
                 │ Unix socket (JSON-RPC)
        ┌────────┴────────┐
        ▼                 ▼
┌──────────────┐  ┌────────────────────┐
│ noo-noo CLI  │  │ Noo-Noo.app        │
│ (Go binary)  │  │ (Wails v3 menubar  │
│              │  │  + Svelte UI)      │
└──────────────┘  └────────────────────┘
```

The daemon is the brain. It runs as a `launchd` agent, samples system metrics on a long interval (sub-millisecond reads, no measurable impact), and writes time-series data to a small SQLite file in `~/Library/Application Support/noo-noo/`. Once a day it runs a deeper scan (still light — just `du`, mtime, and `git log`). The CLI and menubar app are thin frontends that query the daemon over a Unix socket.

## Safety philosophy

We treat your filesystem as production data. The defaults reflect that:

- **Diagnose by default. Clean on request.** The daemon never deletes without confirmation.
- **Auto-clean is opt-in and clearly framed as risky.** Enabling it requires a multi-step toggle, only applies to assets we have very high confidence are safe (e.g. `node_modules` in repos with no commits in 90+ days), and writes a complete audit log to `~/Library/Logs/noo-noo/`.
- **Reversible where physically possible.** `launchctl disable` is reversible; `rm -rf node_modules` is not, but `pnpm install` regenerates it.
- **Suggestions cite their evidence.** Every suggestion shows the signal that produced it: "5.2 GB. Last commit 127 days ago. Last `package.json` mtime 89 days ago."
- **No telemetry. No phone-home. No silent updates.**
- **Not in the App Store.** Sandboxing would make most of the useful checks impossible, so we ship outside the App Store and rely on notarization for trust.

## Roadmap

| Version | What ships | Status |
|---|---|---|
| **0.1** | CLI MVP; the three cleanup modes (startup, caches, dev artifacts) from the bash prototype | shipped |
| **0.2** | `noo-nood` daemon under launchd; SQLite store; JSON-RPC IPC; idle-repo + cache-velocity heuristics; first "smart suggestion" notification | shipped |
| **0.3** | `Noo-Noo.app` — Wails v3 menubar with status badge, suggestions submenu, Run Scan Now, Settings window | shipped |
| **0.4** | Notarized release, Homebrew tap, docs site | planned |
| **0.5** | System-pressure-triggered scans, adaptive scheduling, opt-in auto-clean | planned |

## Install

From source (until 0.4 ships the Brew tap):

```sh
git clone https://github.com/FRIKKern/noo-noo.git && cd noo-noo
go install ./cmd/noo-noo ./cmd/noo-nood

# Install the daemon under launchd. Writes
# ~/Library/LaunchAgents/io.noo-noo.d.plist and bootstraps it via
# launchctl, so noo-nood auto-starts at login and survives reboots.
noo-noo install
```

Uninstall with `noo-noo uninstall` (removes the LaunchAgent; leaves the SQLite store in place).

### Menubar app (v0.3, ad-hoc signed)

```sh
make app-package                       # builds build/Noo-Noo.app + ad-hoc signs
cp -R build/Noo-Noo.app /Applications/
open /Applications/Noo-Noo.app         # menubar icon appears; no dock icon
```

First launch: Right-click > Open to bypass Gatekeeper (the bundle is ad-hoc signed; notarization arrives in 0.4). The daemon must be running (`noo-noo install` then `launchctl bootstrap gui/$UID ~/Library/LaunchAgents/io.noo-noo.d.plist`).

## Usage

One-shot CLI (Phase 0.1, unchanged):

```sh
noo-noo report           # full diagnosis
noo-noo dev list         # scan ~/Documents/GitHub for build artifacts
noo-noo caches clean     # wipe known cache directories
noo-noo startup disable  # disable known launchd auto-start bloat
```

Daemon-driven workflow (new in v0.2):

```sh
noo-noo daemon status         # is noo-nood running? for how long?
noo-noo daemon start          # launchctl bootstrap (one-shot)
noo-noo daemon stop           # launchctl bootout

noo-noo suggestions list      # what has the daemon flagged?
noo-noo suggestions dismiss 17  # mark suggestion #17 as handled
```

All destructive commands prompt for confirmation; pass `-y` to skip. `--dry-run` shows what would happen.

## Develop

```sh
git clone https://github.com/FRIKKern/noo-noo.git
cd noo-noo
go test ./...

# Menubar app dev loop (v0.3+):
make app-dev      # vite dev server + go run ./cmd/noo-noo-app
make app-package  # full bundle + ad-hoc sign for local installs
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for project values and code style.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Security issues: see [SECURITY.md](SECURITY.md).

## License

MIT — see [LICENSE](LICENSE).
