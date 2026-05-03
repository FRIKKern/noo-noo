# noo-noo

> A lightweight, smart, opt-in cleanup daemon for Mac developers — inspired by [Noo-Noo](https://teletubbies.fandom.com/wiki/Noo-Noo), the Teletubbies vacuum cleaner.

[![Status: alpha](https://img.shields.io/badge/status-alpha-orange.svg)]()
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go: 1.23+](https://img.shields.io/badge/go-1.23+-00ADD8.svg)](https://go.dev)

`noo-noo` watches your Mac for the sneaky stuff that piles up while you work — abandoned `node_modules`, runaway caches, idle iOS simulators, dead launchd agents — and quietly suggests cleanups when (and only when) the data says you'll get something back. Nothing destructive runs without your explicit approval.

## Status

**Alpha — not yet usable.** This repo currently contains the scaffolding, vision, and roadmap. The first installable release is targeted for v0.1.

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
| **0.1** | Daemon + CLI; daily scan; the three cleanup modes (startup, caches, dev artifacts) from the bash prototype | planned |
| **0.2** | Smart heuristics: repo-idleness scoring, cache-velocity tracking, first "smart suggestion" notification | planned |
| **0.3** | `Noo-Noo.app` — Wails v3 menubar with status badge | planned |
| **0.4** | Notarized release, Homebrew tap, docs site | planned |
| **0.5** | System-pressure-triggered scans, adaptive scheduling | planned |

## Install

Not yet — see [Status](#status). Once 0.1 ships:

```sh
brew install frikkjarl/noo-noo/noo-noo
```

## Develop

```sh
git clone https://github.com/frikkjarl/noo-noo.git
cd noo-noo
go test ./...
```

The Wails v3 frontend will land in v0.3. See [CONTRIBUTING.md](CONTRIBUTING.md) for project values and code style.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Security issues: see [SECURITY.md](SECURITY.md).

## License

MIT — see [LICENSE](LICENSE).
