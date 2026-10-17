//! The screens: each is a list of rows, built from the screen's data and
//! the backend state, each row with what activating it does.

use crate::app::State;
use crate::proto::{Disk, FsckResult, Listing, Slot, SlotsInfo, Supply, Text, WifiStatus};
use crate::Row;
use slint::{Image, SharedString};
use std::collections::BTreeMap;

/// A request in flight or its outcome.
#[derive(Clone)]
pub enum Load<T> {
    Waiting,
    Failed(String),
    Ready(T),
}

impl<T> From<Result<T, String>> for Load<T> {
    fn from(r: Result<T, String>) -> Self {
        r.map_or_else(Load::Failed, Load::Ready)
    }
}

#[derive(Clone)]
pub enum Screen {
    Main,
    Boot,
    /// Whether to autoboot and for how long (State's `autoboot`).
    Autoboot,
    /// The command-line switches kept for every boot (State's `options`).
    BootOptions,
    /// An entry's kernel command line in a text field, to edit and boot
    /// with for this one boot (GRUB's `e`). The text is the window's field.
    EditCmdline { index: usize, label: String },
    /// The battery and charger (State's `supplies`).
    Battery,
    /// What the micom reports of the vehicle and the link to it (State's `vehicle`).
    Vehicle,
    /// Which bytes of which micom frames change when something on the car
    /// does, to learn what they mean (State's `vehicle`, and a mark).
    Learn,
    Services,
    Disks(Load<Vec<Disk>>),
    Files { dev: String, name: String, path: String, listing: Load<Listing> },
    /// Check a partition's file system, and repair it once the check has
    /// found errors and the repair is confirmed. The run's state is the
    /// State's `fsck`.
    Fsck { dev: String, disk: Load<Disk>, confirm: bool },
    /// The passphrase of a LUKS partition, typed in the field above the
    /// list on the on-screen keyboard; the volume then shows as a partition.
    Unlock { dev: String, name: String },
    /// The Wi-Fi radio: access point or client (State's `wifi`, `wifi_nets`).
    Wifi(Load<WifiStatus>),
    /// The password of a scanned network, in the same field as Unlock's.
    WifiJoin { ssid: String, secured: bool },
    /// One file: what can be done with it.
    File { dev: String, path: String, size: u64 },
    /// A file's start, as text.
    Text { dev: String, path: String, text: Load<Text> },
    /// An image, fitted to the screen, over everything (app.slint's viewer).
    Image { dev: String, path: String, picture: Load<Image> },
    Log(Load<Vec<String>>),
    /// A/B slots and what the bootloader handed over.
    BootInfo(Load<SlotsInfo>),
    /// The menu of what can be inspected: Battery and Boot.
    Diagnostics,
    /// What this is built with, and under which licences: Slint's
    /// Royalty-Free licence asks for the attribution to be shown here.
    About,
}

/// Where an unlock attempt stands, on the Unlock screen.
#[derive(Clone)]
pub enum UnlockRun {
    Idle,
    Working,
    Failed(String),
}

/// A check or repair of the partition on the Fsck screen.
#[derive(Clone)]
pub enum FsckRun {
    Idle,
    /// Running; the checker's latest line.
    Running(String),
    Done(FsckResult),
    Failed(String),
}

#[derive(Clone)]
pub enum Action {
    Open(Screen),
    Boot(usize),
    Service(String, bool),
    Reboot(String),
    PowerOff,
    /// slots.set: a slot and what to make it (active, successful,
    /// unbootable).
    Slot(String, &'static str),
    /// Hand a file to the browser: its path under /files/, and how.
    Url(String, UrlMode),
    /// disk.fsck on a partition: a check, or (true) a repair.
    Fsck(String, bool),
    /// Ask to confirm the repair.
    FsckConfirm,
    /// autoboot.set: on or off, and a countdown in seconds (0: keep it).
    AutobootSet(bool, u32),
    /// boot.option.set: an option, and its new state ("", "on", "off").
    OptionSet(String, String),
    /// boot.start with the command line in the editor's field, for this
    /// boot only.
    BootEdited(usize),
    /// disk.unlock with the typed passphrase; the Unlock screen's dev.
    Unlock,
    /// stage.rekexec: load the USB stick's kernel and initramfs and jump to
    /// them, without going back through the vendor system.
    Rekexec,
    /// vehicle.mark: the baseline the Learn screen's changes count from.
    VehicleMark,
    /// wifi.scan: lists networks in range, and stops the access point.
    WifiScan,
    /// wifi.ap: back to the access point.
    WifiAp,
    /// wifi.join with the typed password (none for an open network).
    WifiJoin,
    /// wifi.join of a saved network, with the key kept on the stick.
    WifiJoinSaved(String),
    /// wifi.forget: every saved network.
    WifiForget,
    /// disk.lock on an unlocked volume (dm-N).
    Lock(String),
    Back,
    Refresh,
    None,
}

/// What the browser does with a file.
#[derive(Clone, Copy, Debug, PartialEq)]
pub enum UrlMode {
    /// In the page, in the browser's own viewer.
    Preview,
    Tab,
    Download,
}

pub fn row(title: impl Into<SharedString>, detail: impl Into<SharedString>, kind: &str, on: bool) -> Row {
    Row { title: title.into(), detail: detail.into(), kind: kind.into(), on, thumb: Image::default() }
}

/// "on", "off", or "failed" for a service switched on whose process died.
fn service_state(v: &crate::proto::Service) -> &'static str {
    match (v.running, v.error.is_empty()) {
        (true, _) => "on",
        (false, true) => "off",
        (false, false) => "failed",
    }
}

fn info(title: impl Into<SharedString>, detail: impl Into<SharedString>) -> (Row, Action) {
    (row(title, detail, "info", false), Action::None)
}

pub fn join(dir: &str, name: &str) -> String {
    format!("{}/{}", dir.trim_end_matches('/'), name)
}

