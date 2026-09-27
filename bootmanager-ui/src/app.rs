//! The UI's state and its conversation with the backend. screens.rs
//! decides what each screen shows; the .slint side only draws.

use crate::proto::{self, Client, DisplayInfo, Event, Hello, Panel, Service, Thumb};
use crate::routes::{route_of, screens_of};
use crate::screens::{self, is_image, join, Action, Load, Screen, UrlMode};
use crate::{cards, AppWindow, Card, Row};
use serde::de::DeserializeOwned;
use serde_json::{json, Value};
use slint::platform::WindowEvent;
use slint::{ComponentHandle, Image, ModelRc, PhysicalSize, Rgba8Pixel, SharedPixelBuffer, VecModel};
use std::cell::RefCell;
use std::collections::{HashMap, VecDeque};
use std::rc::Rc;

thread_local! {
    /// The one App, on the UI thread, where the transport delivers lines.
    static APP: RefCell<Option<Rc<App>>> = const { RefCell::new(None) };
}

/// Runs f with the App, once there is one.
pub fn with_app(f: impl FnOnce(&Rc<App>)) {
    if let Some(app) = APP.with(|a| a.borrow().clone()) {
        f(&app)
    }
}

/// Where the UI draws, which decides what it lays out around.
pub enum Mode {
    /// The device's own panel: its shape and scale from display.info.
    /// fb_width_mm is the scale's fallback when the panel file has no
    /// physical size (0 unknown).
    Panel { fb_width_mm: u32 },
    /// Rendering to memory (--screenshot): as Panel, and the window takes
    /// the panel's size.
    Headless,
    /// A browser page served by the backend at host.
    Browser { host: String },
}

/// The scale factor for a panel `width` pixels and `width_mm` wide (0
/// unknown): logical pixels of about 0.19 mm, which puts 360 of them across
/// a typical 68 mm phone, in quarter steps. Without a physical size, 360
/// logical pixels across.
pub fn scale_for(width: u32, width_mm: u32) -> f32 {
    let raw = if width_mm > 0 { width as f32 / width_mm as f32 * (68.0 / 360.0) } else { width as f32 / 360.0 };
    ((raw * 4.0).round() / 4.0).clamp(1.0, 4.0)
}

/// Opens a file for the browser: the files port, the path under it, how.
pub type UrlOpener = Box<dyn Fn(&str, &str, UrlMode)>;

pub struct State {
    pub screen: Screen,
    stack: Vec<(Screen, i32)>,
    actions: Vec<Action>,
    /// Bumped at every screen change, so a late reply for a screen already
    /// left is dropped.
    generation: u64,
    pub entries: Option<Vec<proto::Entry>>,
    pub services: Vec<Service>,
    pub modes: Vec<String>,
    /// Files can be handed to a browser (the web build).
    pub browser: bool,
    files_port: String,
    /// Thumbnails by (dev, path); None for a file that had none to give.
    pub thumbs: HashMap<(String, String), Option<Image>>,
    /// Image files of the listing on screen still to fetch, one at a time.
    thumb_queue: VecDeque<(String, String)>,
    thumb_busy: bool,
    countdown: i64,
    /// The countdown's length, for its bar: the largest remaining seen.
    countdown_total: i64,
    countdown_label: String,
    error: String,
    /// The menu is up over the carousel.
    menu_open: bool,
    /// The card being booted, or -1.
    launching: i32,
    /// The carousel has made its entrance.
    revealed: bool,
    /// What the carousel shows, to replace its model only on a change:
    /// a new model restarts its cards' animations.
    cards: Vec<Card>,
}

pub struct App {
    ui: slint::Weak<AppWindow>,
    client: Client,
    state: RefCell<State>,
    mode: Mode,
    open_url: RefCell<Option<UrlOpener>>,
    /// The web build's URL sync, and the last route it was told.
    on_route: RefCell<Option<Box<dyn Fn(&str)>>>,
    last_route: RefCell<String>,
}

