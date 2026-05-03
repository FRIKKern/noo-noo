# noo-noo Architecture

> **Status:** Living document — the project's north star. Phase-level decisions, component contracts, and deferred choices live here. Detailed implementation plans live in sibling files (`YYYY-MM-DD-phase-X.Y-*.md`).

**Last updated:** 2026-05-02

---

## 1. Vision

`noo-noo` is a smart, opt-in cleanup daemon for Mac developers. It watches for the digital lint that piles up during dev work — abandoned `node_modules`, runaway caches, idle iOS simulators, dead launchd agents — scores what's safe to remove, and **suggests** cleanups. Nothing destructive runs without the user's explicit approval. Auto-clean exists but is opt-in, multi-step-toggled, and audit-logged.

Inspired by Noo-Noo, the Teletubbies vacuum cleaner.

## 2. Non-goals

- Cross-platform (Linux/Windows). macOS only by design.
- Mac App Store distribution. Sandboxing kills most useful checks.
- Telemetry. None. Ever. Not even opt-in.
- Auto-updates. Users update via `brew upgrade noo-noo` or download a release.
- Replacing CleanMyMac, OmniDiskSweeper, etc. Different audience, different defaults.
- Cleaning user data (Documents, Downloads, Photos). We list large items but never auto-act on them.

## 3. Tech stack & rationale

| Layer | Choice | Why |
|---|---|---|
| Language | Go 1.23+ | Single static binary; pure-Go SQLite available; great cross-arch builds; system programming idioms. |
| State store | `modernc.org/sqlite` | Pure Go (no CGo) → trivial cross-compilation; SQLite for time-series queries. |
| Daemon UI | Wails v3 | Rich web UI option without writing native Cocoa; menubar via `LSUIElement`. |
| Frontend | Svelte 5 | Smallest runtime bundle; compiles to vanilla JS. |
| IPC | Unix socket + `net/jsonrpc` | Stdlib only; one socket file in `~/Library/Application Support/noo-noo/`. |
| CLI flags | Stdlib `flag` + custom subcommand dispatch | Avoids Cobra (200+ KB + transitive deps). |
| Test framework | Stdlib `testing` only | No testify; no mockery. |
| Lint | `golangci-lint` (CI only) | Standard Go ecosystem. |
| Distribution | GitHub releases + Homebrew tap | Native dev workflow; signed + notarized. |
| License | MIT | Permissive, simple. |

## 4. Components

```
┌────────────────────────────────────────────────────────────────┐
│                          User-facing                           │
│                                                                │
│   ┌──────────────┐   ┌──────────────────┐   ┌──────────────┐   │
│   │  noo-noo     │   │ Noo-Noo.app      │   │  launchd     │   │
│   │  (CLI)       │   │ (Wails menubar)  │   │  (scheduler) │   │
│   └──────┬───────┘   └─────────┬────────┘   └──────┬───────┘   │
│          │                     │                   │           │
│          │   Unix socket       │     spawn         │           │
│          │   (JSON-RPC)        │                   │           │
└──────────┼─────────────────────┼───────────────────┼───────────┘
           ▼                     ▼                   ▼
        ┌──────────────────────────────────────────────┐
        │  noo-nood (daemon, pure Go, ~12 MB)          │
        │   • Daily scan loop                          │
        │   • System pressure watcher                  │
        │   • SQLite state log                         │
        │   • Suggestion engine                        │
        └────────────────────┬─────────────────────────┘
                             │
                             ▼
        ┌──────────────────────────────────────────────┐
        │  internal/* (core library, importable by all)│
        │   • core: filesystem, sizes, safety          │
        │   • modules: dev / caches / startup / ...    │
        │   • audit: append-only JSONL log             │
        │   • store: SQLite wrapper                    │
        │   • metrics: vm_stat / sysctl readers        │
        └──────────────────────────────────────────────┘
```

### 4.1 Core library (`internal/`)

The actual cleanup primitives. Importable by daemon, CLI, and Wails app. No CLI/UI/IPC dependencies — the "pure logic" layer.

**Module interface:**

```go
type Module interface {
    Name() string                                  // "dev", "caches", "startup"
    Scan(ctx context.Context) (Report, error)
    Plan(r Report) []Action                        // available cleanup actions
    Apply(ctx context.Context, a Action) (ApplyResult, error)
}

type Report struct {
    Items []Item
    Total int64                                    // bytes
}

type Item struct {
    Path     string
    Size     int64
    Evidence map[string]string                     // e.g. "last_commit_days": "127"
}

type Action struct {
    Module string
    Op     string                                  // "delete", "disable"
    Target string
    Risk   RiskLevel                               // Low | Medium | High
}
```