pub fn human_size(n: u64) -> String {
    const UNITS: [&str; 5] = ["B", "KiB", "MiB", "GiB", "TiB"];
    let (mut v, mut u) = (n as f64, 0);
    while v >= 1024.0 && u < UNITS.len() - 1 {
        v /= 1024.0;
        u += 1;
    }
    if u == 0 {
        format!("{n} B")
    } else {
        format!("{v:.1} {}", UNITS[u])
    }
}

/// The formats the backend makes thumbnails of (Go's standard library).
pub fn is_image(name: &str) -> bool {
    let n = name.to_ascii_lowercase();
    [".jpg", ".jpeg", ".png", ".gif"].iter().any(|e| n.ends_with(e))
}

/// Rows for a loading, failed or empty screen; None once there is data.
fn pending<T>(l: &Load<T>, waiting: &str) -> Option<(Row, Action)> {
    match l {
        Load::Waiting => Some(info(waiting, "")),
        Load::Failed(e) => Some((row("Failed", e.clone(), "info", false), Action::Refresh)),
        Load::Ready(_) => None,
    }
}

fn status_title(r: &FsckResult) -> String {
    let what = if r.repair { "Repair" } else { "Check" };
    match r.status.as_str() {
        "clean" => format!("{what}: no errors"),
        "fixed" => format!("{what}: errors fixed"),
        "errors" if r.repair => format!("{what}: errors remain"),
        "errors" => format!("{what}: errors found"),
        "dirty-log" => format!("{what}: the log needs replaying"),
        _ => format!("{what}: failed"),
    }
}

/// The countdown lengths offered, in seconds.
const COUNTDOWNS: [u32; 6] = [3, 5, 10, 15, 30, 60];

/// The heading, subheading and rows of the screen on show. Every screen
/// starts with a Back row (the menu's first, back to the carousel): with
/// three keys, a row is easier to find than a long press.
pub fn build(s: &State) -> (String, String, Vec<(Row, Action)>) {
    let (heading, sub, mut rows) = screen(s);
    rows.insert(0, (row("Back", "", "back", false), Action::Back));
    (heading, sub, rows)
}

