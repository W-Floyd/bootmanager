//! What is on screen and what each row does. Every screen is a list; this
//! builds the rows from the backend's data and turns an activated row into a
//! request. The .slint side only draws.

use crate::proto::Client;
use crate::{AppWindow, Row};
use serde::Deserialize;
use serde_json::{json, Value};
use slint::{ComponentHandle, Image, ModelRc, Rgba8Pixel, SharedPixelBuffer, SharedString, VecModel};
use std::cell::{Cell, RefCell};
use std::collections::{HashMap, VecDeque};
use std::rc::Rc;

#[derive(Clone, Debug, PartialEq)]
enum Screen {
    Main,
    Boot,
    Services,
    Disks,
    Files { dev: String, name: String, path: String },
    /// One file: what can be done with it.
    File { dev: String, path: String, size: u64 },
    /// A file's start, as text.
    Text { dev: String, path: String },
    Log,
}

#[derive(Clone)]
enum Action {
    Open(Screen),
    Boot(usize),
    Service(String, bool),
    Dir(String),
    /// A file in the current directory: name, size.
    File(String, u64),
    /// Hand a URL on this backend to the browser.
    Url(String, UrlMode),
    Up,
    Power(&'static str),
    Refresh,
    None,
}

#[derive(Deserialize, Clone)]
struct Entry {
    label: String,
    detail: String,
}

#[derive(Deserialize, Clone)]
struct Service {
    name: String,
    title: String,
    detail: String,
    running: bool,
}

#[derive(Deserialize, Clone)]
struct Disk {
    dev: String,
    name: String,
    fstype: String,
    size: u64,
}

#[derive(Deserialize, Clone)]
struct FileEntry {
    name: String,
    dir: bool,
    #[serde(default)]
    link: String,
    size: i64,
}

#[derive(Deserialize, Clone)]
struct Listing {
    path: String,
    entries: Vec<FileEntry>,
    truncated: bool,
}

#[derive(Deserialize, Clone)]
struct TextView {
    #[serde(default)]
    text: String,
    #[serde(default)]
    binary: bool,
    size: u64,
    truncated: bool,
}

/// Opens a URL on the backend's HTTP server. Only the
/// web build has one; elsewhere the file screen offers the viewer alone.
pub type UrlOpener = Box<dyn Fn(&str, UrlMode)>;

/// What the browser should do with a file URL.
#[derive(Clone, Copy, Debug, PartialEq)]
pub enum UrlMode {
    /// In the page, in the browser's own viewer.
    Preview,
    Tab,
    Download,
}

/// A request in flight or its failure, for screens whose data is loading.
enum Load<T> {
    Waiting,
    Failed(String),
    Ready(T),
}

struct State {
    screen: Screen,
    stack: Vec<(Screen, i32)>,
    actions: Vec<Action>,
    entries: Option<Vec<Entry>>,
    services: Vec<Service>,
    disks: Load<Vec<Disk>>,
    files: Load<Listing>,
    log: Load<Vec<String>>,
    text: Load<TextView>,
    /// Thumbnails by "dev:path"; None for a file that had none to give.
    thumbs: HashMap<String, Option<Image>>,
    /// Image files of the listing on screen still to fetch, one at a time.
    thumb_queue: VecDeque<(String, String)>,
    thumb_busy: bool,
    countdown: i64,
    countdown_label: String,
    error: String,
    where_: String,
}

pub struct App {
    ui: slint::Weak<AppWindow>,
    pub client: Client,
    state: RefCell<State>,
    open_url: RefCell<Option<UrlOpener>>,
    /// Lay out around the panel's cutout and corners (display.info): only
    /// when drawing on the phone's own panel.
    panel_shape: Cell<bool>,
    /// The web build's URL sync (set_route_listener) and the last route told.
    on_route: RefCell<Option<Box<dyn Fn(&str)>>>,
    last_route: RefCell<String>,
}

/// Percent-encodes a partition path for /files/<dev><path>, keeping '/'.
fn url_path(p: &str) -> String {
    let mut out = String::new();
    for b in p.bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'.' | b'_' | b'~' | b'/' => out.push(b as char),
            _ => out.push_str(&format!("%{b:02X}")),
        }
    }
    out
}

