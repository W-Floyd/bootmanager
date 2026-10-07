//! The UI's state and its conversation with the backend. screens.rs
//! decides what each screen shows; the .slint side only draws.

use crate::proto::{self, Client, DisplayInfo, Event, Hello, Panel, Service, Thumb};
use crate::routes::{route_of, screens_of};
use crate::screens::{self, is_image, join, Action, FsckRun, Load, Screen, UnlockRun, UrlMode};
use crate::{cards, AppWindow, Card, Row, VirtualKeyboardHandler};
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
    /// Encrypted volumes not open, one tile each after the entries.
    pub locked: Vec<proto::LockedVolume>,
    /// The unlock screen was opened from a tile, not from Disks: when it
    /// succeeds the menu closes, on the carousel and the entries it found.
    unlock_from_tile: bool,
    /// The locked volume the autoboot is waiting to have unlocked, and
    /// whether its passphrase screen has been put up for it already (once:
    /// leaving the screen is the way out to the menu).
    unlock_wanted: Option<proto::LockedVolume>,
    unlock_prompted: bool,
    /// The default entry, and whether the user chose it.
    pub default: usize,
    pub explicit: bool,
    /// The carousel was put on the default, as it is once, when the
    /// entries first arrive.
    centred: bool,
    pub services: Vec<Service>,
    /// The check or repair on the Fsck screen.
    pub fsck: FsckRun,
    /// autoboot.settings, read when the menu or its Autoboot screen opens.
    pub autoboot: Option<proto::AutobootSettings>,
    /// boot.options, read when the menu or its Boot options screen opens.
    pub options: Option<Vec<proto::BootOption>>,
    /// The battery's level (the "battery" event), for the indicator.
    pub battery: Option<proto::Battery>,
    /// A fastboot flash under way (the "flash" event), for its screen, and
    /// the estimate of how far along it is.
    pub flash: proto::Flash,
    pub flash_model: crate::flashmodel::FlashModel,
    /// Every power supply's properties, read when Diagnostics opens.
    pub supplies: Vec<proto::Supply>,
    /// The stage has the checkers (an optional part of a stage build).
    pub fsck_available: bool,
    /// The stage can unlock LUKS partitions (cryptsetup is optional too).
    pub luks_available: bool,
    pub unlock: UnlockRun,
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
    /// Re-reads the power supplies while Diagnostics is on show.
    power_timer: slint::Timer,
    /// Dev stage only: reads the frame rate for the on-screen counter.
    fps_timer: slint::Timer,
    /// Whether the flash screen was up at the last draw, to move the focus
    /// onto it, and back, when that changes.
    flash_shown: std::cell::Cell<bool>,
}

impl State {
    /// A flash event: the screen's, and the estimate's.
    fn take_flash(&mut self, f: proto::Flash) {
        self.flash_model.update(&f);
        self.flash = f;
    }

