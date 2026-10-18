#!/bin/bash
# Builds the image files the integration tests search, under the directory
# given (the stand-in for a USB drive's file system):
#
#   live/debian.img              squashfs with /boot/vmlinuz-* and initrd.img-*
#   live/debian-persistence.img  ext4 labelled persistence
#   live/data.img                ext4 labelled data: not a persistence image
#   nura/os.img                  zstd btrfs: default subvolume @pristine and
#                                subvolume other, each with boot/extlinux
#   gpt.img, mbr.img             whole disks: FAT, ext4 and btrfs (with a
#                                subvolume) partitions; mbr's btrfs is a
#                                logical partition
#   junk.img                     random bytes
#   stick.img                    a FAT file system holding live/debian.img and
#                                live/debian-persistence.img, for running the
#                                init shim against (the test copies it: the
#                                shim writes to the persistence image)
#
# Every extlinux entry is labelled for where it is: g-vfat, g-ext4, g-btrfs,
# g-sub on the GPT disk, m-* on the MBR one, live-pristine and live-other in
# nura/os.img. Needs no mounts or loop devices.
set -euo pipefail
OUT=${1:?usage: fixtures.sh DIR}
W=$(mktemp -d)
trap 'rm -rf "$W"' EXIT
mkdir -p "$OUT/live" "$OUT/nura"

# tree DIR LABEL: boot/extlinux with one entry LABEL, a kernel and an initrd.
tree() {
	mkdir -p "$1/boot/extlinux"
	echo kernel >"$1/boot/zImage"
	echo initrd >"$1/boot/initrd"
	printf 'default %s\ntimeout 1\nlabel %s\n    linux /zImage\n    initrd /initrd\n    append rootwait\n' "$2" "$2" >"$1/boot/extlinux/extlinux.conf"
}

# put PART IMG SECTOR: write a partition's file system into a disk image.
put() { dd if="$1" of="$2" bs=512 seek="$3" conv=notrunc status=none; }

# payloads P: P-fat.img, P-ext.img, P-btrfs.img, entries labelled P-vfat, ...
payloads() {
	local p=$1
	tree "$W/$p-fat" "$p-vfat"
	tree "$W/$p-ext" "$p-ext4"
	tree "$W/$p-btr" "$p-btrfs"
	tree "$W/$p-btr/sub" "$p-sub"
	truncate -s 32M "$W/$p-fat.img"
	mkfs.vfat -n BOOT "$W/$p-fat.img" >/dev/null
	mcopy -s -i "$W/$p-fat.img" "$W/$p-fat/boot" ::/boot
	mkfs.ext4 -q -F -d "$W/$p-ext" "$W/$p-ext.img" 64M
	truncate -s 128M "$W/$p-btrfs.img"
	mkfs.btrfs -q -f -r "$W/$p-btr" --subvol rw:sub "$W/$p-btrfs.img"
}

# --- live squashfs and persistence
mkdir -p "$W/sq/boot" "$W/sq/etc"
echo kernel >"$W/sq/boot/vmlinuz-6.9.1"
echo kernel >"$W/sq/boot/vmlinuz-6.10.2"
echo initrd >"$W/sq/boot/initrd.img-6.9.1"
echo initrd >"$W/sq/boot/initrd.img-6.10.2"
echo ID=test >"$W/sq/etc/os-release"
# u-root's detection reads 128 KiB: an image smaller than that is not found.
head -c 1M /dev/urandom >"$W/sq/blob"
mksquashfs "$W/sq" "$OUT/live/debian.img" -noappend -quiet >/dev/null
truncate -s 32M "$OUT/live/debian-persistence.img"
mkfs.ext4 -q -F -L persistence "$OUT/live/debian-persistence.img"
truncate -s 32M "$OUT/live/data.img"
mkfs.ext4 -q -F -L data "$OUT/live/data.img"
head -c 4M /dev/urandom >"$OUT/junk.img"
truncate -s 128M "$OUT/stick.img"
mkfs.vfat -n STICK "$OUT/stick.img" >/dev/null
mmd -i "$OUT/stick.img" ::live
mcopy -i "$OUT/stick.img" "$OUT/live/debian.img" "$OUT/live/debian-persistence.img" ::live/

# --- nura-style btrfs image: read-only default subvolume, one more
tree "$W/nura/@pristine" live-pristine
tree "$W/nura/other" live-other
truncate -s 256M "$OUT/nura/os.img"
mkfs.btrfs -q -f --compress zstd:3 -r "$W/nura" --subvol default-ro:@pristine --subvol rw:other "$OUT/nura/os.img"

# --- GPT disk
payloads g
truncate -s 400M "$OUT/gpt.img"
sfdisk -q "$OUT/gpt.img" <<T
label: gpt
start=2048, size=65536, type=EBD0A0A2-B9E5-4433-87C0-68B6B72699C7, name=bootfat
start=67584, size=131072, type=0FC63DAF-8483-4772-8E79-3D69D8477DE4, name=rootext
start=198656, size=262144, type=0FC63DAF-8483-4772-8E79-3D69D8477DE4, name=data
T
put "$W/g-fat.img" "$OUT/gpt.img" 2048
put "$W/g-ext.img" "$OUT/gpt.img" 67584
put "$W/g-btrfs.img" "$OUT/gpt.img" 198656

# --- MBR disk, the btrfs partition logical
payloads m
truncate -s 400M "$OUT/mbr.img"
sfdisk -q "$OUT/mbr.img" <<T
label: dos
start=2048, size=65536, type=c
start=67584, size=131072, type=83
start=198656, size=300000, type=5
start=200704, size=262144, type=83
T
put "$W/m-fat.img" "$OUT/mbr.img" 2048
put "$W/m-ext.img" "$OUT/mbr.img" 67584
put "$W/m-btrfs.img" "$OUT/mbr.img" 200704
