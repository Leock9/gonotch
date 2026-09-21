# Changelog

Every release of gonotch, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/). A release's notes on GitHub are its section here: the
release workflow refuses a tag without one.

## [Unreleased]

### Added

- **Update notice.** Once a day gonotch asks GitHub which release is the latest. A newer one is
  announced once with a desktop notification, and stays at the top of the notch's right-click menu:
  *Update to vX.Y.Z* and *What's new in vX.Y.Z*. Switch it off under Settings › Updates.
- **`gonotch update`** downloads the latest release, checks it against `SHA256SUMS`, replaces
  `gonotch` and `gonotch-hook` in place and restarts a running notch on the new binary.
  `gonotch update --check` only says whether there is one. Installed from the `.deb`, it prints the
  `apt` commands instead.
- `gonotch version` and `gonotch status` mention a newer release once one has been found.
- Release notes: each release's notes on GitHub now come from this changelog.

### Changed

- `gonotch demo` shows a GitHub Copilot ring too, and so do the README's images.

## [0.2.0] - 2026-09-18

### Added

- **GitHub Copilot ring.** Copilot's monthly quotas: premium requests on paid plans, chat and code
  completions on Free. The token is borrowed read-only from Copilot's own editor plugins
  (`~/.config/github-copilot/apps.json`), else from `gh`; the keyring is asked only when those
  fail. The ring shows only where Copilot itself is installed.
- **A log file for errors**: `~/.local/state/gonotch/gonotch.log`, kept across runs and rotated at
  1 MiB. It records provider failures (once each, and the recovery), crashes with their stack trace,
  GTK warnings, hook failures and failed Claude token renewals. `gonotch log` prints its end,
  `gonotch doctor` its path, and the notch's menu has *Open log*.

### Changed

- `gonotch status` aligns longer provider names and translates the second ring's label.

### Upgrading

Run the installer again, then quit the notch (right-click › Quit) and start it.

## [0.1.0] - 2026-09-18

The first release: a Linux-first notch, in Go and GTK 3, after [Codenotch](https://github.com/vinzdg/codenotch).

### Added

- One ring per coding assistant — **Claude Code**, **Codex** and **Cursor** — coloured by how much
  of its limit is gone, with a thinner ring for the weekly limit. Credentials are borrowed
  read-only from the tools that own them.
- Claude Code's sessions on its ring: a turning arc while one works, a yellow pulse while one waits
  on you, from Claude Code's hooks (`gonotch install-hooks`) or, without them, its transcripts.
- A hover card with each limit window, its reset time and the running sessions; clicking a session
  brings its terminal forward.
- A settings window: which rings show and in what order, the edge and height, and auto-hide into a
  thin strip on the edge.
- `gonotch status [--json]` for a terminal or a status bar, `gonotch doctor`, `gonotch demo`,
  `gonotch autostart on|off`.
- An installer (`scripts/install.sh`) into `~/.local/bin` and a `.deb`, built on Ubuntu 22.04 and
  tested on 22.04, 24.04, 25.10 and 26.04, on X11 and on GNOME Wayland through XWayland.

[Unreleased]: https://github.com/leock9/gonotch/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/leock9/gonotch/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/leock9/gonotch/commits/v0.1.0
