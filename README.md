# Warning!

This is LLM generated.
That being said, I still think others might find it interesting, so here it is.

Caveat emptor.

# Boot Manager

A boot menu and recovery screen for a LinuxBoot stage, as a PC's firmware
setup and boot menu would be: pick and start a boot entry; switch SSH,
remote access and fastboot on and off; browse partitions read-only, check
their file systems (ext4, FAT32, exFAT, f2fs, XFS, btrfs) and repair those on an SD card or USB
(a repair follows a check that found errors, and a confirmation); view the log;
reboot (into any mode the device tree offers) or power off. It runs on the
device's panel, driven by its keys or touch, and the same UI, built for
wasm, runs in a browser on a connected host.

Nothing in it is tied to a device or distribution. Boot entries come from
extlinux/BLS through u-root (on btrfs, in each subvolume too, up to 16 per file system, snapshot directories skipped), and stock Android's from an Android boot
image -- in the other A/B slot's boot partition, or at `android/boot.img`
on a partition -- when the stage kept the device tree and initramfs its
bootloader handed it (`-bootloader`: a Qualcomm ABL builds both for the
Android it would boot). The panel's shape and
size come from gmobile's display-panel data (or a file the device
supplies), reboot modes from the device tree.

## Pieces

| | what |
|---|---|
| `bootmanager/` | Go, a u-root command. Owns every privileged action, serves them as JSON lines on `/tmp/bootmanager.sock`, starts and restarts the UI, and runs the autoboot itself, so a UI that never draws still boots. Serves the UI's web build (`-web`) |
| `bootmanager-ui/` | Rust + Slint, one UI in two builds: `fbdev` on the panel (fbdev and evdev directly, since a stage has no udev for libinput), `web` for a browser (wasm, over a WebSocket) |
| `build.sh` | builds both for a stage build to pack |
| `dev.sh` | swaps a new build into a running stage (dev builds) |

```
kernel -> u-root init -> the stage's uinit
       -> bootmanager ── /tmp/bootmanager.sock ── bootmanager-ui (fbdev + evdev)
                      ├─ :8080 HTTP + /ws        ── bootmanager-ui wasm (browser)
                      └─ :8081 HTTP /files/      ── files for it
```

## Putting it in a stage

    sh build.sh OUT

gives `OUT/bootmanager-ui` (static musl), `OUT/web/` (the wasm build,
when the `wasm32-unknown-unknown` target and the wasm-bindgen CLI the
crate pins are there) and `OUT/go.work`. With `GOWORK=OUT/go.work`, add
to the u-root build, `-files` before any package path (u-root stops
reading flags at the first):

    -files OUT/bootmanager-ui:bin/bootmanager-ui -files OUT/web:usr/share/bootmanager-web ... <this checkout>/bootmanager

and run `bootmanager` from the uinit once the network is up, or first
thing with `-wait-ready`: the UI's splash is then up while the stage sets
itself up, and the uinit ends that with `bootmanager -send stage.ready`
(after 20 s the backend goes ahead without it). The flags carry what
differs between devices and stages:

| flag | default | |
|---|---|---|
| `-listen` | `127.0.0.1` | Remote UI's address: a USB network's, never one reachable from elsewhere. The default needs an SSH tunnel |
| `-panel` | | a gmobile-format display-panel file, for a panel gmobile does not have yet |
| `-hold` | `/tmp/hold` | no autoboot while it exists |
| `-bootloader` | `/sys/firmware/bootloader` | the device tree (`fdt`) and initramfs (`initrd`) the bootloader handed the stage, if the stage keeps them. With them, an Android boot image (v3/v4) at `android/boot.img` on a partition is an entry: its kernel, with these, and the tree's `/chosen/bootargs` as its command line |
| `-android-partitions` | | GPT labels of partitions that may hold such an image, read raw; empty: the boot partition of the A/B slot the bootloader did not boot (its bootconfig's `androidboot.slot_suffix`), the stage being in the booted one. A partition's image wins over a file's |
| `-android-boot` | | GPT labels (`boot,recovery`) of partitions to look in for a **legacy Android boot image**, header version 0, 1 or 2 (what a device with its own bootloader, no ABL, has: the image carries its ramdisk, and in v2 its device tree; old Qualcomm images with a QCDT are read as v0). Each is an entry, labelled `Android (boot)`, read raw and never written. Its kernel command line is the header's with `-android-cmdline`'s words after it |
| `-android-dtb` | | the GPT label of the partition holding the device tree such an image is booted with when it has none (v0 and v1): a raw flattened tree, cut at its own size and parsed before it is used. Else the tree the bootloader handed the stage (`-bootloader`) |
| `-android-cmdline` | | a file of words a legacy image's bootloader would have added (serial number, partition bases, the tree's own `/chosen/bootargs`, which kexec-tools replaces) |
| `-kexec` | | kexec-tools' command, split at spaces, to load `-android-boot` entries with where u-root cannot kexec the kernel itself (32-bit ARM); the Boot Manager writes the kernel, ramdisk and tree to RAM and runs `CMD -l KERNEL --append=… --initrd=… --dtb=…`. Empty: load them as every other entry |
| `-kexec-prep` | | a command, split at spaces, run after `-kexec` has loaded a kernel and before the jump, to quiet what could write to memory (a Wi-Fi chip, USB) |
| `-live-images` | `true` | look for `.img` files on removable media (USB, microSD, up to 3 directories deep): each squashfs is an entry, `Live NAME`, booted with the kernel and initramfs from its `/boot` (or beside it), the root an overlay of it over an ext4 persistence `.img` in the same directory (labelled `persistence`, `casper-rw` or `writable`, or named `*persist*`; the one whose name starts with the squashfs's, else the first), or over RAM when there is none. A btrfs `.img` (zstd or not; `nura/os.img`) is searched like a partition instead, each of its subvolumes, and its own entries are shown. The initramfs is the system's with its `/init` replaced by one that finds the media by the squashfs's path, so the stage kernel needs loop and squashfs, and the system's initramfs needs sh, mount, modprobe and switch_root. The media is mounted read-write only when there is a persistence image |
| `-scan-retries` | 10 | how many more times, a second apart, to scan when a boot scan finds nothing; the UI's splash waits for it. 0 for a stage with no entries |
| `-micom` | | a serial device to answer a vehicle microcontroller's keep-alive requests on and read its state from (Diagnostics → Vehicle); a build for one device family, see `internal/micom` |
| `-micom-log` | | append every frame the micom sends, decoded where known, and the link's counters to this file |
| `-logs` | | files shown on the Log screen before the backend's (the uinit's log) |
| `-autoboot` | `10` | seconds |
| `-settings` | | where the entries' order and default, and whether to autoboot and the countdown's length (the menu's Autoboot screen; from the next start, and `-autoboot` is the default until chosen), are kept: a file, or `DEV:PATH`, a file on block device `DEV` (a kernel name) from its root, mounted only to read or write it (past the boot scan's read-only mount, which comes back as it was), or `@FILE`, a file holding one of those, for a stage that finds its boot partition at run time (`-find-boot-files`) and writes the location once it has: until `FILE` exists the load is tried again, and an empty `FILE` means nowhere to keep them. Empty: for this run only |
| `-find-boot-files` | off | a client: print where the stage keeps its files as `DEV FSTYPE DIR`, the first block device (a kernel name) whose file system has a `linuxboot` directory at its root (the file system is `/boot`, as on a microSD card's first partition) or under `boot/` (`/boot` is a directory of the root file system, as on UFS); a card's partitions first, waiting up to 2.5 s for the card. Exits non-zero when there is none |
| `-fsck-dir` | `/usr/libexec/fsck` | the file system checkers for Check and Repair: `e2fsck`, `fsck.fat`, `fsck.exfat`, `fsck.f2fs`, `xfs_repair`, `btrfs` (check only). A file system whose checker is missing reports so; the stage packs them (fogona's `tools/linuxboot/fsck-tools.sh`) |
| `-cryptsetup` | `/usr/libexec/cryptsetup/cryptsetup` | unlocks LUKS partitions (an encrypted pmOS root) with a passphrase typed on the on-screen keyboard, so they can be browsed and checked as `dm-N` partitions. Rescue only: booting an encrypted pmOS needs none of it, its initramfs asks for the passphrase. Needs `CONFIG_DM_CRYPT` in the stage kernel; without the binary the UI leaves unlocking out |
| `-reboot-modes` | `bootloader,fastboot,recovery,download` | reboot modes offered when the device tree maps them; leave out any that strand the device (qcom's `edl` needs a long power-button press to leave) |
| `-ssh-keys`, `-ssh-hostkey` | `/etc/ssh/…` | for u-root's sshd, on at start |
| `-ui` | `/bin/bootmanager-ui` | empty for none |
| `-web` | `/usr/share/bootmanager-web` | the UI's web build |
| `-mark-successful` | off | mark the booted A/B slot successful once the stage is ready, as Android does once it is up, so ABL stops counting its tries down |
| `-fastboot-gadget` | | the configfs USB gadget Fastboot joins; empty: the one bound to a UDC |
| `-wait-ready` | off | start the UI and socket at once; hold the boot scan and countdown, disks, SSH, Remote UI and `-dev-once` until `stage.ready` |
| `-send CMD` | | a client: send CMD to `-socket` (retrying 5 s for it to appear), print the reply, exit non-zero on an error |

### Subcommands

The binary is also a multi-call program. A subcommand is the first argument
(`bootmanager qbootctl -a`) or the name the binary is run under (a symlink
called `qbootctl`), and is handled before the flags above, which it does not
see; it has its own.

| subcommand | |
|---|---|
| `qbootctl [flags]` | the A/B slot tool of [go-qbootctl](https://github.com/W-Floyd/go-qbootctl): the subset of `qbootctl` that postmarketOS uses (`-a`, `-c`, `-m`, `-s`, `-u`, `-b`, `-n`, `-x`), in its output format, with `--dry-run`. It needs no udev; it does not set the UFS boot LUN |
| `msm-firmware-loader [flags]` | [go-msm-firmware-loader](https://github.com/W-Floyd/go-msm-firmware-loader): mounts a Qualcomm device's firmware partitions read-only, links their blobs into one directory and points `firmware_class.path` at it. Needs no udev or other program; `--dry-run`, `--teardown` |
| `reprobe [--dry-run]` | for every bus in `/sys/bus`, writes the name of each device that has **no driver** to the bus's `drivers_probe`, once, so a driver that gave up for want of firmware (`Direct firmware load ... failed`) is probed again after the loader has run. A device that has a driver is never written to, so nothing running is rebound. It costs one probe attempt per unbound device, which for drivers that have none to find is a failed match; `--dry-run` only lists them. One line per device and a summary go to stderr; exit 0 |

`UROOT` names the u-root checkout for `go.work` (default `~/u-root`). The
kexec handover on some arm64 devices needs u-root changes not upstream
yet (a DTB passed with kexec_load, `/memory` from the running system, a
gzip Image); use a u-root with them there.

## An initramfs's unlocker

The same binary is a passphrase prompt for an encrypted root, in place of
unl0kr:

    bootmanager-ui --unlock [--fb /dev/fb0] | cryptsetup open /dev/X root -

It shows a passphrase field (with show/hide) and the on-screen keyboard, and
prints what was typed on stdout, no newline, exiting 0. It needs no backend,
no socket, no udev or libinput: touch and keys come from `/dev/input`.
`CRYPTTAB_SOURCE` names the volume on screen, and a non-zero `CRYPTTAB_TRIED`
says the last try failed, as pmOS's `fde-unlock` passes them. The pmOS side
is the `bootmanager-unlocker` aport (`fde-unlock` and a mkinitfs file list),
a draft that is not built or tested yet.

## The screen

It opens on a carousel: a card per boot entry, its look (colours, icon,
landscape) guessed from the entry's label and command line, in the order
last arranged, centred on the default with the countdown on it, and a
last card, Tools, for the menu: services, partitions, the log, reboot and
power off. Starting an entry grows its card to the whole screen until the
handover; a failed boot puts it back.

**Long-press an entry's card** to arrange it: a bar under it moves it
left or right, and makes it the default, or, pressed again, no longer:
the default is then the entry the scan finds first. The default is an
entry, not a place: moving cards never changes it. Both are kept in
`-settings` by the entry's label, so an entry keeps its place when its
card is out or reflashed, and one the settings do not know goes last.

| key | carousel | menu |
|---|---|---|
| Vol+ / Vol-, arrows | previous / next card | up / down; the list wraps |
| Power (short), Enter | start the entry (or open Tools) | select |
| Power held 600 ms, Esc | open the menu | back (each screen also starts with a Back row; the first one's leads back to the carousel) |
| tap | a side card: bring it to the middle; the middle one: start it | select a row (drag to scroll) |
| swipe | move through the cards | |
| long press | arrange the card: Vol+ / Vol- move it, Power makes it (or no longer) the default, Power held ends | |
| any key or touch | stops the autoboot countdown | |

## Panel shape and scale

The UI looks the panel up by the device tree's compatibles in gmobile's
display-panel set, as phosh does: `bootmanager/internal/panel/panels.json`,
reduced from a pinned gmobile release by `go generate ./internal/panel`
(`tools/panelgen`; bump the version and checksum there). `-panel` wins
over the set.

From it:

* **Scale**: logical pixels of about 0.19 mm, from the panel's physical
  width, in quarter steps: 2 on a 68 mm, 720 px phone, 3 on a 1080 px one.
  Without a physical size, the framebuffer's (when the driver reports it),
  else 360 logical pixels across.
* **Cutouts**: the header starts below the lowest of those near the top
  edge.
* **Corners**: the side padding clears the top corners' radius.

The browser build is not that shape and does none of this. The fonts
(DejaVu, `bootmanager-ui/fonts/`) are embedded in both builds and
rasterised at run time, at any scale and for any character they have.

## Remote UI

**Services -> Remote UI** serves the web build at `http://<listen>:8080`.
The screen is in the URL (`/boot`, `/partitions/mmcblk0p2/usr/share`,
`/file/<dev>/<path>`), so Back and Forward walk what was visited and a
link opens its screen. A file's screen offers Preview (the browser's own
viewer, over the UI), a new tab and Download. Nothing there asks for a
password, so:

* it listens on `-listen` only, and answers only requests addressed to an
  IP address (or `localhost`), so a DNS-rebinding page cannot pass as it;
* the WebSocket refuses a cross-origin handshake, so no other page in the
  host's browser can drive the device;
* files from partitions come from `:8081`, another origin, so nothing on a
  card can act as the page. They are also sandboxed by CSP (but PDFs,
  which browsers will not show sandboxed), `nosniff`, and markup goes out
  as text;
* paths resolve through an `os.Root` at the partition's mount point, so
  neither `..` nor a symlink reaches the stage's own files. The cost: an
  absolute symlink on a card is refused rather than followed.

With the default `-listen`, reach it through SSH:
`ssh -L 8080:127.0.0.1:8080 -L 8081:127.0.0.1:8081 root@<device>`.

## Fastboot

**Services -> Fastboot** makes the device a fastboot device beside the
stage's USB network, for the host's stock `fastboot` to flash from the
stage: GPT partitions by name (`fastboot flash boot_b boot.img`), any
block device by its kernel name (`mmcblk1p2`), and the SD card whole as
`sdcard` (`fastboot flash sdcard pmos.img`: a disk image, partition table
and all). It also takes `getvar`, `reboot [bootloader|recovery|...]` (the
device tree's modes) and `continue` (boot the default entry). It answers
as fastbootd does (`is-userspace:yes`), without logical partitions.

* **It is off by default and asks for nothing**: whoever has the cable
  can overwrite any partition, the stage's own included. Switch it on only
  on a host you trust.
* Switching it on or off rebinds the gadget: **the USB network drops for a
  moment**, and SSH over it usually survives.
* The serial number, `getvar` and `fastboot devices` alike, is the
  bootloader's (`androidboot.serialno`), else the device tree's
  `serial-number`: the one ABL's fastboot shows. The gadget has it only
  while Fastboot is on.
* `current-slot` is the bootloader's (`androidboot.slot_suffix`), so
  `flash boot` writes the booted slot's, the stage's own. Name the slot.
* `set_active` and `slot-successful`/`slot-unbootable`/`slot-retry-count`
  are the Diagnostics screen's slots (below).
* A download is held in RAM, up to 256 MiB. The host sends a larger image
  as sparse pieces by itself.
* A flash unmounts the browse and boot-scan mounts first. The device is
  opened exclusively, so a partition still mounted elsewhere is refused.
  The boot entries are found again 2 s after the last flash.
* The kernel needs `CONFIG_USB_CONFIGFS_F_FS`. Its `functionfs` file system
  shows in `/proc/filesystems` only once the first `ffs.*` function exists.

## Diagnostics

**Tools -> Diagnostics** starts with the power supplies the kernel lists
(`/sys/class/power_supply`, or `-power-supply`): per battery its level,
status, voltage, current, temperature, health and charge, per charger
whether it is online and its limits, and any other property raw. The same
level is the battery indicator on the carousel and the menu, pushed to the
UI when it changes (polled every 5 s, and only when a battery is found). It
then shows the A/B slots as a Qualcomm ABL keeps
them, and what the bootloader's bootconfig says (its version, the booted
slot, boot reason, verified boot state, serial). Per slot: active,
priority, tries left or booted successfully, unbootable; and **Boot slot
X next**, **Mark slot X successful**, **Mark slot X failed**.

ABL keeps the state in the GPT attributes of `boot_a`/`boot_b` (bits
48-49 priority, 50 active, 51-53 tries left, 54 successful, 55
unbootable) and boots the active slot of highest priority. On each boot
of a slot not marked successful it takes a try off, and at none marks it
unbootable and boots the other. Nothing in a LinuxBoot stage marks a boot
successful unless `-mark-successful` does.

* **Boot slot X next** is ABL's `set_active`: X at priority 3, active,
  7 tries, not successful; the other slot priority 2, not active; the
  active bit of every `_X` partition set and of the other slot's cleared.
* **Mark failed** is what ABL does when the tries run out: unbootable, not
  successful (ABL reads unbootable only then), priority 0. With no other
  bootable slot ABL stops in its fastboot.
* Only the GPTs of the disks holding `boot_a` and `boot_b` are written.
  ABL's `set_active` also marks `xbl_a`/`xbl_b`, each on a UFS LUN of its
  own, but the boot ROM picks xbl by a UFS attribute, not those bits, and
  those LUNs' GPTs are the ones a bad write could leave the device
  unbootable by.
* Both GPT copies are checked before anything is written (CRCs, and the
  entries identical), then the backup is written and read back from the
  device, then the primary. The disk that makes the chosen slot bootable
  goes first, so a failure midway leaves both slots bootable.

## Test fixtures

No binary fixture is committed, and `go test ./...` needs no generated file.
The A/B slot code and its tests, with the generator of synthetic GPT disks
they run on, are in go-qbootctl; the loader's, with its synthetic super
metadata, in go-msm-firmware-loader. This module's tests build what they need
in process.

## What stopped the autoboot, and the stage's logs

Any input stops the countdown, so the logs say which input.

- **Input log.** The UI (stderr, which the stage's uinit keeps) logs the first
  40 events of each input device, and every event while the countdown is
  shown, 200 lines at most (then one `input log suppressed` line):
  `bootmanager-ui: ev t=3.041 event2 gt9916 EV_ABS ABS_MT_POSITION_X 540`.
  `t` is seconds since the UI started; the device is its event node and name;
  codes are named for keys and axes (`BTN_TOUCH`, `ABS_MT_TRACKING_ID`,
  `KEY_VOLUMEUP`). The devices, with a touchscreen's axis ranges, are listed
  as `bootmanager-ui: input /dev/input/event2 NAME (touch) x 0..1079 y 0..2399`,
  and `autoboot countdown shown` / `gone` marks the countdown in the same log.
- **Cancel reason.** When a touch or key stops the countdown the UI logs
  `bootmanager-ui: t=… activity cancels the autoboot: last input EVENT, N ms ago`
  and sends that event with `autoboot.cancel {reason, age_ms}`. The backend
  logs `autoboot: cancelled by ui: EVENT, input N ms earlier (3.4 s into the
  countdown)`. Any other cancel names itself instead (`boot start`,
  `teardown`, `a file system check`, `switching fastboot on`, `hold file`,
  `internal call`). The rest of the countdown's life is logged too:
  `autoboot: countdown started (3 s, entry NAME)`, `autoboot: not started
  (no boot entries | autoboot is off | hold file PATH exists)`,
  `autoboot: booting NAME`.
- **Logs that outlive the stage.** At teardown, before a handover or a power
  action, the Boot Manager copies `/tmp/uinit.log`, `/tmp/record_boot.log` and
  its own log (`-log`) to the directory of the settings file (the boot files
  directory the `-settings` location is in, on the same device) as
  `stage-uinit.log`, `stage-record_boot.log` and `stage-bootmanager.log`,
  replaced at each boot. Each starts with a line `# bootmanager: copy of SRC,
  taken at Boot Manager uptime N s`; only the last 1 MiB of a log is kept. It
  goes through the same write as the settings (the boot scan's read-only mounts
  of the device are unmounted and restored), takes at most 4 s, and a failure
  is logged and ignored: it never delays or stops the boot. With no settings
  location, or its device gone, nothing is written.

## Dev builds

`TAGS=dev sh build.sh OUT` adds **Services -> Dev uploads**, which takes
new builds over Remote UI's port, and `-dev-once FILE`, services to switch
on at start. A normal build has neither: no remote path to run code as
root.

    sh dev.sh ui                   # UI restarts with the new build
    sh dev.sh backend              # backend re-execs in place
    sh dev.sh web                  # new web build; reload the page
    sh dev.sh kexec Image inherit=running
    sh dev.sh kexec Image.gz inherit=entry:0
    sh dev.sh kexec kernel initrd=initrd dtb=dtb 'cmdline=<cmdline'

Uploads are kept in `/tmp/dev` (RAM) and win over the built-in copies
until the stage reboots.

The backend re-execs keeping its PID, so sshd and the UI stay its
children: **an SSH session survives it**. A state file hands the new image
the UI's and sshd's PIDs, which services were on and whether the autoboot
was stopped; the UI reconnects by itself.

`kexec` boots a kernel from RAM, nothing written. `inherit=running` takes
the stage's device tree and command line; `inherit=entry:N` boot entry N's
initramfs, tree and command line, so the kernel's release must match the
entry's installed modules. **A kexec is not a cold boot**: whatever
depends on the bootloader's hand-off needs a real reboot to test.

## Headless

For layout work anywhere: run the backend against a directory holding an
`extlinux/extlinux.conf`, and have the UI render a PNG after a key script.
The picture takes the named panel's size and scale:

    bootmanager -scan DIR -socket S -ui "" -compatible fairphone,fp4 &
    bootmanager-ui --socket S --screenshot out.png --keys down,enter

For an animation, `shot` saves the frame so far as `out-N.png` and `tick`
waits 100 ms: `--keys enter,tick,shot,tick,shot,...` is a filmstrip.

## Protocol

Requests are `{"id":N,"cmd":C,"args":{...}}`, replies `{"id":N,"data":...}`
or `{"id":N,"error":"..."}`. Pushed events are `{"event":E,"data":...}`:
`autoboot`, `entries`, `services`, `busy`, `error`. The commands:
`hello`, `boot.list`, `boot.start {index}`, `boot.move {index, by}`,
`boot.default {index}` (-1: the scan's first again), `autoboot.cancel`,
`service.list`, `service.set {name, running}`, `disk.list`,
`fs.list {dev, path}`, `fs.read {dev, path}`,
`fs.thumb {dev, path, size | width, height}`, `stage.ready`,
`display.info`, `log.tail {lines}`, `power.modes`, `slots.info`,
`slots.set {slot, action: active|successful|unbootable}`,
`power {action: reboot|poweroff, mode}`, and in dev builds
`dev.restart {what: ui|backend}`. `hello` carries the protocol version,
which the UI and page check. Under `-wait-ready`, `service.set`,
`disk.list` and `fs.*` answer an error until `stage.ready`.

From a script: `bootmanager -send CMD`. Over SSH: `netcat -U /tmp/bootmanager.sock`. u-root's netcat quits when
its stdin ends, so keep it open until the reply:
`(echo '{"id":1,"cmd":"disk.list"}'; sleep 2) | netcat -U /tmp/bootmanager.sock`.

## Traps

* **A dragged menu row can stay lit** (the hover fill) after the finger
  lifts: a drag the list cannot scroll, begun within Flickable's 100 ms
  press delay, leaves the row's `has-hover` set. Slint 1.18.1's bug,
  [slint#5262](https://github.com/slint-ui/slint/issues/5262); fixed by
  [slint#13218](https://github.com/slint-ui/slint/pull/13218), not yet
  released. Take the release that has it rather than working around it.
