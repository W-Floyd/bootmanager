//! The phone's Slint platform: Slint's software renderer drawing into a RAM
//! buffer that is copied to /dev/fb0, and input read straight from
//! /dev/input/event*. Nothing else is available in the stage: no udev (so
//! no libinput), no seat manager, no GPU userspace.
//!
//! Keys, for a phone with three of them:
//!   Vol+ / Vol-          Up / Down
//!   Power, short         Return (select)
//!   Power, held 600 ms   Escape (back)
//! A keyboard's arrows, Enter, Esc and Backspace do the same. The touchscreen
//! (the ICNL9916, built into the stage kernel) drives the pointer: first
//! contact only, taps and drags.
//!
//! `Target::Memory` renders to a buffer only, for --screenshot.

use slint::platform::software_renderer::{
    MinimalSoftwareWindow, PremultipliedRgbaColor, RepaintBufferType, TargetPixel,
};
use slint::platform::{EventLoopProxy, Key, Platform, PointerEventButton, WindowAdapter, WindowEvent};
use slint::{LogicalPosition, PhysicalSize, PlatformError};
use std::cell::RefCell;
use std::collections::VecDeque;
use std::os::fd::RawFd;
use std::rc::Rc;
use std::sync::atomic::{AtomicBool, AtomicI32, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

pub const SCALE: f32 = 2.0;

/// XRGB8888 in a u32, which is also the byte order of the msm fbdev.
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

// --- fbdev ---

const FBIOGET_VSCREENINFO: libc::c_ulong = 0x4600;
const FBIOGET_FSCREENINFO: libc::c_ulong = 0x4602;
const KDSETMODE: libc::c_ulong = 0x4b3a;
const KD_TEXT: libc::c_ulong = 0;
const KD_GRAPHICS: libc::c_ulong = 1;

pub struct Fb {
    mem: *mut u8,
    len: usize,
    stride: usize, // bytes per line
    xres: usize,
    yres: usize,
    yoffset: usize,
    bpp: u32,
    red_offset: u32,
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
        // simpledrm owns fb0 until msm probes (about 1.5 s in) and then
        // replaces it; a mapping of the old one would draw nowhere.
        let t = Instant::now();
        while fb_name().contains("simpledrm") && t.elapsed() < Duration::from_secs(10) {
            std::thread::sleep(Duration::from_millis(100));
        }
        let cpath = std::ffi::CString::new(path).unwrap();
        let fd = unsafe { libc::open(cpath.as_ptr(), libc::O_RDWR | libc::O_CLOEXEC) };
        if fd < 0 {
            return Err(std::io::Error::last_os_error());
        }
        let mut var = [0u32; 40];
        let mut fix = [0u8; 80];
        unsafe {
            if libc::ioctl(fd, FBIOGET_VSCREENINFO as _, var.as_mut_ptr()) < 0
                || libc::ioctl(fd, FBIOGET_FSCREENINFO as _, fix.as_mut_ptr()) < 0
            {
                return Err(std::io::Error::last_os_error());
            }
        }
        let smem_len = u32::from_ne_bytes(fix[24..28].try_into().unwrap()) as usize;
        let stride = u32::from_ne_bytes(fix[48..52].try_into().unwrap()) as usize;
        let mem = unsafe {
            libc::mmap(std::ptr::null_mut(), smem_len, libc::PROT_READ | libc::PROT_WRITE, libc::MAP_SHARED, fd, 0)
        };
        if mem == libc::MAP_FAILED {
            return Err(std::io::Error::last_os_error());
        }
        unsafe { libc::close(fd) };
        let fb = Fb {
            mem: mem as *mut u8,
            len: smem_len,
            stride,
            xres: var[0] as usize,
            yres: var[1] as usize,
            yoffset: var[5] as usize,
            bpp: var[6],
            red_offset: var[8],
        };
        eprintln!(
            "bootmanager-ui: {} {}x{} {} bpp, red at {}, stride {}",
            fb_name(), fb.xres, fb.yres, fb.bpp, fb.red_offset, fb.stride
        );
        if fb.bpp != 32 && fb.bpp != 16 {
            return Err(std::io::Error::other(format!("unsupported {} bpp", fb.bpp)));
        }

        let tty = unsafe { libc::open(c"/dev/tty0".as_ptr(), libc::O_RDWR | libc::O_CLOEXEC) };
        if tty >= 0 {
            unsafe { libc::ioctl(tty, KDSETMODE as _, KD_GRAPHICS) };
            TTY_FD.store(tty, Ordering::Relaxed);
            unsafe {
                libc::signal(libc::SIGTERM, on_term as *const () as libc::sighandler_t);
                libc::signal(libc::SIGINT, on_term as *const () as libc::sighandler_t);
            }
        }
        Ok(fb)
    }

    fn present(&self, buf: &[Px], width: usize, x: usize, y: usize, w: usize, h: usize) {
        for row in y..(y + h).min(self.yres) {
            let off = (row + self.yoffset) * self.stride;
            let src = &buf[row * width + x..row * width + (x + w).min(self.xres)];
            if self.bpp == 32 {
                if off + (x + src.len()) * 4 > self.len {
                    return;
                }
                let dst = unsafe { std::slice::from_raw_parts_mut(self.mem.add(off) as *mut u32, self.xres) };
                if self.red_offset == 16 {
                    dst[x..x + src.len()].copy_from_slice(unsafe { std::mem::transmute::<&[Px], &[u32]>(src) });
                } else {
                    for (d, s) in dst[x..].iter_mut().zip(src) {
                        let v = s.0;
                        *d = (v & 0xff) << 16 | (v & 0xff00) | (v >> 16) & 0xff;
                    }
                }
            } else {
                if off + (x + src.len()) * 2 > self.len {
                    return;
                }
                let dst = unsafe { std::slice::from_raw_parts_mut(self.mem.add(off) as *mut u16, self.xres) };
                for (d, s) in dst[x..].iter_mut().zip(src) {
                    let v = s.0;
                    *d = ((v >> 8 & 0xf800) | (v >> 5 & 0x07e0) | (v >> 3 & 0x001f)) as u16;
                }
            }
        }
    }
}