### 4.2 CLI (`cmd/noo-noo`)

Self-sufficient. Doesn't require the daemon for cleanup commands. Uses the core library directly.

**Phase 0.1 commands:**
- `noo-noo report` — full diagnosis
- `noo-noo startup [list|disable|restore]`
- `noo-noo caches [list|clean]`
- `noo-noo dev [list|clean]`

**Global flags:** `-y` (skip confirm), `--dry-run`, `--json`, `--verbose`.

**Added later:**
- `noo-noo daemon [start|stop|status]` (0.2)
- `noo-noo suggestions` (0.2 — query daemon)
- `noo-noo install` (0.4 — set up launchd agent)

### 4.3 Daemon (`cmd/noo-nood`)

Long-running background process. Runs as `~/Library/LaunchAgents/io.noo-noo.d.plist`.

Responsibilities:
- Sample lightweight system metrics every N minutes (default 5).
- Run a deeper scan once per day (default 03:00 local).
- Write time-series data to SQLite.
- Compute heuristics, generate suggestions, send macOS notifications.
- Serve a Unix socket for queries (used by CLI and Wails app).

**Idle resource budget:** <30 MB RAM, <0.1% CPU averaged over 1 hour.

### 4.4 Menubar app (`cmd/noo-noo-app`)

Wails v3, `LSUIElement: true` (no dock icon). Single status badge in menubar showing reclaimable space. Click → menu: View Report, Clean Now, Settings, Quit. Settings opens a Wails window with Svelte UI for config.

Talks to daemon via Unix socket. Read-only by default — "Clean Now" is a confirmation step, then performs the action.

## 5. Cross-cutting concerns

### 5.1 Safety model

Every destructive action is gated by:

1. **Default-off destructive paths.** Cleanup commands require explicit subcommand (`clean`, `disable`).
2. **Confirmation by default.** `-y` opts out per invocation. Auto-clean is opt-in via config and only applies to assets meeting strict thresholds.
3. **Audit log for everything destructive.** JSONL append-only at `~/Library/Logs/noo-noo/audit-YYYY-MM-DD.jsonl`. Records: timestamp, module, action, target, size, evidence, outcome.
4. **Reversibility where possible.** `launchctl disable` is reversible (we keep an undo log). File deletion is not, but we only delete regenerable assets.
5. **Allowlist-based filesystem operations.** Core lib refuses to delete outside configured root prefixes (e.g. `~/Library/Caches`, `~/Documents/GitHub`). Hard-coded blocklist for `.git`, `.env*`, `/System`, `/Library/...` (system paths).

### 5.2 Configuration

Single TOML file at `~/.config/noo-noo/config.toml`. Defaults baked into binary.

```toml
[scan]
roots = ["~/Documents/GitHub"]
daily_at = "03:00"

[caches]
targets = [
    "~/Library/Caches/Yarn",
    "~/Library/Caches/pnpm",
    "~/Library/Caches/go-build",
    "~/Library/Caches/Cursor",
]

[startup]
allow = ["com.docker.*", "com.microsoft.*"]   # never disable

[auto_clean]
enabled = false
threshold_days = 90
require_acknowledgment = true
```

### 5.3 Telemetry

None. The daemon does not make outbound network connections except:

- Notification scheduling (local, no network).
- Optional: `noo-noo update-check` command, manually invoked, hits GitHub Releases API. Off by default.

### 5.4 Audit log format

`~/Library/Logs/noo-noo/audit-YYYY-MM-DD.jsonl`, append-only, one JSON object per line:

```json
{"ts":"2026-05-02T14:30:00Z","module":"dev","op":"delete","target":"/Users/frikkjarl/Documents/GitHub/old-repo/node_modules","size":2147483648,"evidence":{"last_commit_days":"127"},"outcome":"ok"}
```

Files are never edited or rotated by `noo-noo`; rotation is via `newsyslog` or user discretion.

## 6. Phase roadmap

### Phase 0.1 — CLI MVP (target: 1 week)

