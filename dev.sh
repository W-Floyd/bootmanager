#!/bin/sh
# Rebuild part of the boot manager and swap it into the running stage, no
# reflash: the backend saves the upload in /tmp/dev (RAM) and restarts what
# it replaced (bootmanager/dev.go).
#
#   sh tools/linuxboot/bootmanager/dev.sh ui        # restart the UI with a new build
#   sh tools/linuxboot/bootmanager/dev.sh backend   # re-exec the backend in place
#   sh tools/linuxboot/bootmanager/dev.sh web       # new browser build; reload the page
#
# Kernel tests, straight from RAM (dev.go, kexecTest):
#
#   sh tools/linuxboot/bootmanager/dev.sh kexec-stage  # build the stage, kexec into its Image
#   sh tools/linuxboot/bootmanager/dev.sh kexec-pmos   # build pmOS's kernel, boot the pmOS
#                                                      # entry with it (same version string)
#   sh tools/linuxboot/bootmanager/dev.sh stage-hold   # from pmOS: reboot into the Boot
#                                                      # Manager and stay there, ready for these
#
# A kexec is not a cold boot: it cannot reproduce what depends on ABL's
# hand-off (the stage's first display power-up), only what the kernel does.
#
# Needs Services -> Remote UI and Dev uploads on, on the phone (or from an
# earlier upload's backend, which keeps them on across a re-exec; stage-hold
# turns them on). The SSH session and the UI survive a backend restart; the
# UI reconnects.
#
# An upload wins over the built-in copy until the stage reboots -- so once
# `dev.sh ui` or `web` has run, a backend upload's embedded copies are not
# what runs; send those again too. A running UI is only replaced by
# `dev.sh ui` (or dev.restart over the socket).
set -eu
WHAT=${1:?usage: dev.sh ui|backend|web|kexec-stage|kexec-pmos|stage-hold [HOST]}
HOST=${2:-172.16.42.1}
HERE=$(cd "$(dirname "$0")" && pwd)
export CARGO_TARGET_DIR=${CARGO_TARGET_DIR:-$HOME/.cache/bootmanager-target}
WORK=${WORK:-$HOME/.cache/bootmanager-dev}
URL=http://$HOST:8080/dev

put() {
	echo "-> $2"
	curl -fsS --max-time 60 -T "$1" "$URL/$2"
}

case $WHAT in
ui)
	T=$(rustc -vV | sed -n 's/^host: //p')
	(cd "$HERE/bootmanager-ui" &&
		RUSTFLAGS="-C target-feature=+crt-static" cargo build --release --target "$T")
	put "$CARGO_TARGET_DIR/$T/release/bootmanager-ui" bootmanager-ui
	;;
web)
	(cd "$HERE/bootmanager-ui" && cargo build --release --lib \
		--target wasm32-unknown-unknown --no-default-features --features web)
	rm -rf "$WORK/web"
	wasm-bindgen --target web --no-typescript --out-dir "$WORK/web" \
		"$CARGO_TARGET_DIR/wasm32-unknown-unknown/release/bootmanager_ui.wasm"
	cp "$HERE/bootmanager-ui/web/index.html" "$HERE/bootmanager-ui/web/favicon.svg" "$WORK/web/"
	for f in "$WORK"/web/*; do
		put "$f" "web/${f##*/}"
	done
	;;
backend)
	# The same parts as the stage build, so the upload embeds a matching UI.
	sh "$HERE/build.sh" "$WORK"
	(cd "$HERE/bootmanager" && GOWORK=$WORK/go.work GOARCH=arm64 CGO_ENABLED=0 \
		go build -trimpath -o "$WORK/bootmanager" .)
	put "$WORK/bootmanager" bootmanager
	;;
kexec-stage)
	# The whole stage build; the kernel Image carries its DTB and initramfs.
	REPO=$(cd "$HERE/../../.." && pwd)
	BOOTMANAGER=1 sh "$REPO/tools/linuxboot/build.sh"
	KOUT=${KOUT:-$HOME/exp-linuxboot/.output-host}
	echo "-> kernel-stage (the phone kexecs into it)"
	curl -fsS --max-time 300 -T "$KOUT/arch/arm64/boot/Image" "$URL/kernel-stage"
	;;
kexec-pmos)
	# Built as BUILD.md builds the packaged kernel, but not packaged: only
	# Image.gz goes over, and nothing on the phone is installed or changed.
	KSRC=${KSRC:-$HOME/fogona-mainline}
	(cd "$KSRC" && bash -c 'source /opt/pmbootstrap-src/helpers/envkernel.sh --gcc >/dev/null 2>&1 &&
		pmbootstrap -q chroot --user -- CCACHE_DISABLE=1 ARCH=arm64 \
		make -C /mnt/linux O=/mnt/linux/.output CC=gcc HOSTCC=gcc LOCALVERSION= \
		-j"$(nproc)" Image.gz')
	echo "-> kernel-pmos (the phone boots the pmOS entry with it)"
	curl -fsS --max-time 300 -T "$KSRC/.output/arch/arm64/boot/Image.gz" "$URL/kernel-pmos"
	;;
stage-hold)
	# From pmOS: leave a one-shot request on the boot partition, which the
	# stage's uinit consumes (record_boot), and reboot. That boot stays in
	# the Boot Manager with Remote UI and Dev uploads on; the next autoboots.
	sshpass -p 147147 ssh -o StrictHostKeyChecking=no "user@$HOST" \
		"echo 147147 | sudo -S -p '' sh -c 'printf \"hold remote dev\n\" > /boot/linuxboot/dev-once && sync && systemctl reboot'"
	echo "rebooting into the Boot Manager, held"
	;;
*)
	echo "usage: dev.sh ui|backend|web|kexec-stage|kexec-pmos|stage-hold [HOST]" >&2
	exit 2
	;;
esac
