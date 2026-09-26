# postmarketOS Boot Manager (experiment)

The screen the LinuxBoot stage shows before any OS runs, as a PC's firmware
setup and boot menu would: pick and start a boot entry; switch SSH, telnet
and remote access on and off; browse partitions read-only; view the log;
reboot, reboot to the bootloader, or power off. It runs on the phone's
panel, driven by the volume and power keys or touch. The same UI also runs
as a window on the host and as a page in a browser, both talking to the
phone.

Nothing in it is fogona-specific except the stage it rides in: boot
entries come from extlinux/BLS through u-root, and the panel's shape from
gmobile's per-device files.

**Status (2026-09-26):** RAM-booted on the dev unit with `fastboot boot`.
Tried on the phone:

* It draws on the panel and the volume and power keys drive it, touch too.
* The countdown stops on a key, and Remote UI switches on from the phone.
* It lays out around the camera cutout.
* The partition browser lists and browses the microSD card, and serves
  files, previews and thumbnails.
* The browser build connects and drives it.
* A live backend restart (`dev.sh backend`) kept the SSH session and the UI.

Not yet tried: booting pmOS from it, the host-window build, telnet, and the
power actions. "Reboot to bootloader" may not work, since the running tree
shows no reboot-mode node. Without `BOOTMANAGER=1` the stage is the same as
before.

## Pieces

| | what | where it runs |
|---|---|---|
| `bootmanager/` | Go, a u-root command. Owns every privileged action; serves them as JSON lines on `/tmp/bootmanager.sock`; starts and restarts the UI; runs the 10 s autoboot itself, so a UI that never draws still boots pmOS. **Embeds the UI and its web build** (`assets.go`): one binary is the whole boot manager, and the UI runs from a memfd | phone |
| `bootmanager-ui/` | Rust + Slint 1.18. One list-driven screen (`ui/app.slint`); navigation in `src/app.rs`; transport-free protocol client in `src/proto.rs` | phone, host, browser |
| `build.sh` | builds the UI (static) and its web build, copies both and the display-panel files into `bootmanager/assets/` for the embed, and writes the Go workspace for u-root; the stage build calls it | container |
| `dev.sh` | rebuilds the UI, backend or web build and swaps it into the running stage (below) | container |

```
kernel -> u-root init -> gosh /etc/uinit.sh (USB net, as before)
       -> bootmanager ── /tmp/bootmanager.sock ── bootmanager-ui (fbdev + evdev)
                      ├─ :7777 TCP             ── bootmanager-ui --connect (host window)
                      └─ :8080 HTTP + /ws      ── bootmanager-ui wasm (browser)
```

The last two listen only while **Services -> Remote UI** is on. Neither
asks for a password; the only network the stage has is the USB link.

## Build and try

The stage is built by the fogona repo (`/repo`, `FOGONA` in the scripts
here), which takes this checkout from `BOOTMANAGER_SRC` (default
`~/bootmanager`). From `/repo`:

    BOOTMANAGER=1 sh tools/linuxboot/build.sh   # -> out/linuxboot/boot-linuxboot-bootmanager.img
    fastboot boot out/linuxboot/boot-linuxboot-bootmanager.img    # host, RAM only