fn screen(s: &State) -> (String, String, Vec<(Row, Action)>) {
    let mut rows = vec![];
    match &s.screen {
        Screen::Main => {
            let boot = match &s.entries {
                None => "Looking for boot entries…".to_string(),
                Some(e) if e.is_empty() => "No boot entries found".to_string(),
                Some(e) => format!("Default: {}", e.get(s.default).map_or("", |e| e.label.as_str())),
            };
            let svc: Vec<String> =
                s.services.iter().map(|v| format!("{} {}", v.title, service_state(v))).collect();
            let auto = match &s.autoboot {
                None => String::new(),
                Some(a) if a.enabled => format!("On  ·  {} s", a.seconds),
                Some(_) => "Off".to_string(),
            };
            rows.push((row("Boot", boot, "nav", false), Action::Open(Screen::Boot)));
            rows.push((row("Autoboot", auto, "nav", false), Action::Open(Screen::Autoboot)));
            let opts = match &s.options {
                None => String::new(),
                Some(o) => {
                    let set: Vec<String> = o.iter().filter(|o| !o.state.is_empty()).map(|o| format!("{} {}", o.title, o.state)).collect();
                    if set.is_empty() { "As each entry has them".to_string() } else { set.join("  ·  ") }
                }
            };
            rows.push((row("Boot options", opts, "nav", false), Action::Open(Screen::BootOptions)));
            rows.push((row("Services", svc.join("  ·  "), "nav", false), Action::Open(Screen::Services)));
            if let Some(r) = s.services.iter().find(|v| v.name == "remote" && v.running) {
                rows.push((row("Remote UI is on", r.detail.clone(), "info", false), Action::Open(Screen::Services)));
            }
            rows.push((row("Partitions", "Browse file systems, read-only", "nav", false), Action::Open(Screen::Disks(Load::Waiting))));
            if s.wifi_available {
                rows.push((row("Wi-Fi", "Access point or join a network", "nav", false), Action::Open(Screen::Wifi(Load::Waiting))));
            }
            rows.push((row("Log", "Stage and backend output", "nav", false), Action::Open(Screen::Log(Load::Waiting))));
            rows.push((row("Diagnostics", "Battery, boot slots and the bootloader", "nav", false), Action::Open(Screen::Diagnostics)));
            rows.push((row("About", "Credits and licences", "nav", false), Action::Open(Screen::About)));
            if s.rekexec_available {
                rows.push((row("Re-kexec from USB", "Load the stick's kernel and stage, then jump to it", "action", false), Action::Rekexec));
            }
            rows.push((row("Reboot", "", "action", false), Action::Reboot(String::new())));
            for m in &s.modes {
                rows.push((row(format!("Reboot to {m}"), "", "action", false), Action::Reboot(m.clone())));
            }
            rows.push((row("Power off", "", "danger", false), Action::PowerOff));
            ("Tools".into(), String::new(), rows)
        }
        Screen::Boot => {
            match &s.entries {
                None => rows.push(info("Scanning…", "")),
                Some(e) if e.is_empty() => rows.push(info("No boot entries", "Nothing on any disk has an extlinux.conf")),
                Some(e) => {
                    for (i, e) in e.iter().enumerate() {
                        let title = if i == s.default { format!("{}  (default)", e.label) } else { e.label.clone() };
                        rows.push((row(title, e.detail.clone(), "action", false), Action::Boot(i)));
                    }
                    // Linux entries (they carry a command line) can be edited
                    // for one boot.
                    for (i, e) in e.iter().enumerate().filter(|(_, e)| !e.detail.is_empty()) {
                        let open = Screen::EditCmdline { index: i, label: e.label.clone() };
                        rows.push((row("Edit command line", e.label.clone(), "nav", false), Action::Open(open)));
                    }
                }
            }
            ("Boot".into(), "Load the entry and kexec into it".into(), rows)
        }
        Screen::EditCmdline { index, label } => {
            rows.push((row("Boot with this command line", "For this boot only; nothing is saved", "action", false), Action::BootEdited(*index)));
            rows.push(info("Back leaves it as it was", ""));
            ("Command line".into(), label.clone(), rows)
        }
        Screen::Autoboot => {
            match &s.autoboot {
                None => rows.push(info("Reading…", "")),
                Some(a) => {
                    rows.push((
                        row("Autoboot", "Start the default entry when the countdown ends", "toggle", a.enabled),
                        Action::AutobootSet(!a.enabled, 0),
                    ));
                    rows.push(info("Countdown", "How long to wait before it starts"));
                    for n in COUNTDOWNS {
                        let current = a.seconds == n;
                        let title = if current { format!("{n} seconds  ✓") } else { format!("{n} seconds") };
                        rows.push((row(title, "", "action", false), Action::AutobootSet(a.enabled, n)));
                    }
                    if !COUNTDOWNS.contains(&a.seconds) {
                        rows.push(info(format!("{} seconds", a.seconds), "Set elsewhere; choose one above to change it"));
                    }
                    rows.push(info("Applies from the next start", ""));
                    if !a.saved {
                        rows.push(info("Not saved", "This stage has no settings location, so it lasts until restart"));
                    }
                }
            }
            ("Autoboot".into(), "Starting the default entry on its own".into(), rows)
        }
        Screen::BootOptions => {
            match &s.options {
                None => rows.push(info("Reading…", "")),
                Some(opts) => {
                    for o in opts {
                        // Default, then on, then off, round again.
                        let (label, next) = match o.state.as_str() {
                            "on" => ("On", "off"),
                            "off" => ("Off", ""),
                            _ => ("Entry's own", "on"),
                        };
                        rows.push((
                            row(o.title.clone(), format!("{label}  ·  {}", o.detail), "nav", false),
                            Action::OptionSet(o.id.clone(), next.to_string()),
                        ));
                    }
                    rows.push(info("Applies to every Linux entry, from the next boot", "Kept until changed"));
                }
            }
            ("Boot options".into(), "Select to change: entry's own, on, off".into(), rows)
        }
        Screen::Services => {
            for v in &s.services {
                let detail = if v.error.is_empty() { v.detail.clone() } else { format!("Stopped: {}", v.error) };
                rows.push((row(v.title.clone(), detail, "toggle", v.running), Action::Service(v.name.clone(), !v.running)));
            }
            ("Services".into(), "Select to switch".into(), rows)
        }
        Screen::Disks(disks) => {
            rows.extend(pending(disks, "Probing…"));
            if let Load::Ready(d) = disks {
                if d.is_empty() {
                    rows.push((row("No file systems found", "", "info", false), Action::Refresh));
                }
                for d in d {
                    let title = if d.name.is_empty() { d.dev.clone() } else { format!("{}  ·  {}", d.dev, d.name) };
                    if d.fstype == "crypto_LUKS" {
                        if s.luks_available {
                            let open = Screen::Unlock { dev: d.dev.clone(), name: d.name.clone() };
                            rows.push((row(title, format!("Encrypted (LUKS)  ·  {}  ·  tap to unlock", human_size(d.size)), "nav", false), Action::Open(open)));
                        } else {
                            rows.push(info(title, format!("Encrypted (LUKS)  ·  {}  ·  not supported in this build", human_size(d.size))));
                        }
                        continue;
                    }
                    let open = Screen::Files { dev: d.dev.clone(), name: d.name.clone(), path: "/".into(), listing: Load::Waiting };
                    rows.push((row(title, format!("{}  ·  {}", d.fstype, human_size(d.size)), "nav", false), Action::Open(open)));
                }
            }
            ("Partitions".into(), "Mounted read-only".into(), rows)
        }
        Screen::Files { dev, name, path, listing } => {
            if path == "/" && dev.starts_with("dm-") && s.luks_available {
                rows.push((row("Lock", "Close the encrypted volume", "action", false), Action::Lock(dev.clone())));
            }
            if path == "/" && s.fsck_available {
                let open = Screen::Fsck { dev: dev.clone(), disk: Load::Waiting, confirm: false };
                rows.push((row("Check file system", "Find errors; repair is offered after", "nav", false), Action::Open(open)));
            }
            rows.extend(pending(listing, "Reading…"));
            if let Load::Ready(l) = listing {
                for f in &l.entries {
                    let p = join(path, &f.name);
                    if f.dir {
                        let open = Screen::Files { dev: dev.clone(), name: name.clone(), path: p, listing: Load::Waiting };
                        rows.push((row(format!("{}/", f.name), "", "nav", false), Action::Open(open)));
                    } else if !f.link.is_empty() {
                        rows.push(info(f.name.clone(), format!("-> {}", f.link)));
                    } else {
                        let mut r = row(f.name.clone(), human_size(f.size.max(0) as u64), "nav", false);
                        if let Some(Some(img)) = s.thumbs.get(&(dev.clone(), p.clone())) {
                            r.thumb = img.clone();
                        }
                        let open = Screen::File { dev: dev.clone(), path: p, size: f.size.max(0) as u64 };
                        rows.push((r, Action::Open(open)));
                    }
                }
                if l.truncated {
                    rows.push(info("(listing truncated)", ""));
                }
            }
            let heading = if name.is_empty() { dev.clone() } else { name.clone() };
            (heading, format!("{dev}:{path}"), rows)
        }
        Screen::Unlock { dev, name } => {
            // The passphrase field is above the list (app.slint).
            match &s.unlock {
                UnlockRun::Working => rows.push(info("Unlocking…", "Key derivation can take a few seconds")),
                UnlockRun::Failed(e) => rows.push(info("Not unlocked", e.clone())),
                UnlockRun::Idle => {}
            }
            let sub = if name.is_empty() { dev.clone() } else { format!("{dev}  ·  {name}") };
            ("Unlock".into(), sub, rows)
        }
        Screen::Wifi(st) => {
            rows.extend(pending(st, "Reading…"));
            if let Load::Ready(w) = st {
                let (title, detail) = match w.mode.as_str() {
                    "ap" => (format!("Access point {}", w.ssid), format!("{}  ·  ssh to this address", w.ip)),
                    "client" if w.state == "COMPLETED" => (format!("Connected to {}", w.ssid), w.ip.clone()),
                    "client" => (format!("Joining {}", w.ssid), w.state.clone()),
                    "none" => ("No radio".to_string(), w.state.clone()),
                    _ => ("Radio off".to_string(), String::new()),
                };
                rows.push(info(title, detail));
                match &s.wifi_run {
                    UnlockRun::Working => rows.push(info("Working…", "Scanning takes a few seconds")),
                    UnlockRun::Failed(e) => rows.push(info("Failed", e.clone())),
                    UnlockRun::Idle => {}
                }
                if w.mode == "none" {
                    return ("Wi-Fi".into(), "Waiting for the radio".into(), rows);
                }
                if w.mode != "ap" {
                    rows.push((row("Start access point", "Others join this device", "action", false), Action::WifiAp));
                }
                rows.push((
                    row("Scan for networks", if w.mode == "ap" { "Stops the access point (and ssh over it)" } else { "" }, "action", false),
                    Action::WifiScan,
                ));
                for name in &w.saved {
                    rows.push((row(name.clone(), "Saved  ·  tap to connect", "nav", false), Action::WifiJoinSaved(name.clone())));
                }
                if !w.saved.is_empty() {
                    rows.push((row("Forget saved networks", "Their keys are removed from the stick", "danger", false), Action::WifiForget));
                }
                for n in &s.wifi_nets {
                    let detail = format!("{} dBm  ·  {}", n.signal, if n.secured { "secured" } else { "open" });
                    let open = Screen::WifiJoin { ssid: n.ssid.clone(), secured: n.secured };
                    rows.push((row(n.ssid.clone(), detail, "nav", false), Action::Open(open)));
                }
            }
            ("Wi-Fi".into(), "Select a network to join".into(), rows)
        }
        Screen::WifiJoin { ssid, secured } => {
            match &s.wifi_run {
                UnlockRun::Working => rows.push(info("Connecting…", "Waiting for an address")),
                UnlockRun::Failed(e) => rows.push(info("Not connected", e.clone())),
                UnlockRun::Idle => {}
            }
            if !secured {
                rows.push((row("Connect", "Open network, no password", "action", false), Action::WifiJoin));
            }
            (ssid.clone(), if *secured { "Type the password, then Return" } else { "Open network" }.into(), rows)
        }
        Screen::Fsck { dev, disk, confirm } => {
            // A link to /check/<dev> on a stage without the tools.
            if !s.fsck_available {
                rows.push(info("Not in this build", "This stage has no file system checkers"));
                return ("Check".into(), String::new(), rows);
            }
            rows.extend(pending(disk, "Probing…"));
            let mut sub = String::new();
            if let Load::Ready(d) = disk {
                sub = format!("{dev}  ·  {}  ·  {}", d.fstype, human_size(d.size));
                match &s.fsck {
                    FsckRun::Running(line) => rows.push(info("Running…", line.clone())),
                    run => {
                        let (title, detail) = match run {
                            FsckRun::Done(r) => (status_title(r), format!("exit {}", r.code)),
                            FsckRun::Failed(e) => ("Could not run".to_string(), e.clone()),
                            _ => ("Not checked yet".to_string(), "A check changes nothing on the partition".to_string()),
                        };
                        rows.push(info(title, detail));
                        rows.push((row("Check", "Read-only", "action", false), Action::Fsck(dev.clone(), false)));
                        // Repair follows a check that found errors, and only on removable media.
                        let errors = matches!(run, FsckRun::Done(r) if r.status == "errors" && !r.repair);
                        if errors && d.removable && d.fstype != "xfs" {
                            if *confirm {
                                rows.push(info(format!("Repair writes to {dev}"), "Copy off anything that matters first"));
                                rows.push((row(format!("Repair {dev} now"), "Writes to the card", "action", false), Action::Fsck(dev.clone(), true)));
                            } else {
                                rows.push((row("Repair…", "Asks to confirm", "action", false), Action::FsckConfirm));
                            }
                        } else if errors && d.fstype == "xfs" {
                            rows.push(info("XFS is not repaired here", "Mount it so the log replays, then check again"));
                        } else if errors {
                            rows.push(info("Repair is for SD cards", "This partition is internal"));
                        }
                        if let FsckRun::Done(r) = run {
                            rows.extend(r.lines.iter().rev().take(60).map(|l| info(l.clone(), "")));
                        }
                    }
                }
            }
            ("Check".into(), sub, rows)
        }
        Screen::File { dev, path, size } => {
            let text = Screen::Text { dev: dev.clone(), path: path.clone(), text: Load::Waiting };
            if s.browser {
                let url = format!("/files/{dev}{}", crate::routes::encode(path));
                let action = |mode| Action::Url(url.clone(), mode);
                rows.push((row("Preview", "In the browser's own viewer: images, video, audio, text, PDF", "action", false), action(UrlMode::Preview)));
                rows.push((row("Open in a new tab", "", "action", false), action(UrlMode::Tab)));
                // A file screen opened from a link has no size (it comes from the listing).
                let size = if *size > 0 { human_size(*size) } else { String::new() };
                rows.push((row("Download", size, "action", false), action(UrlMode::Download)));
                rows.push((row("View as text", "The start of it, in this UI", "nav", false), Action::Open(text)));
            } else {
                if is_image(path) {
                    let view = Screen::Image { dev: dev.clone(), path: path.clone(), picture: Load::Waiting };
                    rows.push((row("View image", "On this screen", "nav", false), Action::Open(view)));
                }
                rows.push((row("View", "The start of it, as text", "nav", false), Action::Open(text)));
                rows.push(info(human_size(*size), "Preview or download it from the browser (Remote UI)"));
            }
            (base_name(path), format!("{dev}:{path}"), rows)
        }
        Screen::Text { dev, path, text } => {
            rows.extend(pending(text, "Reading…"));
            match text {
                Load::Ready(t) if t.binary => rows.push(info(format!("Binary, {}", human_size(t.size)), "Not shown as text")),
                Load::Ready(t) => {
                    // Tabs as spaces: the list draws one line per row, no tab stops.
                    rows.extend(t.text.lines().take(5000).map(|l| info(l.replace('\t', "    "), "")));
                    if t.truncated {
                        rows.push(info(format!("(first {} of {})", human_size(t.text.len() as u64), human_size(t.size)), ""));
                    }
                }
                _ => {}
            }
            (base_name(path), format!("{dev}:{path}"), rows)
        }
        // The viewer covers the list.
        Screen::Image { dev, path, .. } => (base_name(path), format!("{dev}:{path}"), rows),
        Screen::Log(log) => {
            rows.extend(pending(log, "Reading…"));
            if let Load::Ready(lines) = log {
                rows.push((row("Refresh", "", "action", false), Action::Refresh));
                rows.extend(lines.iter().rev().map(|l| info(l.clone(), "")));
            }
            ("Log".into(), "Newest first".into(), rows)
        }
        // The battery and what charges it, as the kernel reports them,
        // read again while the screen is up (App::refresh_power).
        Screen::Battery => {
            rows.extend(power_rows(&s.supplies));
            let sub = match &s.battery {
                Some(b) if b.present && b.percent >= 0 => format!("{} %  ·  {}", b.percent, if b.charging { "Charging" } else { "Not charging" }),
                Some(b) if b.present => "Level unknown".to_string(),
                _ => "No battery found".to_string(),
            };
            ("Battery".into(), sub, rows)
        }
        // The micom's frames, decoded, and the alive exchange it resets the
        // SoC without; read again while the screen is up (App::refresh_power).
        Screen::Learn => {
            rows.extend(learn_rows(s.vehicle.as_ref()));
            ("Learn".into(), "Which bytes change when something on the car does".into(), rows)
        }
        Screen::Vehicle => {
            rows.push((row("Learn", "Which bytes change when something on the car does", "nav", false), Action::Open(Screen::Learn)));
            rows.extend(vehicle_rows(s.vehicle.as_ref()));
            let sub = match &s.vehicle {
                Some(v) if v.present && v.up => "Micom link up".to_string(),
                Some(v) if v.present => "Micom link silent".to_string(),
                _ => "No micom link on this stage".to_string(),
            };
            ("Vehicle".into(), sub, rows)
        }
        Screen::Diagnostics => {
            let power = match &s.battery {
                Some(b) if b.present && b.percent >= 0 => format!("{} %  ·  {}", b.percent, if b.charging { "Charging" } else { "Not charging" }),
                Some(b) if b.present => "Level unknown".to_string(),
                _ => "No battery found".to_string(),
            };
            rows.push((row("Battery", power, "nav", false), Action::Open(Screen::Battery)));
            let vehicle = match &s.vehicle {
                Some(v) if v.present && v.up => "Micom link up".to_string(),
                Some(v) if v.present => "Micom link silent".to_string(),
                _ => "The micom's view of the vehicle".to_string(),
            };
            rows.push((row("Vehicle", vehicle, "nav", false), Action::Open(Screen::Vehicle)));
            rows.push((row("Boot", "A/B slots and the bootloader's state", "nav", false), Action::Open(Screen::BootInfo(Load::Waiting))));
            ("Diagnostics".into(), "What the device reports".into(), rows)
        }
        Screen::About => {
            rows.push(info("Boot Manager", format!("Version {}", env!("CARGO_PKG_VERSION"))));
            // Slint's Royalty-Free licence, for desktop, mobile and web: the
            // attribution it asks for.
            rows.push(info("User interface: Slint", "Built with Slint (slint.dev), used under the Slint Royalty-Free License"));
            rows.push(info("Boot stage", "Linux (GPL-2.0), u-root (BSD-3-Clause), Go (BSD-3-Clause)"));
            rows.push(info("Logos", "postmarketOS (CC-BY-SA-4.0), Android robot by Google (CC-BY-3.0), Debian Open Use Logo (LGPL-3.0-or-later, CC-BY-SA-3.0)"));
            rows.push(info("Typeface", "DejaVu Sans (Bitstream Vera and Arev licences)"));
            ("About".into(), "Credits and licences".into(), rows)
        }
        Screen::BootInfo(d) => {
            rows.extend(pending(d, "Reading…"));
            if let Load::Ready(d) = d {
                if !d.slots_error.is_empty() {
                    rows.push(info("No boot slots", d.slots_error.clone()));
                }
                for sl in &d.slots {
                    slot_rows(&mut rows, sl, &d.slots);
                }
                if !d.bootloader.is_empty() {
                    rows.push(info("Bootloader", ""));
                    rows.extend(d.bootloader.iter().map(|r| info(r.title.clone(), r.detail.clone())));
                }
            }
            ("Boot".into(), "What the bootloader boots next".into(), rows)
        }
    }
}