impl App {
    pub fn install(ui: &AppWindow, mode: Mode) -> Rc<App> {
        let app = Rc::new(App {
            ui: ui.as_weak(),
            client: Client::default(),
            state: RefCell::new(State {
                screen: Screen::Main,
                stack: vec![],
                actions: vec![],
                generation: 0,
                entries: None,
                services: vec![],
                modes: vec![],
                browser: matches!(mode, Mode::Browser { .. }),
                files_port: String::new(),
                thumbs: HashMap::new(),
                thumb_queue: VecDeque::new(),
                thumb_busy: false,
                countdown: -1,
                countdown_total: 0,
                countdown_label: String::new(),
                error: String::new(),
                menu_open: false,
                launching: -1,
                revealed: false,
                cards: vec![],
            }),
            mode,
            open_url: RefCell::new(None),
            on_route: RefCell::new(None),
            last_route: RefCell::new("/".into()),
        });
        let weak = Rc::downgrade(&app);
        let on = move |f: fn(&Rc<App>)| {
            let weak = weak.clone();
            move || {
                if let Some(a) = weak.upgrade() {
                    f(&a)
                }
            }
        };
        ui.on_back(on(|a| a.back()));
        ui.on_close_menu(on(|a| a.close_menu()));
        ui.on_activity(on(|a| a.activity()));
        ui.on_open_menu(on(|a| a.open_menu()));
        let weak = Rc::downgrade(&app);
        ui.on_activated(move |i| {
            if let Some(a) = weak.upgrade() {
                a.activate(i as usize)
            }
        });
        let weak = Rc::downgrade(&app);
        ui.on_card_activated(move |i| {
            if let Some(a) = weak.upgrade() {
                a.activate_card(i as usize)
            }
        });
        APP.with(|a| *a.borrow_mut() = Some(app.clone()));
        app.render();
        // The entrance waits for the boot entries, but not for ever.
        let weak = Rc::downgrade(&app);
        slint::Timer::single_shot(std::time::Duration::from_millis(1500), move || {
            if let Some(a) = weak.upgrade() {
                a.reveal()
            }
        });
        app
    }

    /// Plays the carousel's entrance, once its cards are in the window.
    fn reveal(self: &Rc<Self>) {
        if std::mem::replace(&mut self.state.borrow_mut().revealed, true) {
            return;
        }
        if let Some(ui) = self.ui.upgrade() {
            ui.set_revealed(true);
        }
    }

    /// Reveals once the entries are in: a frame later, so the cards exist
    /// to make their entrance.
    fn entries_arrived(self: &Rc<Self>) {
        let weak = Rc::downgrade(self);
        slint::Timer::single_shot(std::time::Duration::from_millis(60), move || {
            if let Some(a) = weak.upgrade() {
                a.reveal()
            }
        });
    }

    // --- transport hooks ---

    pub fn connected(self: &Rc<Self>, send: Box<dyn Fn(String)>) {
        self.client.set_sender(Some(send));
        self.state.borrow_mut().error.clear();
        self.call("hello", json!({}), |a, r: Result<Hello, _>| match r {
            Ok(h) if h.version == proto::VERSION => {
                let top = match &a.mode {
                    Mode::Browser { host } => [h.model.as_str(), &format!("remote: {host}")].join("  ·  "),
                    _ => h.model,
                };
                if let Some(ui) = a.ui.upgrade() {
                    ui.set_model(top.trim_start_matches("  ·  ").into());
                }
                a.state.borrow_mut().files_port = h.files_port;
            }
            Ok(h) => a.state.borrow_mut().error = format!("Backend protocol {}, UI {}: update both", h.version, proto::VERSION),
            Err(e) => a.state.borrow_mut().error = e,
        });
        if !matches!(self.mode, Mode::Browser { .. }) {
            self.call("display.info", json!({}), |a, r: Result<DisplayInfo, _>| a.apply_panel(r.ok().and_then(|d| d.panel)));
        }
        self.call("power.modes", json!({}), |a, r| a.state.borrow_mut().modes = r.unwrap_or_default());
        self.refresh_boot();
        self.refresh_services();
        self.load_screen();
    }