// --- evdev ---

const EV_SYN: u16 = 0;
const EV_KEY: u16 = 1;
const EV_ABS: u16 = 3;
const ABS_X: u16 = 0x00;
const ABS_Y: u16 = 0x01;
const ABS_MT_POSITION_X: u16 = 0x35;
const ABS_MT_POSITION_Y: u16 = 0x36;
const ABS_MT_TRACKING_ID: u16 = 0x39;
const ABS_MT_SLOT: u16 = 0x2f;
const BTN_TOUCH: u16 = 0x14a;
const KEY_POWER: u16 = 116;
const LONG_PRESS: Duration = Duration::from_millis(600);

fn eviocgabs(abs: u16) -> libc::c_ulong {
    0x8018_4540 + abs as libc::c_ulong
}

struct InputDev {
    path: String,
    fd: RawFd,
    // Touch axes: (min, max) for x and y, when this is a touchscreen.
    axes: Option<((i32, i32), (i32, i32))>,
    x: i32,
    y: i32,
    down: bool,
    // The multitouch slot the next ABS_MT_* events are for; only the first
    // contact (slot 0) drives the pointer.
    slot: i32,
    was_down: bool,
}

fn abs_range(fd: RawFd, code: u16) -> Option<(i32, i32)> {
    let mut info = [0i32; 6];
    let r = unsafe { libc::ioctl(fd, eviocgabs(code) as _, info.as_mut_ptr()) };
    (r >= 0 && info[2] > info[1]).then_some((info[1], info[2]))
}

