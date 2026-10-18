package boot

// liveInit replaces the /init of a live entry's initramfs. It uses only what
// every initramfs has (sh, mount with -o loop, modprobe, switch_root), finds
// the media by the squashfs's path on it (bman.live=), puts an overlay of the
// squashfs over the ext4 persistence image (bman.persist=) or over RAM as
// the root, and switches to it. The media is mounted read-write only when
// there is a persistence image to write to. On failure it leaves a shell.
const liveInit = `#!/bin/sh
export PATH=/sbin:/usr/sbin:/bin:/usr/bin
mount -t proc proc /proc
mount -t sysfs sysfs /sys
mount -t devtmpfs devtmpfs /dev

fail() { echo "bman live: $*"; exec /bin/sh; }

for w in $(cat /proc/cmdline); do
	case $w in
	bman.live=*) LIVE=${w#bman.live=} ;;
	bman.persist=*) PERSIST=${w#bman.persist=} ;;
	init=*) INIT=${w#init=} ;;
	esac
done
[ -n "$LIVE" ] || fail "no bman.live= on the command line"

for m in loop squashfs overlay ext4 vfat exfat usb_storage uas sd_mod mmc_block; do
	modprobe $m 2>/dev/null
done

mkdir -p /media /ro /rw /newroot
opt=ro
[ -n "$PERSIST" ] && opt=rw
i=0
found=
while [ -z "$found" ] && [ $i -lt 30 ]; do
	for d in /dev/sd*[0-9] /dev/mmcblk*p[0-9]* /dev/nvme*n*p[0-9]* /dev/vd*[0-9]; do
		[ -b "$d" ] || continue
		mount -o $opt $d /media 2>/dev/null || continue
		if [ -f "/media$LIVE" ]; then found=$d; break; fi
		umount /media
	done
	[ -n "$found" ] || { sleep 1; i=$((i + 1)); }
done
[ -n "$found" ] || fail "no media with $LIVE"

mount -o loop,ro -t squashfs "/media$LIVE" /ro || fail "cannot mount $LIVE"
if [ -n "$PERSIST" ]; then
	mount -o loop,rw -t ext4 "/media$PERSIST" /rw || fail "cannot mount $PERSIST"
else
	mount -t tmpfs tmpfs /rw || fail "no tmpfs"
fi
mkdir -p /rw/upper /rw/work
mount -t overlay overlay -o lowerdir=/ro,upperdir=/rw/upper,workdir=/rw/work /newroot || fail "cannot mount the overlay"

exec switch_root /newroot "${INIT:-/sbin/init}"
`
