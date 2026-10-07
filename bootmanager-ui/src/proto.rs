//! The backend protocol (bootmanager/internal/rpc), one JSON object per
//! line, and the shapes of its replies and events. Transport-free: a
//! transport hands complete lines to `handle_line` and supplies the
//! function that sends one.

use serde::de::DeserializeOwned;
use serde::Deserialize;
use serde_json::{json, Value};
use std::cell::{Cell, RefCell};
use std::collections::HashMap;

/// Changes whenever a command or event changes shape (bootmanager's
/// protocolVersion).
pub const VERSION: u32 = 7;

#[derive(Deserialize)]
pub struct Hello {
    pub version: u32,
    #[serde(default)]
    pub model: String,
    /// Where /files/ is served, for the web build.
    #[serde(default)]
    pub files_port: String,
    /// The stage has file system checkers: Check and Repair are offered.
    #[serde(default)]
    pub fsck: bool,
    /// The stage can unlock LUKS partitions.
    #[serde(default)]
    pub luks: bool,
}

/// disk.unlock's reply: the dm-N device the volume is now.
#[derive(Deserialize)]
pub struct Unlocked {
    pub dev: String,
}

#[derive(Deserialize, Clone)]
pub struct Entry {
    pub label: String,
    pub detail: String,
    /// Where the entry was found ("microSD card", "UFS userdata (encrypted)"),
    /// to tell entries of one name apart; empty if unknown.
    #[serde(default)]
    pub source: String,
}

/// An encrypted volume that is not open (a LUKS partition).
#[derive(Deserialize, Clone, PartialEq)]
pub struct LockedVolume {
    /// The partition's kernel name (sde27).
    pub dev: String,
    /// Its GPT name (userdata), or the kernel name.
    pub name: String,
    /// Where it is: "UFS userdata", "microSD card".
    #[serde(default)]
    pub source: String,
}

#[derive(Deserialize)]
pub struct Entries {
    /// The locked volume the autoboot waits on: its passphrase is asked for
    /// at once, and the default entry inside is booted when it is in.
    #[serde(default)]
    pub unlock: Option<LockedVolume>,
    pub scanning: bool,
    pub entries: Vec<Entry>,
    /// Encrypted volumes not open: entries may be inside, and unlocking one
    /// finds them. Each is a tile of its own on the carousel.
    #[serde(default)]
    pub locked: Vec<LockedVolume>,
    /// The entry the autoboot boots; explicit: the user chose it, rather
    /// than it being the first.
    #[serde(default)]
    pub default: usize,
    #[serde(default)]
    pub explicit: bool,
}

/// autoboot.settings: whether the stage autoboots at start, the countdown's
/// length, and whether a change outlives this run.
#[derive(Deserialize, Clone)]
pub struct AutobootSettings {
    pub enabled: bool,
    pub seconds: u32,
    pub saved: bool,
}

/// A fastboot flash under way, pushed as the "flash" event: an image comes
/// as pieces, each received and then written, and the whole image has a
/// size. An empty phase clears the screen.
#[derive(Deserialize, Clone, Default, PartialEq)]
pub struct Flash {
    /// "receiving", "writing", "waiting" (for the next piece), "done",
    /// "finished", or "".
    #[serde(default)]
    pub phase: String,
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub piece: i64,
    #[serde(default)]
    pub recv: i64,
    #[serde(default)]
    pub recv_total: i64,
    #[serde(default)]
    pub wrote: i64,
    #[serde(default)]
    pub wrote_total: i64,
    /// How far into the image the last written piece got, of total bytes.
    #[serde(default)]
    pub end: i64,
    #[serde(default)]
    pub total: i64,
    /// When this was said, in milliseconds from the first report: the
    /// clock the flash screen's estimate runs on.
    #[serde(default)]
    pub t: i64,
}

/// The battery's level, pushed as the "battery" event.
#[derive(Deserialize, Clone, PartialEq)]
pub struct Battery {
    /// A battery was found and is in.
    pub present: bool,
    /// 0 to 100; -1 when unknown.
    pub percent: i32,
    /// The kernel's: Charging, Discharging, Full, Not charging, Unknown.
    #[serde(default)]
    pub status: String,
    pub charging: bool,
}

/// One power supply, from power.supplies: a battery or what charges it,
/// with every property the kernel reports (lowercase, in the kernel's units:
/// µV, µA, µAh, tenths of °C).
#[derive(Deserialize, Clone)]
pub struct Supply {
    pub name: String,
    #[serde(rename = "type", default)]
    pub kind: String,
    #[serde(default)]
    pub props: std::collections::BTreeMap<String, String>,
}

#[derive(Deserialize)]
pub struct Autoboot {
    pub remaining: i64,
    pub label: String,
}

#[derive(Deserialize, Clone)]
pub struct Service {
    pub name: String,
    pub title: String,
    pub detail: String,
    pub running: bool,
    /// Why it is off though it was switched on: its process died.
    #[serde(default)]
    pub error: String,
}

#[derive(Deserialize, Clone)]
pub struct Disk {
    pub dev: String,
    pub name: String,
    pub fstype: String,
    pub size: u64,
    /// On an SD card or USB: repair is offered only there.
    #[serde(default)]
    pub removable: bool,
}

/// disk.fsck's reply: how a check or repair ended, and the end of what the
/// checker printed.
#[derive(Deserialize, Clone)]
pub struct FsckResult {
    pub repair: bool,
    /// clean, fixed, errors, dirty-log or failed.
    pub status: String,
    pub code: i32,
    #[serde(default)]
    pub lines: Vec<String>,
}

#[derive(Deserialize)]
pub struct FsckLine {
    pub dev: String,
    pub line: String,
}