/// A property in the kernel's units, scaled: µV to V, µA to mA and the like.
fn scaled(props: &BTreeMap<String, String>, key: &str, div: f64, decimals: usize, unit: &str) -> Option<String> {
    let v: f64 = props.get(key)?.parse().ok()?;
    Some(format!("{:.*} {unit}", decimals, v / div))
}

/// The properties the rows below spell out; any others follow as raw lines.
const KNOWN: [&str; 31] = [
    "name", "type", "status", "capacity", "present", "voltage_now", "current_now", "temp", "health", "technology",
    "charge_now", "charge_full", "charge_full_design", "cycle_count", "online", "usb_type", "current_max", "voltage_max",
    "scope", "capacity_level", "model_name", "manufacturer", "charge_type", "input_current_limit",
    "constant_charge_current", "constant_charge_voltage", "charge_term_current", "precharge_current",
    "voltage_min_design", "voltage_max_design", "constant_charge_current_max",
];

/// "3 s ago" for a reading's age in milliseconds.
fn age(ms: i64) -> String {
    match ms {
        i64::MIN..=-1 => "never".into(),
        0..=1499 => "just now".into(),
        1500..=89_999 => format!("{} s ago", (ms + 500) / 1000),
        _ => format!("{} min ago", ms / 60_000),
    }
}