impl InputDev {
    fn open(path: &str) -> Option<InputDev> {
        let c = std::ffi::CString::new(path).ok()?;
        let fd = unsafe { libc::open(c.as_ptr(), libc::O_RDONLY | libc::O_NONBLOCK | libc::O_CLOEXEC) };
        if fd < 0 {
            return None;
        }
        let axes = match (abs_range(fd, ABS_MT_POSITION_X), abs_range(fd, ABS_MT_POSITION_Y)) {
            (Some(x), Some(y)) => Some((x, y)),
            _ => abs_range(fd, ABS_X).zip(abs_range(fd, ABS_Y)),
        };
        eprintln!("bootmanager-ui: input {path}{}", if axes.is_some() { " (touch)" } else { "" });
        Some(InputDev { path: path.to_string(), fd, axes, x: 0, y: 0, down: false, slot: 0, was_down: false })
    }
}

fn map_key(code: u16) -> Option<Key> {
    Some(match code {
        115 | 103 => Key::UpArrow,  // KEY_VOLUMEUP, KEY_UP
        114 | 108 => Key::DownArrow, // KEY_VOLUMEDOWN, KEY_DOWN
        28 | 96 | 57 => Key::Return, // KEY_ENTER, KEY_KPENTER, KEY_SPACE
        1 | 14 | 105 => Key::Escape, // KEY_ESC, KEY_BACKSPACE, KEY_LEFT
        106 => Key::Return,          // KEY_RIGHT
        _ => return None,
    })
}

// --- the platform ---

struct Shared {
    queue: Mutex<VecDeque<Box<dyn FnOnce() + Send>>>,
    wake_w: RawFd,
    quit: AtomicBool,
}

