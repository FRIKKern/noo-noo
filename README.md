# noo-noo — v0.5.0 (pressure-triggered scans + opt-in auto-clean)

[![CI](https://github.com/FRIKKern/noo-noo/actions/workflows/ci.yml/badge.svg)](https://github.com/FRIKKern/noo-noo/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/FRIKKern/noo-noo)](https://github.com/FRIKKern/noo-noo/releases)
[![Brew Tap](https://img.shields.io/badge/brew-FRIKKern%2Ftap%2Fnoo--noo-orange)](https://github.com/FRIKKern/homebrew-tap)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Smart cleanup for Mac developers. Universal Mach-O binaries (amd64 + arm64), menubar app + CLI, installable in one line:

```sh
brew install FRIKKern/tap/noo-noo
```

> Inspired by [Noo-Noo](https://teletubbies.fandom.com/wiki/Noo-Noo), the Teletubbies vacuum cleaner. `noo-noo` watches your Mac for the sneaky stuff that piles up while you work — abandoned `node_modules`, runaway caches, idle iOS simulators, dead launchd agents — and quietly suggests cleanups when (and only when) the data says you'll get something back. Nothing destructive runs without your explicit approval.

## What's new in 0.5

- **Pressure-triggered scans.** `noo-nood` now watches memory + free-disk
  pressure in real time and fires an out-of-band scan when the machine
  starts to fill up — instead of waiting for the daily 03:00 tick.
- **Opt-in auto-clean.** With your explicit consent, the daemon can delete
  qualifying assets on its own. Default-off, multi-gate safety design,
  audited. See [the section below](#auto-clean-opt-in-default-off).
- **`noo-noo auto-clean`** CLI subcommand: `enable | disable | status | history`.
- **`[pressure]` and `[auto_clean]`** new TOML config sections with safe defaults.

## Pressure-triggered scans (new in 0.5)

`noo-noo` watches memory and free-disk pressure in real time. When sustained-high
pressure is detected (default: 60 s above the threshold), the daemon fires an
out-of-band scan instead of waiting for the daily tick. Configure via
`[pressure]` in `~/.config/noo-noo/config.toml`:

```toml
[pressure]
sample_interval_seconds = 15
debounce_seconds = 60
mem_high_ratio = 0.85
disk_low_gb = 10
```

## Auto-clean (opt-in, default off)

`noo-noo` can delete qualifying assets on its own — but only after you opt
in with a deliberate friction step:

```sh
noo-noo auto-clean enable --i-understand-the-risks
```

By default, auto-clean is **off**. When enabled, it only acts on
suggestions that meet **all** of these gates:

- module is in `auto_clean.modules_allowed` (default `["dev"]` — node_modules etc.)
- repo is >= `auto_clean.min_idle_days` days idle (default 90)
- target size is >= `auto_clean.min_size_mb` MB (default 1024)
- per-tick budget remaining (default cap: 10 GB/tick)
- safety guard at delete-time (path resolves under configured roots; not a symlink out)
- only on the daily tick — pressure-triggered scans never auto-clean

Every auto-clean attempt is logged. View history:

```sh
noo-noo auto-clean history
```

Disable at any time (kill switch, takes effect within 60 s):

```sh
noo-noo auto-clean disable
```

See the [Phase 0.5 plan](docs/2026-05-03-noo-noo-phase-0.5-pressure-and-autoclean.html)
for the full safety design.

## What's new in 0.4

- **`brew install` works.** GitHub Actions builds and ships universal binaries on every tag push; the [Homebrew tap](https://github.com/FRIKKern/homebrew-tap) at `FRIKKern/homebrew-tap` picks up the new version automatically.
- **`Noo-Noo-vX.Y.Z.dmg`** — drag-install disk image attached to every GitHub Release, ad-hoc signed (right-click → Open on first launch).
- **`CHANGELOG.md`** — per-version history in keep-a-changelog format, used as the release-notes source.
- **`scripts/release.sh --dry-run`** — local smoke test of the release pipeline without pushing anything.

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

### Quick install (recommended)

```sh
brew tap FRIKKern/tap
brew install noo-noo
open -a Noo-Noo
```

This installs both `Noo-Noo.app` (in `/Applications`) and the `noo-noo`
+ `noo-nood` CLI binaries (in `/opt/homebrew/bin`). The Brew Cask
postflight hook automatically registers the LaunchAgent so the daemon
runs in the background.

**Gatekeeper note:** noo-noo is currently ad-hoc signed (Apple
Developer ID + notarization land in 0.4.1). The first time you launch
`Noo-Noo.app`, macOS will block it. To allow it: right-click
`/Applications/Noo-Noo.app`, choose "Open", and click "Open" in the
confirmation dialog. macOS will remember this choice.

### Headless install (CLI only, e.g. CI Mac mini)

```sh
brew install --formula FRIKKern/tap/noo-noo
brew services start noo-noo
```

### Build from source

```sh
git clone https://github.com/FRIKKern/noo-noo.git
cd noo-noo
make app-package
cp -R build/Noo-Noo.app /Applications/
open /Applications/Noo-Noo.app
```

### Direct download

Pre-built artifacts are attached to every
[GitHub Release](https://github.com/FRIKKern/noo-noo/releases):

- `Noo-Noo-vX.Y.Z.dmg` — drag-install disk image
- `Noo-Noo.app.zip` — bare app bundle
- `noo-noo`, `noo-nood` — universal Mach-O CLI binaries
- `noo-noo-vX.Y.Z-darwin.tar.gz` — both CLIs in a tarball
- `checksums.txt` — SHA-256 manifest, verify with `shasum -c`

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

## Maintainer notes

### One-time tap repo bootstrap

The Brew tap lives in a separate repo (`FRIKKern/homebrew-tap`). To
create it the first time:

```sh
# 1. create the tap repo
gh repo create FRIKKern/homebrew-tap --public --description \
  "Homebrew tap for noo-noo and friends" --confirm

# 2. seed it with placeholder formulas (CI will overwrite on first release)
TAP_DIR="$(mktemp -d)"
gh repo clone FRIKKern/homebrew-tap "$TAP_DIR"
cd "$TAP_DIR"
mkdir -p Casks Formula
cat > README.md <<'EOF'
# FRIKKern/homebrew-tap

Homebrew tap for noo-noo (https://github.com/FRIKKern/noo-noo).

## Usage

```sh
brew tap FRIKKern/tap
brew install noo-noo            # GUI app (Cask)
brew install --formula \
  FRIKKern/tap/noo-noo          # CLI only (Formula, headless servers)
```
EOF
cat > Casks/noo-noo.rb <<'EOF'
# placeholder; replaced by CI on first release.
cask "noo-noo" do
  version "0.0.0"
  sha256 :no_check
  url "https://example.com/noo-noo"
  name "Noo-Noo (placeholder)"
  desc "Awaiting first release"
  homepage "https://github.com/FRIKKern/noo-noo"
  app "Noo-Noo.app"
end
EOF
cat > Formula/noo-noo.rb <<'EOF'
class NooNoo < Formula
  desc "Placeholder; awaiting first release"
  homepage "https://github.com/FRIKKern/noo-noo"
  url "https://example.com/noo-noo"
  version "0.0.0"
  sha256 :no_check
  def install; end
end
EOF
git add .
git commit -m "chore: bootstrap tap structure"
git push origin main

# 3. create a fine-grained PAT for CI to push updates
#    Settings → Developer settings → Personal access tokens
#    → Fine-grained tokens → Generate new token
#    Repository access: Only select repositories → FRIKKern/homebrew-tap
#    Permissions: Contents -> Read and write
#    Copy the token (starts with github_pat_...)

# 4. add the PAT as a secret in the source repo
gh secret set TAP_TOKEN --repo FRIKKern/noo-noo
# (paste the PAT when prompted)
```

After these four steps, every `git push origin v*.*.*` from the source
repo triggers `release.yml`, which builds artifacts, creates a GitHub
Release, renders the Brew templates, and pushes them to
`FRIKKern/homebrew-tap`.