#[derive(Deserialize, Clone)]
pub struct FileEntry {
    pub name: String,
    pub dir: bool,
    #[serde(default)]
    pub link: String,
    pub size: i64,
}

#[derive(Deserialize, Clone)]
pub struct Listing {
    pub entries: Vec<FileEntry>,
    pub truncated: bool,
}

#[derive(Deserialize, Clone)]
pub struct Text {
    #[serde(default)]
    pub text: String,
    #[serde(default)]
    pub binary: bool,
    pub size: u64,
    pub truncated: bool,
}

/// An A/B slot as the bootloader keeps it (bootmanager/internal/slots).
#[derive(Deserialize, Clone)]
pub struct Slot {
    pub name: String,
    pub booted: bool,
    pub active: bool,
    pub priority: u32,
    pub retries: u32,
    pub successful: bool,
    pub unbootable: bool,
}

#[derive(Deserialize, Clone)]
pub struct InfoRow {
    pub title: String,
    pub detail: String,
}

#[derive(Deserialize, Clone)]
pub struct SlotsInfo {
    pub slots: Vec<Slot>,
    /// Why there are no slots, when there are none.
    #[serde(default)]
    pub slots_error: String,
    pub bootloader: Vec<InfoRow>,
}

#[derive(Deserialize)]
pub struct Thumb {
    pub width: u32,
    pub height: u32,
    /// Base64 of straight RGBA.
    pub rgba: String,
}

#[derive(Deserialize)]
pub struct DisplayInfo {
    pub panel: Option<Panel>,
}

/// A display panel in physical pixels (bootmanager/internal/panel).
#[derive(Deserialize, Clone)]
pub struct Panel {
    #[serde(rename = "x-res")]
    pub xres: u32,
    #[serde(rename = "y-res")]
    pub yres: u32,
    #[serde(rename = "width-mm", default)]
    pub width_mm: u32,
    /// Top-left, top-right, bottom-right, bottom-left.
    #[serde(rename = "corner-radii")]
    pub corner_radii: [u32; 4],
    pub cutouts: Vec<Cutout>,
}

#[derive(Deserialize, Clone)]
pub struct Cutout {
    pub x: i32,
    pub y: i32,
    pub w: i32,
    pub h: i32,
}

/// A pushed event.
pub enum Event {
    Autoboot(Autoboot),
    Entries(Entries),
    Services(Vec<Service>),
    Busy(String),
    Error(String),
    /// The latest line of a running check.
    Fsck(FsckLine),
    Battery(Battery),
    Flash(Flash),
}

type Reply = Box<dyn FnOnce(Result<Value, String>)>;

#[derive(Default)]
pub struct Client {
    send: RefCell<Option<Box<dyn Fn(String)>>>,
    next_id: Cell<u64>,
    pending: RefCell<HashMap<u64, Reply>>,
}

#[derive(Deserialize)]
struct Message {
    id: Option<u64>,
    event: Option<String>,
    #[serde(default)]
    data: Value,
    error: Option<String>,
}

#[derive(Deserialize)]
struct TextData {
    #[serde(default, alias = "message")]
    text: String,
}

impl Client {
    pub fn set_sender(&self, send: Option<Box<dyn Fn(String)>>) {
        let dead = send.is_none();
        *self.send.borrow_mut() = send;
        if dead {
            // Nothing is coming back on a dead connection.
            let dropped: Vec<Reply> = self.pending.borrow_mut().drain().map(|(_, r)| r).collect();
            for r in dropped {
                r(Err("disconnected".into()));
            }
        }
    }

    pub fn connected(&self) -> bool {
        self.send.borrow().is_some()
    }

    /// Sends cmd and calls reply with its result, decoded as T.
    pub fn call<T: DeserializeOwned>(&self, cmd: &str, args: Value, reply: impl FnOnce(Result<T, String>) + 'static) {
        let id = self.next_id.get() + 1;
        self.next_id.set(id);
        let line = json!({"id": id, "cmd": cmd, "args": args}).to_string() + "\n";
        let decode = move |r: Result<Value, String>| reply(r.and_then(|v| serde_json::from_value(v).map_err(|e| e.to_string())));
        match &*self.send.borrow() {
            Some(send) => {
                self.pending.borrow_mut().insert(id, Box::new(decode));
                send(line);
            }
            None => decode(Err("not connected".into())),
        }
    }

    /// Settles a reply, or returns an event for the caller to act on.
    pub fn handle_line(&self, line: &str) -> Option<Event> {
        let m: Message = serde_json::from_str(line).map_err(|e| eprintln!("bootmanager-ui: bad line: {e}")).ok()?;
        if let Some(name) = m.event {
            return event(&name, m.data);
        }
        // Take the callback out before calling it: it may well send again.
        let reply = self.pending.borrow_mut().remove(&m.id?)?;
        reply(match m.error {
            Some(e) => Err(e),
            None => Ok(m.data),
        });
        None
    }
}

fn event(name: &str, data: Value) -> Option<Event> {
    fn de<T: DeserializeOwned>(v: Value) -> Option<T> {
        serde_json::from_value(v).map_err(|e| eprintln!("bootmanager-ui: bad event: {e}")).ok()
    }
    Some(match name {
        "autoboot" => Event::Autoboot(de(data)?),
        "entries" => Event::Entries(de(data)?),
        "services" => Event::Services(de(data)?),
        "busy" => Event::Busy(de::<TextData>(data)?.text),
        "error" => Event::Error(de::<TextData>(data)?.text),
        "fsck" => Event::Fsck(de(data)?),
        "battery" => Event::Battery(de(data)?),
        "flash" => Event::Flash(de(data)?),
        _ => return None,
    })
}
