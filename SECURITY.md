# Security policy

## Reporting a vulnerability

Email **frikk@guerrilla.no** with a description and steps to reproduce. Please do not open a public issue for security reports.

You can expect an initial response within 7 days.

## Inherent risk profile

`noo-noo` is a system utility that:

- Reads your filesystem, including code repositories and home directory contents
- Disables and re-enables `launchd` agents (with `sudo`, when invoked)
- Deletes regenerable assets (`node_modules`, build artifacts, caches) on user request
- Writes a SQLite state log to `~/Library/Application Support/noo-noo/`
- Sends macOS user notifications

It does **not**:

- Delete anything without explicit user confirmation (in the default configuration)
- Send any data over the network
- Auto-update itself
- Embed third-party telemetry

Auto-clean mode (opt-in) is the only path that performs deletion without per-action confirmation, and even then only for assets meeting strict idleness thresholds documented in `README.md`. Enabling it requires a multi-step toggle and writes a complete audit log.