impl Shared {
    fn wake(&self) {
        unsafe { libc::write(self.wake_w, [1u8].as_ptr() as *const _, 1) };
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

pub struct FbPlatform {
    window: Rc<MinimalSoftwareWindow>,
    target: Target,
    size: PhysicalSize,
    pub buffer: Rc<RefCell<Vec<Px>>>,
    shared: Arc<Shared>,
    wake_r: RawFd,
    start: Instant,
}

impl FbPlatform {
    pub fn new(target: Target, memory_size: PhysicalSize) -> FbPlatform {
        let size = match &target {
            Target::Fb(fb) => PhysicalSize::new(fb.xres as u32, fb.yres as u32),
            Target::Memory => memory_size,
        };
        let mut fds = [0; 2];
        unsafe { libc::pipe2(fds.as_mut_ptr(), libc::O_NONBLOCK | libc::O_CLOEXEC) };
        let window = MinimalSoftwareWindow::new(RepaintBufferType::ReusedBuffer);
        window.dispatch_event(WindowEvent::ScaleFactorChanged { scale_factor: SCALE });
        window.set_size(size);
        FbPlatform {
            window,
            target,
            size,
            buffer: Rc::new(RefCell::new(vec![Px::default(); (size.width * size.height) as usize])),
            shared: Arc::new(Shared { queue: Mutex::new(VecDeque::new()), wake_w: fds[1], quit: AtomicBool::new(false) }),
            wake_r: fds[0],
            start: Instant::now(),
        }
    }

    fn scan_inputs(&self, inputs: &mut Vec<InputDev>) {
        let Ok(dir) = std::fs::read_dir("/dev/input") else { return };
        for e in dir.flatten() {
            let p = e.path().to_string_lossy().to_string();
            if p.contains("/event") && !inputs.iter().any(|d| d.path == p) {
                if let Some(d) = InputDev::open(&p) {
                    inputs.push(d);
                }
            }
        }
    }

    fn key(&self, k: Key) {
        let text: slint::SharedString = k.into();
        self.window.dispatch_event(WindowEvent::KeyPressed { text: text.clone() });
        self.window.dispatch_event(WindowEvent::KeyReleased { text });
    }

    fn touch_pos(&self, d: &InputDev) -> LogicalPosition {
        let ((x0, x1), (y0, y1)) = d.axes.unwrap();
        let px = (d.x - x0) as f32 / (x1 - x0) as f32 * self.size.width as f32;
        let py = (d.y - y0) as f32 / (y1 - y0) as f32 * self.size.height as f32;
        LogicalPosition::new(px / SCALE, py / SCALE)
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
        let mut inputs = vec![];
        let reads_input = matches!(self.target, Target::Fb(_));
        if reads_input {
            self.scan_inputs(&mut inputs);
        }
        let mut last_scan = Instant::now();
        let mut power_down: Option<Instant> = None;
        let mut power_fired = false;
        let width = self.size.width as usize;

        loop {
            slint::platform::update_timers_and_animations();
            let jobs: Vec<_> = self.shared.queue.lock().unwrap().drain(..).collect();
            for job in jobs {
                job();
            }
            if self.shared.quit.load(Ordering::SeqCst) {
                return Ok(());
            }

            self.window.draw_if_needed(|renderer| {
                let mut buf = self.buffer.borrow_mut();
                let region = renderer.render(&mut buf, width);
                if let Target::Fb(fb) = &self.target {
                    let o = region.bounding_box_origin();
                    let s = region.bounding_box_size();
                    fb.present(&buf, width, o.x.max(0) as usize, o.y.max(0) as usize, s.width as usize, s.height as usize);
                }
            });

            // How long poll may sleep: the next Slint timer, an animation
            // frame, the long-press deadline, or the input rescan (a touch
            // module loaded after start shows up within 3 s).
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

            let mut fds: Vec<libc::pollfd> = std::iter::once(self.wake_r)
                .chain(inputs.iter().map(|d| d.fd))
                .map(|fd| libc::pollfd { fd, events: libc::POLLIN, revents: 0 })
                .collect();
            let ms = timeout.as_millis().min(i32::MAX as u128) as i32;
            unsafe { libc::poll(fds.as_mut_ptr(), fds.len() as _, ms) };

            if fds[0].revents != 0 {
                let mut b = [0u8; 64];
                while unsafe { libc::read(self.wake_r, b.as_mut_ptr() as *mut _, b.len()) } > 0 {}
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
                let mut ev = [0u8; 24 * 32];
                let n = unsafe { libc::read(pfd.fd, ev.as_mut_ptr() as *mut _, ev.len()) };
                if n <= 0 {
                    continue;
                }
                for e in ev[..n as usize].chunks_exact(24) {
                    let ty = u16::from_ne_bytes([e[16], e[17]]);
                    let code = u16::from_ne_bytes([e[18], e[19]]);
                    let value = i32::from_ne_bytes([e[20], e[21], e[22], e[23]]);
                    let d = &mut inputs[i];
                    match (ty, code) {
                        (EV_KEY, KEY_POWER) => {
                            if value == 1 {
                                power_down = Some(Instant::now());
                                power_fired = false;
                            } else if value == 0 {
                                if power_down.take().is_some() && !power_fired {
                                    self.key(Key::Return);
                                }
                            }
                        }
                        (EV_KEY, BTN_TOUCH) => d.down = value != 0,
                        (EV_KEY, c) if value >= 1 => {
                            if let Some(k) = map_key(c) {
                                self.key(k);
                            }
                        }
                        (EV_ABS, ABS_MT_SLOT) => d.slot = value,
                        (EV_ABS, ABS_MT_POSITION_X) if d.slot == 0 => d.x = value,
                        (EV_ABS, ABS_X) => d.x = value,
                        (EV_ABS, ABS_MT_POSITION_Y) if d.slot == 0 => d.y = value,
                        (EV_ABS, ABS_Y) => d.y = value,
                        (EV_ABS, ABS_MT_TRACKING_ID) if d.slot == 0 => d.down = value >= 0,
                        (EV_SYN, 0) if d.axes.is_some() => {
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
                unsafe { libc::close(inputs[i].fd) };
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
