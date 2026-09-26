# TODO

- **Test on a device.** Only tested headless and in a container: the
  handover (kexec), touch and keys through the `evdev` crate, the fbdev
  pixel packing from bitfields, the scale from a real panel, reboot modes
  from a real device tree, a dev re-exec that adopts sshd and the UI, and
  `PUT /dev/kexec`.
- **Use the web build in a real browser.** Only screenshotted in headless
  Chromium (main, boot, services and file screens, over the WebSocket);
  Preview, new tab and Download not clicked.
- **The stage build (fogona repo) needs, for this branch:**
  - `-files "$RDIR/bootmanager-ui:bin/bootmanager-ui" -files
    "$RDIR/web:usr/share/bootmanager-web"` before the package paths: the
    UI and its web build are no longer embedded in the backend.
  - The uinit passing `-listen 172.16.42.1 -logs /tmp/uinit.log -panel
    /etc/bootmanager/panel.json` (with `motorola-fogona.json` added there
    by `-files`: gmobile's set does not have it), and `-dev-once
    /tmp/dev-once` in dev builds (`TAGS=dev`).
  - What moved out of here: `android-payload.py` (in this repo's history,
    commit afe4863), and wrappers for `kexec-stage`, `kexec-pmos` and
    `stage-hold` over the generic `dev.sh kexec`.
- **CI** once the repo has a host: `go vet`, `go test`, `go generate`
  diff-check, `cargo clippy`/`test`, and `build.sh` for aarch64-musl.
