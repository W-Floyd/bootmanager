#!/bin/sh
# Build postmarketOS Boot Manager's parts for the LinuxBoot stage into
# OUTDIR; the fogona repo's stage build (tools/linuxboot/build.sh,
# BOOTMANAGER=1) runs this and packs the result into the u-root initramfs.
#
#   sh build.sh OUTDIR
#
# OUTDIR gets (and the UI and web build are also copied into
# bootmanager/assets/, for the backend to embed):
#   bootmanager-ui  the phone build, static musl (nothing to resolve in the
#                   stage's /lib)
#   web/            the browser build, when the wasm toolchain is present
#                   (apk add rust-wasm wasm-bindgen); skipped otherwise
#   go.work         u-root plus bootmanager, the workspace u-root builds the
#                   backend from
#
# Cargo's target dir is on the named volume (default
# ~/.cache/bootmanager-target), not under /repo: rustc mmaps its outputs,
# and the macOS bind mount answers that with SIGBUS (fogona's TRAPS.md).
set -eu
OUT=${1:?usage: build.sh OUTDIR}
HERE=$(cd "$(dirname "$0")" && pwd)
UROOT=${UROOT:-$HOME/u-root}
export CARGO_TARGET_DIR=${CARGO_TARGET_DIR:-$HOME/.cache/bootmanager-target}
mkdir -p "$OUT"

# An explicit --target keeps RUSTFLAGS off the build scripts and proc-macros,
# which run on the host and cannot be static.
HOST=$(rustc -vV | sed -n "s/^host: //p")

(cd "$HERE/bootmanager-ui" &&
	RUSTFLAGS="-C target-feature=+crt-static" cargo build --release --target "$HOST")
install -m 755 "$CARGO_TARGET_DIR/$HOST/release/bootmanager-ui" "$OUT/bootmanager-ui"
file "$OUT/bootmanager-ui" | grep -q "static" ||
	{ echo "$OUT/bootmanager-ui is not static" >&2; exit 1; }

rm -rf "$OUT/web"
if command -v wasm-bindgen >/dev/null &&
	[ -d "$(rustc --print sysroot)/lib/rustlib/wasm32-unknown-unknown" ]; then
	(cd "$HERE/bootmanager-ui" && cargo build --release --lib \
		--target wasm32-unknown-unknown --no-default-features --features web)
	wasm-bindgen --target web --no-typescript --out-dir "$OUT/web" \
		"$CARGO_TARGET_DIR/wasm32-unknown-unknown/release/bootmanager_ui.wasm"
	cp "$HERE/bootmanager-ui/web/index.html" "$HERE/bootmanager-ui/web/favicon.svg" "$OUT/web/"
else
	echo "bootmanager: no wasm toolchain, building without the browser UI" >&2
fi

rm -f "$OUT/go.work"
(cd "$OUT" && go work init "$UROOT" "$HERE/bootmanager")

# The backend embeds both (bootmanager/assets.go), so the backend is the
# whole boot manager. assets/ is gitignored but for its README.
A=$HERE/bootmanager/assets
find "$A" -mindepth 1 ! -name README -exec rm -rf {} +
cp "$OUT/bootmanager-ui" "$A/"
[ -d "$OUT/web" ] && cp -r "$OUT/web" "$A/"

# Display-panel descriptions for display.info (bootmanager/display.go), in
# gmobile's format and named by DT compatible, as gmobile installs them:
# gmobile's own set, from the release pmOS builds (the overlay APKBUILD's
# pkgver; pmbootstrap keeps the tarball in its distfiles), then fogona's
# from the fogona repo's aports overlay on top until it is upstream.
GM=${FOGONA:-/repo}/aports/community/gmobile
GMVER=$(sed -n 's/^pkgver=//p' "$GM/APKBUILD")
GMOBILE_TARBALL=${GMOBILE_TARBALL:-$HOME/.local/var/pmbootstrap/cache_distfiles/gmobile-$GMVER.tar.xz}
mkdir -p "$A/display-panels"
if [ -f "$GMOBILE_TARBALL" ]; then
	tar -xJf "$GMOBILE_TARBALL" -C "$A/display-panels" --strip-components=4 \
		"gmobile-$GMVER/data/devices/display-panels"
else
	echo "bootmanager: no $GMOBILE_TARBALL, only fogona's display panel" >&2
fi
cp "$GM/motorola-fogona.json" "$A/display-panels/motorola,fogona.json"
