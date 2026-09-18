#!/bin/bash
# Renders docs/assets from the real drawing code: the notch as transparent PNG frames (the `snapshot`
# test in internal/ui), composed over a wallpaper. Runs inside scripts/screenshots/Dockerfile.
set -euo pipefail
cd /src
OUT=docs/assets; FRAMES=/tmp/frames; mkdir -p $OUT $FRAMES
export HOME=/tmp/home XDG_RUNTIME_DIR=/tmp/run LANG=en_US.UTF-8 GOCACHE=/cache/build GOMODCACHE=/cache/mod
mkdir -p $HOME $XDG_RUNTIME_DIR && chmod 700 $XDG_RUNTIME_DIR

go build -o /tmp/gonotch ./cmd/gonotch
Xvfb :99 -screen 0 1280x800x24 >/dev/null 2>&1 &
sleep 1
export DISPLAY=:99
GONOTCH_SNAPSHOTS=$FRAMES go test -tags snapshot -run Snapshots -count=1 ./internal/ui/

# Wallpapers: the banner's carries the gopher and the title; the rest are the plain gradient
F=/usr/share/fonts/truetype/dejavu
convert -size 800x1280 gradient:'#0b0f1e'-'#5b21b6' -rotate -90 /tmp/plain.png
rsvg-convert -h 330 scripts/screenshots/gopher-headlamp.svg -o /tmp/gopher.png
convert /tmp/plain.png \
  \( -size 1280x800 radial-gradient:'#ffffff30'-'#00000000' -geometry -380-160 \) -compose over -composite \
  /tmp/gopher.png -geometry +70+232 -compose over -composite \
  -font $F/DejaVuSans-Bold.ttf -pointsize 104 -fill white -annotate +410+345 'gonotch' \
  -font $F/DejaVuSans.ttf -pointsize 33 -fill '#e2e8f0' -annotate +414+405 'Your AI coding limits, one glance away.' \
  -font $F/DejaVuSans.ttf -pointsize 23 -fill '#c4b5fd' -annotate +416+455 'Claude Code · Codex · Cursor  —  a notch for Linux, written in Go' \
  /tmp/banner-wall.png

# over FRAME CROP OUT [WALLPAPER]: the notch window where it sits on a 1280x800 screen
over() { convert "${4:-/tmp/plain.png}" "$1" -geometry +920+75 -composite -crop "$2" +repage "$3"; }
over $FRAMES/notch.png 1280x420+0+190 $OUT/banner.png /tmp/banner-wall.png
over $FRAMES/notch.png 150x440+1130+180 /tmp/notch.png
over $FRAMES/waiting.png 150x440+1130+180 /tmp/waiting.png
over $FRAMES/tucked.png 150x440+1130+180 /tmp/tucked.png
over $FRAMES/card-claude.png 440x600+840+100 $OUT/card.png
convert /tmp/notch.png /tmp/waiting.png /tmp/tucked.png -background '#0b0f1e' -splice 10x0 +append -chop 10x0 $OUT/states.png

gif() { # gif OUT FPS COLOURS: /tmp/gif/%03d.png into a looping GIF
  ffmpeg -loglevel error -y -framerate "$2" -i /tmp/gif/%03d.png \
    -vf "split[a][b];[a]palettegen=max_colors=$3:stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=4:diff_mode=rectangle" \
    -loop 0 "$1"
  rm -rf /tmp/gif
}
mkdir -p /tmp/gif; i=0
add() { over "$1" "$2" /tmp/gif/$(printf %03d $i).png; i=$((i+1)); }
for f in $FRAMES/spin-*.png; do add $f 440x600+840+100; done
for p in claude codex cursor; do for f in $FRAMES/card-$p-*.png; do add $f 440x600+840+100; done; done
for f in $FRAMES/spin-*.png; do add $f 440x600+840+100; done
gif $OUT/demo.gif 15 128
mkdir -p /tmp/gif; i=0
for n in $(seq 1 12); do add $FRAMES/slide-00.png 150x440+1130+180; done
for f in $FRAMES/slide-0*.png; do add $f 150x440+1130+180; done
for n in $(seq 1 16); do add $FRAMES/slide-08.png 150x440+1130+180; done
for f in $(ls $FRAMES/slide-0*.png | sort -r); do add $f 150x440+1130+180; done
gif $OUT/autohide.gif 15 96

# The settings window, dark, from a running demo
GTK_THEME=Adwaita:dark /tmp/gonotch demo >/dev/null 2>&1 &
APP=$!
sleep 3
/tmp/gonotch settings
sleep 1.5
geo=$(xwininfo -root -tree 2>/dev/null | grep '"gonotch"' | grep -v '360x650\|10x10' | head -1 | grep -o '[0-9]*x[0-9]*+[0-9]*+[0-9]*' | head -1)
xwd -root -silent | convert xwd:- -crop "$geo" +repage $OUT/settings.png
kill $APP
[ -n "${HOST_UID:-}" ] && chown "$HOST_UID:${HOST_GID:-$HOST_UID}" $OUT/*
ls -la $OUT