fn human_size(n: u64) -> String {
    let units = ["B", "KiB", "MiB", "GiB", "TiB"];
    let mut v = n as f64;
    let mut u = 0;
    while v >= 1024.0 && u < units.len() - 1 {
        v /= 1024.0;
        u += 1;
    }
    if u == 0 {
        format!("{n} B")
    } else {
        format!("{v:.1} {}", units[u])
    }
}

fn row(title: impl Into<SharedString>, detail: impl Into<SharedString>, kind: &str, on: bool) -> Row {
    Row { title: title.into(), detail: detail.into(), kind: kind.into(), on, thumb: Image::default() }
}

impl App {
    /// `where_` names the backend in the header when it is not local, so a
    /// remote window is never mistaken for the phone's own screen.
    pub fn new(ui: &AppWindow, where_: &str) -> Rc<App> {
        let app = Rc::new(App {
            ui: ui.as_weak(),
            client: Client::default(),
            state: RefCell::new(State {
                screen: Screen::Main,
                stack: vec![],
                actions: vec![],
                entries: None,
                services: vec![],
                disks: Load::Waiting,
                files: Load::Waiting,
                log: Load::Waiting,
                text: Load::Waiting,
                thumbs: HashMap::new(),
                thumb_queue: VecDeque::new(),
                thumb_busy: false,
                countdown: -1,
                countdown_label: String::new(),
                error: String::new(),
                where_: where_.to_string(),
            }),
            open_url: RefCell::new(None),
            panel_shape: Cell::new(false),
            on_route: RefCell::new(None),
            last_route: RefCell::new("/".into()),
        });
        let a = Rc::downgrade(&app);
        ui.on_activated(move |i| {
            if let Some(a) = a.upgrade() {
                a.activate(i as usize)
            }
        });
        let a = Rc::downgrade(&app);
        ui.on_back(move || {
            if let Some(a) = a.upgrade() {
                a.back()
            }
        });
        let a = Rc::downgrade(&app);
        ui.on_activity(move || {
            if let Some(a) = a.upgrade() {
                a.activity()
            }
        });
        if !where_.is_empty() {
            ui.set_origin(format!("remote: {where_}").into());
        }
        app.render();
        app
    }

    /// Fetches the next queued thumbnail, one request at a time so a
    /// directory of photos does not flood the backend; each reply starts
    /// the next. Leaving the directory clears the queue (see load_screen).
    fn pump_thumbs(self: &Rc<Self>) {
        let next = {
            let mut s = self.state.borrow_mut();
            if s.thumb_busy {
                return;
            }
            let cur = match &s.screen {
                Screen::Files { dev, path, .. } => Some((dev.clone(), path.trim_end_matches('/').to_string())),
                _ => None,
            };
            let on_dir = |dev: &str, p: &str| {
                cur.as_ref().is_some_and(|(d, dir)| d == dev && p.rsplit_once('/').map(|x| x.0).unwrap_or("") == dir)
            };
            let mut next = None;
            while let Some((dev, p)) = s.thumb_queue.pop_front() {
                if on_dir(&dev, &p) {
                    next = Some((dev, p));
                    break;
                }
            }
            s.thumb_busy = next.is_some();
            next
        };
        let Some((dev, path)) = next else { return };
        let key = thumb_key(&dev, &path);
        // Physical pixels at the phone's scale: a 52 px image at 2x.
        self.call("fs.thumb", json!({"dev": dev, "path": path, "size": 104}), move |a, r| {
            let img = r.ok().and_then(|v| thumb_image(&v));
            {
                let mut s = a.state.borrow_mut();
                s.thumbs.insert(key, img);
                s.thumb_busy = false;
            }
            a.pump_thumbs();
        });
    }

    /// Lays the UI out around the panel's shape; for the phone's own panel.
    pub fn use_panel_shape(&self) {
        self.panel_shape.set(true);
    }

