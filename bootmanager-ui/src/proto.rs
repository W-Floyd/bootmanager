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
pub const VERSION: u32 = 4;

#[derive(Deserialize)]
pub struct Hello {
    pub version: u32,
    #[serde(default)]
    pub model: String,
    /// Where /files/ is served, for the web build.
    #[serde(default)]
    pub files_port: String,
}

#[derive(Deserialize, Clone)]
pub struct Entry {
    pub label: String,
    pub detail: String,
}

#[derive(Deserialize)]
pub struct Entries {
    pub scanning: bool,
    pub entries: Vec<Entry>,
    /// The entry the autoboot boots; explicit: the user chose it, rather
    /// than it being the first.
    #[serde(default)]
    pub default: usize,
    #[serde(default)]
    pub explicit: bool,
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
}

#[derive(Deserialize, Clone)]
pub struct Disk {
    pub dev: String,
    pub name: String,
    pub fstype: String,
    pub size: u64,
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
        _ => return None,
    })
}
