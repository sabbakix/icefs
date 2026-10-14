#!/bin/sh
# Regenerates the README screenshots. Runs the real icefs TUI in tmux against
# the demo API in ./demo (made-up shares, nothing on disk is touched), turns
# each screen into HTML with render.py and photographs it with headless
# Chromium. Needs Go, tmux, python3, ImageMagick and Chromium
# (set CHROME if it is not on PATH as chromium).
set -eu
cd "$(dirname "$0")/../.."
out=docs/screenshots
chrome=${CHROME:-chromium}
tmp=$(mktemp -d)
sock=$tmp/icefs.sock
export TZ=UTC

go build -o "$tmp/icefs" ./cmd/icefs
go build -o "$tmp/demo" ./docs/screenshots/demo
"$tmp/demo" -socket "$sock" 2>/dev/null &
demo=$!
trap 'kill $demo; tmux kill-session -t icefs-shots 2>/dev/null; rm -rf "$tmp"' EXIT
sleep 1

tmux new-session -d -s icefs-shots -x 104 -y 22 "TERM=xterm-256color $tmp/icefs -socket $sock"
sleep 1

key() { for k in "$@"; do tmux send-keys -t icefs-shots "$k"; sleep 0.4; done; }
shot() {
	tmux capture-pane -e -p -t icefs-shots >"$tmp/$1.ansi"
	python3 docs/screenshots/render.py "$tmp/$1.ansi" >"$tmp/$1.html"
	"$chrome" --headless --no-sandbox --hide-scrollbars --force-device-scale-factor=2 \
		--window-size=1400,700 --default-background-color=00000000 \
		--screenshot="$tmp/$1.png" "file://$tmp/$1.html" >/dev/null 2>&1
	convert "$tmp/$1.png" -trim +repage -bordercolor none -border 8 "$out/$1.png"
}

shot shares
key s;                    shot settings
key Escape Enter Down;    shot chains
key Up d; tmux send-keys -t icefs-shots -l delete; sleep 0.4; shot delete
key Escape Down Down Down Down Enter; shot files
# The activity log has long details; give it a wider terminal.
key Escape; tmux resize-window -t icefs-shots -x 128; key e; shot activity
