#!/bin/sh
# Build the boot manager's parts into OUTDIR, for a LinuxBoot stage build to
# pack into its u-root initramfs.
#
#   sh build.sh OUTDIR
#
# OUTDIR gets:
#   bootmanager-ui  the UI, a static musl binary (nothing to resolve in the
#                   stage's /lib); the stage adds it with
#                   -files OUTDIR/bootmanager-ui:bin/bootmanager-ui
#   web/            the UI's web build, when the wasm32-unknown-unknown
#                   target and the wasm-bindgen CLI are there (skipped
#                   otherwise); the stage adds it with
#                   -files OUTDIR/web:usr/share/bootmanager-web
#   go.work         u-root plus bootmanager: the workspace u-root builds the
#                   backend in, as a u-root command (this checkout's
#                   bootmanager/ directory)
#   bootmanager     the backend on its own, static, for dev.sh's uploads
#
# Environment:
#   TARGET   Rust target triple, a musl one (default: the host's when it is
#            musl, else <machine>-unknown-linux-musl)
#   UROOT    u-root checkout for go.work (default ~/u-root). The kexec
#            handover may need changes not yet upstream; see README.md.
#   TAGS     Go build tags for the backend, "dev" for dev.sh's features
#   CARGO_TARGET_DIR  (default ~/.cache/bootmanager-target)
set -eu
OUT=${1:?usage: build.sh OUTDIR}
HERE=$(cd "$(dirname "$0")" && pwd)
UROOT=${UROOT:-$HOME/u-root}
export CARGO_TARGET_DIR=${CARGO_TARGET_DIR:-$HOME/.cache/bootmanager-target}
mkdir -p "$OUT"

HOST=$(rustc -vV | sed -n 's/^host: //p')
case ${TARGET:=$HOST} in
*-musl*) ;;
*) TARGET=$(uname -m)-unknown-linux-musl ;;
esac
case $TARGET in
aarch64-*) GOARCH=arm64 ;;
x86_64-*) GOARCH=amd64 ;;
arm*) GOARCH=arm ;;
riscv64*) GOARCH=riscv64 ;;
*) echo "build.sh: no GOARCH for $TARGET" >&2; exit 1 ;;
esac

# An explicit --target keeps RUSTFLAGS off build scripts and proc-macros,
# which run on the host and cannot be static.
(cd "$HERE/bootmanager-ui" &&
	RUSTFLAGS="-C target-feature=+crt-static" cargo build --release --target "$TARGET")
install -m 755 "$CARGO_TARGET_DIR/$TARGET/release/bootmanager-ui" "$OUT/bootmanager-ui"
file "$OUT/bootmanager-ui" | grep -q "static" ||
	{ echo "build.sh: $OUT/bootmanager-ui is not static" >&2; exit 1; }

# The CLI must be the version the crate pins (Cargo.toml).
rm -rf "$OUT/web"
want=$(sed -n '/^name = "wasm-bindgen"$/{n;s/^version = "\(.*\)"$/\1/p;}' "$HERE/bootmanager-ui/Cargo.lock")
if command -v wasm-bindgen >/dev/null &&
	[ -d "$(rustc --print sysroot)/lib/rustlib/wasm32-unknown-unknown" ]; then
	have=$(wasm-bindgen --version | cut -d" " -f2)
	[ "$have" = "$want" ] ||
		{ echo "build.sh: wasm-bindgen CLI is $have, the crate pins $want" >&2; exit 1; }
	(cd "$HERE/bootmanager-ui" && cargo build --release --lib \
		--target wasm32-unknown-unknown --no-default-features --features web)
	wasm-bindgen --target web --no-typescript --out-dir "$OUT/web" \
		"$CARGO_TARGET_DIR/wasm32-unknown-unknown/release/bootmanager_ui.wasm"
	cp "$HERE"/bootmanager-ui/web/* "$OUT/web/"
else
	echo "build.sh: no wasm32 target or wasm-bindgen $want, building without the web UI" >&2
fi

rm -f "$OUT/go.work"
if [ -d "$UROOT" ]; then
	(cd "$OUT" && go work init "$UROOT" "$HERE/bootmanager")
	export GOWORK=$OUT/go.work
fi
(cd "$HERE/bootmanager" && GOARCH=$GOARCH CGO_ENABLED=0 \
	go build -trimpath -tags "${TAGS:-}" -o "$OUT/bootmanager" .)
