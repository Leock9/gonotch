# gonotch

A Linux-first notch for coding assistants, in Go: a small black pill on the screen edge with one ring
per tool, showing how much of its usage limit is gone and, for Claude Code, whether a session is
working (a turning arc) or waiting on you (a yellow pulse).

Based on [Codenotch](https://github.com/vinzdg/codenotch) (MIT) — its design, measures and palette,
and the providers' documented behaviour. The code is a new implementation.

| Ring | Where the number comes from |
|---|---|
| **Claude** | Claude Code's own OAuth credential in `~/.claude/.credentials.json`, against the endpoint its `/usage` asks. Current session + weekly ring. The token is renewed by running `claude -p` shortly before it expires. |
| **Codex** | The Codex CLI's `~/.codex/auth.json` against ChatGPT's usage endpoint; falls back to the limits the last run wrote into `~/.codex/sessions`. |
| **Cursor** | The editor's own session in `~/.config/Cursor/User/globalStorage/state.vscdb`, against `cursor.com/api/usage-summary`. |

Credentials are borrowed read-only from the tools that own them; gonotch never signs in, refreshes or
writes them, and never logs them. A tool that is not installed gets no ring.

## Build

```sh
sudo apt install libgtk-3-dev libgirepository1.0-dev librsvg2-common   # Ubuntu/Debian
make build          # bin/gonotch and bin/gonotch-hook
make install        # into ~/.local/bin
```

Go 1.24+. The GTK bindings are [gotk4](https://github.com/diamondburned/gotk4) **v0.2.2**, the last
release generated against a GLib as old as Ubuntu 22.04's 2.72; newer ones need GLib 2.76+.

If Homebrew's `ld` comes before `/usr/bin` on your `PATH`, the link fails with undefined references to
X11/epoxy/freetype symbols. The Makefile passes `-B/usr/bin/` so gcc uses the system linker.

## Use

```sh
gonotch                  # the notch, on the right edge of the primary monitor
gonotch status           # the readings in a terminal (--json for a status bar)
gonotch doctor           # what each provider finds on this machine
gonotch install-hooks    # Claude Code hooks → precise working / waiting / done states
gonotch autostart on     # start at login
```

- **Hover** a ring for its limit windows, reset times and (Claude) the sessions.
- **Click** a ring to read that provider again; **click a session** to bring its terminal forward.
- **Right-click** for refresh, the provider's usage page, hooks and quit.

Without hooks, session states are inferred from Claude Code's transcripts in `~/.claude/projects`.
`install-hooks` edits `~/.claude/settings.json` in place — only its own entries, keeping the rest of
the file's order and format — and writes a backup first. `uninstall-hooks` removes them again.

The local server listens on `127.0.0.1:48777` (`port` in `~/.config/gonotch/config.json`) and refuses
browser requests. Readings persist in `~/.local/state/gonotch`.

## Desktop support

The notch is an override-redirect X11 window with an input shape, so clicks outside the pill go to the
window behind it. That works on any X11 desktop, and on Wayland through XWayland (gonotch sets
`GDK_BACKEND=x11` itself). A native Wayland backend would need layer-shell (KDE, Hyprland, Sway); GNOME
on Wayland would need a Shell extension.

## Notes for contributors

- `internal/` outside `internal/ui` has no cgo: `make test` runs without compiling GTK.
- gotk4 v0.2.2 takes a reference on each frame's `cairo_t` and drops it in a GC finalizer, off the GTK
  thread. When that is the last reference, the window's Xlib surface is finished from another thread
  and the main loop hangs forever in `XSync`. The draw handler releases it on the GTK thread instead
  (`release` in `internal/ui/ui.go`); keep it that way.