    /// display.info -> the header's cutout split and corner padding. The
    /// panel file is in physical pixels; the UI's lengths are logical.
    fn load_panel(self: &Rc<Self>) {
        self.call("display.info", json!({}), |a, r| {
            let Some(ui) = a.ui.upgrade() else { return };
            let Ok(v) = r else { return };
            let p = &v["panel"];
            if p.is_null() {
                return;
            }
            let scale = ui.window().scale_factor();
            let px = |v: &Value| v.as_f64().unwrap_or(0.0) as f32 / scale;
            ui.set_corner_radius(px(&p["border-radius"]));
            // The first cutout on the top edge is the one the header meets.
            let top = p["cutouts"].as_array().and_then(|c| c.iter().find(|c| c["y"].as_i64() == Some(0)));
            if let Some(c) = top {
                ui.set_cutout_x(px(&c["x"]));
                ui.set_cutout_w(px(&c["w"]));
                ui.set_cutout_h(px(&c["h"]));
            }
        });
    }

    /// Lets the file screen hand files to the browser (web build only).
    pub fn set_url_opener(&self, open: UrlOpener) {
        *self.open_url.borrow_mut() = Some(open);
    }

    // --- transport hooks ---

    pub fn connected(self: &Rc<Self>, send: Box<dyn Fn(String)>) {
        self.client.set_sender(Some(send));
        self.state.borrow_mut().error.clear();
        self.refresh_boot();
        self.refresh_services();
        if self.panel_shape.get() {
            self.load_panel();
        }
        self.load_screen();
    }

    pub fn disconnected(self: &Rc<Self>) {
        self.client.set_sender(None);
        self.state.borrow_mut().countdown = -1;
        if let Some(ui) = self.ui.upgrade() {
            ui.set_busy("".into());
        }
        self.render();
    }

    pub fn line(self: &Rc<Self>, line: &str) {
        let Some((event, data)) = self.client.handle_line(line) else { return };
        match event.as_str() {
            "autoboot" => {
                let mut s = self.state.borrow_mut();
                s.countdown = data["remaining"].as_i64().unwrap_or(-1);
                s.countdown_label = data["label"].as_str().unwrap_or("").to_string();
            }
            "entries" => self.set_entries(&data),
            "services" => {
                if let Ok(v) = serde_json::from_value(data) {
                    self.state.borrow_mut().services = v;
                }
            }
            "busy" => {
                if let Some(ui) = self.ui.upgrade() {
                    ui.set_busy(data["text"].as_str().unwrap_or("").into());
                }
            }
            "error" => {
                self.state.borrow_mut().error = data["message"].as_str().unwrap_or("").to_string();
            }
            _ => {}
        }
        self.render();
    }

    // --- requests ---

    fn call(self: &Rc<Self>, cmd: &str, args: Value, done: impl FnOnce(&Rc<App>, Result<Value, String>) + 'static) {
        let weak = Rc::downgrade(self);
        self.client.call(cmd, args, move |r| {
            if let Some(a) = weak.upgrade() {
                done(&a, r);
                a.render();
            }
        });
    }

    fn set_entries(&self, data: &Value) {
        if data["scanning"].as_bool() == Some(true) {
            return;
        }
        if let Ok(v) = serde_json::from_value(data["entries"].clone()) {
            self.state.borrow_mut().entries = Some(v);
        }
    }

    fn refresh_boot(self: &Rc<Self>) {
        self.call("boot.list", json!({}), |a, r| match r {
            Ok(v) => a.set_entries(&v),
            Err(e) => a.state.borrow_mut().error = e,
        });
    }

    fn refresh_services(self: &Rc<Self>) {
        self.call("service.list", json!({}), |a, r| {
            if let Ok(v) = r.and_then(|v| serde_json::from_value(v).map_err(|e| e.to_string())) {
                a.state.borrow_mut().services = v;
            }
        });
    }