**Deliverables:**
- Core lib with three modules ported from bash (`startup`, `caches`, `dev`).
- CLI with `report`, `startup`, `caches`, `dev` commands.
- Audit log writer.
- Unit tests, golangci-lint clean, gofmt clean.

**Out of scope:** daemon, SQLite, IPC, Wails app, launchd plist, Brew tap.

**Success criteria:** the CLI does what the bash scripts did, with structured output, machine-readable JSON, and an audit log.

**Detailed plan:** `2026-05-02-phase-0.1-cli.md`

### Phase 0.2 — Daemon + SQLite + heuristics (target: 1 week)

**Deliverables:**
- `noo-nood` daemon with daily scan loop.
- SQLite state store with cache-size time series and action history.
- IPC: Unix socket + JSON-RPC for CLI ↔ daemon.
- Smart heuristics: repo idleness scoring, cache growth velocity.
- `noo-noo daemon` and `noo-noo suggestions` CLI commands.

**Depends on:** Phase 0.1 modules + audit log.

**Success criteria:** daemon runs in foreground for a week, generates ≥1 useful suggestion, idle resource budget met.

### Phase 0.3 — Wails menubar app (target: 1 week)

**Deliverables:**
- `Noo-Noo.app` with menubar status badge.
- Click-to-clean menu items.
- Svelte settings panel.
- Wails build pipeline.

**Depends on:** Phase 0.2 IPC (Unix socket + protocol).

**Success criteria:** menubar shows reclaimable space; clicking suggestions performs confirmed cleanups.

### Phase 0.4 — Notarized release + Brew tap (target: 3-5 days)

**Deliverables:**
- Apple Developer ID code-signing in CI.
- Notarization (CI or manual handoff).
- `brew install frikkjarl/tap/noo-noo` works.
- `noo-noo install` command sets up launchd agent.
- v0.4.0 release on GitHub.
- Homebrew tap repo (`frikkjarl/homebrew-tap`).
- Docs site (GitHub Pages from `docs/`).

**Depends on:** Phase 0.3 working app.

**Success criteria:** a stranger on macOS can `brew install` and have a working menubar app within 60 seconds.

### Phase 0.5 — Pressure-triggered scans (target: 1 week)

**Deliverables:**
- Real-time `vm_stat` / `sysctl` watcher in daemon.
- Adaptive scheduling (more frequent scans when free disk < 20 GB).
- Notification when high-CPU process detected.
- Optional: integrate with Stats.app via JSON output.

**Depends on:** Phase 0.2 daemon, Phase 0.3 notification UX.

### Beyond 0.5 — backlog (rough priority)

- Per-project rules (`.noo-noo.toml` opt-out file in repo root).
- Scan reports as PDF/Markdown export.
- Time Machine integration (skip cleanup if recent backup is stale).
- Plugin system for cleanup modules.
- Teams: shared config across a fleet of Macs (probably never — scope creep).

## 7. Decisions deferred

| Decision | When |
|---|---|
| `net/rpc` (gob) vs JSON-RPC for IPC | Phase 0.2, when we write the protocol. |
| Notification library vs shelling out to `osascript display notification` | Phase 0.2, when we add suggestions. |
| Wails v3 final API for menubar | Phase 0.3, pinned to a release at that point. |
| Single binary with subcommands vs separate `noo-noo-cli` | Phase 0.4, when sizing matters for distribution. |
| GUI-driven config editor vs config-file-only | Phase 0.4. |
| Sparkle update framework vs Brew-only | Phase 0.4 (likely Brew-only — no Sparkle). |

## 8. Open questions

- **Code-signing certificate:** does the maintainer have an Apple Developer ID? If not, distribution defaults to "ad-hoc signed; users right-click → Open the first time."
- **Homebrew tap or homebrew-core?** Tap is faster to ship; core is more discoverable. Tap first, core if/when we have ≥30 days of stable releases and ≥100 GitHub stars.
- **License header in source files?** SPDX comments add noise; LICENSE at repo root is sufficient for MIT. Decision: no per-file headers.

## 9. References

- Original bash prototypes (reference implementations for Phase 0.1 ports):
  - `~/startup-cleanup.sh`
  - `~/cache-cleanup.sh`
  - `~/deep-cleanup.sh`
- Wails v3 docs: https://v3alpha.wails.io/
- modernc.org/sqlite: https://pkg.go.dev/modernc.org/sqlite
- Apple notarization: https://developer.apple.com/documentation/security/notarizing_macos_software_before_distribution
