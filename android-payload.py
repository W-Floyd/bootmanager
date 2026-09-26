#!/usr/bin/env python3
"""Assemble what ABL would hand stock Android, for the Boot Manager to kexec.

    python3 tools/linuxboot/bootmanager/android-payload.py OUTDIR

OUTDIR gets kernel, initrd, dtb and cmdline, which `dev.sh kexec-android`
uploads (bootmanager/dev.go, kexecTest). What goes in, and why each piece:

  kernel   the RETUS boot image's kernel, out/retus/boot.img -- the phone's
           original boot_a, the kernel its Android boots with (TRAPS.md:
           out/stock/boot.img is an older build that does not boot it).
  initrd   the phone's OWN vendor ramdisk (out/dump/vendor_boot_a.img) and
           generic ramdisk (out/dump/init_boot_a.img), concatenated as ABL
           concatenates them, then a bootconfig trailer. Not the RETUS
           package's: the phone's vendor_boot and init_boot are the factory
           U1TF34.100-35 build (vendor_boot is never written, a hard
           constraint), and its RETUS 5.15.167 kernel loads those 5.15.123
           vendor modules because GKI modules are matched by symbol CRCs
           (modversions), not by release.
  dtb      the live tree ABL handed stock Android, captured as
           out/stockdt/stock.dtb: vendor_boot's base with dtbo's overlays
           applied, and ABL's /memory and /reserved-memory. The loader
           keeps its segments out of every reserved region in it. One edit:
           the ramoops region loses its pmsg zone, so it is laid out as the
           stage lays out the same region (two dump records and the
           console), and the Boot Manager boot after a failed attempt reads
           Android's log in /sys/fs/pstore.
  cmdline  /proc/cmdline of this unit's stock Android, from the bugreport,
           plus reboot=warm: this firmware's default reset is cold, and
           only a warm one keeps the ramoops region (tested 2026-09-26).
  bootconfig (in the initrd trailer) its /proc/bootconfig, from the same
           bugreport: the androidboot.* values ABL sets, verifiedbootstate
           "orange" (unlocked) among them.

Nothing here is written to the phone; the Boot Manager boots it from RAM.
"""
import glob
import os
import struct
import subprocess
import sys
import tempfile
import zipfile

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__)))))
OUT = sys.argv[1] if len(sys.argv) > 1 else sys.exit(__doc__)
BOOT = os.path.join(REPO, "out/retus/boot.img")
VENDOR_BOOT = os.path.join(REPO, "out/dump/vendor_boot_a.img")
INIT_BOOT = os.path.join(REPO, "out/dump/init_boot_a.img")
DTB = os.path.join(REPO, "out/stockdt/stock.dtb")
BUGREPORT = os.path.join(REPO, "fogona-bugreport.zip")


def unpack(img, kind):
    d = tempfile.mkdtemp(prefix=kind + "-")
    subprocess.run(["unpack_bootimg", "--boot_img", img, "--out", d],
                   check=True, stdout=subprocess.DEVNULL)
    return d


def bugreport_sections():
    """The stock command line and bootconfig, from the bugreport's text."""
    z = zipfile.ZipFile(BUGREPORT)
    name = next(n for n in z.namelist() if n.startswith("bugreport") and n.endswith(".txt"))
    cmdline, boot = None, []
    in_boot = False
    for line in z.read(name).decode("utf-8", "replace").splitlines():
        if cmdline is None and line.startswith("Command line: "):
            cmdline = line[len("Command line: "):].strip()
        if line.startswith("Bootconfig: "):
            in_boot = True
            line = line[len("Bootconfig: "):]
        if in_boot:
            if " = " not in line or not line.split(" = ")[0].replace(".", "").replace("_", "").isalnum():
                break
            boot.append(line.strip())
    if not cmdline or not boot:
        sys.exit("bugreport: no command line or bootconfig found")
    return cmdline, boot


def bootconfig_trailer(lines):
    """Linux's bootconfig-in-initrd layout (Documentation/admin-guide/
    bootconfig.rst): the data, NUL-padded to 4 bytes, then its size and
    checksum as little-endian u32s, then the magic."""
    data = ("\n".join(lines) + "\n").encode()
    data += b"\0" * (-len(data) % 4)
    csum = sum(data) & 0xffffffff
    return data + struct.pack("<II", len(data), csum) + b"#BOOTCONFIG\n"


def main():
    for f in (BOOT, VENDOR_BOOT, INIT_BOOT, DTB, BUGREPORT):
        if not os.path.isfile(f):
            sys.exit(f"missing {f}")
    os.makedirs(OUT, exist_ok=True)

    b = unpack(BOOT, "boot")
    with open(os.path.join(b, "kernel"), "rb") as f:
        kernel = f.read()
    if kernel[56:60] != b"ARM\x64":
        sys.exit("boot.img's kernel is not an uncompressed arm64 Image")

    vb = unpack(VENDOR_BOOT, "vendor_boot")
    ib = unpack(INIT_BOOT, "init_boot")
    fragments = sorted(glob.glob(os.path.join(vb, "vendor_ramdisk[0-9]*")))
    if not fragments:
        sys.exit("vendor_boot: no vendor ramdisk")
    ramdisk = b"".join(open(p, "rb").read() for p in fragments)
    ramdisk += open(os.path.join(ib, "ramdisk"), "rb").read()

    cmdline, boot = bugreport_sections()
    initrd = ramdisk + bootconfig_trailer(boot)

    open(os.path.join(OUT, "kernel"), "wb").write(kernel)
    open(os.path.join(OUT, "initrd"), "wb").write(initrd)
    dtb = os.path.join(OUT, "dtb")
    open(dtb, "wb").write(open(DTB, "rb").read())
    subprocess.run(["fdtput", "-d", dtb, "/reserved-memory/ramoops_region", "pmsg-size"], check=True)
    open(os.path.join(OUT, "cmdline"), "w").write(cmdline + " reboot=warm\n")
    print(f"kernel {len(kernel)} B, initrd {len(initrd)} B "
          f"({len(fragments)} vendor ramdisk(s) + init_boot + {len(boot)} bootconfig lines), "
          f"cmdline {len(cmdline)} B -> {OUT}")


main()
