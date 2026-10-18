#!/bin/bash
# Builds awkward and malformed cases for the integration tests: one directory
# per case under DIR, each a stand-in for a USB drive's file system, and
# scan-disk.img, a partitioned stick for the whole scan. MAINDIR is
# fixtures.sh's output, whose squashfs and persistence images are reused.
#
#   gpt-badcrc/          GPT disk with a corrupt header CRC: no table
#   truncated/           GPT disk cut inside its third partition: the first
#                        two are found, the third is not
#   gpt-4kn/             GPT disk with 4096-byte sectors: FAT and ext4 partitions
#   crypt-swap/          disk with a LUKS, a swap and an ext4 partition: only
#                        the ext4 has an entry
#   sq-nokernel/         live squashfs with no kernel: no entry
#   sq-noinitrd/         live squashfs with a kernel and no initramfs: none
#   sq-beside/           live squashfs with its kernel and initramfs beside it
#   spaces/              a live image with a space in its name: skipped
#   depth/               images 3 and 4 directories down: only the first
#   persist-pairing/     two live images, two persistence images
#   btrfs-default/       btrfs whose default subvolume is not the top level
#   btrfs-many/          20 subvolumes and a snapshot: 16 are searched, the
#                        snapshot is not
#   btrfs-bls/           a subvolume with BLS entries under /boot, as Fedora
#   scan-disk.img        GPT: FAT with an extlinux entry, ext4 with live images
set -euo pipefail
OUT=${1:?usage: edge.sh DIR MAINDIR}
MAIN=${2:?usage: edge.sh DIR MAINDIR}
W=$(mktemp -d)
trap 'rm -rf "$W"' EXIT

tree() { # DIR LABEL [APPEND]
	mkdir -p "$1/boot/extlinux"
	echo kernel >"$1/boot/zImage"
	echo initrd >"$1/boot/initrd"
	printf 'default %s\ntimeout 1\nlabel %s\n    linux /zImage\n    initrd /initrd\n    append %s\n' "$2" "$2" "${3:-rootwait}" >"$1/boot/extlinux/extlinux.conf"
}
put() { dd if="$1" of="$2" bs=512 seek="$3" conv=notrunc status=none; }

# A squashfs under 128 KiB is not detected, so each carries a blob.
sq() { # SRCDIR OUTFILE
	head -c 1M /dev/urandom >"$1/blob"
	mksquashfs "$1" "$2" -noappend -quiet >/dev/null
}
withkernel() { # DIR
	mkdir -p "$1/boot"
	echo kernel >"$1/boot/vmlinuz-6.1"
	echo initrd >"$1/boot/initrd.img-6.1"
}
persistence() { # FILE LABEL
	truncate -s 16M "$1"
	mkfs.ext4 -q -F -L "$2" "$1"
}
fat() { # P: $W/P-fat.img with an entry P-vfat
	tree "$W/$1-fat" "$1-vfat"
	truncate -s 32M "$W/$1-fat.img"
	mkfs.vfat -n BOOT "$W/$1-fat.img" >/dev/null
	mcopy -s -i "$W/$1-fat.img" "$W/$1-fat/boot" ::/boot
}
ext() { # P: $W/P-ext.img with an entry P-ext4
	tree "$W/$1-ext" "$1-ext4"
	mkfs.ext4 -q -F -d "$W/$1-ext" "$W/$1-ext.img" 64M
}

# --- disks
fat t
ext t
truncate -s 400M "$OUT/truncated.img"
sfdisk -q "$OUT/truncated.img" <<T
label: gpt
start=2048, size=65536, type=0FC63DAF-8483-4772-8E79-3D69D8477DE4
start=67584, size=131072, type=0FC63DAF-8483-4772-8E79-3D69D8477DE4
start=198656, size=131072, type=0FC63DAF-8483-4772-8E79-3D69D8477DE4
T
put "$W/t-fat.img" "$OUT/truncated.img" 2048
put "$W/t-ext.img" "$OUT/truncated.img" 67584
truncate -s $((200000 * 512)) "$OUT/truncated.img"
mkdir -p "$OUT/truncated"
mv "$OUT/truncated.img" "$OUT/truncated/disk.img"

mkdir -p "$OUT/gpt-badcrc"
cp "$OUT/truncated/disk.img" "$OUT/gpt-badcrc/disk.img"
printf '\xff\xff' | dd of="$OUT/gpt-badcrc/disk.img" bs=1 seek=$((512 + 16)) conv=notrunc status=none

ext c
head -c 16M /dev/zero >"$W/luks.img"
printf 'LUKS\xba\xbe' | dd of="$W/luks.img" conv=notrunc status=none
truncate -s 16M "$W/swap.img"
mkswap -q "$W/swap.img" >/dev/null
mkdir -p "$OUT/crypt-swap"
truncate -s 300M "$OUT/crypt-swap/disk.img"
sfdisk -q "$OUT/crypt-swap/disk.img" <<T
label: gpt
start=2048, size=32768, type=0FC63DAF-8483-4772-8E79-3D69D8477DE4
start=34816, size=32768, type=0657FD6D-A4AB-43C4-84E5-0933C84B4F4F
start=67584, size=131072, type=0FC63DAF-8483-4772-8E79-3D69D8477DE4
T
put "$W/luks.img" "$OUT/crypt-swap/disk.img" 2048
put "$W/swap.img" "$OUT/crypt-swap/disk.img" 34816
put "$W/c-ext.img" "$OUT/crypt-swap/disk.img" 67584