/// A byte that changed this many times since the mark is a counter or a
/// timer, not a switch.
const BUSY: u32 = 6;

fn hex_or_unknown(v: i32) -> String {
    if v < 0 {
        "?".into()
    } else {
        format!("{v:02x}")
    }
}

/// The bytes of a frame that changed, for the Learn screen: the ones that
/// change constantly are only listed ("busy"), the ones that moved a few
/// times are shown with what they were and what they are.
fn diff_text(f: &crate::proto::VehicleFrame) -> String {
    let busy: Vec<String> = f.diffs.iter().filter(|d| d.changes >= BUSY).map(|d| format!("b{}", d.idx)).collect();
    let quiet: Vec<String> = f
        .diffs
        .iter()
        .filter(|d| d.changes < BUSY)
        .map(|d| format!("b{} {}→{} ({}×, {})", d.idx, hex_or_unknown(d.from), hex_or_unknown(d.to), d.changes, age(d.age_ms)))
        .collect();
    let mut parts = vec![];
    for q in quiet.iter().take(6) {
        parts.push(q.clone());
    }
    if quiet.len() > 6 {
        parts.push(format!("{} more", quiet.len() - 6));
    }
    if !busy.is_empty() {
        parts.push(format!("busy: {}", busy.join(" ")));
    }
    parts.join("  ·  ")
}