    pub fn disconnected(self: &Rc<Self>) {
        self.client.set_sender(None);
        {
            let mut s = self.state.borrow_mut();
            s.countdown = -1;
            s.launching = -1;
        }
        if let Some(ui) = self.ui.upgrade() {
            ui.set_busy("".into());
        }
        self.render();
    }

    pub fn line(self: &Rc<Self>, line: &str) {
        let Some(event) = self.client.handle_line(line) else { return };
        let mut s = self.state.borrow_mut();
        match event {
            Event::Autoboot(a) => {
                (s.countdown, s.countdown_label) = (a.remaining, a.label);
                s.countdown_total = s.countdown_total.max(a.remaining);
            }
            Event::Entries(e) if !e.scanning => {
                s.entries = Some(e.entries);
                drop(s);
                self.entries_arrived();
                s = self.state.borrow_mut();
            }
            Event::Entries(_) => {}
            Event::Services(v) => s.services = v,
            Event::Error(e) => s.error = e,
            Event::Busy(text) => {
                // A boot the UI did not start (the autoboot): launch its card
                // too. A busy that ends without a handover is a failed boot.
                if text.is_empty() {
                    s.launching = -1;
                } else if s.launching < 0 {
                    if let Some(i) = s.entries.iter().flatten().position(|e| text == format!("Loading {}…", e.label)) {
                        s.launching = i as i32;
                        s.menu_open = false;
                    }
                }
                if let Some(ui) = self.ui.upgrade() {
                    ui.set_busy(text.into());
                }
            }
        }
        drop(s);
        self.render();
    }

    // --- requests ---