    fn load_screen(self: &Rc<Self>) {
        let screen = self.state.borrow().screen.clone();
        match screen {
            Screen::Boot => self.refresh_boot(),
            Screen::Services => self.refresh_services(),
            Screen::Disks => {
                self.state.borrow_mut().disks = Load::Waiting;
                self.call("disk.list", json!({}), |a, r| {
                    a.state.borrow_mut().disks = match r.and_then(|v| serde_json::from_value(v).map_err(|e| e.to_string())) {
                        Ok(v) => Load::Ready(v),
                        Err(e) => Load::Failed(e),
                    };
                });
            }
            Screen::Files { dev, path, .. } => {
                self.state.borrow_mut().files = Load::Waiting;
                let want = screen_key(&self.state.borrow().screen);
                let (dir_dev, dir) = (dev.clone(), path.clone());
                self.call("fs.list", json!({"dev": dev, "path": path}), move |a, r| {
                    // Ignore a listing for a directory we have already left.
                    if screen_key(&a.state.borrow().screen) != want {
                        return;
                    }
                    let listing = r.and_then(|v| serde_json::from_value::<Listing>(v).map_err(|e| e.to_string()));
                    if let Ok(l) = &listing {
                        let mut s = a.state.borrow_mut();
                        s.thumb_queue.clear();
                        for f in l.entries.iter().filter(|f| !f.dir && f.link.is_empty() && is_image(&f.name)) {
                            let p = format!("{}/{}", dir.trim_end_matches('/'), f.name);
                            if !s.thumbs.contains_key(&thumb_key(&dir_dev, &p)) {
                                s.thumb_queue.push_back((dir_dev.clone(), p));
                            }
                        }
                    }
                    a.state.borrow_mut().files = match listing {
                        Ok(v) => Load::Ready(v),
                        Err(e) => Load::Failed(e),
                    };
                    a.pump_thumbs();
                });
            }
            Screen::Log => {
                self.state.borrow_mut().log = Load::Waiting;
                self.call("log.tail", json!({"lines": 300}), |a, r| {
                    a.state.borrow_mut().log = match r.and_then(|v| serde_json::from_value(v).map_err(|e| e.to_string())) {
                        Ok(v) => Load::Ready(v),
                        Err(e) => Load::Failed(e),
                    };
                });
            }
            Screen::Text { dev, path } => {
                self.state.borrow_mut().text = Load::Waiting;
                self.call("fs.read", json!({"dev": dev, "path": path}), |a, r| {
                    a.state.borrow_mut().text = match r.and_then(|v| serde_json::from_value(v).map_err(|e| e.to_string())) {
                        Ok(v) => Load::Ready(v),
                        Err(e) => Load::Failed(e),
                    };
                });
            }
            Screen::Main | Screen::File { .. } => {}
        }
    }

    // --- input ---

    fn activity(self: &Rc<Self>) {
        let counting = self.state.borrow().countdown >= 0;
        if counting {
            self.state.borrow_mut().countdown = -1;
            self.call("autoboot.cancel", json!({}), |_, _| {});
            self.render();
        }
    }

    fn open(self: &Rc<Self>, screen: Screen) {
        let selected = self.ui.upgrade().map(|u| u.get_selected()).unwrap_or(0);
        {
            let mut s = self.state.borrow_mut();
            let prev = std::mem::replace(&mut s.screen, screen);
            s.stack.push((prev, selected));
        }
        self.render_at(1);
        self.load_screen();
    }

    fn back(self: &Rc<Self>) {
        let popped = self.state.borrow_mut().stack.pop();
        if let Some((screen, selected)) = popped {
            self.state.borrow_mut().screen = screen;
            self.render_at(selected);
            self.load_screen();
        }
    }

    fn activate(self: &Rc<Self>, i: usize) {
        let action = self.state.borrow().actions.get(i).cloned().unwrap_or(Action::None);
        match action {
            Action::Open(s) => self.open(s),
            Action::Boot(i) => {
                self.call("boot.start", json!({"index": i}), |a, r| {
                    if let Err(e) = r {
                        a.state.borrow_mut().error = e;
                    }
                });
            }
            Action::Service(name, on) => {
                self.call("service.set", json!({"name": name, "running": on}), |a, r| match r {
                    Ok(v) => {
                        if let Ok(v) = serde_json::from_value(v) {
                            a.state.borrow_mut().services = v;
                        }
                    }
                    Err(e) => a.state.borrow_mut().error = e,
                });
            }
            Action::Dir(name) => {
                let next = match &self.state.borrow().screen {
                    Screen::Files { dev, name: part, path } => Some(Screen::Files {
                        dev: dev.clone(),
                        name: part.clone(),
                        path: format!("{}/{}", path.trim_end_matches('/'), name),
                    }),
                    _ => None,
                };
                if let Some(s) = next {
                    self.open(s);
                }
            }
            Action::Up => self.back(),
            Action::Power(what) => {
                self.call("power", json!({"action": what}), |a, r| {
                    if let Err(e) = r {
                        a.state.borrow_mut().error = e;
                    }
                });
            }
            Action::File(name, size) => {
                let next = match &self.state.borrow().screen {
                    Screen::Files { dev, path, .. } => Some(Screen::File {
                        dev: dev.clone(),
                        path: format!("{}/{}", path.trim_end_matches('/'), name),
                        size,
                    }),
                    _ => None,
                };
                if let Some(s) = next {
                    self.open(s);
                }
            }
            Action::Url(url, mode) => {
                if let Some(open) = &*self.open_url.borrow() {
                    open(&url, mode);
                }
            }
            Action::Refresh => self.load_screen(),
            Action::None => {}
        }
    }

