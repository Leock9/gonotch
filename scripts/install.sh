#!/bin/sh
# Installs gonotch from its GitHub release into ~/.local/bin — no root, no Go toolchain:
#
#   curl -fsSL https://raw.githubusercontent.com/leock9/gonotch/main/scripts/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/leock9/gonotch/main/scripts/install.sh | sh -s -- --hooks --autostart
#
# Options:
#   --hooks            wire Claude Code's hooks to gonotch (precise working / waiting states)
#   --autostart        start gonotch at login
#   --version vX.Y.Z   a given release instead of the latest
#   --prefix DIR       install into DIR/bin (default ~/.local)
#   --uninstall        remove the binaries, the hooks and the autostart entry
#
# GONOTCH_TARBALL=path/to/gonotch-linux-amd64.tar.gz installs a local build instead of downloading.
set -eu

REPO=leock9/gonotch
ASSET=gonotch-linux-amd64.tar.gz
PREFIX=${PREFIX:-$HOME/.local}
VERSION=latest
HOOKS=0
AUTOSTART=0
UNINSTALL=0

say() { printf '\033[1m%s\033[0m\n' "$*"; }
warn() { printf '\033[33m! %s\033[0m\n' "$*" >&2; }
die() { printf '\033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
	case $1 in
	--hooks) HOOKS=1 ;;
	--autostart) AUTOSTART=1 ;;
	--uninstall) UNINSTALL=1 ;;
	--version) [ $# -ge 2 ] || die "--version needs a tag, e.g. v0.1.0"; VERSION=$2; shift ;;
	--prefix) [ $# -ge 2 ] || die "--prefix needs a directory"; PREFIX=$2; shift ;;
	-h | --help) sed -n '2,15p' "$0" 2>/dev/null | sed 's/^# \{0,1\}//'; exit 0 ;;
	*) die "unknown option: $1" ;;
	esac
	shift
done
BINDIR=$PREFIX/bin

if [ "$UNINSTALL" = 1 ]; then
	if [ -x "$BINDIR/gonotch" ]; then
		pkill -x gonotch 2>/dev/null || true
		"$BINDIR/gonotch" uninstall-hooks >/dev/null 2>&1 || true
		"$BINDIR/gonotch" autostart off >/dev/null 2>&1 || true
	fi
	rm -f "$BINDIR/gonotch" "$BINDIR/gonotch-hook"
	say "gonotch removed from $BINDIR (settings stay in ~/.config/gonotch)"
	exit 0
fi

[ "$(uname -s)" = Linux ] || die "gonotch runs on Linux only"
[ "$(uname -m)" = x86_64 ] || die "only x86_64 builds are published; build from source: https://github.com/$REPO#from-source"
if ! ldconfig -p 2>/dev/null | grep -q 'libgtk-3\.so\.0'; then
	warn "GTK 3 was not found. On Ubuntu 24.04 or newer: sudo apt install libgtk-3-0t64 — on 22.04: sudo apt install libgtk-3-0"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

if [ -n "${GONOTCH_TARBALL:-}" ]; then
	say "Installing from $GONOTCH_TARBALL"
	cp "$GONOTCH_TARBALL" "$tmp/$ASSET"
else
	command -v curl >/dev/null 2>&1 || die "curl is needed: sudo apt install curl"
	if [ "$VERSION" = latest ]; then
		base=https://github.com/$REPO/releases/latest/download
	else
		base=https://github.com/$REPO/releases/download/$VERSION
	fi
	say "Downloading gonotch ($VERSION)"
	curl -fsSL "$base/$ASSET" -o "$tmp/$ASSET" || die "download failed: $base/$ASSET"
	curl -fsSL "$base/SHA256SUMS" -o "$tmp/SHA256SUMS" || die "download failed: $base/SHA256SUMS"
	(cd "$tmp" && grep " $ASSET\$" SHA256SUMS | sha256sum -c --status) || die "checksum mismatch for $ASSET"
fi

tar -xzf "$tmp/$ASSET" -C "$tmp"
mkdir -p "$BINDIR"
running=0
if pgrep -x gonotch >/dev/null 2>&1; then running=1; fi
install -m 755 "$tmp/gonotch-linux-amd64/gonotch" "$BINDIR/gonotch"
install -m 755 "$tmp/gonotch-linux-amd64/gonotch-hook" "$BINDIR/gonotch-hook"
say "✓ $("$BINDIR/gonotch" version) installed in $BINDIR"

[ "$HOOKS" = 1 ] && "$BINDIR/gonotch" install-hooks
[ "$AUTOSTART" = 1 ] && "$BINDIR/gonotch" autostart on

case ":$PATH:" in
*":$BINDIR:"*) ;;
*) warn "$BINDIR is not on your PATH; add it, e.g.: echo 'export PATH=\"$BINDIR:\$PATH\"' >> ~/.profile" ;;
esac

echo
if [ "$running" = 1 ]; then
	echo "gonotch is running the previous version — quit it (right-click › Quit) and start it again."
else
	echo "Start it:            gonotch            (or try it first: gonotch demo)"
fi
[ "$HOOKS" = 1 ] || echo "Claude Code hooks:   gonotch install-hooks"
[ "$AUTOSTART" = 1 ] || echo "Start at login:      gonotch autostart on"