    fn call<T: DeserializeOwned + 'static>(
        self: &Rc<Self>,
        cmd: &str,
        args: Value,
        done: impl FnOnce(&Rc<App>, Result<T, String>) + 'static,
    ) {
        let weak = Rc::downgrade(self);
        self.client.call(cmd, args, move |r| {
            if let Some(a) = weak.upgrade() {
                done(&a, r);
                a.render();
            }
        });
    }

    /// Requests data for the screen on show and hands the reply to put,
    /// unless the screen has changed in the meantime.
    fn fetch<T: DeserializeOwned + 'static>(self: &Rc<Self>, cmd: &str, args: Value, put: impl FnOnce(&mut State, Load<T>) + 'static) {
        let generation = self.state.borrow().generation;
        self.call(cmd, args, move |a, r| {
            let mut s = a.state.borrow_mut();
            if s.generation == generation {
                put(&mut s, r.into());
            }
        });
    }

    /// Reports a failed action on the status line.
    fn report(a: &Rc<App>, r: Result<Value, String>) {
        if let Err(e) = r {
            a.state.borrow_mut().error = e;
        }
    }

    fn refresh_boot(self: &Rc<Self>) {
        self.call("boot.list", json!({}), |a, r: Result<proto::Entries, _>| match r {
            Ok(e) if !e.scanning => {
                a.state.borrow_mut().entries = Some(e.entries);
                a.entries_arrived();
            }
            Ok(_) => {}
            Err(e) => a.state.borrow_mut().error = e,
        });
    }

    fn refresh_services(self: &Rc<Self>) {
        self.call("service.list", json!({}), |a, r: Result<Vec<Service>, _>| {
            if let Ok(v) = r {
                a.state.borrow_mut().services = v;
            }
        });
    }

    fn load_screen(self: &Rc<Self>) {
        let screen = self.state.borrow().screen.clone();
        match screen {
            Screen::Boot => self.refresh_boot(),
            Screen::Services => self.refresh_services(),
            Screen::Disks(_) => self.fetch("disk.list", json!({}), |s, l| {
                if let Screen::Disks(d) = &mut s.screen {
                    *d = l
                }
            }),
            Screen::Files { dev, path, .. } => self.fetch("fs.list", json!({"dev": dev, "path": path}), move |s, l: Load<proto::Listing>| {
                if let Load::Ready(listing) = &l {
                    s.thumb_queue = listing
                        .entries
                        .iter()
                        .filter(|f| !f.dir && f.link.is_empty() && is_image(&f.name))
                        .map(|f| (dev.clone(), join(&path, &f.name)))
                        .filter(|k| !s.thumbs.contains_key(k))
                        .collect();
                }
                if let Screen::Files { listing, .. } = &mut s.screen {
                    *listing = l
                }
            }),
            Screen::Text { dev, path, .. } => self.fetch("fs.read", json!({"dev": dev, "path": path}), |s, l| {
                if let Screen::Text { text, .. } = &mut s.screen {
                    *text = l
                }
            }),
            Screen::Log(_) => self.fetch("log.tail", json!({"lines": 300}), |s, l| {
                if let Screen::Log(log) = &mut s.screen {
                    *log = l
                }
            }),
            Screen::Main | Screen::File { .. } => {}
        }
    }

    /// Fetches the next queued thumbnail, one request at a time so a
    /// directory of photos does not flood the backend; each reply starts
    /// the next. A screen change clears the queue.
    fn pump_thumbs(self: &Rc<Self>) {
        if !self.client.connected() {
            return;
        }
        let next = {
            let mut s = self.state.borrow_mut();
            if s.thumb_busy {
                return;
            }
            let next = s.thumb_queue.pop_front();
            s.thumb_busy = next.is_some();
            next
        };
        let Some(key) = next else { return };
        let scale = self.ui.upgrade().map_or(1.0, |u| u.window().scale_factor());
        // The row's 52 px picture, in physical pixels.
        let size = (52.0 * scale).round() as u32;
        self.call("fs.thumb", json!({"dev": key.0, "path": key.1, "size": size}), move |a, r: Result<Thumb, _>| {
            let mut s = a.state.borrow_mut();
            s.thumbs.insert(key, r.ok().and_then(|t| thumb_image(&t)));
            s.thumb_busy = false;
        });
    }

    /// Scale, corner padding and the cutouts' span, from display.info. The
    /// panel file is in the panel's pixels; the window may be the
    /// framebuffer's, and the UI's lengths are logical.
    fn apply_panel(&self, p: Option<Panel>) {
        let Some(ui) = self.ui.upgrade() else { return };
        let window = ui.window();
        let (size, fb_mm) = match (&self.mode, &p) {
            (Mode::Headless, Some(p)) => (PhysicalSize::new(p.xres, p.yres), 0),
            (Mode::Panel { fb_width_mm }, _) => (window.size(), *fb_width_mm),
            _ => (window.size(), 0),
        };
        let width = size.width;
        let mm = p.as_ref().map_or(0, |p| p.width_mm);
        let scale = scale_for(width, if mm > 0 { mm } else { fb_mm });
        window.dispatch_event(WindowEvent::ScaleFactorChanged { scale_factor: scale });
        // The window keeps its logical size across a scale change.
        window.set_size(size);
        let Some(p) = p else { return };
        let k = width as f32 / p.xres as f32 / scale;
        ui.set_corner_radius(p.corner_radii[0].max(p.corner_radii[1]) as f32 * k);
        // The cutouts near the top edge share the header's first line, which
        // grows to clear the lowest of them.
        let top: Vec<_> = p.cutouts.iter().filter(|c| c.y < p.yres as i32 / 10).collect();
        if let (Some(x0), Some(x1), Some(y1)) = (
            top.iter().map(|c| c.x).min(),
            top.iter().map(|c| c.x + c.w).max(),
            top.iter().map(|c| c.y + c.h).max(),
        ) {
            ui.set_cutout_x(x0 as f32 * k);
            ui.set_cutout_w((x1 - x0) as f32 * k);
            ui.set_cutout_h(y1 as f32 * k);
        }
    }

    // --- input ---

    fn activity(self: &Rc<Self>) {
        if self.state.borrow().countdown >= 0 {
            self.state.borrow_mut().countdown = -1;
            self.call("autoboot.cancel", json!({}), App::report);
        }
    }

    fn show(self: &Rc<Self>, screen: Screen, selected: i32) {
        {
            let mut s = self.state.borrow_mut();
            s.screen = screen;
            s.generation += 1;
            s.thumb_queue.clear();
        }
        self.render();
        if let Some(ui) = self.ui.upgrade() {
            ui.set_selected(selected);
            ui.invoke_scroll_to_selected();
        }
        self.load_screen();
    }

    /// Opens a screen with the row after Back selected, so a stray select
    /// does not leave it.
    fn open(self: &Rc<Self>, screen: Screen) {
        let selected = self.ui.upgrade().map_or(0, |u| u.get_selected());
        let mut s = self.state.borrow_mut();
        let prev = std::mem::replace(&mut s.screen, Screen::Main);
        s.stack.push((prev, selected));
        drop(s);
        self.show(screen, 1);
    }

    /// Back a screen; from the menu's first, back to the carousel.
    fn back(self: &Rc<Self>) {
        let popped = self.state.borrow_mut().stack.pop();
        match popped {
            Some((screen, selected)) => self.show(screen, selected),
            None => self.close_menu(),
        }
    }

    fn open_menu(self: &Rc<Self>) {
        {
            let mut s = self.state.borrow_mut();
            if s.launching >= 0 {
                return;
            }
            s.menu_open = true;
            s.stack.clear();
        }
        self.show(Screen::Main, 1);
        if let Some(ui) = self.ui.upgrade() {
            ui.invoke_focus_menu();
        }
    }

    fn close_menu(self: &Rc<Self>) {
        self.state.borrow_mut().menu_open = false;
        self.render();
        if let Some(ui) = self.ui.upgrade() {
            ui.invoke_focus_home();
        }
    }

    /// A card chosen on the carousel: boot its entry, or open the menu.
    fn activate_card(self: &Rc<Self>, i: usize) {
        let entries = self.state.borrow().entries.as_ref().map_or(0, |e| e.len());
        if i < entries {
            self.launch(i);
        } else {
            self.open_menu();
        }
    }

    /// Boots entry i, its card taking the screen until the handover; a
    /// failure puts it back.
    fn launch(self: &Rc<Self>, i: usize) {
        {
            let mut s = self.state.borrow_mut();
            if s.launching >= 0 {
                return;
            }
            (s.launching, s.menu_open, s.countdown) = (i as i32, false, -1);
        }
        if let Some(ui) = self.ui.upgrade() {
            ui.set_card_selected(i as i32);
            ui.invoke_focus_home();
        }
        self.render();
        self.call("boot.start", json!({"index": i}), |a, r: Result<Value, _>| {
            if let Err(e) = r {
                let mut s = a.state.borrow_mut();
                (s.launching, s.error) = (-1, e);
            }
        });
    }

    fn activate(self: &Rc<Self>, i: usize) {
        let action = self.state.borrow().actions.get(i).cloned().unwrap_or(Action::None);
        match action {
            Action::Open(s) => self.open(s),
            Action::Back => self.back(),
            Action::Refresh => self.load_screen(),
            Action::Boot(i) => self.launch(i),
            Action::Service(name, on) => self.call("service.set", json!({"name": name, "running": on}), |a, r| match r {
                Ok(v) => a.state.borrow_mut().services = v,
                Err(e) => a.state.borrow_mut().error = e,
            }),
            Action::Reboot(mode) => self.call("power", json!({"action": "reboot", "mode": mode}), App::report),
            Action::PowerOff => self.call("power", json!({"action": "poweroff"}), App::report),
            Action::Url(path, mode) => {
                if let Some(open) = &*self.open_url.borrow() {
                    open(&self.state.borrow().files_port, &path, mode);
                }
            }
            Action::None => {}
        }
    }

    // --- drawing ---

    pub fn render(self: &Rc<Self>) {
        let Some(ui) = self.ui.upgrade() else { return };
        let connected = self.client.connected();
        let mut s = self.state.borrow_mut();
        let cards = cards::build(s.entries.as_deref().unwrap_or_default());
        if cards != s.cards {
            ui.set_cards(ModelRc::new(VecModel::from(cards.clone())));
            s.cards = cards;
        }
        ui.set_menu_open(s.menu_open);
        ui.set_launching(s.launching);
        ui.set_remaining(if connected { s.countdown as i32 } else { -1 });
        ui.set_total(s.countdown_total.max(1) as i32);
        ui.set_toast(if !connected { "Waiting for the backend…".into() } else { s.error.clone().into() });

        let (heading, sub, items) = if connected { screens::build(&s) } else { ("Boot Manager".into(), String::new(), vec![]) };
        let (rows, actions): (Vec<Row>, Vec<Action>) = items.into_iter().unzip();
        s.actions = actions;

        let status = if !connected {
            "Waiting for the backend…".to_string()
        } else if s.countdown >= 0 {
            format!("Booting {} in {} s. Press any key to stay.", s.countdown_label, s.countdown)
        } else {
            s.error.clone()
        };
        let n = rows.len() as i32;
        ui.set_compact(matches!(s.screen, Screen::Log(_) | Screen::Text { .. }));
        ui.set_heading(heading.into());
        ui.set_subheading(sub.into());
        ui.set_status(status.into());
        ui.set_rows(ModelRc::new(VecModel::from(rows)));
        if ui.get_selected() >= n {
            ui.set_selected((n - 1).max(0));
        }
        let more_thumbs = !s.thumb_busy && !s.thumb_queue.is_empty();
        let route = if s.menu_open { route_of(&s.screen) } else { "/".to_string() };
        drop(s);
        if *self.last_route.borrow() != route {
            *self.last_route.borrow_mut() = route.clone();
            if let Some(f) = &*self.on_route.borrow() {
                f(&route);
            }
        }
        if more_thumbs {
            self.pump_thumbs();
        }
    }

    // --- the web build's hooks ---

    pub fn set_url_opener(&self, open: UrlOpener) {
        *self.open_url.borrow_mut() = Some(open);
    }

    /// Called with the new route whenever the screen changes.
    pub fn set_route_listener(&self, f: Box<dyn Fn(&str)>) {
        *self.on_route.borrow_mut() = Some(f);
    }

    /// Goes to the screen a route names, with the Back stack its path
    /// implies, and loads it.
    pub fn navigate_to(self: &Rc<Self>, route: &str) {
        let (stack, screen) = screens_of(route);
        // Anything that is not a menu screen is the carousel.
        let home = matches!(screen, Screen::Main) && route != "/menu";
        self.state.borrow_mut().menu_open = !home;
        let selected = if matches!(screen, Screen::Main) { 0 } else { 1 };
        {
            let mut s = self.state.borrow_mut();
            // Back from each screen lands on the row it was opened from:
            // not known here, so the row after Back (the main screen's first).
            s.stack = stack.into_iter().enumerate().map(|(i, sc)| (sc, if i == 0 { 0 } else { 1 })).collect();
        }
        // The listener must not echo this back as a new history entry.
        *self.last_route.borrow_mut() = if home { "/".into() } else { route_of(&screen) };
        self.show(screen, selected);
        if let Some(ui) = self.ui.upgrade() {
            if home {
                ui.invoke_focus_home();
            } else {
                ui.invoke_focus_menu();
            }
        }
    }
}

/// fs.thumb's reply as an image.
fn thumb_image(t: &Thumb) -> Option<Image> {
    use base64::Engine;
    let px = base64::engine::general_purpose::STANDARD.decode(&t.rgba).ok()?;
    if t.width == 0 || t.height == 0 || px.len() != (t.width * t.height * 4) as usize {
        return None;
    }
    Some(Image::from_rgba8(SharedPixelBuffer::<Rgba8Pixel>::clone_from_slice(&px, t.width, t.height)))
}
