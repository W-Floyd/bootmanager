#!/bin/sh
# Rebuild part of the recovery and swap it into the running stage, no
# reflash: the backend saves the upload in /tmp/dev (RAM) and restarts what
# it replaced (recovery-backend/dev.go).
#
#   sh tools/linuxboot/recovery/dev.sh ui        # restart the UI with a new build
#   sh tools/linuxboot/recovery/dev.sh backend   # re-exec the backend in place
#   sh tools/linuxboot/recovery/dev.sh web       # new browser build; reload the page
#
# Needs Services -> Remote UI and Dev uploads on, on the phone (or from an
# earlier upload's backend, which keeps them on across a re-exec). The SSH
# session and the UI survive a backend restart; the UI reconnects.
#
# An upload wins over the built-in copy until the stage reboots. The
# backend's build embeds the current UI, but a running UI is only replaced
# by `dev.sh ui`.
set -eu
WHAT=${1:?usage: dev.sh ui|backend|web [HOST]}
HOST=${2:-172.16.42.1}
HERE=$(cd "$(dirname "$0")" && pwd)
export CARGO_TARGET_DIR=${CARGO_TARGET_DIR:-$HOME/.cache/recovery-target}
WORK=${WORK:-$HOME/.cache/recovery-dev}
URL=http://$HOST:8080/dev

put() {
	echo "-> $2"
	curl -fsS --max-time 60 -T "$1" "$URL/$2"
}

case $WHAT in
ui)
	T=$(rustc -vV | sed -n 's/^host: //p')
	(cd "$HERE/recovery-ui" &&
		RUSTFLAGS="-C target-feature=+crt-static" cargo build --release --target "$T")
	put "$CARGO_TARGET_DIR/$T/release/recovery-ui" recovery-ui
	;;
web)
	(cd "$HERE/recovery-ui" && cargo build --release --lib \
		--target wasm32-unknown-unknown --no-default-features --features web)
	rm -rf "$WORK/web"
	wasm-bindgen --target web --no-typescript --out-dir "$WORK/web" \
		"$CARGO_TARGET_DIR/wasm32-unknown-unknown/release/recovery_ui.wasm"
	cp "$HERE/recovery-ui/web/index.html" "$HERE/recovery-ui/web/favicon.svg" "$WORK/web/"
	for f in "$WORK"/web/*; do
		put "$f" "web/${f##*/}"
	done
	;;
backend)
	# The same parts as the stage build, so the upload embeds a matching UI.
	sh "$HERE/build.sh" "$WORK"
	(cd "$HERE/recovery-backend" && GOWORK=$WORK/go.work GOARCH=arm64 CGO_ENABLED=0 \
		go build -trimpath -o "$WORK/recovery-backend" .)
	put "$WORK/recovery-backend" recovery-backend
	;;
*)
	echo "usage: dev.sh ui|backend|web [HOST]" >&2
	exit 2
	;;
esac