    // --- drawing ---

    fn render_at(&self, selected: i32) {
        self.render();
        if let Some(ui) = self.ui.upgrade() {
            ui.set_selected(selected);
            ui.invoke_scroll_to_selected();
        }
    }

    pub fn render(&self) {
        let Some(ui) = self.ui.upgrade() else { return };
        let mut s = self.state.borrow_mut();
        let (heading, sub, items) = build(&s, self.client.connected(), self.open_url.borrow().is_some());
        let (rows, actions): (Vec<Row>, Vec<Action>) = items.into_iter().unzip();
        s.actions = actions;

        let (status, alert) = if !self.client.connected() {
            (format!("Waiting for the backend{}…", if s.where_.is_empty() { String::new() } else { format!(" at {}", s.where_) }), true)
        } else if s.countdown >= 0 {
            (format!("Booting {} in {} s. Press any key to stay.", s.countdown_label, s.countdown), true)
        } else if !s.error.is_empty() {
            (s.error.clone(), true)
        } else {
            (String::new(), false)
        };

        let n = rows.len() as i32;
        ui.set_compact(matches!(s.screen, Screen::Log | Screen::Text { .. }));
        ui.set_heading(heading.into());
        ui.set_subheading(sub.into());
        ui.set_status(status.into());
        ui.set_status_alert(alert);
        ui.set_rows(ModelRc::new(VecModel::from(rows)));
        if ui.get_selected() >= n {
            ui.set_selected((n - 1).max(0));
        }

        let route = route_of(&s.screen);
        drop(s);
        if *self.last_route.borrow() != route {
            *self.last_route.borrow_mut() = route.clone();
            if let Some(f) = &*self.on_route.borrow() {
                f(&route);
            }
        }
    }

    // --- routes: the screen as a URL path, for the web build ---

    /// Called with the new route whenever the screen changes.
    pub fn set_route_listener(&self, f: Box<dyn Fn(&str)>) {
        *self.on_route.borrow_mut() = Some(f);
    }

    /// Goes to the screen a route names, with the Back stack its path
    /// implies (a directory's parents, then the file), and loads it. An
    /// unknown route is the main screen.
    pub fn navigate_to(self: &Rc<Self>, route: &str) {
        let (stack, screen) = screens_of(route);
        let selected = if screen == Screen::Main { 0 } else { 1 };
        {
            let mut s = self.state.borrow_mut();
            s.stack = stack.into_iter().map(|sc| (sc, 1)).collect();
            if let Some(first) = s.stack.first_mut() {
                first.1 = 0;
            }
            s.screen = screen;
        }
        // The listener must not echo this back as a new history entry.
        *self.last_route.borrow_mut() = route_of(&self.state.borrow().screen);
        self.render_at(selected);
        self.load_screen();
    }
}

/// The screen's URL path. Directories and files are percent-encoded.
fn route_of(s: &Screen) -> String {
    match s {
        Screen::Main => "/".into(),
        Screen::Boot => "/boot".into(),
        Screen::Services => "/services".into(),
        Screen::Disks => "/partitions".into(),
        Screen::Log => "/log".into(),
        Screen::Files { dev, path, .. } => {
            format!("/partitions/{dev}{}", url_path(path.trim_end_matches('/')))
        }
        Screen::File { dev, path, .. } => format!("/file/{dev}{}", url_path(path)),
        Screen::Text { dev, path } => format!("/text/{dev}{}", url_path(path)),
    }
}

