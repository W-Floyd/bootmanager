# Boot Manager

A boot menu and recovery screen for a LinuxBoot stage, as a PC's firmware
setup and boot menu would be: pick and start a boot entry; switch SSH,
remote access and fastboot on and off; browse partitions read-only; view the log;
reboot (into any mode the device tree offers) or power off. It runs on the
device's panel, driven by its keys or touch, and the same UI, built for
wasm, runs in a browser on a connected host.

Nothing in it is tied to a device or distribution. Boot entries come from
extlinux/BLS through u-root, and stock Android's from an Android boot
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
| `-logs` | | files shown on the Log screen before the backend's (the uinit's log) |
| `-autoboot` | `10` | seconds |
| `-reboot-modes` | `bootloader,fastboot,recovery,download` | reboot modes offered when the device tree maps them; leave out any that strand the device (qcom's `edl` needs a long power-button press to leave) |
| `-ssh-keys`, `-ssh-hostkey` | `/etc/ssh/…` | for u-root's sshd, on at start |
| `-ui` | `/bin/bootmanager-ui` | empty for none |
| `-web` | `/usr/share/bootmanager-web` | the UI's web build |
| `-mark-successful` | off | mark the booted A/B slot successful once the stage is ready, as Android does once it is up, so ABL stops counting its tries down |
| `-fastboot-gadget` | | the configfs USB gadget Fastboot joins; empty: the one bound to a UDC |
| `-wait-ready` | off | start the UI and socket at once; hold the boot scan and countdown, disks, SSH, Remote UI and `-dev-once` until `stage.ready` |
| `-send CMD` | | a client: send CMD to `-socket` (retrying 5 s for it to appear), print the reply, exit non-zero on an error |

`UROOT` names the u-root checkout for `go.work` (default `~/u-root`). The
kexec handover on some arm64 devices needs u-root changes not upstream
yet (a DTB passed with kexec_load, `/memory` from the running system, a
gzip Image); use a u-root with them there.

## The screen

It opens on a carousel: a card per boot entry, its look (colours, icon,
landscape) guessed from the entry's label and command line, the default
first with the countdown on it, and a last card, Tools, for the menu:
services, partitions, the log, reboot and power off. Starting an entry
grows its card to the whole screen until the handover; a failed boot
puts it back.

| key | carousel | menu |
|---|---|---|
| Vol+ / Vol-, arrows | previous / next card | up / down; the list wraps |
| Power (short), Enter | start the entry (or open Tools) | select |
| Power held 600 ms, Esc | open the menu | back (each screen also starts with a Back row; the first one's leads back to the carousel) |
| tap | a side card: bring it to the middle; the middle one: start it | select a row (drag to scroll) |
| swipe | move through the cards | |
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
* **Cutouts**: those near the top edge share the header's first line,
  which grows to clear the lowest; its text stays left of them.
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

**Tools -> Diagnostics** shows the A/B slots as a Qualcomm ABL keeps
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
`hello`, `boot.list`, `boot.start {index}`, `autoboot.cancel`,
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

* **The wasm-bindgen crate and CLI must be the same version**; the crate
  is pinned (`Cargo.toml`), and `build.sh` refuses a CLI that differs.
* **Slint links fontconfig through fontique** unless
  `i-slint-common/fontconfig-dlopen` is on (`Cargo.toml`). Linked, the
  binary cannot be static.
* **`block.GetBlockDevices` never fills in `FSType`**; the backend reads
  it from the superblock (`mount.FSFromBlock`).
* **Browse mounts' `noload` does not reach a partition the boot scan
  already mounted**: a second mount shares the first one's superblock and
  options. On those, a dirty journal was replayed at the scan, as u-root's
  `boot` always does, and as it must to read a current `extlinux.conf`.
* **A plain `Rectangle` in a Slint layout lifts its parent's height
  limit**; the header has `vertical-stretch: 0` for it. Check layout
  changes on a full-screen screenshot.
* **Slint's window keeps its logical size across a scale change**: set the
  physical size again after one (`app.rs`, `apply_panel`).
