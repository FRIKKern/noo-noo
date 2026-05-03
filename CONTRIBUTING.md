# Contributing to noo-noo

Thanks for your interest. The project is in alpha and the architecture is still being established. Outside contributions are not yet accepted; that opens up with the v0.1 release.

If you want to follow along: star the repo, watch releases, and feel free to open an issue with ideas — but please hold PRs until v0.1 ships.

## Project values

- **No bloat.** Every dependency is profiled and justified.
- **Safety first.** Anything destructive is opt-in, reversible where possible, and audit-logged.
- **No telemetry. Ever.**
- **macOS-only by design.** We use Mac-specific APIs heavily.

## Code style

- Go: standard `gofmt` + `go vet`. CI enforces both.
- Commit messages: [Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `docs:`, `chore:` …) so changelogs can be generated.
- Branch naming: `feat/<topic>`, `fix/<topic>`.

## Development setup

```sh
go test ./...
```

The Wails v3 + Svelte frontend setup arrives in v0.3.
