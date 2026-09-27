//! The Slint platform for a LinuxBoot stage: Slint's software renderer
//! drawing into a RAM buffer that is copied to the framebuffer, and input
//! read straight from /dev/input/event*. Nothing else is there: no udev (so
//! no libinput), no seat manager, no GPU userspace.
//!
//! Keys, for a phone with three of them:
//!   Vol+ / Vol-          Up / Down
//!   Power, short         Return (select)
//!   Power, held 600 ms   Escape (back)
//! A keyboard's arrows, Enter, Esc and Backspace do the same. A touchscreen
//! drives the pointer: first contact only, taps and drags.
//!
//! `Target::Memory` renders to the buffer only, for --screenshot.

use crate::app::scale_for;
use evdev::raw_stream::RawDevice;
use evdev::{AbsoluteAxisCode, EventSummary, KeyCode, SynchronizationCode};
use slint::platform::software_renderer::{MinimalSoftwareWindow, PremultipliedRgbaColor, RepaintBufferType, TargetPixel};
use slint::platform::{EventLoopProxy, Key, Platform, PointerEventButton, WindowAdapter, WindowEvent};
use slint::{LogicalPosition, PhysicalSize, PlatformError};
use std::cell::RefCell;
use std::collections::VecDeque;
use std::io::{Read, Write};
use std::os::fd::{AsRawFd, RawFd};
use std::os::unix::net::UnixStream;
use std::path::PathBuf;
use std::rc::Rc;
use std::sync::atomic::{AtomicBool, AtomicI32, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

/// XRGB8888 in a u32.
#[derive(Clone, Copy, Default)]
#[repr(transparent)]
pub struct Px(pub u32);

impl TargetPixel for Px {
    fn blend(&mut self, c: PremultipliedRgbaColor) {
        let a = (255 - c.alpha) as u32;
        let ch = |shift: u32, add: u8| (((self.0 >> shift) & 0xff) * a / 255 + add as u32) & 0xff;
        self.0 = ch(16, c.red) << 16 | ch(8, c.green) << 8 | ch(0, c.blue);
    }
    fn from_rgb(r: u8, g: u8, b: u8) -> Self {
        Px((r as u32) << 16 | (g as u32) << 8 | b as u32)
    }
}

/// What is drawn, at the window's size.
#[derive(Default)]
pub struct Frame {
    pub px: Vec<Px>,
    pub width: usize,
    pub height: usize,
}

// --- fbdev (linux/fb.h) ---

const FBIOGET_VSCREENINFO: libc::c_ulong = 0x4600;
const FBIOGET_FSCREENINFO: libc::c_ulong = 0x4602;
const KDSETMODE: libc::c_ulong = 0x4b3a;
const KD_TEXT: libc::c_ulong = 0;
const KD_GRAPHICS: libc::c_ulong = 1;

#[repr(C)]
#[derive(Default, Clone, Copy)]
#[allow(dead_code)]
struct FbBitfield {
    offset: u32,
    length: u32,
    msb_right: u32,
}

#[repr(C)]
#[derive(Default)]
#[allow(dead_code)] // the kernel's layout, whole
struct FbVarScreeninfo {
    xres: u32,
    yres: u32,
    xres_virtual: u32,
    yres_virtual: u32,
    xoffset: u32,
    yoffset: u32,
    bits_per_pixel: u32,
    grayscale: u32,
    red: FbBitfield,
    green: FbBitfield,
    blue: FbBitfield,
    transp: FbBitfield,
    nonstd: u32,
    activate: u32,
    height: u32, // mm
    width: u32,  // mm
    rest: [u32; 16],
}

#[repr(C)]
#[derive(Default)]
#[allow(dead_code)]
struct FbFixScreeninfo {
    id: [u8; 16],
    smem_start: libc::c_ulong,
    smem_len: u32,
    type_: u32,
    type_aux: u32,
    visual: u32,
    xpanstep: u16,
    ypanstep: u16,
    ywrapstep: u16,
    line_length: u32,
    mmio_start: libc::c_ulong,
    mmio_len: u32,
    accel: u32,
    capabilities: u16,
    reserved: [u16; 2],
}

pub struct Fb {
    mem: *mut u8,
    len: usize,
    stride: usize,
    pub xres: usize,
    pub yres: usize,
    /// Physical width in mm, 0 unknown.
    pub width_mm: u32,
    yoffset: usize,
    bytes: usize,
    rgb: [FbBitfield; 3],
}

static TTY_FD: AtomicI32 = AtomicI32::new(-1);

extern "C" fn on_term(_: libc::c_int) {
    // Hand the console back to fbcon, so whatever runs next (a kexec'd
    // kernel's handover, a shell) is not left with a frozen picture.
    let fd = TTY_FD.load(Ordering::Relaxed);
    if fd >= 0 {
        unsafe { libc::ioctl(fd, KDSETMODE as _, KD_TEXT) };
    }
    unsafe { libc::_exit(0) };
}

fn fb_name() -> String {
    std::fs::read_to_string("/sys/class/graphics/fb0/name").unwrap_or_default().trim().to_string()
}

impl Fb {
    pub fn open(path: &str) -> std::io::Result<Fb> {
        // simpledrm owns fb0 until the real display driver probes and
        // replaces it; a mapping of the old one would draw nowhere.
        let t = Instant::now();
        while fb_name().contains("simpledrm") && t.elapsed() < Duration::from_secs(10) {
            std::thread::sleep(Duration::from_millis(100));
        }
        let file = std::fs::OpenOptions::new().read(true).write(true).open(path)?;
        let fd = file.as_raw_fd();
        let mut var = FbVarScreeninfo::default();
        let mut fix = FbFixScreeninfo::default();
        unsafe {
            if libc::ioctl(fd, FBIOGET_VSCREENINFO as _, &mut var) < 0 || libc::ioctl(fd, FBIOGET_FSCREENINFO as _, &mut fix) < 0 {
                return Err(std::io::Error::last_os_error());
            }
        }
        if var.bits_per_pixel != 32 && var.bits_per_pixel != 16 {
            return Err(std::io::Error::other(format!("unsupported {} bpp", var.bits_per_pixel)));
        }
        let len = fix.smem_len as usize;
        let mem = unsafe { libc::mmap(std::ptr::null_mut(), len, libc::PROT_READ | libc::PROT_WRITE, libc::MAP_SHARED, fd, 0) };
        if mem == libc::MAP_FAILED {
            return Err(std::io::Error::last_os_error());
        }
        let fb = Fb {
            mem: mem as *mut u8,
            len,
            stride: fix.line_length as usize,
            xres: var.xres as usize,
            yres: var.yres as usize,
            width_mm: if var.width == u32::MAX { 0 } else { var.width },
            yoffset: var.yoffset as usize,
            bytes: var.bits_per_pixel as usize / 8,
            rgb: [var.red, var.green, var.blue],
        };
        eprintln!(
            "bootmanager-ui: {} {}x{} ({} mm) {} bpp, rgb at {}/{}/{}, stride {}",
            fb_name(),
            fb.xres,
            fb.yres,
            fb.width_mm,
            var.bits_per_pixel,
            fb.rgb[0].offset,
            fb.rgb[1].offset,
            fb.rgb[2].offset,
            fb.stride
        );

        if let Ok(tty) = std::fs::OpenOptions::new().read(true).write(true).open("/dev/tty0") {
            unsafe { libc::ioctl(tty.as_raw_fd(), KDSETMODE as _, KD_GRAPHICS) };
            // Kept open for on_term, for the life of the process.
            TTY_FD.store(std::os::fd::IntoRawFd::into_raw_fd(tty), Ordering::Relaxed);
            unsafe {
                libc::signal(libc::SIGTERM, on_term as *const () as libc::sighandler_t);
                libc::signal(libc::SIGINT, on_term as *const () as libc::sighandler_t);
            }
        }
        Ok(fb)
    }

    /// Px in the framebuffer's layout, from its bitfields.
    fn pack(&self, p: Px) -> u32 {
        let ch = |v: u32, f: &FbBitfield| ((v & 0xff) >> (8 - f.length.min(8))) << f.offset;
        ch(p.0 >> 16, &self.rgb[0]) | ch(p.0 >> 8, &self.rgb[1]) | ch(p.0, &self.rgb[2])
    }

    fn present(&self, frame: &Frame, x: usize, y: usize, w: usize, h: usize) {
        let x1 = (x + w).min(self.xres).min(frame.width);
        for row in y..(y + h).min(self.yres).min(frame.height) {
            let off = (row + self.yoffset) * self.stride + x * self.bytes;
            let n = x1.saturating_sub(x);
            if off + n * self.bytes > self.len {
                return;
            }
            let src = &frame.px[row * frame.width + x..][..n];
            let dst = unsafe { std::slice::from_raw_parts_mut(self.mem.add(off), n * self.bytes) };
            for (d, s) in dst.chunks_exact_mut(self.bytes).zip(src) {
                let v = self.pack(*s);
                match self.bytes {
                    4 => d.copy_from_slice(&v.to_ne_bytes()),
                    _ => d.copy_from_slice(&(v as u16).to_ne_bytes()),
                }
            }
        }
    }
}

/// Keep this process on the CPUs with the highest cpu_capacity, where the
/// big.LITTLE SoCs have them: the scheduler otherwise starts a frame after
/// an idle spell on a little core, a hitch of several frames at the start
/// of every swipe. Nothing changes where all CPUs are alike.
pub fn pin_to_fastest_cpus() {
    let cap: Vec<(usize, u32)> = (0..libc::CPU_SETSIZE as usize)
        .map_while(|i| {
            let d = format!("/sys/devices/system/cpu/cpu{i}");
            std::path::Path::new(&d).exists().then(|| {
                let c = std::fs::read_to_string(format!("{d}/cpu_capacity")).ok().and_then(|s| s.trim().parse().ok());
                (i, c.unwrap_or(0))
            })
        })
        .collect();
    let max = cap.iter().map(|&(_, c)| c).max().unwrap_or(0);
    if max == 0 || cap.iter().all(|&(_, c)| c == max) {
        return;
    }
    let fast: Vec<usize> = cap.iter().filter(|&&(_, c)| c == max).map(|&(i, _)| i).collect();
    unsafe {
        let mut set: libc::cpu_set_t = std::mem::zeroed();
        for &i in &fast {
            libc::CPU_SET(i, &mut set);
        }
        if libc::sched_setaffinity(0, std::mem::size_of::<libc::cpu_set_t>(), &set) != 0 {
            eprintln!("bootmanager-ui: pinning to cpus {fast:?}: {}", std::io::Error::last_os_error());
            return;
        }
    }
    eprintln!("bootmanager-ui: pinned to cpus {fast:?} (capacity {max})");
}

// --- evdev ---

const LONG_PRESS: Duration = Duration::from_millis(600);

struct Input {
    path: PathBuf,
    dev: RawDevice,
    /// Touch axes, (min, max) for x and y, when this is a touchscreen.
    axes: Option<((i32, i32), (i32, i32))>,
    x: i32,
    y: i32,
    down: bool,
    was_down: bool,
    /// The multitouch slot the next ABS_MT_* events are for; only the first
    /// contact (slot 0) drives the pointer.
    slot: i32,
}

impl Input {
    fn open(path: PathBuf) -> Option<Input> {
        let dev = RawDevice::open(&path).ok()?;
        let range = |code| {
            dev.get_absinfo().ok()?.find(|(c, _)| *c == code).map(|(_, i)| (i.minimum(), i.maximum())).filter(|(lo, hi)| hi > lo)
        };
        let axes = range(AbsoluteAxisCode::ABS_MT_POSITION_X)
            .zip(range(AbsoluteAxisCode::ABS_MT_POSITION_Y))
            .or_else(|| range(AbsoluteAxisCode::ABS_X).zip(range(AbsoluteAxisCode::ABS_Y)));
        eprintln!("bootmanager-ui: input {} {}{}", path.display(), dev.name().unwrap_or(""), if axes.is_some() { " (touch)" } else { "" });
        Some(Input { path, dev, axes, x: 0, y: 0, down: false, was_down: false, slot: 0 })
    }
}

fn map_key(code: KeyCode) -> Option<Key> {
    Some(match code {
        KeyCode::KEY_VOLUMEUP | KeyCode::KEY_UP => Key::UpArrow,
        KeyCode::KEY_VOLUMEDOWN | KeyCode::KEY_DOWN => Key::DownArrow,
        KeyCode::KEY_ENTER | KeyCode::KEY_KPENTER | KeyCode::KEY_SPACE | KeyCode::KEY_RIGHT => Key::Return,
        KeyCode::KEY_ESC | KeyCode::KEY_BACKSPACE | KeyCode::KEY_LEFT => Key::Escape,
        _ => return None,
    })
}

// --- the platform ---

struct Shared {
    queue: Mutex<VecDeque<Box<dyn FnOnce() + Send>>>,
    wake: UnixStream,
    quit: AtomicBool,
}

impl Shared {
    fn wake(&self) {
        let _ = (&self.wake).write(&[1]);
    }
}


struct Proxy(Arc<Shared>);

impl EventLoopProxy for Proxy {
    fn quit_event_loop(&self) -> Result<(), slint::EventLoopError> {
        self.0.quit.store(true, Ordering::SeqCst);
        self.0.wake();
        Ok(())
    }
    fn invoke_from_event_loop(&self, f: Box<dyn FnOnce() + Send>) -> Result<(), slint::EventLoopError> {
        self.0.queue.lock().unwrap().push_back(f);
        self.0.wake();
        Ok(())
    }
}

pub enum Target {
    Fb(Fb),
    Memory,
}

/// Frame timings, logged once a second while frames are drawn: where a slow
/// UI spends its time, and on which CPU at what clock.
struct Timing {
    since: Instant,
    frames: u32,
    render: Duration,
    render_max: Duration,
    present: Duration,
    present_max: Duration,
    damaged: u64,
}

impl Timing {
    fn new() -> Timing {
        Timing {
            since: Instant::now(),
            frames: 0,
            render: Duration::ZERO,
            render_max: Duration::ZERO,
            present: Duration::ZERO,
            present_max: Duration::ZERO,
            damaged: 0,
        }
    }

    fn add(&mut self, render: Duration, present: Duration, damaged: u64, screen: u64) {
        self.frames += 1;
        self.render += render;
        self.render_max = self.render_max.max(render);
        self.present += present;
        self.present_max = self.present_max.max(present);
        self.damaged += damaged;
        let span = self.since.elapsed();
        if span < Duration::from_secs(1) {
            return;
        }
        let n = self.frames;
        let cpu = unsafe { libc::sched_getcpu() };
        let khz = std::fs::read_to_string(format!("/sys/devices/system/cpu/cpu{cpu}/cpufreq/scaling_cur_freq")).unwrap_or_default();
        eprintln!(
            "bootmanager-ui: timing {n} frames in {} ms: render avg {} max {} us, present avg {} max {} us, damaged avg {}%, cpu{cpu} {} MHz",
            span.as_millis(),
            (self.render / n).as_micros(),
            self.render_max.as_micros(),
            (self.present / n).as_micros(),
            self.present_max.as_micros(),
            self.damaged * 100 / (n as u64 * screen.max(1)),
            khz.trim().parse::<u64>().map_or(0, |k| k / 1000),
        );
        *self = Timing::new();
    }
}

pub struct FbPlatform {
    window: Rc<MinimalSoftwareWindow>,
    target: Target,
    pub frame: Rc<RefCell<Frame>>,
    shared: Arc<Shared>,
    wake: UnixStream,
    start: Instant,
    /// Set in a dev stage (/etc/bootmanager/dev).
    timing: Option<RefCell<Timing>>,
}

impl FbPlatform {
    /// A platform drawing to target; `size` is Memory's until the app
    /// sizes the window to the panel.
    pub fn new(target: Target, size: PhysicalSize) -> std::io::Result<FbPlatform> {
        let (size, mm) = match &target {
            Target::Fb(fb) => (PhysicalSize::new(fb.xres as u32, fb.yres as u32), fb.width_mm),
            Target::Memory => (size, 0),
        };
        let (wake, wake_w) = UnixStream::pair()?;
        wake.set_nonblocking(true)?;
        wake_w.set_nonblocking(true)?;
        let window = MinimalSoftwareWindow::new(RepaintBufferType::ReusedBuffer);
        window.dispatch_event(WindowEvent::ScaleFactorChanged { scale_factor: scale_for(size.width, mm) });
        window.set_size(size);
        Ok(FbPlatform {
            window,
            target,
            frame: Rc::default(),
            shared: Arc::new(Shared { queue: Mutex::default(), wake: wake_w, quit: AtomicBool::new(false) }),
            wake,
            start: Instant::now(),
            timing: std::path::Path::new("/etc/bootmanager/dev").exists().then(|| RefCell::new(Timing::new())),
        })
    }

    fn scan_inputs(&self, inputs: &mut Vec<Input>) {
        let Ok(dir) = std::fs::read_dir("/dev/input") else { return };
        for e in dir.flatten() {
            let p = e.path();
            let is_event = p.file_name().is_some_and(|n| n.to_string_lossy().starts_with("event"));
            if is_event && !inputs.iter().any(|d| d.path == p) {
                inputs.extend(Input::open(p));
            }
        }
    }

    fn key(&self, k: Key) {
        let text: slint::SharedString = k.into();
        self.window.dispatch_event(WindowEvent::KeyPressed { text: text.clone() });
        self.window.dispatch_event(WindowEvent::KeyReleased { text });
    }

    fn touch_pos(&self, d: &Input) -> LogicalPosition {
        let ((x0, x1), (y0, y1)) = d.axes.unwrap();
        let size = self.window.size();
        let scale = self.window.scale_factor();
        let px = (d.x - x0) as f32 / (x1 - x0) as f32 * size.width as f32;
        let py = (d.y - y0) as f32 / (y1 - y0) as f32 * size.height as f32;
        LogicalPosition::new(px / scale, py / scale)
    }

    fn draw(&self) {
        self.window.draw_if_needed(|renderer| {
            let size = self.window.size();
            let mut frame = self.frame.borrow_mut();
            let (w, h) = (size.width as usize, size.height as usize);
            if (frame.width, frame.height) != (w, h) {
                *frame = Frame { px: vec![Px::default(); w * h], width: w, height: h };
                renderer.set_repaint_buffer_type(RepaintBufferType::NewBuffer);
            } else {
                renderer.set_repaint_buffer_type(RepaintBufferType::ReusedBuffer);
            }
            let t0 = Instant::now();
            let region = renderer.render(&mut frame.px, w);
            let t1 = Instant::now();
            let (o, s) = (region.bounding_box_origin(), region.bounding_box_size());
            if let Target::Fb(fb) = &self.target {
                fb.present(&frame, o.x.max(0) as usize, o.y.max(0) as usize, s.width as usize, s.height as usize);
            }
            if let Some(t) = &self.timing {
                t.borrow_mut().add(t1 - t0, t1.elapsed(), s.width as u64 * s.height as u64, (w * h) as u64);
            }
        });
    }
}

impl Platform for FbPlatform {
    fn create_window_adapter(&self) -> Result<Rc<dyn WindowAdapter>, PlatformError> {
        Ok(self.window.clone())
    }

    fn duration_since_start(&self) -> Duration {
        self.start.elapsed()
    }

    fn new_event_loop_proxy(&self) -> Option<Box<dyn EventLoopProxy>> {
        Some(Box::new(Proxy(self.shared.clone())))
    }

    fn run_event_loop(&self) -> Result<(), PlatformError> {
        let reads_input = matches!(self.target, Target::Fb(_));
        let mut inputs = vec![];
        if reads_input {
            self.scan_inputs(&mut inputs);
        }
        let mut last_scan = Instant::now();
        let mut power_down: Option<Instant> = None;
        let mut power_fired = false;

        loop {
            slint::platform::update_timers_and_animations();
            let jobs: Vec<_> = self.shared.queue.lock().unwrap().drain(..).collect();
            for job in jobs {
                job();
            }
            if self.shared.quit.load(Ordering::SeqCst) {
                return Ok(());
            }
            self.draw();

            // How long poll may sleep: the next Slint timer, an animation
            // frame, the long-press deadline, or the input rescan (a device
            // whose driver probes late shows up within 3 s).
            let mut timeout = Duration::from_secs(3);
            if self.window.has_active_animations() {
                timeout = Duration::from_millis(16);
            }
            if let Some(t) = slint::platform::duration_until_next_timer_update() {
                timeout = timeout.min(t);
            }
            if let (Some(t), false) = (power_down, power_fired) {
                timeout = timeout.min(LONG_PRESS.saturating_sub(t.elapsed()));
            }

            let mut fds: Vec<libc::pollfd> = std::iter::once(self.wake.as_raw_fd())
                .chain(inputs.iter().map(|d| d.dev.as_raw_fd()))
                .map(|fd: RawFd| libc::pollfd { fd, events: libc::POLLIN, revents: 0 })
                .collect();
            let ms = timeout.as_millis().min(i32::MAX as u128) as i32;
            unsafe { libc::poll(fds.as_mut_ptr(), fds.len() as _, ms) };

            if fds[0].revents != 0 {
                let mut b = [0u8; 64];
                while (&self.wake).read(&mut b).is_ok_and(|n| n > 0) {}
            }

            let mut dead = vec![];
            for (i, pfd) in fds[1..].iter().enumerate() {
                if pfd.revents & (libc::POLLERR | libc::POLLHUP | libc::POLLNVAL) != 0 {
                    dead.push(i);
                    continue;
                }
                if pfd.revents & libc::POLLIN == 0 {
                    continue;
                }
                let d = &mut inputs[i];
                let Ok(events) = d.dev.fetch_events().map(|e| e.collect::<Vec<_>>()) else { continue };
                for ev in events {
                    match ev.destructure() {
                        EventSummary::Key(_, KeyCode::KEY_POWER, 1) => (power_down, power_fired) = (Some(Instant::now()), false),
                        EventSummary::Key(_, KeyCode::KEY_POWER, 0) => {
                            if power_down.take().is_some() && !power_fired {
                                self.key(Key::Return);
                            }
                        }
                        EventSummary::Key(_, KeyCode::BTN_TOUCH, v) => d.down = v != 0,
                        EventSummary::Key(_, code, v) if v >= 1 => {
                            if let Some(k) = map_key(code) {
                                self.key(k);
                            }
                        }
                        EventSummary::AbsoluteAxis(_, code, v) => match code {
                            AbsoluteAxisCode::ABS_MT_SLOT => d.slot = v,
                            AbsoluteAxisCode::ABS_MT_POSITION_X if d.slot == 0 => d.x = v,
                            AbsoluteAxisCode::ABS_MT_POSITION_Y if d.slot == 0 => d.y = v,
                            AbsoluteAxisCode::ABS_MT_TRACKING_ID if d.slot == 0 => d.down = v >= 0,
                            AbsoluteAxisCode::ABS_X => d.x = v,
                            AbsoluteAxisCode::ABS_Y => d.y = v,
                            _ => {}
                        },
                        EventSummary::Synchronization(_, SynchronizationCode::SYN_REPORT, _) if d.axes.is_some() => {
                            let position = self.touch_pos(d);
                            let button = PointerEventButton::Left;
                            let event = match (d.was_down, d.down) {
                                (false, true) => Some(WindowEvent::PointerPressed { position, button }),
                                (true, true) => Some(WindowEvent::PointerMoved { position }),
                                (true, false) => Some(WindowEvent::PointerReleased { position, button }),
                                (false, false) => None,
                            };
                            d.was_down = d.down;
                            if let Some(event) = event {
                                self.window.dispatch_event(event);
                                if !d.down {
                                    self.window.dispatch_event(WindowEvent::PointerExited);
                                }
                            }
                        }
                        _ => {}
                    }
                }
            }
            for i in dead.into_iter().rev() {
                inputs.remove(i);
            }

            if let (Some(t), false) = (power_down, power_fired) {
                if t.elapsed() >= LONG_PRESS {
                    power_fired = true;
                    self.key(Key::Escape);
                }
            }
            if reads_input && last_scan.elapsed() >= Duration::from_secs(3) {
                self.scan_inputs(&mut inputs);
                last_scan = Instant::now();
            }
        }
    }
}
