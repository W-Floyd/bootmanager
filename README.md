# Recovery UI for the LinuxBoot stage (experiment)

A TWRP-style screen for the stage: pick a boot entry, switch SSH, telnet
and remote access on and off, browse partitions read-only, see the log,
reboot / reboot to bootloader / power off. It runs on the phone's panel,
driven by the volume and power keys. The same UI also runs as a
window on the host and as a page in a browser, both talking to the phone.

**Status (2026-09-26):** RAM-booted on the dev unit with `fastboot boot`: it
draws on the panel, the volume and power keys navigate it, the countdown
stops on a key, Remote UI switches on from the phone, touch works, the
partition browser lists and browses the microSD card, and a live backend
restart (`dev.sh backend`) kept the SSH session and the UI. Not yet tried on
the phone: booting pmOS from it, the browser and host-window builds,
telnet, and the power actions ("Reboot to bootloader"
may not work, since the running tree shows no reboot-mode node). Without
`RECOVERY=1` the stage is the same as before.

## Pieces

| | what | where it runs |
|---|---|---|
| `recovery-backend/` | Go, a u-root command. Owns every privileged action; serves them as JSON lines on `/tmp/recovery.sock`; starts and restarts the UI; runs the 10 s autoboot itself, so a UI that never draws still boots pmOS. **Embeds the UI and its web build** (`assets.go`): one binary is the whole recovery, and the UI runs from a memfd | phone |
| `recovery-ui/` | Rust + Slint 1.18. One list-driven screen (`ui/app.slint`); navigation in `src/app.rs`; transport-free protocol client in `src/proto.rs` | phone, host, browser |
| `build.sh` | builds the UI (static) and its web build, copies both into `recovery-backend/assets/` for the embed, and writes the Go workspace for u-root; `../build.sh` calls it | container |
| `dev.sh` | rebuilds the UI, backend or web build and swaps it into the running stage (below) | container |

```
kernel -> u-root init -> gosh /etc/uinit.sh (USB net, as before)
       -> recovery-backend ── /tmp/recovery.sock ── recovery-ui (fbdev + evdev)
                           ├─ :7777 TCP        ── recovery-ui --connect (host window)
                           └─ :8080 HTTP + /ws ── recovery-ui wasm (browser)
```

The last two listen only while **Services -> Remote UI** is on. Neither
asks for a password; the only network the stage has is the USB link.

## Build and try

    RECOVERY=1 sh tools/linuxboot/build.sh   # -> out/linuxboot/boot-linuxboot-recovery.img
    fastboot boot out/linuxboot/boot-linuxboot-recovery.img    # host, RAM only

The image runs the same USB network and `/tmp/hold` as the plain stage
(FLASHING.md, "LinuxBoot"). The one difference is that sshd belongs to
the backend.

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
A tap anywhere stops the countdown. Not yet tried on the phone.

## Live restart, no reflash

Turn on **Remote UI** and **Dev uploads** on the phone, then from the
container:

    sh tools/linuxboot/recovery/dev.sh ui        # UI restarts with the new build
    sh tools/linuxboot/recovery/dev.sh backend   # backend re-execs in place
    sh tools/linuxboot/recovery/dev.sh web       # then reload the browser page

Each is a `PUT` to `:8080/dev/<name>`. The file is kept in `/tmp/dev`
(RAM) and wins over the built-in copy until the stage reboots.

The backend restarts with `exec`, keeping its PID, so sshd and the UI stay
its children. **An SSH session survives it.** It writes
`/tmp/recovery-state.json` first (the UI's and sshd's PIDs, which services
were on, whether the autoboot was stopped), and the new image adopts from
it. The sockets close on exec, and the UI reconnects by itself. Over the
socket, `dev.restart {what: ui|backend}` does the same without an upload.

Tested in the container: the re-exec keeps the PID and restores Telnet,
Remote UI and Dev uploads. The UI process survives, is adopted, and can
then be restarted. On the phone (2026-09-26), `dev.sh backend` re-exec'd in
place and adopted sshd and the UI; the SSH session stayed up.

Dev uploads is a remote root code path with no authentication, like
telnet. That is acceptable on a USB-only link, and it is off by default.

## Other builds

Host window, against a phone whose Remote UI is on:

    cd tools/linuxboot/recovery/recovery-ui
    cargo run --release --no-default-features --features desktop -- --connect 172.16.42.1:7777

Browser: turn on Remote UI on the phone and open `http://172.16.42.1:8080`.
The page fills the window and follows resizes.

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

* Every file response but a PDF carries `Content-Security-Policy: sandbox`, which
  gives it an opaque origin and no scripts, and all carry `nosniff`.
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

    recovery-backend -scan DIR -socket S -ui "" &
    recovery-ui --socket S --screenshot out.png --keys down,enter

## Protocol

Requests are `{"id":N,"cmd":C,"args":{...}}` and replies are
`{"id":N,"data":...}` or `{"id":N,"error":"..."}`. Pushed events are
`{"event":E,"data":...}` with `autoboot`, `entries`, `services`, `busy` or
`error`. The commands are `boot.list`, `boot.start {index}`,
`autoboot.cancel`, `service.list`, `service.set {name, running}`,
`disk.list`, `fs.list {dev, path}`, `fs.read {dev, path}`, `fs.thumb {dev, path, size}`, `log.tail {lines}`,
`dev.restart {what}` and
`power {action: reboot|bootloader|poweroff}`.

## Traps

* **Cargo's target dir must not be under `/repo`.** rustc mmaps its outputs,
  and the macOS bind mount answers with SIGBUS / `Bad address`. `build.sh`
  uses `~/.cache/recovery-target`.
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
