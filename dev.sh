#!/bin/sh
# Rebuild part of the boot manager and swap it into a running stage, no
# reflash. Needs a dev build on the device (TAGS=dev sh build.sh, then the
# stage build) with Services -> Remote UI and Dev uploads on.
#
#   sh dev.sh ui                 # restart the UI with a new build
#   sh dev.sh backend            # re-exec the backend in place
#   sh dev.sh web                # new web build; reload the page
#   sh dev.sh kexec KERNEL [initrd=F] [dtb=F] [cmdline=S] [inherit=running|entry:N]
#                                # boot a kernel from RAM (bootmanager/dev.go)
#
# HOST is where Remote UI listens (default 127.0.0.1, through an SSH
# tunnel: ssh -L 8080:127.0.0.1:8080 root@<device>).
#
# The SSH session and the UI survive a backend restart; the UI
# reconnects. An upload wins over the built-in copy until the stage
# reboots.
set -eu
WHAT=${1:?usage: dev.sh ui|backend|web|kexec KERNEL [part=value...]}
HERE=$(cd "$(dirname "$0")" && pwd)
WORK=${WORK:-$HOME/.cache/bootmanager-dev}
URL=http://${HOST:-127.0.0.1}:8080/dev

put() {
	echo "-> $2"
	curl -fsS --max-time 60 -T "$1" "$URL/$2"
}

case $WHAT in
ui | backend)
	TAGS=dev sh "$HERE/build.sh" "$WORK"
	f=bootmanager
	[ "$WHAT" = ui ] && f=bootmanager-ui
	put "$WORK/$f" "$f"
	;;
web)
	sh "$HERE/build.sh" "$WORK"
	for f in "$WORK"/web/*; do
		put "$f" "web/${f##*/}"
	done
	;;
kexec)
	K=${2:?usage: dev.sh kexec KERNEL [initrd=F] [dtb=F] [cmdline=S] [inherit=X]}
	shift 2
	n=$#
	for a; do
		case $a in
		initrd=* | dtb=*) set -- "$@" -F "${a%%=*}=@${a#*=}" ;;
		*) set -- "$@" -F "$a" ;;
		esac
	done
	shift "$n"
	echo "-> kexec"
	curl -fsS --max-time 300 -X PUT -F "kernel=@$K" "$@" "$URL/kexec"
	;;
*)
	echo "usage: dev.sh ui|backend|web|kexec KERNEL [part=value...]" >&2
	exit 2
	;;
esac
