#!/bin/sh
# Prints a release's notes: its section of CHANGELOG.md, then the link the changelog keeps for it.
# Fails when the version has no section with something in it, so no release goes out without notes.
#
#   scripts/release-notes.sh v0.2.0
set -eu

v=${1:?usage: release-notes.sh vX.Y.Z}
v=${v#v}
changelog=${CHANGELOG:-CHANGELOG.md}

notes=$(awk -v head="## [$v]" '
	index($0, head) == 1 { on = 1; next }
	on && /^## \[/ { exit }
	on { lines[++n] = $0 }
	END {
		first = 1; while (first <= n && lines[first] ~ /^[[:space:]]*$/) first++
		last = n; while (last >= first && lines[last] ~ /^[[:space:]]*$/) last--
		for (i = first; i <= last; i++) print lines[i]
	}
' "$changelog")

if [ -z "$notes" ]; then
	echo "release-notes: $changelog has no notes for $v — add a \"## [$v] - YYYY-MM-DD\" section" >&2
	exit 1
fi
printf '%s\n' "$notes"
link=$(awk -v ref="[$v]: " 'index($0, ref) == 1 { print substr($0, length(ref) + 1) }' "$changelog")
if [ -n "$link" ]; then
	printf '\n**Full Changelog**: %s\n' "$link"
fi