/// A route's screen and the stack of screens Back walks from it.
fn screens_of(route: &str) -> (Vec<Screen>, Screen) {
    let route = url_unpath(route);
    let parts: Vec<&str> = route.split('/').filter(|p| !p.is_empty()).collect();
    let dir_stack = |dev: &str, dirs: &[&str]| {
        let mut st = vec![Screen::Main, Screen::Disks];
        let mut p = String::new();
        st.push(Screen::Files { dev: dev.into(), name: String::new(), path: "/".into() });
        for d in dirs {
            p.push('/');
            p.push_str(d);
            st.push(Screen::Files { dev: dev.into(), name: String::new(), path: p.clone() });
        }
        st
    };
    match parts.as_slice() {
        ["boot"] => (vec![Screen::Main], Screen::Boot),
        ["services"] => (vec![Screen::Main], Screen::Services),
        ["log"] => (vec![Screen::Main], Screen::Log),
        ["partitions"] => (vec![Screen::Main], Screen::Disks),
        ["partitions", dev, rest @ ..] => {
            let mut st = dir_stack(dev, rest);
            let screen = st.pop().unwrap();
            (st, screen)
        }
        [kind @ ("file" | "text"), dev, rest @ ..] if !rest.is_empty() => {
            let path = format!("/{}", rest.join("/"));
            let mut st = dir_stack(dev, &rest[..rest.len() - 1]);
            let file = Screen::File { dev: dev.to_string(), path: path.clone(), size: 0 };
            if *kind == "file" {
                (st, file)
            } else {
                st.push(file);
                (st, Screen::Text { dev: dev.to_string(), path })
            }
        }
        _ => (vec![], Screen::Main),
    }
}