/// The Learn screen: Mark records a baseline, then each kind of frame whose
/// bytes changed since is listed, the quiet changes (a switch, a door) before
/// the counters, newest first, so what the car just did is on top.
pub fn learn_rows(v: Option<&crate::proto::Vehicle>) -> Vec<(Row, Action)> {
    let Some(v) = v.filter(|v| v.present) else {
        return vec![info("Micom", "This stage does not run the micom link")];
    };
    let marked = if v.marked_age_ms >= 0 {
        format!("Marked {}: now change something on the car", age(v.marked_age_ms))
    } else {
        "Records a baseline; what changes after it is listed here".to_string()
    };
    let mut rows = vec![(row("Mark now", marked, "action", false), Action::VehicleMark)];
    let mut changed: Vec<&crate::proto::VehicleFrame> = v.frames.iter().filter(|f| !f.diffs.is_empty()).collect();
    // Frames with a quiet change first, then by how recent their newest change is.
    changed.sort_by_key(|f| {
        let quiet = f.diffs.iter().any(|d| d.changes < BUSY);
        (!quiet, f.diffs.iter().map(|d| d.age_ms).min().unwrap_or(i64::MAX))
    });
    for f in &changed {
        let title = if f.name.is_empty() { f.key.clone() } else { format!("{}  ({})", f.name, f.key) };
        rows.push(info(title, diff_text(f)));
    }
    rows.push(info("Unchanged", format!("{} kinds of frame", v.frames.len() - changed.len())));
    rows
}

/// The micom link for Diagnostics: its health and the alive exchange first,
/// then each decoded value, then every kind of frame it sent (those with no
/// known layout by their header and command).
pub fn vehicle_rows(v: Option<&crate::proto::Vehicle>) -> Vec<(Row, Action)> {
    let Some(v) = v.filter(|v| v.present) else {
        return vec![info("Micom", "This stage does not run the micom link")];
    };
    let mut rows = vec![];
    let link = if v.up { "Up" } else { "No frames lately" };
    rows.push(info("Link", format!("{link}  ·  {} received, {} sent", v.rx_frames, v.tx_frames)));
    if v.bad_sum > 0 || v.dropped > 0 {
        rows.push(info("   Errors", format!("{} bad checksums, {} bytes dropped", v.bad_sum, v.dropped)));
    }
    let asked = if v.last_alive_age_ms >= 0 { format!("last asked {}", age(v.last_alive_age_ms)) } else { "not asked yet".into() };
    rows.push(info("CPU alive", format!("{} of {} answered  ·  {asked}", v.alive_tx, v.alive_rx)));
    for r in &v.readings {
        let value = if r.valid { format!("{}{}", r.value, r.unit) } else { "n/a".to_string() };
        rows.push(info(r.name.clone(), format!("{value}  ·  {}", age(r.age_ms))));
    }
    if !v.frames.is_empty() {
        rows.push(info("Frames", format!("{} kinds seen", v.frames.len())));
        for f in &v.frames {
            let name = if f.name.is_empty() { f.key.clone() } else { format!("{}  ({})", f.name, f.key) };
            rows.push(info(format!("   {name}"), format!("{}  ·  {}", f.count, age(f.age_ms))));
        }
    }
    rows
}

