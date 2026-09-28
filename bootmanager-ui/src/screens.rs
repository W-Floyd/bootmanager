//! The screens: each is a list of rows, built from the screen's data and
//! the backend state, each row with what activating it does.

use crate::app::State;
use crate::proto::{Disk, Listing, Slot, SlotsInfo, Text};
use crate::Row;
use slint::{Image, SharedString};

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
    Services,
    Disks(Load<Vec<Disk>>),
    Files { dev: String, name: String, path: String, listing: Load<Listing> },
    /// One file: what can be done with it.
    File { dev: String, path: String, size: u64 },
    /// A file's start, as text.
    Text { dev: String, path: String, text: Load<Text> },
    /// An image, fitted to the screen, over everything (app.slint's viewer).
    Image { dev: String, path: String, picture: Load<Image> },
    Log(Load<Vec<String>>),
    /// A/B slots and what the bootloader handed over.
    Diagnostics(Load<SlotsInfo>),
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
                Some(e) => format!("Default: {}", e[0].label),
            };
            let svc: Vec<String> =
                s.services.iter().map(|v| format!("{} {}", v.title, if v.running { "on" } else { "off" })).collect();
            rows.push((row("Boot", boot, "nav", false), Action::Open(Screen::Boot)));
            rows.push((row("Services", svc.join("  ·  "), "nav", false), Action::Open(Screen::Services)));
            if let Some(r) = s.services.iter().find(|v| v.name == "remote" && v.running) {
                rows.push((row("Remote UI is on", r.detail.clone(), "info", false), Action::Open(Screen::Services)));
            }
            rows.push((row("Partitions", "Browse file systems, read-only", "nav", false), Action::Open(Screen::Disks(Load::Waiting))));
            rows.push((row("Log", "Stage and backend output", "nav", false), Action::Open(Screen::Log(Load::Waiting))));
            rows.push((row("Diagnostics", "Boot slots and the bootloader", "nav", false), Action::Open(Screen::Diagnostics(Load::Waiting))));
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
                        let title = if i == 0 { format!("{}  (default)", e.label) } else { e.label.clone() };
                        rows.push((row(title, e.detail.clone(), "action", false), Action::Boot(i)));
                    }
                }
            }
            ("Boot".into(), "Load the entry and kexec into it".into(), rows)
        }
        Screen::Services => {
            for v in &s.services {
                rows.push((row(v.title.clone(), v.detail.clone(), "toggle", v.running), Action::Service(v.name.clone(), !v.running)));
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
                    let open = Screen::Files { dev: d.dev.clone(), name: d.name.clone(), path: "/".into(), listing: Load::Waiting };
                    rows.push((row(title, format!("{}  ·  {}", d.fstype, human_size(d.size)), "nav", false), Action::Open(open)));
                }
            }
            ("Partitions".into(), "Mounted read-only".into(), rows)
        }
        Screen::Files { dev, name, path, listing } => {
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
        Screen::Diagnostics(d) => {
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
            ("Diagnostics".into(), "What the bootloader boots next".into(), rows)
        }
    }
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