/// Undoes url_path's percent-encoding.
fn url_unpath(s: &str) -> String {
    let b = s.as_bytes();
    let mut out = Vec::with_capacity(b.len());
    let mut i = 0;
    while i < b.len() {
        if b[i] == b'%' && i + 2 < b.len() {
            if let Ok(v) = u8::from_str_radix(&s[i + 1..i + 3], 16) {
                out.push(v);
                i += 3;
                continue;
            }
        }
        out.push(b[i]);
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

fn thumb_key(dev: &str, path: &str) -> String {
    format!("{dev}:{path}")
}

/// The formats the backend can make thumbnails of (Go's standard library).
fn is_image(name: &str) -> bool {
    let n = name.to_ascii_lowercase();
    [".jpg", ".jpeg", ".png", ".gif"].iter().any(|e| n.ends_with(e))
}

/// fs.thumb's reply -- width, height, base64 RGBA -- as an image.
fn thumb_image(v: &Value) -> Option<Image> {
    use base64::Engine;
    let w = v["width"].as_u64()? as u32;
    let h = v["height"].as_u64()? as u32;
    let px = base64::engine::general_purpose::STANDARD.decode(v["rgba"].as_str()?).ok()?;
    if w == 0 || h == 0 || px.len() != (w * h * 4) as usize {
        return None;
    }
    Some(Image::from_rgba8(SharedPixelBuffer::<Rgba8Pixel>::clone_from_slice(&px, w, h)))
}

fn screen_key(s: &Screen) -> String {
    format!("{s:?}")
}

/// Every screen but the main one starts with a Back row: with three keys,
/// an item is easier to find than a long press. `open` selects the row
/// after it, so a stray Power press does not leave the screen.
fn build(s: &State, connected: bool, web: bool) -> (String, String, Vec<(Row, Action)>) {
    let (heading, sub, mut items) = build_screen(s, connected, web);
    if connected && s.screen != Screen::Main {
        items.insert(0, (row("< Back", "", "back", false), Action::Up));
    }
    (heading, sub, items)
}

fn build_screen(s: &State, connected: bool, web: bool) -> (String, String, Vec<(Row, Action)>) {
    let mut items = vec![];
    let sub = String::new();
    if !connected {
        return ("Recovery".into(), sub, items);
    }
    match &s.screen {
        Screen::Main => {
            let boot_detail = match &s.entries {
                None => "Looking for boot entries…".to_string(),
                Some(e) if e.is_empty() => "No boot entries found".to_string(),
                Some(e) => format!("Default: {}", e[0].label),
            };
            let svc: Vec<String> = s
                .services
                .iter()
                .map(|v| format!("{} {}", v.title, if v.running { "on" } else { "off" }))
                .collect();
            items.push((row("Boot", boot_detail, "nav", false), Action::Open(Screen::Boot)));
            items.push((row("Services", svc.join("  ·  "), "nav", false), Action::Open(Screen::Services)));
            if let Some(r) = s.services.iter().find(|v| v.name == "remote" && v.running) {
                items.push((row("Remote UI is on", r.detail.clone(), "info", false), Action::Open(Screen::Services)));
            }
            items.push((row("Partitions", "Browse file systems, read-only", "nav", false), Action::Open(Screen::Disks)));
            items.push((row("Log", "Stage and backend output", "nav", false), Action::Open(Screen::Log)));
            items.push((row("Reboot", "", "action", false), Action::Power("reboot")));
            items.push((row("Reboot to bootloader", "ABL fastboot", "action", false), Action::Power("bootloader")));
            items.push((row("Power off", "", "danger", false), Action::Power("poweroff")));
            ("Recovery".into(), sub, items)
        }
        Screen::Boot => {
            match &s.entries {
                None => items.push((row("Scanning…", "", "info", false), Action::None)),
                Some(e) if e.is_empty() => {
                    items.push((row("No boot entries", "Nothing on any disk has an extlinux.conf", "info", false), Action::None))
                }
                Some(e) => {
                    for (i, e) in e.iter().enumerate() {
                        let title = if i == 0 { format!("{}  (default)", e.label) } else { e.label.clone() };
                        items.push((row(title, e.detail.clone(), "action", false), Action::Boot(i)));
                    }
                }
            }
            ("Boot".into(), "Load the entry and kexec into it".into(), items)
        }
        Screen::Services => {
            for v in &s.services {
                items.push((row(v.title.clone(), v.detail.clone(), "toggle", v.running), Action::Service(v.name.clone(), !v.running)));
            }
            ("Services".into(), "Power toggles the selected service".into(), items)
        }
        Screen::Disks => {
            match &s.disks {
                Load::Waiting => items.push((row("Probing…", "", "info", false), Action::None)),
                Load::Failed(e) => items.push((row("Failed", e.clone(), "info", false), Action::Refresh)),
                Load::Ready(d) if d.is_empty() => items.push((row("No file systems found", "", "info", false), Action::Refresh)),
                Load::Ready(d) => {
                    for d in d {
                        let title = if d.name.is_empty() { d.dev.clone() } else { format!("{}  ·  {}", d.dev, d.name) };
                        items.push((
                            row(title, format!("{}  ·  {}", d.fstype, human_size(d.size)), "nav", false),
                            Action::Open(Screen::Files { dev: d.dev.clone(), name: d.name.clone(), path: "/".into() }),
                        ));
                    }
                }
            }
            ("Partitions".into(), "Mounted read-only".into(), items)
        }
        Screen::Files { dev, name, path } => {
            match &s.files {
                Load::Waiting => items.push((row("Reading…", "", "info", false), Action::None)),
                Load::Failed(e) => items.push((row("Failed", e.clone(), "info", false), Action::Refresh)),
                Load::Ready(l) => {
                    for f in &l.entries {
                        if f.dir {
                            items.push((row(format!("{}/", f.name), "", "nav", false), Action::Dir(f.name.clone())));
                        } else if !f.link.is_empty() {
                            items.push((row(f.name.clone(), format!("-> {}", f.link), "info", false), Action::None));
                        } else {
                            let mut r = row(f.name.clone(), human_size(f.size as u64), "nav", false);
                            let p = format!("{}/{}", path.trim_end_matches('/'), f.name);
                            if let Some(Some(img)) = s.thumbs.get(&thumb_key(dev, &p)) {
                                r.thumb = img.clone();
                            }
                            items.push((r, Action::File(f.name.clone(), f.size.max(0) as u64)));
                        }
                    }
                    if l.truncated {
                        items.push((row("(listing truncated)", "", "info", false), Action::None));
                    }
                    let _ = &l.path;
                }
            }
            let heading = if name.is_empty() { dev.clone() } else { name.clone() };
            (heading, format!("{dev}:{path}"), items)
        }
        Screen::File { dev, path, size } => {
            let name = path.rsplit('/').next().unwrap_or(path).to_string();
            let text = (
                row(if web { "View as text" } else { "View" }, "The start of it, in this UI", "nav", false),
                Action::Open(Screen::Text { dev: dev.clone(), path: path.clone() }),
            );
            if web {
                let url = format!("/files/{dev}{}", url_path(path));
                items.push((
                    row("Preview", "In the browser's own viewer: images, video, audio, text, PDF", "action", false),
                    Action::Url(url.clone(), UrlMode::Preview),
                ));
                items.push((row("Open in a new tab", "", "action", false), Action::Url(url.clone(), UrlMode::Tab)));
                // A deep-linked file screen has no size (it comes from the listing).
                let size = if *size > 0 { human_size(*size) } else { String::new() };
                items.push((row("Download", size, "action", false), Action::Url(url, UrlMode::Download)));
                items.push(text);
            } else {
                items.push(text);
                items.push((row(human_size(*size), "Download it from the browser UI (Remote UI)", "info", false), Action::None));
            }
            (name, format!("{dev}:{path}"), items)
        }
        Screen::Text { dev, path } => {
            match &s.text {
                Load::Waiting => items.push((row("Reading…", "", "info", false), Action::None)),
                Load::Failed(e) => items.push((row("Failed", e.clone(), "info", false), Action::Refresh)),
                Load::Ready(t) if t.binary => {
                    items.push((row(format!("Binary, {}", human_size(t.size)), "Not shown as text", "info", false), Action::None))
                }
                Load::Ready(t) => {
                    // Tabs as spaces: the list draws one line per row, no tab stops.
                    for l in t.text.lines().take(5000) {
                        items.push((row(l.replace('\t', "    "), "", "info", false), Action::None));
                    }
                    if t.truncated {
                        items.push((row(format!("(first {} of {})", human_size(t.text.len() as u64), human_size(t.size)), "", "info", false), Action::None));
                    }
                }
            }
            let name = path.rsplit('/').next().unwrap_or(path).to_string();
            (name, format!("{dev}:{path}"), items)
        }
        Screen::Log => {
            match &s.log {
                Load::Waiting => items.push((row("Reading…", "", "info", false), Action::None)),
                Load::Failed(e) => items.push((row("Failed", e.clone(), "info", false), Action::Refresh)),
                Load::Ready(lines) => {
                    items.push((row("Refresh", "", "action", false), Action::Refresh));
                    for l in lines.iter().rev() {
                        items.push((row(l.clone(), "", "info", false), Action::None));
                    }
                }
            }
            ("Log".into(), "Newest first".into(), items)
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn files(dev: &str, path: &str) -> Screen {
        Screen::Files { dev: dev.into(), name: String::new(), path: path.into() }
    }

    #[test]
    fn routes_round_trip() {
        let d = "mmcblk0p2";
        for (route, stack_len, screen) in [
            ("/", 0, Screen::Main),
            ("/boot", 1, Screen::Boot),
            ("/partitions", 1, Screen::Disks),
            ("/partitions/mmcblk0p2", 2, files(d, "/")),
            ("/partitions/mmcblk0p2/usr/share", 4, files(d, "/usr/share")),
            ("/file/mmcblk0p2/etc/os-release", 4, Screen::File { dev: d.into(), path: "/etc/os-release".into(), size: 0 }),
            ("/text/mmcblk0p2/etc/os-release", 5, Screen::Text { dev: d.into(), path: "/etc/os-release".into() }),
            ("/file/mmcblk0p2/my%20photo%20%C3%A9.jpg", 3, Screen::File { dev: d.into(), path: "/my photo é.jpg".into(), size: 0 }),
        ] {
            let (stack, got) = screens_of(route);
            assert_eq!(got, screen, "{route}");
            assert_eq!(stack.len(), stack_len, "{route}: {stack:?}");
            assert_eq!(route_of(&got), route, "{route} back to a route");
        }
        // The stack under a file is its directories, parent last.
        let (stack, _) = screens_of("/text/mmcblk0p2/a/b/c.txt");
        assert_eq!(stack[3], files(d, "/a"));
        assert_eq!(stack[4], files(d, "/a/b"));
        assert!(matches!(stack[5], Screen::File { .. }));
        // Anything else is the main screen.
        assert_eq!(screens_of("/nope/x").1, Screen::Main);
        assert_eq!(screens_of("/file/mmcblk0p2").1, Screen::Main);
    }
}