/// What the kernel says of each power supply, for Diagnostics: the battery
/// first (its level, voltage, current, temperature, health, charge),
/// then what charges it.
pub fn power_rows(supplies: &[Supply]) -> Vec<(Row, Action)> {
    let mut rows = vec![];
    if supplies.is_empty() {
        rows.push(info("Power", "The kernel lists no power supplies"));
        return rows;
    }
    for sp in supplies {
        let p = &sp.props;
        let mut head = vec![];
        if sp.kind == "Battery" {
            match p.get("capacity").and_then(|c| c.parse::<i32>().ok()) {
                Some(c) => head.push(format!("{c} %")),
                None => head.push("level unknown".into()),
            }
            if let Some(st) = p.get("status") {
                head.push(st.clone());
            }
        } else {
            if !sp.kind.is_empty() {
                head.push(sp.kind.clone());
            }
            if let Some(o) = p.get("online") {
                head.push(if o == "0" { "offline".into() } else { "online".into() });
            }
        }
        let title = if sp.kind == "Battery" { format!("Battery  ·  {}", sp.name) } else { sp.name.clone() };
        rows.push(info(title, head.join("  ·  ")));
        let mut line = |k: &str, v: Option<String>| {
            if let Some(v) = v {
                rows.push(info(format!("   {k}"), v));
            }
        };
        line("Voltage", scaled(p, "voltage_now", 1e6, 3, "V"));
        line("Current", scaled(p, "current_now", 1e3, 0, "mA"));
        line("Temperature", scaled(p, "temp", 10.0, 1, "°C"));
        line("Health", p.get("health").cloned());
        line("Technology", p.get("technology").cloned());
        let charge = match (scaled(p, "charge_now", 1e3, 0, "mAh"), scaled(p, "charge_full", 1e3, 0, "mAh")) {
            (Some(n), Some(f)) => Some(format!("{n} of {f}")),
            (Some(n), None) => Some(n),
            _ => None,
        };
        line("Charge", charge);
        line("Full when new", scaled(p, "charge_full_design", 1e3, 0, "mAh"));
        line("Charge cycles", p.get("cycle_count").cloned());
        line("Type", p.get("usb_type").cloned());
        line("Level", p.get("capacity_level").cloned());
        let model = match (p.get("manufacturer"), p.get("model_name")) {
            (Some(m), Some(n)) => Some(format!("{m} {n}")),
            (m, n) => m.or(n).cloned(),
        };
        line("Part", model);
        let design = match (scaled(p, "voltage_min_design", 1e6, 1, "V"), scaled(p, "voltage_max_design", 1e6, 1, "V")) {
            (Some(lo), Some(hi)) => Some(format!("{} to {hi}", lo.trim_end_matches(" V"))),
            _ => None,
        };
        line("Design voltage", design);
        // A charger's: what it takes from the port, and how it charges.
        line("Charge type", p.get("charge_type").cloned());
        line("Input limit", scaled(p, "input_current_limit", 1e3, 0, "mA").or_else(|| scaled(p, "current_max", 1e3, 0, "mA")));
        line("Charge current", scaled(p, "constant_charge_current", 1e3, 0, "mA"));
        line("Charge voltage", scaled(p, "constant_charge_voltage", 1e6, 2, "V"));
        line("Ends at", scaled(p, "charge_term_current", 1e3, 0, "mA"));
        line("Precharge", scaled(p, "precharge_current", 1e3, 0, "mA"));
        line("Voltage limit", scaled(p, "voltage_max", 1e6, 3, "V"));
        for (k, v) in p.iter().filter(|(k, _)| !KNOWN.contains(&k.as_str())) {
            line(k, Some(v.clone()));
        }
    }
    rows
}

/// A slot's state, then what can be done to it: the bootloader boots the
/// active slot with the highest priority, and counts its tries down
/// until one boot is marked successful.
fn slot_rows(rows: &mut Vec<(Row, Action)>, sl: &Slot, all: &[Slot]) {
    let mut state = vec![];
    if sl.active {
        state.push("active".to_string());
    }
    state.push(format!("priority {}", sl.priority));
    if sl.successful {
        state.push("booted successfully".into());
    } else {
        state.push(format!("{} {} left", sl.retries, if sl.retries == 1 { "try" } else { "tries" }));
    }
    if sl.unbootable {
        state.push("unbootable".into());
    }
    let title = format!("Slot {}{}", sl.name.to_uppercase(), if sl.booted { "  (booted)" } else { "" });
    rows.push(info(title, state.join("  ·  ")));
    let name = sl.name.to_uppercase();
    // ABL reads unbootable only on a slot not marked successful, and
    // marks one unbootable once its tries are gone.
    let bootable = |o: &&Slot| o.successful || !o.unbootable && o.retries > 0;
    let next = all.iter().filter(bootable).max_by_key(|o| (o.active, o.priority)).map(|o| o.name == sl.name);
    if next != Some(true) {
        rows.push((row(format!("Boot slot {name} next"), "Active, top priority, every try back", "action", false), Action::Slot(sl.name.clone(), "active")));
    }
    if !sl.successful {
        rows.push((row(format!("Mark slot {name} successful"), "Stop counting its tries down", "action", false), Action::Slot(sl.name.clone(), "successful")));
    }
    if !sl.unbootable {
        let detail = if all.iter().filter(bootable).any(|o| o.name != sl.name && o.priority > 0) {
            "The bootloader boots the other slot"
        } else {
            "No other bootable slot: ABL stops in fastboot"
        };
        rows.push((row(format!("Mark slot {name} failed"), detail, "danger", false), Action::Slot(sl.name.clone(), "unbootable")));
    }
}