    fn take_entries(&mut self, e: proto::Entries) {
        (self.default, self.explicit) = (e.default.min(e.entries.len().saturating_sub(1)), e.explicit);
        self.entries = Some(e.entries);
        self.locked = e.locked;
        self.unlock_wanted = e.unlock;
        if self.unlock_wanted.is_none() {
            self.unlock_prompted = false;
        }
    }
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
                locked: Vec::new(),
                unlock_from_tile: false,
                unlock_wanted: None,
                unlock_prompted: false,
                default: 0,
                explicit: false,
                centred: false,
                services: vec![],
                fsck: FsckRun::Idle,
                autoboot: None,
                options: None,
                battery: None,
                flash: proto::Flash::default(),
                flash_model: Default::default(),
                supplies: vec![],
                fsck_available: false,
                luks_available: false,
                unlock: UnlockRun::Idle,
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
            power_timer: slint::Timer::default(),
            fps_timer: slint::Timer::default(),
            flash_shown: std::cell::Cell::new(false),
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
        ui.on_passphrase_accepted(on(|a| a.unlock()));
        // The on-screen keyboard presses a key of the window, as the
        // examples/virtual_keyboard of Slint does; the focused text field
        // takes it like a real key.
        let weak_keys = ui.as_weak();
        ui.global::<VirtualKeyboardHandler>().on_key_pressed(move |key| {
            if let Some(ui) = weak_keys.upgrade() {
                ui.window().dispatch_event(WindowEvent::KeyPressed { text: key.clone() });
                ui.window().dispatch_event(WindowEvent::KeyReleased { text: key });
            }
        });
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
        let weak = Rc::downgrade(&app);
        ui.on_card_move(move |i, by| {
            if let Some(a) = weak.upgrade() {
                a.move_card(i as usize, by)
            }
        });
        let weak = Rc::downgrade(&app);
        ui.on_card_default(move |i| {
            if let Some(a) = weak.upgrade() {
                a.default_card(i as usize)
            }
        });
        let weak = Rc::downgrade(&app);
        ui.on_flash_stop(move || {
            if let Some(a) = weak.upgrade() {
                a.stop_flash()
            }
        });
        let weak = Rc::downgrade(&app);
        ui.on_flash_close(move || {
            if let Some(a) = weak.upgrade() {
                a.state.borrow_mut().take_flash(proto::Flash::default());
                a.render();
            }
        });
        APP.with(|a| *a.borrow_mut() = Some(app.clone()));
        let weak = Rc::downgrade(&app);
        app.power_timer.start(slint::TimerMode::Repeated, std::time::Duration::from_secs(2), move || {
            if let Some(a) = weak.upgrade() {
                a.refresh_power()
            }
        });
        if crate::fps::enabled() {
            // Dev stage: the frame rate, bottom left. It reads 0 once the UI
            // has been idle for a second and a half (no damage, no frames).
            let weak = ui.as_weak();
            let (mut last_tick, mut idle) = (0u32, 0u32);
            let mut shown = String::new();
            app.fps_timer.start(slint::TimerMode::Repeated, std::time::Duration::from_millis(500), move || {
                let Some(ui) = weak.upgrade() else { return };
                let (x10, tick) = crate::fps::read();
                idle = if tick == last_tick { idle + 1 } else { 0 };
                last_tick = tick;
                let text = if idle >= 3 { "0 fps".to_string() } else { format!("{}.{} fps", x10 / 10, x10 % 10) };
                // Setting it draws a frame; only when it changed, or an idle UI would never rest.
                if text != shown {
                    ui.set_fps_text(text.clone().into());
                    shown = text;
                }
            });
        }
        app.render();
        // Up from black once there is a first frame to fade to.
        let weak = ui.as_weak();
        slint::Timer::single_shot(std::time::Duration::from_millis(50), move || {
            if let Some(ui) = weak.upgrade() {
                ui.set_started(true);
            }
        });
        // The splash waits for the boot entries, which a -wait-ready backend
        // holds until the stage is set up (20 s at most), but not for ever:
        // without a backend, the carousel says it is waiting for one.
        let weak = Rc::downgrade(&app);
        slint::Timer::single_shot(std::time::Duration::from_secs(25), move || {
            if let Some(a) = weak.upgrade() {
                a.reveal()
            }
        });
        app
    }

    /// The Battery screen follows the battery and charger live: read again
    /// every couple of seconds while it is up, which is when a plug or
    /// unplug, or the voltage and current moving, shows.
    fn refresh_power(self: &Rc<Self>) {
        if !self.client.connected() || !matches!(self.state.borrow().screen, Screen::Battery) {
            return;
        }
        self.refresh_power_now();
    }

    fn refresh_power_now(self: &Rc<Self>) {
        self.call("power.supplies", json!({}), |a, r: Result<Vec<proto::Supply>, _>| {
            if let Ok(v) = r {
                a.state.borrow_mut().supplies = v;
            }
        });
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
                a.state.borrow_mut().fsck_available = h.fsck;
                a.state.borrow_mut().luks_available = h.luks;
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
                crate::inputlog::set_countdown(a.remaining >= 0);
                (s.countdown, s.countdown_label) = (a.remaining, a.label);
                s.countdown_total = s.countdown_total.max(a.remaining);
            }
            Event::Entries(e) if !e.scanning => {
                s.take_entries(e);
                drop(s);
                self.prompt_unlock();
                self.entries_arrived();
                s = self.state.borrow_mut();
            }
            Event::Entries(_) => {}
            Event::Services(v) => {
                // A service that has just died: say so, once, as a toast.
                for n in v.iter().filter(|n| !n.error.is_empty()) {
                    if !s.services.iter().any(|o| o.name == n.name && o.error == n.error) {
                        s.error = format!("{} stopped: {}", n.title, n.error);
                    }
                }
                s.services = v;
            }
            Event::Error(e) => s.error = e,
            Event::Battery(b) => s.battery = Some(b),
            Event::Flash(f) => s.take_flash(f),
            Event::Fsck(f) => {
                if matches!(&s.screen, Screen::Fsck { dev, .. } if *dev == f.dev) && matches!(s.fsck, FsckRun::Running(_)) {
                    s.fsck = FsckRun::Running(f.line);
                }
            }
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
                a.state.borrow_mut().take_entries(e);
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
            Screen::Fsck { dev, .. } => self.fetch("disk.list", json!({}), move |s, l: Load<Vec<proto::Disk>>| {
                if let Screen::Fsck { disk, .. } = &mut s.screen {
                    *disk = match l {
                        Load::Ready(v) => v.into_iter().find(|d| d.dev == dev).map_or_else(|| Load::Failed(format!("{dev} has no file system")), Load::Ready),
                        Load::Failed(e) => Load::Failed(e),
                        Load::Waiting => Load::Waiting,
                    };
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
            Screen::Image { dev, path, .. } => {
                let Some(ui) = self.ui.upgrade() else { return };
                let size = ui.window().size();
                let args = json!({"dev": dev, "path": path, "width": size.width, "height": size.height});
                self.fetch("fs.thumb", args, |s, l: Load<Thumb>| {
                    if let Screen::Image { picture, .. } = &mut s.screen {
                        *picture = match l {
                            Load::Ready(t) => thumb_image(&t).map_or(Load::Failed("Not an image".into()), Load::Ready),
                            Load::Failed(e) => Load::Failed(e),
                            Load::Waiting => Load::Waiting,
                        };
                    }
                })
            }
            Screen::Log(_) => self.fetch("log.tail", json!({"lines": 300}), |s, l| {
                if let Screen::Log(log) = &mut s.screen {
                    *log = l
                }
            }),
            Screen::Battery => self.refresh_power_now(),
            Screen::BootInfo(_) => self.fetch("slots.info", json!({}), |s, l| {
                if let Screen::BootInfo(d) = &mut s.screen {
                    *d = l;
                }
            }),
            Screen::Main => {
                self.call("autoboot.settings", json!({}), |a, r: Result<proto::AutobootSettings, _>| {
                    if let Ok(v) = r {
                        a.state.borrow_mut().autoboot = Some(v);
                    }
                });
                self.call("boot.options", json!({}), |a, r: Result<Vec<proto::BootOption>, _>| {
                    if let Ok(v) = r {
                        a.state.borrow_mut().options = Some(v);
                    }
                });
            }
            Screen::Autoboot => self.call("autoboot.settings", json!({}), |a, r: Result<proto::AutobootSettings, _>| {
                if let Ok(v) = r {
                    a.state.borrow_mut().autoboot = Some(v);
                }
            }),
            Screen::BootOptions => self.call("boot.options", json!({}), |a, r: Result<Vec<proto::BootOption>, _>| {
                if let Ok(v) = r {
                    a.state.borrow_mut().options = Some(v);
                }
            }),
            // The field gets the command line the entry would boot with.
            Screen::EditCmdline { index, .. } => {
                let gen = self.state.borrow().generation;
                self.call("boot.cmdline", json!({"index": index}), move |a, r: Result<String, _>| match r {
                    Ok(text) => {
                        if a.state.borrow().generation == gen {
                            if let Some(ui) = a.ui.upgrade() {
                                ui.set_passphrase(text.into());
                            }
                        }
                    }
                    Err(e) => a.state.borrow_mut().error = e,
                });
            }
            Screen::File { .. } | Screen::Unlock { .. } | Screen::Diagnostics | Screen::About => {}
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
        // The header starts below the lowest of the cutouts near the top edge.
        let top = p.cutouts.iter().filter(|c| c.y < p.yres as i32 / 10).map(|c| c.y + c.h).max();
        if let Some(y1) = top {
            ui.set_cutout_h(y1 as f32 * k);
        }
    }

    // --- input ---

    fn activity(self: &Rc<Self>) {
        if self.state.borrow().countdown >= 0 {
            self.state.borrow_mut().countdown = -1;
            // The backend logs why: the input event the UI saw last, and how
            // long before this it was. A cancel with no event near it was
            // not a person.
            let (reason, age_ms) = crate::inputlog::last_event()
                .unwrap_or_else(|| ("no input event seen by this UI (web UI, or not from a device)".to_string(), 0));
            eprintln!("bootmanager-ui: t={:.3} activity cancels the autoboot: last input {reason}, {age_ms} ms ago", crate::inputlog::t());
            self.call("autoboot.cancel", json!({ "reason": reason, "age_ms": age_ms }), App::report);
        }
    }

    fn show(self: &Rc<Self>, screen: Screen, selected: i32) {
        {
            let mut s = self.state.borrow_mut();
            // A passphrase, or an edited command line, lives only while its
            // screen is up.
            if !matches!(screen, Screen::Unlock { .. } | Screen::EditCmdline { .. }) {
                s.unlock = UnlockRun::Idle;
                if let Some(ui) = self.ui.upgrade() {
                    ui.set_passphrase("".into());
                }
            }
            s.screen = screen;
            s.generation += 1;
            s.thumb_queue.clear();
        }
        self.render();
        if let Some(ui) = self.ui.upgrade() {
            ui.set_selected(selected);
            ui.invoke_scroll_to_selected();
            let s = self.state.borrow();
            if matches!(s.screen, Screen::Image { .. }) {
                ui.invoke_focus_viewer();
            } else if s.menu_open {
                ui.invoke_focus_menu();
            }
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
        if matches!(screen, Screen::Fsck { .. }) && !matches!(s.fsck, FsckRun::Running(_)) {
            s.fsck = FsckRun::Idle;
        }
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
            if crate::fps::enabled() {
                eprintln!("bootmanager-ui: t={:.3} open_menu (launching {}, editing)", crate::inputlog::t(), s.launching);
            }
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

    /// The flash screen's emergency stop: fastboot off, which the backend
    /// answers by aborting the flash and clearing the screen. Cleared here
    /// as well, in case the answer never comes.
    fn stop_flash(self: &Rc<Self>) {
        self.call("service.set", json!({"name": "fastboot", "running": false}), |a, r| {
            let mut s = a.state.borrow_mut();
            match r {
                Ok(v) => s.services = v,
                Err(e) => s.error = e,
            }
            s.take_flash(proto::Flash::default());
        });
    }

    fn close_menu(self: &Rc<Self>) {
        if crate::fps::enabled() {
            eprintln!("bootmanager-ui: t={:.3} close_menu", crate::inputlog::t());
        }
        let waiting = {
            let mut s = self.state.borrow_mut();
            s.menu_open = false;
            s.unlock_from_tile = false;
            s.unlock_wanted.is_some()
        };
        if waiting {
            // Back out of the passphrase screen the autoboot put up: no
            // unlock, so no boot after it.
            self.call("autoboot.cancel", json!({ "reason": "the passphrase screen was closed" }), App::report);
        }
        self.render();
        // Not from inside the key press that closes the menu: the focus it
        // moves is the one that press was delivered to, and the move did
        // not hold (the carousel took no key afterwards).
        let weak = self.ui.clone();
        slint::Timer::single_shot(std::time::Duration::from_millis(0), move || {
            if let Some(ui) = weak.upgrade() {
                ui.invoke_focus_home();
            }
        });
    }

    /// Moves entry i's card by places (-1 left), the carousel and the edit
    /// bar going with it.
    fn move_card(self: &Rc<Self>, i: usize, by: i32) {
        self.call("boot.move", json!({"index": i, "by": by}), move |a, r: Result<proto::Entries, _>| match r {
            Ok(e) => {
                a.state.borrow_mut().take_entries(e);
                if let Some(ui) = a.ui.upgrade() {
                    let j = i as i32 + by;
                    ui.set_card_editing(j);
                    ui.set_card_selected(j);
                }
            }
            Err(e) => a.state.borrow_mut().error = e,
        });
    }

    /// Makes entry i the default, or, when it already is by choice, goes
    /// back to the first being the default.
    fn default_card(self: &Rc<Self>, i: usize) {
        let s = self.state.borrow();
        let index = if s.explicit && s.default == i { -1 } else { i as i64 };
        drop(s);
        self.call("boot.default", json!({"index": index}), |a, r: Result<proto::Entries, _>| match r {
            Ok(e) => a.state.borrow_mut().take_entries(e),
            Err(e) => a.state.borrow_mut().error = e,
        });
    }

    /// A card chosen on the carousel: boot its entry, ask for an encrypted
    /// volume's passphrase, or open the menu.
    fn activate_card(self: &Rc<Self>, i: usize) {
        let (entries, volume) = {
            let s = self.state.borrow();
            let n = s.entries.as_ref().map_or(0, |e| e.len());
            (n, i.checked_sub(n).and_then(|k| s.locked.get(k)).cloned())
        };
        if i < entries {
            self.launch(i);
        } else if let Some(v) = volume {
            self.open_unlock_tile(v);
        } else {
            self.open_menu();
        }
    }

    /// The autoboot's default is inside a locked volume: its passphrase
    /// screen, without being asked. Once; the way out of it is Back, which
    /// calls the autoboot off (close_menu).
    fn prompt_unlock(self: &Rc<Self>) {
        let v = {
            let mut s = self.state.borrow_mut();
            match s.unlock_wanted.clone() {
                Some(v) if !s.unlock_prompted && s.launching < 0 => {
                    s.unlock_prompted = true;
                    v
                }
                _ => return,
            }
        };
        self.open_unlock_tile(v);
    }

    /// The passphrase screen for a locked volume's tile. It is the menu's
    /// Unlock screen with nothing under it: Back and a success both close the
    /// menu, to the carousel.
    fn open_unlock_tile(self: &Rc<Self>, v: proto::LockedVolume) {
        {
            let mut s = self.state.borrow_mut();
            if s.launching >= 0 {
                return;
            }
            s.menu_open = true;
            s.stack.clear();
            s.unlock = UnlockRun::Idle;
            s.unlock_from_tile = true;
        }
        let name = if v.source.is_empty() { v.name.clone() } else { v.source.clone() };
        self.show(Screen::Unlock { dev: v.dev, name }, 0);
    }

    /// Boots entry i, its card taking the screen until the handover; a
    /// failure puts it back.
    fn launch(self: &Rc<Self>, i: usize) {
        self.launch_with(i, None);
    }

    /// launch, with a command line for this boot only (the editor's), or
    /// the entry's own (None).
    fn launch_with(self: &Rc<Self>, i: usize, cmdline: Option<String>) {
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
        self.call("boot.start", json!({"index": i, "cmdline": cmdline.unwrap_or_default()}), |a, r: Result<Value, _>| {
            if let Err(e) = r {
                let mut s = a.state.borrow_mut();
                (s.launching, s.error) = (-1, e);
            }
        });
    }

    /// Sends the typed passphrase. It is cleared at once, kept nowhere but
    /// in the request; the screen goes back to the partition list on
    /// success, where the volume now is a partition, and stays on a wrong
    /// passphrase.
    fn unlock(self: &Rc<Self>) {
        let Some(ui) = self.ui.upgrade() else { return };
        let (dev, pass) = {
            let mut s = self.state.borrow_mut();
            let Screen::Unlock { dev, .. } = &s.screen else { return };
            let dev = dev.clone();
            let pass = ui.get_passphrase();
            if pass.is_empty() || matches!(s.unlock, UnlockRun::Working) {
                return;
            }
            s.unlock = UnlockRun::Working;
            ui.set_working(true);
            ui.set_passphrase("".into());
            (dev, pass.to_string())
        };
        self.call("disk.unlock", json!({"dev": dev, "passphrase": pass}), |a, r: Result<proto::Unlocked, _>| {
            if let Some(ui) = a.ui.upgrade() {
                ui.set_working(false);
            }
            match r {
            Ok(_) => {
                let from_tile = {
                    let mut s = a.state.borrow_mut();
                    s.unlock = UnlockRun::Idle;
                    // The backend took the autoboot's wait with this unlock
                    // and boots the default itself.
                    s.unlock_wanted = None;
                    std::mem::take(&mut s.unlock_from_tile)
                };
                if from_tile {
                    a.close_menu();
                } else {
                    a.back();
                }
            }
            Err(e) => a.state.borrow_mut().unlock = UnlockRun::Failed(e),
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
            Action::Slot(slot, what) => self.call("slots.set", json!({"slot": slot, "action": what}), |a, r| {
                let mut s = a.state.borrow_mut();
                match r {
                    Ok(info) => {
                        if let Screen::BootInfo(d) = &mut s.screen {
                            *d = Load::Ready(info);
                        }
                    }
                    Err(e) => s.error = e,
                }
            }),
            Action::Url(path, mode) => {
                if let Some(open) = &*self.open_url.borrow() {
                    open(&self.state.borrow().files_port, &path, mode);
                }
            }
            Action::AutobootSet(enabled, seconds) => {
                self.call("autoboot.set", json!({"enabled": enabled, "seconds": seconds}), |a, r: Result<proto::AutobootSettings, _>| match r {
                    Ok(v) => a.state.borrow_mut().autoboot = Some(v),
                    Err(e) => a.state.borrow_mut().error = e,
                })
            }
            Action::BootEdited(i) => {
                let text = self.ui.upgrade().map(|ui| ui.get_passphrase().to_string()).unwrap_or_default();
                // One line: the on-screen keyboard's Return makes a new one.
                let line = text.split_whitespace().collect::<Vec<_>>().join(" ");
                if line.is_empty() {
                    self.state.borrow_mut().error = "The command line is empty".into();
                } else {
                    self.launch_with(i, Some(line));
                }
            }
            Action::OptionSet(id, state) => {
                self.call("boot.option.set", json!({"id": id, "state": state}), |a, r: Result<Vec<proto::BootOption>, _>| match r {
                    Ok(v) => a.state.borrow_mut().options = Some(v),
                    Err(e) => a.state.borrow_mut().error = e,
                })
            }
            Action::Unlock => self.unlock(),
            Action::Lock(dev) => self.call("disk.lock", json!({"dev": dev}), |a, r: Result<Value, _>| match r {
                // The volume is gone: back to the partition list.
                Ok(_) => a.back(),
                Err(e) => a.state.borrow_mut().error = e,
            }),
            Action::FsckConfirm => {
                if let Screen::Fsck { confirm, .. } = &mut self.state.borrow_mut().screen {
                    *confirm = true;
                }
            }
            Action::Fsck(dev, repair) => {
                {
                    let mut s = self.state.borrow_mut();
                    if matches!(s.fsck, FsckRun::Running(_)) {
                        return;
                    }
                    s.fsck = FsckRun::Running(String::new());
                    if let Screen::Fsck { confirm, .. } = &mut s.screen {
                        *confirm = false;
                    }
                }
                self.call("disk.fsck", json!({"dev": dev, "repair": repair}), |a, r: Result<proto::FsckResult, _>| {
                    a.state.borrow_mut().fsck = match r {
                        Ok(res) => FsckRun::Done(res),
                        Err(e) => FsckRun::Failed(e),
                    };
                });
            }
            Action::None => {}
        }
    }

    // --- drawing ---

    pub fn render(self: &Rc<Self>) {
        let Some(ui) = self.ui.upgrade() else { return };
        let connected = self.client.connected();
        let mut s = self.state.borrow_mut();
        let cards = cards::build(s.entries.as_deref().unwrap_or_default(), &s.locked, s.default);
        ui.set_boot_entries(s.entries.as_ref().map_or(0, |e| e.len()) as i32);
        if cards != s.cards {
            ui.set_cards(ModelRc::new(VecModel::from(cards.clone())));
            s.cards = cards;
        }
        ui.set_default_index(s.default as i32);
        ui.set_default_explicit(s.explicit);
        if !s.centred && s.entries.is_some() {
            s.centred = true;
            ui.set_card_selected(s.default as i32);
        }
        ui.set_menu_open(s.menu_open);
        let editing = matches!(s.screen, Screen::EditCmdline { .. });
        ui.set_text_entry(editing || matches!(s.screen, Screen::Unlock { .. }));
        ui.set_text_secret(!editing);
        ui.set_text_lines(if editing { 6 } else { 1 });
        ui.set_text_hint(if editing { "Kernel command line" } else { "Passphrase" }.into());
        match &s.battery {
            Some(b) if b.present => {
                ui.set_battery_present(true);
                ui.set_battery_percent(b.percent);
                ui.set_battery_charging(b.charging);
            }
            _ => ui.set_battery_present(false),
        }
        set_flash(&ui, &s.flash, &s.flash_model);
        let flash_up = !s.flash.phase.is_empty();
        if flash_up != self.flash_shown.replace(flash_up) {
            // The focus moves from the next turn of the loop, not from here:
            // a move made inside the event that caused it does not hold
            // (see close_menu).
            let (weak, menu) = (self.ui.clone(), s.menu_open);
            slint::Timer::single_shot(std::time::Duration::from_millis(0), move || {
                if let Some(ui) = weak.upgrade() {
                    match (flash_up, menu) {
                        (true, _) => ui.invoke_focus_flash(),
                        (false, true) => ui.invoke_focus_menu(),
                        (false, false) => ui.invoke_focus_home(),
                    }
                }
            });
        }
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
        let (viewing, picture, note) = match &s.screen {
            Screen::Image { picture: Load::Ready(img), .. } => (true, img.clone(), String::new()),
            Screen::Image { picture: Load::Failed(e), .. } => (true, Image::default(), e.clone()),
            Screen::Image { .. } => (true, Image::default(), "Loading…".into()),
            _ => (false, Image::default(), String::new()),
        };
        ui.set_viewing(viewing);
        ui.set_picture(picture);
        ui.set_picture_note(note.into());
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

// --- the flash screen ---

/// What the flash screen shows for a flash event: the whole image, and the
/// piece being received and written.
#[derive(Debug, PartialEq)]
struct FlashView {
    active: bool,
    finished: bool,
    title: String,
    overall: f32,
    percent: String,
    eta: String,
    overall_text: String,
    recv: f32,
    recv_text: String,
    write: f32,
    write_text: String,
}

fn size_text(bytes: i64) -> String {
    const MIB: f64 = 1024. * 1024.;
    let b = bytes.max(0) as f64;
    if b >= 1024. * MIB {
        format!("{:.2} GB", b / (1024. * MIB))
    } else {
        format!("{:.0} MB", b / MIB)
    }
}

fn fraction(part: i64, whole: i64) -> f32 {
    if whole <= 0 {
        0.
    } else {
        (part as f32 / whole as f32).clamp(0., 1.)
    }
}

/// "about 1 min 5 s left", "about 40 s left"; "" when it is not known or the
/// flash is over.
fn eta_text(ms: Option<i64>) -> String {
    match ms {
        Some(ms) if ms >= 3_000 => {
            let s = (ms + 500) / 1000;
            if s >= 60 {
                format!("about {} min {} s left", s / 60, s % 60 / 5 * 5)
            } else {
                format!("about {} s left", (s + 2) / 5 * 5)
            }
        }
        _ => String::new(),
    }
}

/// The screen for an event, and the model's estimate of how far along it is.
fn flash_view(f: &proto::Flash, overall: f32, eta_ms: Option<i64>) -> FlashView {
    let finished = f.phase == "finished";
    let name = if f.name.is_empty() { "the device" } else { &f.name };
    let piece = f.piece.max(1);
    let overall = if finished { 1. } else { overall.clamp(0., 1.) };
    let written = (overall as f64 * f.total.max(0) as f64) as i64;
    let overall_text = if f.total > 0 {
        format!("{} of {}", size_text(written), size_text(f.total))
    } else {
        String::new()
    };
    let percent = format!("{:.0}%", overall * 100.);
    let eta = if finished { "Done".to_string() } else { eta_text(eta_ms) };
    let (recv, recv_text) = match f.phase.as_str() {
        "receiving" => (fraction(f.recv, f.recv_total), format!("Receiving piece {piece}: {} of {}", size_text(f.recv), size_text(f.recv_total))),
        "writing" | "waiting" | "done" | "finished" => (1., format!("Piece {piece} received")),
        _ => (0., String::new()),
    };
    let (write, write_text) = match f.phase.as_str() {
        "receiving" => (0., format!("Piece {piece} waits to be written")),
        "writing" => (fraction(f.wrote, f.wrote_total), format!("Writing piece {piece}: {} of {}", size_text(f.wrote), size_text(f.wrote_total))),
        "waiting" | "done" | "finished" => (1., format!("Piece {piece} written")),
        _ => (0., String::new()),
    };
    FlashView {
        active: !f.phase.is_empty(),
        finished,
        // What it is, under the screen's own "FLASHING" or "FLASH COMPLETE".
        title: name.to_string(),
        overall,
        percent,
        eta,
        overall_text,
        recv,
        recv_text,
        write,
        write_text,
    }
}

fn set_flash(ui: &AppWindow, f: &proto::Flash, model: &crate::flashmodel::FlashModel) {
    let v = flash_view(f, model.overall(), model.eta_ms());
    ui.set_flash_active(v.active);
    ui.set_flash_finished(v.finished);
    ui.set_flash_title(v.title.into());
    ui.set_flash_overall(v.overall);
    ui.set_flash_percent(v.percent.into());
    ui.set_flash_eta(v.eta.into());
    ui.set_flash_overall_text(v.overall_text.into());
    ui.set_flash_recv(v.recv);
    ui.set_flash_recv_text(v.recv_text.into());
    ui.set_flash_write(v.write);
    ui.set_flash_write_text(v.write_text.into());
}

#[cfg(test)]
mod flash_tests {
    use super::*;

    fn flash(phase: &str) -> proto::Flash {
        proto::Flash { phase: phase.into(), name: "sde27".into(), piece: 3, total: 3_000_000_000, end: 750_000_000, ..Default::default() }
    }

    #[test]
    fn a_piece_coming_in() {
        let v = flash_view(&proto::Flash { recv: 100 << 20, recv_total: 200 << 20, ..flash("receiving") }, 0.25, Some(65_000));
        assert!(v.active && !v.finished);
        assert_eq!(v.title, "sde27");
        assert_eq!((v.recv, v.write), (0.5, 0.));
        assert_eq!(v.recv_text, "Receiving piece 3: 100 MB of 200 MB");
        assert_eq!((v.percent.as_str(), v.overall_text.as_str()), ("25%", "715 MB of 2.79 GB"));
        assert_eq!(v.eta, "about 1 min 5 s left");
    }

    #[test]
    fn a_piece_going_out() {
        let v = flash_view(&proto::Flash { wrote: 50 << 20, wrote_total: 200 << 20, ..flash("writing") }, 0.3, None);
        assert_eq!((v.recv, v.write), (1., 0.25));
        assert_eq!(v.write_text, "Writing piece 3: 50 MB of 200 MB");
        assert_eq!(v.eta, "", "no estimate before a piece has been timed");
    }

    #[test]
    fn finished_and_cleared() {
        let v = flash_view(&flash("finished"), 0.4, Some(9_000));
        assert!(v.finished);
        assert_eq!((v.overall, v.title.as_str(), v.percent.as_str(), v.eta.as_str()), (1., "sde27", "100%", "Done"));
        assert!(!flash_view(&proto::Flash::default(), 0., None).active);
    }

    #[test]
    fn the_time_left_in_words() {
        assert_eq!(eta_text(Some(42_000)), "about 40 s left");
        assert_eq!(eta_text(Some(61_000)), "about 1 min 0 s left");
        assert_eq!(eta_text(Some(2_000)), "", "the last seconds are not worth a number");
        assert_eq!(eta_text(None), "");
    }
}
