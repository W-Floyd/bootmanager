#!/bin/sh
# Build the recovery UI's parts for the LinuxBoot stage into OUTDIR; the
# stage build (tools/linuxboot/build.sh, RECOVERY=1) runs this and packs
# the results into the u-root initramfs.
#
#   sh tools/linuxboot/recovery/build.sh OUTDIR
#
# OUTDIR gets (and the UI and web build are also copied into
# recovery-backend/assets/, for the backend to embed):
#   recovery-ui     the phone build, static musl (nothing to resolve in the
#                   stage's /lib)
#   web/            the browser build, when the wasm toolchain is present
#                   (apk add rust-wasm wasm-bindgen); skipped otherwise
#   go.work         u-root plus recovery-backend, the workspace u-root
#                   builds the backend from
#
# Cargo's target dir is on the named volume (default
# ~/.cache/recovery-target), not under /repo: rustc mmaps its outputs, and
# the macOS bind mount answers that with SIGBUS (TRAPS.md).
set -eu
OUT=${1:?usage: build.sh OUTDIR}
HERE=$(cd "$(dirname "$0")" && pwd)
UROOT=${UROOT:-$HOME/u-root}
export CARGO_TARGET_DIR=${CARGO_TARGET_DIR:-$HOME/.cache/recovery-target}
mkdir -p "$OUT"

# An explicit --target keeps RUSTFLAGS off the build scripts and proc-macros,
# which run on the host and cannot be static.
HOST=$(rustc -vV | sed -n "s/^host: //p")

(cd "$HERE/recovery-ui" &&
	RUSTFLAGS="-C target-feature=+crt-static" cargo build --release --target "$HOST")
install -m 755 "$CARGO_TARGET_DIR/$HOST/release/recovery-ui" "$OUT/recovery-ui"
file "$OUT/recovery-ui" | grep -q "static" ||
	{ echo "$OUT/recovery-ui is not static" >&2; exit 1; }

rm -rf "$OUT/web"
if command -v wasm-bindgen >/dev/null &&
	[ -d "$(rustc --print sysroot)/lib/rustlib/wasm32-unknown-unknown" ]; then
	(cd "$HERE/recovery-ui" && cargo build --release --lib \
		--target wasm32-unknown-unknown --no-default-features --features web)
	wasm-bindgen --target web --no-typescript --out-dir "$OUT/web" \
		"$CARGO_TARGET_DIR/wasm32-unknown-unknown/release/recovery_ui.wasm"
	cp "$HERE/recovery-ui/web/index.html" "$HERE/recovery-ui/web/favicon.svg" "$OUT/web/"
else
	echo "recovery: no wasm toolchain, building without the browser UI" >&2
fi

rm -f "$OUT/go.work"
(cd "$OUT" && go work init "$UROOT" "$HERE/recovery-backend")

# The backend embeds both (recovery-backend/assets.go), so the backend is
# the whole recovery. assets/ is gitignored but for its README.
A=$HERE/recovery-backend/assets
find "$A" -mindepth 1 ! -name README -exec rm -rf {} +
cp "$OUT/recovery-ui" "$A/"
[ -d "$OUT/web" ] && cp -r "$OUT/web" "$A/"
