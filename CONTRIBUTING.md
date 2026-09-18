# Contributing

Thanks for helping. A few things make a change easy to take in.

## Setup

```bash
sudo apt install build-essential pkg-config libgtk-3-dev libgirepository1.0-dev   # plus Go 1.26+
make build   # the first build compiles the GTK bindings and takes several minutes
make test    # the core, with the race detector
gonotch demo # or bin/gonotch demo: the UI with made-up data, no accounts needed
```

## Where things live

| | |
|---|---|
| `cmd/gonotch` | The app and its subcommands |
| `cmd/gonotch-hook` | What Claude Code's hooks run — stays tiny and cgo-free |
| `internal/providers/*` | One package per vendor; `demo` holds the made-up data |
| `internal/sessions` | Claude Code session states, from hooks and transcripts |
| `internal/app`, `internal/server` | The glue, and the Unix-socket endpoint |
| `internal/logs` | The log file: `~/.local/state/gonotch/gonotch.log`, rotated at 1 MiB |
| `internal/ui` | GTK 3 + Cairo; the only package with cgo |

## Guidelines

- **Keep the core cgo-free.** Anything outside `internal/ui` must build and test without GTK.
- **A provider borrows, never manages.** Read the credential the vendor's own tool keeps; never
  write, refresh or log it. On failure keep the last reading marked stale — never invent a number.
- **Respect rate limits.** Back off on 429 and honour `Retry-After`.
- **Errors go to the log, not to stderr.** Use `log/slog` (`internal/logs` makes it the app's
  log file): Error for what stops a feature, Warn for what degrades one, and a failure that repeats
  logged once. A provider's failures are already logged by its `Runner` from the snapshot's note.
- **Test behaviour, not wiring.** New logic in the core comes with a test; `go test -race` must pass.
- **UI changes:** attach a screenshot, and run `make screenshots` if the README's images change.
  It renders them in a container from the real drawing code (`internal/ui/snapshot_test.go`).

## Adding a provider

Implement `providers.Provider` (`internal/providers/provider.go`) in a new package, declare which
window the ring shows (`Snapshot.Headline`) and, if any, the weekly one (`Snapshot.Weekly`), add a
mark to `internal/ui/glyphs/` with its licence in `NOTICE.md`, and register it in `app.New`.