fn base_name(p: &str) -> String {
    p.rsplit('/').next().unwrap_or(p).to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn slot(name: &str, active: bool, priority: u32, successful: bool) -> Slot {
        Slot { name: name.into(), booted: name == "a", active, priority, retries: 3, successful, unbootable: false }
    }

    #[test]
    fn vehicle_rows_read_the_link() {
        use crate::proto::{Vehicle, VehicleFrame, VehicleReading};
        let text = |rows: Vec<(Row, Action)>| rows.into_iter().map(|(r, _)| (r.title.to_string(), r.detail.to_string())).collect::<Vec<_>>();
        assert_eq!(text(vehicle_rows(None))[0].0, "Micom");
        let v = Vehicle {
            present: true,
            up: true,
            rx_frames: 100,
            tx_frames: 20,
            alive_rx: 4,
            alive_tx: 4,
            last_alive_age_ms: 3000,
            readings: vec![
                VehicleReading { name: "Climate power".into(), value: 255, valid: true, unit: " W".into(), age_ms: 1000 },
                VehicleReading { name: "AVN motor power".into(), value: 0, valid: false, unit: String::new(), age_ms: 1000 },
            ],
            frames: vec![VehicleFrame { key: "0d8f018d 2d".into(), name: String::new(), count: 9, age_ms: 200, payload: String::new(), diffs: vec![] }],
            ..Default::default()
        };
        let rows = text(vehicle_rows(Some(&v)));
        assert_eq!(rows[0], ("Link".into(), "Up  ·  100 received, 20 sent".into()));
        assert_eq!(rows[1], ("CPU alive".into(), "4 of 4 answered  ·  last asked 3 s ago".into()));
        assert_eq!(rows[2], ("Climate power".into(), "255 W  ·  just now".into()));
        assert_eq!(rows[3].1, "n/a  ·  just now");
        assert_eq!(rows.last().unwrap(), &("   0d8f018d 2d".into(), "9  ·  just now".into()));
    }

    #[test]
    fn learn_rows_put_quiet_changes_first() {
        use crate::proto::{Vehicle, VehicleDiff, VehicleFrame};
        let diff = |idx, from, to, changes, age_ms| VehicleDiff { idx, from, to, changes, age_ms };
        let frame = |key: &str, name: &str, diffs| VehicleFrame { key: key.into(), name: name.into(), count: 10, age_ms: 0, payload: String::new(), diffs };
        let v = Vehicle {
            present: true,
            up: true,
            marked_age_ms: 12_000,
            frames: vec![
                // A counter that moves all the time, and the newest change.
                frame("0d8f018d 2d", "", vec![diff(0, 1, 90, 40, 100), diff(1, 0, 3, 40, 100)]),
                frame("0c88018c 4f", "Charger state", vec![diff(3, 0x00, 0x40, 1, 4000)]),
                frame("0c88018c 55", "Departure timers", vec![]),
            ],
            ..Default::default()
        };
        let rows: Vec<(String, String)> = learn_rows(Some(&v)).into_iter().map(|(r, _)| (r.title.to_string(), r.detail.to_string())).collect();
        assert_eq!(rows[0], ("Mark now".into(), "Marked 12 s ago: now change something on the car".into()));
        // The switch-like change is above the counter, though it is older.
        assert_eq!(rows[1].0, "Charger state  (0c88018c 4f)");
        assert_eq!(rows[1].1, "b3 00→40 (1×, 4 s ago)");
        assert_eq!(rows[2], ("0d8f018d 2d".into(), "busy: b0 b1".into()));
        assert_eq!(rows[3], ("Unchanged".into(), "1 kinds of frame".into()));
        assert_eq!(learn_rows(None)[0].0.title.as_str(), "Micom");
    }

    #[test]
    fn power_rows_in_units() {
        let props = |kv: &[(&str, &str)]| kv.iter().map(|(k, v)| (k.to_string(), v.to_string())).collect();
        let supplies = [
            Supply {
                name: "cw221x".into(),
                kind: "Battery".into(),
                props: props(&[
                    ("capacity", "78"),
                    ("status", "Charging"),
                    ("voltage_now", "3987000"),
                    ("current_now", "-412000"),
                    ("temp", "315"),
                    ("charge_now", "3912000"),
                    ("charge_full", "4890000"),
                    ("cycle_count", "12"),
                    ("manufacturer", "Cellwise"),
                    ("model_name", "CW2217"),
                    ("voltage_min_design", "3400000"),
                    ("voltage_max_design", "4400000"),
                    ("odd", "1"),
                ]),
            },
            Supply { name: "usb".into(), kind: "USB".into(), props: props(&[("online", "1"), ("current_max", "2000000")]) },
        ];
        let rows: Vec<(String, String)> = power_rows(&supplies).into_iter().map(|(r, _)| (r.title.to_string(), r.detail.to_string())).collect();
        let get = |t: &str| rows.iter().find(|(k, _)| k.trim() == t).map(|(_, v)| v.as_str());
        assert_eq!(rows[0], ("Battery  ·  cw221x".into(), "78 %  ·  Charging".into()));
        assert_eq!(get("Voltage"), Some("3.987 V"));
        assert_eq!(get("Current"), Some("-412 mA"));
        assert_eq!(get("Temperature"), Some("31.5 °C"));
        assert_eq!(get("Charge"), Some("3912 mAh of 4890 mAh"));
        assert_eq!(get("Charge cycles"), Some("12"));
        // Properties with no row of their own follow raw; the known ones do not repeat.
        assert_eq!(get("odd"), Some("1"));
        assert_eq!(get("model_name"), None);
        assert_eq!(get("Part"), Some("Cellwise CW2217"));
        assert_eq!(get("Design voltage"), Some("3.4 to 4.4 V"));
        assert_eq!(get("usb"), Some("USB  ·  online"));
        assert_eq!(get("Input limit"), Some("2000 mA"));
        assert_eq!(power_rows(&[])[0].0.detail.as_str(), "The kernel lists no power supplies");
    }

    #[test]
    fn slot_actions() {
        // As a stage in slot A finds a phone whose B was never set up.
        let all = [slot("a", true, 3, true), slot("b", false, 0, false)];
        let mut rows = vec![];
        for sl in &all {
            slot_rows(&mut rows, sl, &all);
        }
        let titles: Vec<String> = rows.iter().map(|(r, _)| format!("{} | {}", r.title, r.detail)).collect();
        assert_eq!(
            titles,
            [
                "Slot A  (booted) | active  ·  priority 3  ·  booted successfully",
                "Mark slot A failed | No other bootable slot: ABL stops in fastboot",
                "Slot B | priority 0  ·  3 tries left",
                "Boot slot B next | Active, top priority, every try back",
                "Mark slot B successful | Stop counting its tries down",
                "Mark slot B failed | The bootloader boots the other slot",
            ]
        );
        assert!(matches!(&rows[3].1, Action::Slot(s, "active") if s == "b"));

        // B at priority 2 but out of tries is no fallback.
        let all = [slot("a", true, 3, true), Slot { retries: 0, ..slot("b", false, 2, false) }];
        let mut rows = vec![];
        slot_rows(&mut rows, &all[0], &all);
        assert_eq!(rows[1].0.detail, "No other bootable slot: ABL stops in fastboot");
    }
}
