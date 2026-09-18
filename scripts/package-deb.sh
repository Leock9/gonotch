#!/bin/bash
# package-deb.sh VERSION BINDIR OUTDIR: a .deb holding gonotch and gonotch-hook in /usr/bin.
set -euo pipefail
# dpkg-deb wants 0755 directories whatever the builder's umask (a restrictive 027 made them 0750)
umask 022
version=${1#v}; bindir=$2; out=$3
# Debian versions start with a digit; a build between tags (5d47da3-dirty) becomes 0.0.0~git…
[[ $version =~ ^[0-9] ]] || version="0.0.0~git${version}"
root=$(mktemp -d)
chmod 755 "$root"
trap 'rm -rf "$root"' EXIT
install -Dm755 "$bindir/gonotch" "$root/usr/bin/gonotch"
install -Dm755 "$bindir/gonotch-hook" "$root/usr/bin/gonotch-hook"
install -Dm644 LICENSE "$root/usr/share/doc/gonotch/copyright"
install -Dm644 /dev/stdin "$root/usr/share/applications/gonotch.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=gonotch
Comment=How much of your AI coding limits is left, in a notch on the screen edge
Exec=gonotch
Icon=utilities-system-monitor
Terminal=false
Categories=Utility;Development;
DESKTOP
mkdir -p "$root/DEBIAN"
cat > "$root/DEBIAN/control" <<CONTROL
Package: gonotch
Version: $version
Architecture: amd64
Maintainer: Leonardo Khouri <leonardock9@gmail.com>
Depends: libgtk-3-0t64 | libgtk-3-0, librsvg2-common
Recommends: xdg-utils
Section: utils
Priority: optional
Homepage: https://github.com/leock9/gonotch
Description: AI coding usage limits in a notch on the screen edge
 A small black notch on the edge of the desktop with one ring per coding
 assistant (Claude Code, Codex, Cursor, GitHub Copilot), coloured by how much
 of its usage limit is gone, and whether Claude Code is working or waiting on
 you.
CONTROL
dpkg-deb --build --root-owner-group "$root" "$out/gonotch_${version}_amd64.deb" >/dev/null
echo "$out/gonotch_${version}_amd64.deb"
