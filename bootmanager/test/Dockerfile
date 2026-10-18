# The tools that make real file systems and images for the integration tests.
FROM golang:1.26
RUN apt-get update -qq \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
    btrfs-progs dosfstools mtools e2fsprogs fdisk squashfs-tools util-linux busybox-static \
 && rm -rf /var/lib/apt/lists/*
