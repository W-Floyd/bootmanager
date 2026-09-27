# Boot Manager: restructure plan

**Status:** implemented on branch `restructure`, device- and
distro-neutral (not just fogona-neutral). Where it differs from below:

- Device facts are flags (`-listen`, `-panel`, `-hold`, `-logs`,
  `-ssh-*`, `-dev-once`), not a JSON config: the stage's uinit passes them.
- `-listen` defaults to `127.0.0.1` (SSH tunnel), not a USB address. No
  token auth.
- The wasm build stays (served from `-web`, not embedded); the desktop
  build and its TCP port are gone.
- `platform.rs` keeps `libc` for fbdev, with `repr(C)` structs, not
  `rustix`. The `evdev` crate does input.
- No `slog`, no JSON-RPC renaming, no Makefile (`build.sh` is the stage
  build's entry point; `dev.sh` calls it), no CI (no host for it yet).
- The u-root fork is documented, not replaced.

Goals: device-agnostic, idiomatic, more secure, less to maintain, smarter
gmobile panel bundling (driving shape, size and scale), less complexity.

## Findings that drive the plan

**Hard-coded to fogona.** The README says nothing in it is fogona-specific, but these are:

- `go.mod` module path `fogona.local/bootmanager`.
- [build.sh](build.sh): gmobile's version comes from fogona's aports overlay
  and the tarball from the pmbootstrap distfiles cache. It also copies
  `motorola-fogona.json` in.
- [dev.sh](dev.sh): `kexec-stage` and `kexec-pmos` assume `/repo`,
  `~/exp-linuxboot`, `~/fogona-mainline` and `/opt/pmbootstrap-src`.
  `stage-hold` assumes pmOS's default user and password (`147147`) via
  `sshpass`.
- [android-payload.py](android-payload.py) and `kexec-android` are
  fogona-only experiments.
- **Scale factor 2 is baked in** at
  [build.rs](bootmanager-ui/build.rs) and
  [platform.rs](bootmanager-ui/src/platform.rs) (`SCALE`). The screenshot
  size is 720×1600 in [main.rs](bootmanager-ui/src/main.rs).
- **Cutout bug:** [app.rs](bootmanager-ui/src/app.rs) `load_panel` only uses
  cutouts with `y == 0`, so punch-hole panels are ignored. That includes the
  `motorola,dubai` case in the test.
- Hard-coded values: `172.16.42.1` in
  [services.go](bootmanager/services.go); "ABL fastboot"; `mmcblk` sorted
  first in [disks.go](bootmanager/disks.go); and the stage's `/tmp/hold`,
  `/tmp/dev-once`, `/tmp/uinit.log` and `/etc/ssh/*`.
- Hidden dependency: `go.mod` pins u-root v0.15.0, but `go.work` points at
  `~/u-root`. That is the fork with the kexec_load path, and it isn't
  declared anywhere.

**Security (real holes, not just "no auth")**

1. **Any website can control the phone as root.** The `/ws` handshake
   accepts any Origin ([remote.go](bootmanager/remote.go)), so a page open in
   any tab on the host can drive boot, power and file reads.
2. **Any website can get root code execution while Dev uploads is on.**
   `devHandler` in [dev.go](bootmanager/dev.go) accepts `POST`, and a
   `text/plain` POST is a CORS "simple request" with no preflight check. A
   site can upload a replacement `bootmanager` binary, which the backend
   then re-execs.
3. Everything listens on every interface (`:23`, `:7777`, `:8080`). That
   becomes a problem as soon as a device has Wi-Fi or a USB-Ethernet adapter
   in the stage.
4. Uploads have no size cap and fill the RAM-backed `/tmp`. The HTTP server
   has no timeouts.
5. Card files are served from the same origin as the control page. PDFs are
   exempt from the sandbox header.
6. `open_url` in [web.rs](bootmanager-ui/src/web.rs) calls `window.open`
   without `noopener`.

**Robustness bug: a stalled client stops autoboot.** `broadcast` holds
`clientsMu` and does blocking writes with no deadline
([main.go](bootmanager/main.go)). A frozen browser tab or a half-dead TCP
peer blocks the countdown loop, and the phone never boots.

**Complexity hot spots**

- Go: package-level mutable state everywhere. `init()` exists only to break
  initialisation cycles. The teardown sequence is copied between `handover`
  and `power`, and the sleep-then-reexec is copied between `devHandler` and
  `devRestart`. `fsList` mounts and opens the root twice. `hello` is never
  called.
- Rust: 927-line `app.rs` with one giant `build_screen` match. Per-screen
  `Load<T>` fields sit on `State`. Stale replies are detected by comparing
  `format!("{:?}")` strings. Percent-decoding is duplicated in `app.rs` and
  `web.rs`. The protocol is untyped `serde_json::Value`. `platform.rs` reads
  ioctl structs by byte offset (`fix[24..28]`, `var[8]`).
- Build: `build.sh` and `dev.sh` duplicate the cargo commands. Go can't
  build until Rust output has been copied into a gitignored `assets/`.
  `wasm-bindgen` is pinned to Alpine's CLI version.

## Plan

### Phase 1: Security fixes (small, do first)

- Bind Remote UI, Dev uploads and telnet to the USB gadget address from a
  `-listen` flag, not `0.0.0.0`.
- Reject a `/ws` Origin that doesn't match Host. Swapping
  `x/net/websocket` for `github.com/coder/websocket` gives this check by
  default and removes the pipe-and-newline shim.
- `/dev/` accepts `PUT` only. Add `http.MaxBytesReader` and set
  `ReadHeaderTimeout`.
- Give each client a buffered send channel and a writer goroutine. Drop a
  client when its buffer is full. Autoboot must never wait on a peer.
- Stronger option: add a per-boot token, shown on the panel and required as
  a cookie. Or bind Remote UI to `127.0.0.1` only and document
  `ssh -L 8080:localhost:8080`, which reuses SSH's pubkey auth for free.

### Phase 2: Split device facts from code

Define the stage contract as **one JSON config** (stdlib only), e.g.
`/etc/bootmanager.json`, written by each device's stage build:

```json
{"listen":"172.16.42.1","hold_file":"/tmp/hold","once_file":"/tmp/dev-once",
 "extra_logs":["/tmp/uinit.log"],"ssh":{"keys":"…","host_key":"…"},
 "panel":"/etc/bootmanager/panel.json"}
```

- **Reboot modes from the device tree, not a hard-coded row.** Walk
  `/proc/device-tree` for `mode-*` properties: qcom PON, `syscon-reboot-mode`
  and others all use them. Offer one row per mode found (bootloader,
  recovery, edl, …) and none when there are none. That also resolves the
  README's "reboot to bootloader may not work" note.
- **Disk ordering:** put SD and USB first, using
  `/sys/block/X/device/type == "SD"` and `removable`, instead of `mmcblk*`.
- **Build for any target:** a `TARGET` triple (default
  `aarch64-unknown-linux-musl`) that sets `GOARCH` too, replacing host-triple
  detection and the hard-coded `GOARCH=arm64`.
- Module path → a neutral one. Make the u-root fork an explicit `replace` in
  `go.mod`, or better, upstream the kexec change.
- Move `android-payload.py`, `kexec-android`, `kexec-pmos` (the
  envkernel/pmbootstrap invocation) and `stage-hold` into the fogona repo.
  Here, keep only generic hooks (see Phase 5).

### Phase 3: gmobile panels drive shape, size and scale

**Bundling**

- Add a `go:generate` tool (`tools/panelgen`) that fetches gmobile's
  `data/devices/display-panels` at a **pinned tag and checksum** from
  upstream. No pmbootstrap cache and no fogona APKBUILD.
- It reduces every panel to a compact table: compatible →
  `{xres, yres, width_mm, height_mm, radius, cutouts: [box]}`. The table is
  written to `panels_gen.json` and committed; the Go code embeds it with
  `//go:embed`.
- The SVG path parser moves into the generator. The stage parses no SVG at
  runtime, and `TestPathBoundsGmobileData` runs on every build instead of
  being skipped behind an env var.
- Device overrides (a panel not yet upstream, like fogona's): the config's
  `panel` path, a file in that device's initramfs, is checked before the
  table. No rebuild of bootmanager is needed. Bumping gmobile is one line
  and `go generate`.

**Dimensions and scale**

- `display.info` becomes the UI's source for everything: physical size,
  border radius, cutouts and a **computed scale factor**.
- Compute the scale from the panel's physical size in mm, the way phosh
  does: pick the closest of 1, 1.25, 1.5, 1.75, 2, 2.5, 3 for a target
  logical DPI. This assumes gmobile panels carry `width` and `height` in mm
  (not yet confirmed). Fallbacks, in order: the fb's
  `fb_var_screeninfo.width`/`height` (mm), then `xres / 360`.
- The UI starts, calls `display.info`, then sets `ScaleFactorChanged`. The
  screenshot path takes its size from the same panel
  (`bootmanager -compatible X` → UI), which replaces 720×1600.
- Scale the cutout coordinates by `fb.xres / panel.xres` so a scaled fb
  still lines up.
- Fix the cutout selection: take every cutout whose box intersects the
  status row, not just `y == 0`. Put the status text left of the leftmost
  one and right of the rightmost one.
- **Prerequisite:** a runtime scale needs runtime glyphs. Switch the fbdev
  build from `EmbedForSoftwareRenderer` at a fixed 2.0 to embedding the TTF
  and rasterising at run time. The lockfile already has `skrifa` and `zeno`,
  so the software renderer's font path looks available. The exact Slint
  feature name for 1.18 still needs checking. This also fixes the README's
  known gap: non-ASCII file names don't render. Vendor a DejaVu subset (or
  the full TTF) into the repo so `FONT_DIR` and `apk add font-dejavu` stop
  being build inputs.

### Phase 4: Clean, idiomatic Go backend

```
cmd/bootmanager/     flags + config, wiring only
internal/rpc/        conn abstraction (lines or WS messages), typed dispatch, per-client writers
internal/boot/       scan, autoboot (context.WithCancel + time.Ticker), handover
internal/svc/        Service interface {Start, Stop, PID}; sshd, remote
internal/storage/    disks, a cache of one *os.Root per mounted partition, fs.list/read, files handler
internal/panel/      lookup + embedded generated table
internal/dev/        //go:build dev: uploads, re-exec, kexec tests
```

- **A `Manager` struct owns all state.** Handlers become methods, so the
  `init()` cycle workarounds go away and the code becomes testable.
- Typed registration: `rpc.Handle("boot.start", func(a BootStart) (any, error))`,
  with typed reply structs instead of `map[string]any`.
- One `teardown()` for boot, power and re-exec. Fix the re-exec failure
  path: today in-process services are stopped but still marked running if
  `Exec` fails.
- Use `hello` for protocol version checks, or delete it.
- Standard library and x/ packages instead of hand-rolled code:
  - `slices.SortStableFunc` instead of the old sort calls.
  - `log/slog` instead of `log`.
  - `golang.org/x/image/draw` instead of `shrink`/`clamp` and their test.
  - A semaphore to cap concurrent thumbnails; one decode can use about
    160 MB at the pixel cap.
- **Put dev features behind a build tag** (uploads, re-exec and handoff
  adoption, kexec tests). Production images then carry no remote code-upload
  path at all, and roughly 400 lines drop out of the default build.
- **Drop telnet** (`telnet.go` and the `creack/pty` dependency). SSH covers
  it. Keep telnet only if key-less devices need a way in, and then bind it
  to the USB address.
- **Stop embedding the UI in the backend.** Ship `bootmanager-ui` and `web/`
  as initramfs files through u-root `-files`, and let `hello`'s version
  check catch drift. That deletes `assets.go`'s memfd logic, the three-layer
  override (upload / embed / `-ui` on disk), the gitignored `assets/`
  directory, and the rule that Rust must build before Go.

### Phase 5: A generic kexec test endpoint (dev tag)

Replace the six upload names (`kernel-stage`, `kernel-pmos`,
`android-kernel`, `android-initrd`, `android-dtb`, `kexec-android`) with one
endpoint:

`PUT /dev/kexec` (multipart) with `kernel`, and optionally `initrd`, `dtb`,
`cmdline`, and `inherit=entry:N|running`.

Missing pieces come from boot entry N or from the running system. This
covers stage, pmOS and Android without naming any of them. The fogona repo
keeps its own wrappers.

### Phase 6: Rust UI

- **Typed protocol:** `#[serde(tag="event", content="data")] enum Event`,
  typed replies, and `call::<T>()`. That removes about five copies of
  `from_value(...).map_err(...)` and all the `data["x"].as_str()` lookups.
- **The screen owns its data:** `Files { dev, path, listing: Load<Listing> }`
  and so on. Each screen gets `load()` and `rows()`, in a module per screen.
  Replace the `screen_key` string comparison with a request generation
  counter. Target: `app.rs` under 400 lines.
- **`platform.rs`:** use the `evdev` crate for input (capabilities, abs
  ranges, event parsing). Use `rustix` (already in the lockfile) for mmap,
  poll and pipe. Define `repr(C)` `fb_*_screeninfo` structs instead of byte
  offsets, and build the pixel packer from the fb's red/green/blue
  bitfields. Render 16 bpp directly with Slint's `Rgb565Pixel`.
- Use `percent-encoding` (already in the lockfile) instead of the two
  hand-written decoders and `url_path`.

### Phase 7: Broader restructures (recommended, substantial)

1. **Replace the wasm and desktop builds with a plain HTML/JS page served by
   Go** (recommended). The browser version is a remote control panel and
   file browser, and that works better as real HTML: native scrolling,
   selectable text, real links and downloads, and the preview overlay
   becomes trivial.
   - Deletes: `web.rs`, the route and history code in `app.rs` (about 200
     lines), the `web` and `desktop` features, winit and femtovg, the pinned
     `wasm-bindgen`, the wasm toolchain check, and the canvas `!important`
     workarounds.
   - Costs: about 300–400 lines of vanilla JS against the same JSON
     protocol, and two front ends. The protocol is already the contract
     between them.
   - Slint stays for the panel only, which is its strength.
2. **Serve card files from a separate origin** (another port). Nothing from
   the card ever shares the control page's origin, so the PDF exemption and
   the per-type sandbox logic go away.
3. **Consider JSON-RPC 2.0 wire names** (`method`, `params`, `result`). It's
   the same shape as today's protocol, and generic tools (websocat, jsonrpc
   CLIs) can then drive the backend.
4. **Replace `build.sh` and `dev.sh` with one `Makefile`** (`ui`, `web`,
   `backend`, `panels`, `test`, `push-ui`, `push-backend`), with no FOGONA
   paths.
5. **Add CI:** `go vet`/staticcheck, `cargo clippy`/test, `go generate`
   diff-check, and an aarch64-musl static build.

## Suggested order

Phase 1 → Phase 3's generator and cutout fix → Phase 2 config → Phase 4 →
Phase 7.1 (if taken, Phase 6 shrinks a lot) → Phase 6 → Phase 3's runtime
scale (it depends on runtime glyphs) → Phase 5.

## Not verified

- That gmobile panel JSON has `width`/`height` in mm.
- The exact Slint 1.18 feature for runtime glyph rendering in the software
  renderer.