The image runs the same USB network and `/tmp/hold` as the plain stage
(fogona's FLASHING.md, "LinuxBoot"). The one difference is that sshd
belongs to the boot manager.

Besides the stage, the build reads two things from the fogona repo:
gmobile's version and fogona's display-panel file from its aports overlay
(`build.sh`), and the stock images `android-payload.py` assembles.

## Keys

| key | does |
|---|---|
| Vol+ / Vol- | up / down; the list wraps at both ends |
| Power, short | select |
| Power, held 600 ms | back (every screen but the main one also starts with a Back row) |
| tap a row | select it (drag to scroll) |
| any key or touch | stops the autoboot countdown |

## Why a custom Slint platform on the phone

Slint's own Linux backend (linuxkms) reads input through libinput, and
libinput needs udev, which u-root does not have. The phone build therefore
provides its own platform (`src/platform.rs`). It uses Slint's software
renderer into RAM, copied to `/dev/fb0` (msm's fbdev emulation), and reads
raw `/dev/input/event*`. It sets the console to `KD_GRAPHICS` so fbcon stops
drawing over the UI, and puts it back to text mode on SIGTERM, which the
backend sends before a kexec. Glyphs are rendered into the binary at build
time (`build.rs`), at the phone's scale factor of 2, because the stage has
no fonts. Only ASCII plus the UI's own strings are embedded, so other
characters in file names do not render.

Touch: the stage kernel builds the ICNL9916 driver in and carries its
firmware in the initramfs (the stage loads no modules). The UI follows the
first contact only, so tapping a row selects it and drags scroll the list.
A tap anywhere stops the countdown.

## Panel shape (cutout, rounded corners)

On the phone the UI lays out around the panel, as phosh does. The top line
becomes a status row as tall as the camera cutout, with its text split to
either side, and the side padding keeps clear of the rounded corners. The
browser and host-window builds are not that shape and do not do this.

The shape comes from gmobile's display-panel files (`display.info`,
`bootmanager/display.go`), looked up by device-tree compatible as gmobile
does. gmobile compiles them into its library, so there is nothing to read
off the rootfs. The build embeds gmobile's own set, from the release
tarball of the version pmOS builds (the overlay APKBUILD's `pkgver`, from
pmbootstrap's distfiles), plus fogona's from the aports overlay on top.
That keeps the set version-locked to phosh's without a submodule.

Cutout paths are reduced to bounding boxes. Arcs (round punch-holes) are
boxed conservatively, never smaller than the shape.
`GMOBILE_PANELS=<gmobile>/data/devices/display-panels go test` checks that
all 33 cutouts in gmobile 0.7.3 parse and land on their panels. Headless,
`bootmanager -compatible "motorola,fogona"` stands in for the device tree.

## Live restart, no reflash

Turn on **Remote UI** and **Dev uploads** on the phone, then from the
container:

    sh dev.sh ui        # UI restarts with the new build
    sh dev.sh backend   # backend re-execs in place
    sh dev.sh web       # then reload the browser page

Each is a `PUT` to `:8080/dev/<name>`. The file is kept in `/tmp/dev`
(RAM) and wins over the built-in copy until the stage reboots. So once the
UI or web build has been uploaded, a backend upload's embedded copies are
not what runs; send those again too.

The backend restarts with `exec`, keeping its PID, so sshd and the UI stay
its children. **An SSH session survives it.** It writes
`/tmp/bootmanager-state.json` first (the UI's and sshd's PIDs, which
services were on, whether the autoboot was stopped), and the new image
adopts from it. The sockets close on exec, and the UI reconnects by itself.
Over the socket, `dev.restart {what: ui|backend}` does the same without an
upload.

Tested in the container: the re-exec keeps the PID and restores Telnet,
Remote UI and Dev uploads. The UI process survives, is adopted, and can
then be restarted. On the phone (2026-09-26), `dev.sh backend` re-exec'd in
place and adopted sshd and the UI; the SSH session stayed up.

Dev uploads is a remote root code path with no authentication, like
telnet. That is acceptable on a USB-only link, and it is off by default.

## Kernel tests by kexec, no flash, no install

The Boot Manager can boot a kernel you upload, straight from RAM:

    sh dev.sh stage-hold    # from pmOS: reboot into the Boot Manager, held
    sh dev.sh kexec-stage   # build the stage, kexec into its Image
    sh dev.sh kexec-pmos    # build pmOS's kernel, boot the pmOS entry with it

* **`stage-hold`** leaves a one-shot request on the boot partition
  (`linuxboot/dev-once`) and reboots. The stage's uinit consumes it, and
  that boot stays in the Boot Manager with no countdown, with Remote UI and
  Dev uploads on, ready for an upload. The next boot autoboots as usual.
* **`kexec-stage`** builds the whole stage and uploads its `Image`, which
  carries its DTB and initramfs. The Boot Manager kexecs into it with the
  running tree and command line. Tested 2026-09-26: build to pmOS back in
  about 2 minutes, with the new stage's record in `backlight.log`.
* **`kexec-pmos`** builds `Image.gz` as fogona's BUILD.md does, without
  packaging it, and uploads it. The Boot Manager boots the first entry with only the
  kernel replaced: the entry's initramfs, DTB and command line, and the
  rootfs's modules. So the release string (`uname -r`) must match the
  installed kernel's; `LOCALVERSION=` keeps it so. Tested 2026-09-26: a
  build stamped `KBUILD_BUILD_VERSION=900` came up as `#900` with its
  modules loaded.

Nothing is written, so a kernel that does not come up costs a reboot. **A
kexec is not a cold boot**: anything that depends on ABL's hand-off, such as
the stage's first display power-up, needs a real reboot to test.

**Stock Android, experimental and not working yet:** `dev.sh
kexec-android` boots stock Android's own kernel from RAM, with what ABL
would hand it (`android-payload.py` assembles it). It resets the SoC so
far. The fogona repo's `investigations/stock-android-kexec.md` has the
payload, the attempts, the slot check and the next step.

## Other builds

Host window, against a phone whose Remote UI is on:

    cd bootmanager-ui
    cargo run --release --no-default-features --features desktop -- --connect 172.16.42.1:7777

Browser: turn on Remote UI on the phone and open `http://172.16.42.1:8080`.
The page fills the window and follows resizes. The screen is in the URL
(`/boot`, `/partitions/mmcblk0p2/usr/share`, `/file/<dev>/<path>`,
`/text/<dev>/<path>`). Each screen change is a history entry, so the
browser's Back and Forward walk what you visited. A reloaded or pasted
link opens its screen with its Back stack, and the server answers any such
path with the page. Rows tint on hover.

### Files

In Partitions, tapping a file opens a screen for it. What it offers:

* **Preview** (browser): the file opens over the UI in the browser's own
  viewer, for images, video, audio, text and PDF. `index.html`'s
  `#preview` holds the frame.
* **Open in a new tab** and **Download** (browser): both use
  `GET /files/<dev>/<path>` on the Remote UI port, with `?download=1` for
  an attachment.
* **View as text** (everywhere, the phone too): shows the start of the
  file, up to 256 KiB, in the UI. Binary files say so instead.

JPEG, PNG and GIF files get **thumbnails** in the list, in every build.
The backend makes them (`fs.thumb`, Go's standard decoders, with file and
pixel caps checked before decoding). The UI fetches them one at a time for
the directory on screen and caches them by path. The pmOS wallpaper takes
one request on the phone.

This page runs the phone as root (`/ws`, `/dev/`), so a file from the card
must never run as part of it:

* Every file response but a PDF carries `Content-Security-Policy: sandbox`,
  which gives it an opaque origin and no scripts, and all carry `nosniff`.
  Browsers refuse to show a PDF in a sandboxed document. Their PDF viewers
  run in their own isolated context, never as this page.
* HTML, SVG and XML are served as plain text.
* Paths resolve through an `os.Root` at the partition's mount point, so
  neither `..` nor a symlink on the card reaches the stage's own files
  (`/etc/ssh` holds the host key).
* The cost: an absolute symlink on the card, such as `/bin/sh ->
  /bin/busybox`, is refused rather than followed inside the partition.
  Relative ones work.
* `fs.list` goes through the same root.

Headless, for layout work in the container: run the backend against a
directory holding an `extlinux/extlinux.conf`, and have the UI render a PNG
after a key script:

    bootmanager -scan DIR -socket S -ui "" &
    bootmanager-ui --socket S --screenshot out.png --keys down,enter

## Protocol

Requests are `{"id":N,"cmd":C,"args":{...}}` and replies are
`{"id":N,"data":...}` or `{"id":N,"error":"..."}`. Pushed events are
`{"event":E,"data":...}` with `autoboot`, `entries`, `services`, `busy` or
`error`. The commands are `boot.list`, `boot.start {index}`,
`autoboot.cancel`, `service.list`, `service.set {name, running}`,
`disk.list`, `fs.list {dev, path}`, `fs.read {dev, path}`,
`fs.thumb {dev, path, size}`, `display.info`, `log.tail {lines}`,
`dev.restart {what}` and `power {action: reboot|bootloader|poweroff}`.

By hand on the phone, over SSH: `netcat -U /tmp/bootmanager.sock`. u-root's
netcat quits when its stdin ends, so keep stdin open until the reply
arrives, for example `(echo '{"id":1,"cmd":"disk.list"}'; sleep 2) | netcat
-U /tmp/bootmanager.sock`.

## Traps

* **Cargo's target dir must not be under `/repo`.** rustc mmaps its outputs,
  and the macOS bind mount answers with SIGBUS / `Bad address`. `build.sh`
  uses `~/.cache/bootmanager-target`.
* **u-root stops reading flags at the first package path**, so any `-files`
  given after one is taken as a package and the build fails.
* **Slint 1.18 links fontconfig through fontique** unless
  `i-slint-common/fontconfig-dlopen` is on (see `Cargo.toml`). Linked, the
  binary cannot be static.
* **`block.GetBlockDevices` never fills in `FSType`**, only the file system
  UUID. u-root's own mounting tries every type instead. The backend reads
  the type from the superblock (`mount.FSFromBlock`).
* **The `noload` on browse mounts does not reach a partition the boot scan
  already mounted.** A second mount shares the first one's superblock and
  options, and u-root's scan mounts every partition plain `ro`. On those,
  a dirty journal is replayed at the scan, as u-root's `boot` always
  does. That is a write, but it is also what lets the scan read a current
  `extlinux.conf`. `noload` only protects partitions the scan did not mount.
* **A plain `Rectangle` in a Slint layout lifts its parent's height limit.**
  The header's cutout row did that and the header took half the screen;
  the header has `vertical-stretch: 0` for it. Check a layout change on a
  full-screen screenshot, not a crop.