# 4Kn: 4096-byte logical sectors, so the GPT header is at byte 4096
fat k
ext k
mkdir -p "$OUT/gpt-4kn"
truncate -s 200M "$OUT/gpt-4kn/disk.img"
sfdisk -q --sector-size 4096 "$OUT/gpt-4kn/disk.img" <<T
label: gpt
start=256, size=8192, type=EBD0A0A2-B9E5-4433-87C0-68B6B72699C7, name=bootfat
start=8448, size=16384, type=0FC63DAF-8483-4772-8E79-3D69D8477DE4, name=rootext
T
dd if="$W/k-fat.img" of="$OUT/gpt-4kn/disk.img" bs=4096 seek=256 conv=notrunc status=none
dd if="$W/k-ext.img" of="$OUT/gpt-4kn/disk.img" bs=4096 seek=8448 conv=notrunc status=none

# --- live squashfs
mkdir -p "$OUT/sq-nokernel" "$W/nokernel/etc"
echo ID=test >"$W/nokernel/etc/os-release"
sq "$W/nokernel" "$OUT/sq-nokernel/live.img"

mkdir -p "$OUT/sq-noinitrd" "$W/noinitrd/boot"
echo kernel >"$W/noinitrd/boot/vmlinuz-6.1"
sq "$W/noinitrd" "$OUT/sq-noinitrd/live.img"

mkdir -p "$OUT/sq-beside" "$W/beside/etc"
echo ID=test >"$W/beside/etc/os-release"
sq "$W/beside" "$OUT/sq-beside/sq.img"
echo beside-kernel >"$OUT/sq-beside/vmlinuz-6.1"
echo beside-initrd >"$OUT/sq-beside/initrd.img-6.1"

mkdir -p "$W/good"
withkernel "$W/good"
sq "$W/good" "$W/good.sqfs"

mkdir -p "$OUT/spaces"
cp "$W/good.sqfs" "$OUT/spaces/with space.img"

mkdir -p "$OUT/depth/a/b/c/d"
cp "$W/good.sqfs" "$OUT/depth/a/b/c/ok3.img"
cp "$W/good.sqfs" "$OUT/depth/a/b/c/d/deep.img"

mkdir -p "$OUT/persist-pairing"
cp "$W/good.sqfs" "$OUT/persist-pairing/alpha.img"
cp "$W/good.sqfs" "$OUT/persist-pairing/beta.img"
persistence "$OUT/persist-pairing/alpha-persistence.img" persistence
persistence "$OUT/persist-pairing/zzz-persist.img" data

# --- btrfs
mkdir -p "$OUT/btrfs-default" "$W/bd"
tree "$W/bd/@root" e-root
tree "$W/bd/other" e-other
truncate -s 256M "$OUT/btrfs-default/os.img"
mkfs.btrfs -q -f -r "$W/bd" --subvol default:@root --subvol rw:other "$OUT/btrfs-default/os.img"

mkdir -p "$OUT/btrfs-many" "$W/bm/.snapshots/1"
tree "$W/bm/.snapshots/1/snapshot" snap
args=(--subvol rw:.snapshots/1/snapshot)
for i in $(seq -w 1 20); do
	tree "$W/bm/v$i" "v$i"
	args+=(--subvol "rw:v$i")
done
truncate -s 512M "$OUT/btrfs-many/os.img"
mkfs.btrfs -q -f -r "$W/bm" "${args[@]}" "$OUT/btrfs-many/os.img"

mkdir -p "$OUT/btrfs-bls" "$W/bls/root/boot/loader/entries"
echo kernel >"$W/bls/root/boot/vmlinuz-6.1"
echo initrd >"$W/bls/root/boot/initramfs-6.1.img"
printf 'title Fedora Test\nversion 6.1\nlinux /vmlinuz-6.1\ninitrd /initramfs-6.1.img\noptions root=UUID=x ro\n' >"$W/bls/root/boot/loader/entries/fedora.conf"
truncate -s 256M "$OUT/btrfs-bls/os.img"
mkfs.btrfs -q -f -r "$W/bls" --subvol rw:root "$OUT/btrfs-bls/os.img"

# --- a whole stick: FAT boot partition with an extlinux entry (its own
# console=, which the scan replaces), ext4 with the live images
tree "$W/s-fat" stick-boot "root=/dev/x console=ttyS9"
truncate -s 64M "$W/s-fat.img"
mkfs.vfat -n BOOT "$W/s-fat.img" >/dev/null
mcopy -s -i "$W/s-fat.img" "$W/s-fat/boot" ::/boot
mkdir -p "$W/s-ext/live"
cp "$MAIN/live/debian.img" "$MAIN/live/debian-persistence.img" "$W/s-ext/live/"
mkfs.ext4 -q -F -d "$W/s-ext" "$W/s-ext.img" 128M
truncate -s 300M "$OUT/scan-disk.img"
sfdisk -q "$OUT/scan-disk.img" <<T
label: gpt
start=2048, size=131072, type=EBD0A0A2-B9E5-4433-87C0-68B6B72699C7, name=BOOT
start=133120, size=262144, type=0FC63DAF-8483-4772-8E79-3D69D8477DE4, name=LIVE
T
put "$W/s-fat.img" "$OUT/scan-disk.img" 2048
put "$W/s-ext.img" "$OUT/scan-disk.img" 133120
