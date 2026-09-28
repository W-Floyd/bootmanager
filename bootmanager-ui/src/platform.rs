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
use slint::platform::software_renderer::{
    DrawRectangleArgs, MinimalSoftwareWindow, PhysicalRegion, PremultipliedRgbaColor, RenderingRotation, RepaintBufferType, TargetPixel,
    TargetPixelBuffer,
};
use slint::platform::{EventLoopProxy, Key, Platform, PointerEventButton, WindowAdapter, WindowEvent};
use slint::{Brush, LogicalPosition, PhysicalSize, PlatformError};
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

/// 8x8 ordered-dither thresholds, 0..64.
const BAYER: [[u8; 8]; 8] = [
    [0, 32, 8, 40, 2, 34, 10, 42],
    [48, 16, 56, 24, 50, 18, 58, 26],
    [12, 44, 4, 36, 14, 46, 6, 38],
    [60, 28, 52, 20, 62, 30, 54, 22],
    [3, 35, 11, 43, 1, 33, 9, 41],
    [51, 19, 59, 27, 49, 17, 57, 25],
    [15, 47, 7, 39, 13, 45, 5, 37],
    [63, 31, 55, 23, 61, 29, 53, 21],
];

/// The frame as Slint's render target. Slint truncates gradients to 8 bits
/// per channel (vertical ones to 256 steps along their length), which bands
/// a dark backdrop; plain unrounded gradient rectangles are drawn here
/// instead, in float, dithered on the pixel's screen position so a partial
/// repaint matches what is around it.
struct Canvas<'a>(&'a mut Frame);

impl TargetPixelBuffer for Canvas<'_> {
    type TargetPixel = Px;

    fn line_slice(&mut self, line: usize) -> &mut [Px] {
        let w = self.0.width;
        &mut self.0.px[line * w..][..w]
    }

    fn num_lines(&self) -> usize {
        self.0.height
    }

    fn draw_rectangle(&mut self, a: &DrawRectangleArgs, clip: &PhysicalRegion) -> bool {
        let Brush::LinearGradient(g) = &a.background else { return false };
        let radii = [a.top_left_radius, a.top_right_radius, a.bottom_right_radius, a.bottom_left_radius];
        if radii.iter().any(|r| *r > 0.) || a.border_width > 0. || a.rotation != RenderingRotation::NoRotation {
            return false;
        }
        // Premultiplied, 0..255, with the item's opacity.
        let op = a.alpha as f32 / 255.;
        let stops: Vec<(f32, [f32; 4])> = g
            .stops()
            .map(|s| {
                let c = s.color;
                let al = c.alpha() as f32 * op;
                let k = al / 255.;
                (s.position, [c.red() as f32 * k, c.green() as f32 * k, c.blue() as f32 * k, al])
            })
            .collect();
        if stops.is_empty() {
            return false;
        }
        let grad = Gradient::new(&stops, g.angle(), (a.x, a.y, a.width, a.height));
        let (x0, y0) = (a.x.round() as i32, a.y.round() as i32);
        let (x1, y1) = ((a.x + a.width).round() as i32, (a.y + a.height).round() as i32);
        for (o, s) in clip.iter() {
            let rx0 = o.x.max(x0).max(0);
            let ry0 = o.y.max(y0).max(0);
            let rx1 = (o.x + s.width as i32).min(x1).min(self.0.width as i32);
            let ry1 = (o.y + s.height as i32).min(y1).min(self.0.height as i32);
            grad.fill(self.0, (rx0, ry0, rx1, ry1));
        }
        true
    }
}

/// Entries in a translucent gradient's lookup table: finer than 8 bits can
/// show along any line on this screen, so the table adds nothing the dither
/// can see.
const LUT_LEN: usize = 2048;

/// A linear gradient ready to fill, in fixed point with 8 bits of colour
/// plus the fraction below them that the dither spreads.
///
/// Opaque: along a row each stop segment is linear in x, so a run is a
/// 16.16 start and step per channel, filled eight pixels at a time (which
/// vectorises). Values are taken from the row's x = 0, so a partial repaint
/// computes the same integers as a whole one.
///
/// Translucent: a table along the gradient line of premultiplied colour and
/// of what survives of the pixel underneath, in 8.8.
struct Gradient {
    /// Opaque only: t range [lo, hi), colour at t = 0, d colour / dt.
    segs: Vec<(f64, f64, [f64; 3], [f64; 3])>,
    /// Translucent only.
    lut: Vec<[u32; 4]>,
    // t = base + x * tx + y * ty, at the pixel's centre.
    base: f64,
    tx: f64,
    ty: f64,
}

impl Gradient {
    /// CSS: the gradient line runs through the centre of the rectangle at
    /// the angle (0deg up, clockwise), long enough to reach the corners.
    fn new(stops: &[(f32, [f32; 4])], angle: f32, (x, y, w, h): (f32, f32, f32, f32)) -> Self {
        let (mut segs, mut lut) = (vec![], vec![]);
        if stops.iter().all(|s| s.1[3] >= 255.) {
            let c = |s: &(f32, [f32; 4])| [s.1[0] as f64, s.1[1] as f64, s.1[2] as f64];
            let (first, last) = (stops[0], stops[stops.len() - 1]);
            segs.push((f64::NEG_INFINITY, first.0 as f64, c(&first), [0.; 3]));
            for w in stops.windows(2) {
                let (p0, p1) = (w[0].0 as f64, w[1].0 as f64);
                if p1 <= p0 {
                    continue;
                }
                let (c0, c1) = (c(&w[0]), c(&w[1]));
                let d: [f64; 3] = std::array::from_fn(|j| (c1[j] - c0[j]) / (p1 - p0));
                segs.push((p0, p1, std::array::from_fn(|j| c0[j] - p0 * d[j]), d));
            }
            segs.push((last.0 as f64, f64::INFINITY, c(&last), [0.; 3]));
        } else {
            lut = (0..LUT_LEN)
                .map(|i| {
                    let c = colour_at(stops, i as f32 / (LUT_LEN - 1) as f32);
                    let keep = 1. - c[3] / 255.;
                    // Colour in 8.8; keep as a 16-bit fraction, so under *
                    // keep >> 8 is 8.8 too.
                    [(c[0] * 256.) as u32, (c[1] * 256.) as u32, (c[2] * 256.) as u32, (keep * 65536.) as u32]
                })
                .collect();
        }
        let (sin, cos) = (angle as f64).to_radians().sin_cos();
        let len = (w as f64 * sin).abs() + (h as f64 * cos).abs();
        let (cx, cy) = (x as f64 + w as f64 / 2., y as f64 + h as f64 / 2.);
        let (tx, ty) = (sin / len, -cos / len);
        let base = 0.5 + (0.5 - cx) * tx + (0.5 - cy) * ty;
        Gradient { segs, lut, base, tx, ty }
    }

    /// Fills [x0, x1) x [y0, y1) of the frame, dithered on the pixel's
    /// screen position so a partial repaint matches what is around it.
    fn fill(&self, f: &mut Frame, (x0, y0, x1, y1): (i32, i32, i32, i32)) {
        if x0 >= x1 || y0 >= y1 {
            return;
        }
        for y in y0..y1 {
            let row = &mut f.px[y as usize * f.width..][..x1 as usize];
            if self.segs.is_empty() {
                self.blend_row(row, x0, y);
            } else {
                self.opaque_row(row, x0, y);
            }
        }
    }

    /// row[x0..] of row y, which starts at x = 0.
    fn opaque_row(&self, row: &mut [Px], x0: i32, y: i32) {
        const ONE: f64 = 65536.;
        let x1 = row.len() as f64;
        let t0 = self.base + y as f64 * self.ty;
        let bayer = &BAYER[y as usize & 7];
        for &(lo, hi, c0, d) in &self.segs {
            // The pixels whose t is in [lo, hi).
            let (a, b) = if self.tx > 0. {
                (((lo - t0) / self.tx).ceil(), ((hi - t0) / self.tx).ceil())
            } else if self.tx < 0. {
                (((hi - t0) / self.tx).floor() + 1., ((lo - t0) / self.tx).floor() + 1.)
            } else if (lo..hi).contains(&t0) {
                (f64::NEG_INFINITY, f64::INFINITY)
            } else {
                continue;
            };
            let (xs, xe) = (a.max(x0 as f64) as usize, b.min(x1) as usize);
            if xs >= xe {
                continue;
            }
            let v0: [i64; 3] = std::array::from_fn(|j| ((c0[j] + d[j] * t0) * ONE).round() as i64);
            let dv: [i64; 3] = std::array::from_fn(|j| (d[j] * self.tx * ONE).round() as i64);
            let st: [i32; 3] = std::array::from_fn(|j| (v0[j] + xs as i64 * dv[j]) as i32);
            let dv = dv.map(|v| v as i32);
            let pat: [i32; 8] = std::array::from_fn(|k| (bayer[(xs + k) & 7] as i32 * 4 + 2) << 8);
            let px = |v: [i32; 3], k: usize| {
                let ch = |i: usize| ((v[i] + pat[k]) >> 16).clamp(0, 255) as u32;
                ch(0) << 16 | ch(1) << 8 | ch(2)
            };
            let run = &mut row[xs..xe];
            if dv == [0; 3] {
                // One colour along the run (a vertical gradient): eight
                // pixels, repeated.
                let tile: [u32; 8] = std::array::from_fn(|k| px(st, k));
                for (i, p) in run.iter_mut().enumerate() {
                    p.0 = tile[i & 7];
                }
                continue;
            }
            let mut v: [[i32; 8]; 3] = std::array::from_fn(|i| std::array::from_fn(|k| st[i] + k as i32 * dv[i]));
            let mut chunks = run.chunks_exact_mut(8);
            for chunk in &mut chunks {
                for k in 0..8 {
                    chunk[k].0 = px([v[0][k], v[1][k], v[2][k]], k);
                }
                for i in 0..3 {
                    for k in 0..8 {
                        v[i][k] += dv[i] * 8;
                    }
                }
            }
            for (k, p) in chunks.into_remainder().iter_mut().enumerate() {
                p.0 = px([v[0][k], v[1][k], v[2][k]], k);
            }
        }
    }

    /// row[x0..] of row y, which starts at x = 0.
    fn blend_row(&self, row: &mut [Px], x0: i32, y: i32) {
        let last = (LUT_LEN - 1) as i64;
        let scale = last as f64 * 65536.;
        let dti = (self.tx * scale).round() as i64;
        let bayer = &BAYER[y as usize & 7];
        let mut ti = ((self.base + x0 as f64 * self.tx + y as f64 * self.ty) * scale).round() as i64;
        for (i, p) in row[x0 as usize..].iter_mut().enumerate() {
            let e = &self.lut[(ti >> 16).clamp(0, last) as usize];
            p.0 = blended_px(e, dither(bayer[(x0 as usize + i) & 7]), p.0);
            ti += dti;
        }
    }
}

/// The colour at t along the gradient, premultiplied, 0..255.
fn colour_at(stops: &[(f32, [f32; 4])], t: f32) -> [f32; 4] {
    let (first, last) = (stops[0], stops[stops.len() - 1]);
    if t <= first.0 {
        return first.1;
    }
    if t >= last.0 {
        return last.1;
    }
    let k = stops.windows(2).position(|w| t < w[1].0).unwrap_or(0);
    let ((p0, c0), (p1, c1)) = (stops[k], stops[k + 1]);
    let f = if p1 > p0 { (t - p0) / (p1 - p0) } else { 0. };
    std::array::from_fn(|j| c0[j] + (c1[j] - c0[j]) * f)
}

/// An ordered-dither threshold as the 8.8 fraction added before the shift:
/// (b + 0.5) / 64 of one step.
#[inline]
fn dither(b: u8) -> u32 {
    b as u32 * 4 + 2
}

#[inline]
fn blended_px(e: &[u32; 4], d: u32, under: u32) -> u32 {
    let ch = |shift: u32, v: u32| ((((under >> shift & 0xff) * e[3] >> 8) + v + d) >> 8).min(255) << shift;
    ch(16, e[0]) | ch(8, e[1]) | ch(0, e[2])
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
    /// The display mode's refresh rate in Hz, when DRM says.
    refresh: Option<f64>,
}

// --- DRM (drm/drm_mode.h), only to learn the refresh rate: fbdev emulation
// reports pixclock 0 ---

const DRM_IOCTL_MODE_GETRESOURCES: libc::c_ulong = 0xc040_64a0;
const DRM_IOCTL_MODE_GETCRTC: libc::c_ulong = 0xc068_64a1;

#[repr(C)]
#[derive(Default)]
struct DrmModeCardRes {
    fb_id_ptr: u64,
    crtc_id_ptr: u64,
    connector_id_ptr: u64,
    encoder_id_ptr: u64,
    count_fbs: u32,
    count_crtcs: u32,
    count_connectors: u32,
    count_encoders: u32,
    min_width: u32,
    max_width: u32,
    min_height: u32,
    max_height: u32,
}

#[repr(C)]
#[derive(Default)]
#[allow(dead_code)]
struct DrmModeModeinfo {
    clock: u32, // kHz
    hdisplay: u16,
    hsync_start: u16,
    hsync_end: u16,
    htotal: u16,
    hskew: u16,
    vdisplay: u16,
    vsync_start: u16,
    vsync_end: u16,
    vtotal: u16,
    vscan: u16,
    vrefresh: u32,
    flags: u32,
    type_: u32,
    name: [u8; 32],
}

#[repr(C)]
#[derive(Default)]
#[allow(dead_code)]
struct DrmModeCrtc {
    set_connectors_ptr: u64,
    count_connectors: u32,
    crtc_id: u32,
    fb_id: u32,
    x: u32,
    y: u32,
    gamma_size: u32,
    mode_valid: u32,
    mode: DrmModeModeinfo,
}

/// The refresh rate, in Hz, of the mode scanning out a width x height
/// picture on any DRM card, from the mode's timings (59.94, not 60).
fn drm_refresh(width: usize, height: usize) -> Option<f64> {
    let mut found = None;
    for i in 0..8 {
        let Ok(card) = std::fs::File::open(format!("/dev/dri/card{i}")) else { continue };
        let fd = card.as_raw_fd();
        let mut res = DrmModeCardRes::default();
        if unsafe { libc::ioctl(fd, DRM_IOCTL_MODE_GETRESOURCES as _, &mut res) } < 0 || res.count_crtcs == 0 {
            continue;
        }
        let mut ids = vec![0u32; res.count_crtcs as usize];
        res = DrmModeCardRes { crtc_id_ptr: ids.as_mut_ptr() as u64, count_crtcs: res.count_crtcs, ..Default::default() };
        if unsafe { libc::ioctl(fd, DRM_IOCTL_MODE_GETRESOURCES as _, &mut res) } < 0 {
            continue;
        }
        for &id in ids.iter().take(res.count_crtcs as usize) {
            let mut crtc = DrmModeCrtc { crtc_id: id, ..Default::default() };
            if unsafe { libc::ioctl(fd, DRM_IOCTL_MODE_GETCRTC as _, &mut crtc) } < 0 || crtc.mode_valid == 0 {
                continue;
            }
            let m = &crtc.mode;
            let total = m.htotal as f64 * m.vtotal as f64;
            let hz = if m.clock > 0 && total > 0.0 { m.clock as f64 * 1000.0 / total } else { m.vrefresh as f64 };
            if hz <= 0.0 {
                continue;
            }
            // The one showing our framebuffer, else the first lit at all.
            if (m.hdisplay as usize, m.vdisplay as usize) == (width, height) {
                return Some(hz);
            }
            found.get_or_insert(hz);
        }
    }
    found
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
            refresh: drm_refresh(var.xres as usize, var.yres as usize),
        };
        eprintln!(
            "bootmanager-ui: {} {}x{} ({} mm) {} bpp, rgb at {}/{}/{}, stride {}, {}",
            fb_name(),
            fb.xres,
            fb.yres,
            fb.width_mm,
            var.bits_per_pixel,
            fb.rgb[0].offset,
            fb.rgb[1].offset,
            fb.rgb[2].offset,
            fb.stride,
            fb.refresh.map_or("refresh unknown, 60 Hz assumed".to_string(), |hz| format!("{hz:.2} Hz"))
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

    /// Whether there was anything to draw.
    fn draw(&self) -> bool {
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
            let region = renderer.render_into_buffer(&mut Canvas(&mut frame));
            let t1 = Instant::now();
            let (o, s) = (region.bounding_box_origin(), region.bounding_box_size());
            if let Target::Fb(fb) = &self.target {
                fb.present(&frame, o.x.max(0) as usize, o.y.max(0) as usize, s.width as usize, s.height as usize);
            }
            if let Some(t) = &self.timing {
                t.borrow_mut().add(t1 - t0, t1.elapsed(), s.width as u64 * s.height as u64, (w * h) as u64);
            }
        })
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
        // An animation frame: one refresh of the panel, 60 Hz where DRM
        // does not say (or says something implausible).
        let hz = match &self.target {
            Target::Fb(fb) => fb.refresh.filter(|hz| (20.0..=500.0).contains(hz)).unwrap_or(60.0),
            Target::Memory => 60.0,
        };
        let frame = Duration::from_secs_f64(1.0 / hz);
        // When the last frame was started: at most one is drawn per
        // refresh, however often input asks (a touch panel reports faster
        // than the display shows, and a drag moves the cards on each report).
        let mut last_frame = Instant::now() - frame;

        loop {
            let frame_start = Instant::now();
            slint::platform::update_timers_and_animations();
            let jobs: Vec<_> = self.shared.queue.lock().unwrap().drain(..).collect();
            for job in jobs {
                job();
            }
            if self.shared.quit.load(Ordering::SeqCst) {
                return Ok(());
            }
            if last_frame.elapsed() >= frame && self.draw() {
                last_frame = frame_start;
            }

            // How long poll may sleep: the next Slint timer, the rest of the
            // frame, the long-press deadline, or the input rescan (a device
            // whose driver probes late shows up within 3 s). The frame is
            // timed from its start, not from the end of drawing it: a fixed
            // 16 ms after a 13 ms frame ran animations at 35 fps. Until the
            // frame is over a redraw may be waiting, so poll wakes for it;
            // after that, only an animation needs the next one.
            let mut timeout = Duration::from_secs(3);
            let since = last_frame.elapsed();
            if since < frame {
                timeout = frame - since;
            } else if self.window.has_active_animations() {
                timeout = frame;
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
            // Rounded up: rounded down, the last millisecond of a frame
            // would spin through poll(0).
            let ms = timeout.as_micros().div_ceil(1000).min(i32::MAX as u128) as i32;
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

#[cfg(test)]
mod tests {
    use super::*;

    /// The float fill Gradient replaced, per pixel, as the reference.
    fn reference(f: &mut Frame, stops: &[(f32, [f32; 4])], angle: f32, (x, y, w, h): (f32, f32, f32, f32)) {
        let opaque = stops.iter().all(|s| s.1[3] >= 255.);
        let (sin, cos) = angle.to_radians().sin_cos();
        let len = (w * sin).abs() + (h * cos).abs();
        let (cx, cy) = (x + w / 2., y + h / 2.);
        for py in y as usize..(y + h) as usize {
            for px in x as usize..(x + w) as usize {
                let t = 0.5 + ((px as f32 + 0.5 - cx) * sin - (py as f32 + 0.5 - cy) * cos) / len;
                let c = colour_at(stops, t);
                let d = (BAYER[py & 7][px & 7] as f32 + 0.5) / 64.;
                let keep = if opaque { 0. } else { 1. - c[3] / 255. };
                let p = &mut f.px[py * f.width + px];
                let ch = |shift: u32, v: f32| {
                    let under = ((p.0 >> shift) & 0xff) as f32;
                    ((under * keep + v + d) as u32).min(255) << shift
                };
                p.0 = ch(16, c[0]) | ch(8, c[1]) | ch(0, c[2]);
            }
        }
    }

    fn frame(w: usize, h: usize, fill: u32) -> Frame {
        Frame { px: vec![Px(fill); w * h], width: w, height: h }
    }

    fn stops(a: [u8; 4], b: [u8; 4]) -> Vec<(f32, [f32; 4])> {
        [(0., a), (1., b)]
            .map(|(p, c)| {
                let k = c[3] as f32 / 255.;
                (p, [c[0] as f32 * k, c[1] as f32 * k, c[2] as f32 * k, c[3] as f32])
            })
            .to_vec()
    }

    fn max_diff(a: &Frame, b: &Frame) -> u32 {
        a.px.iter()
            .zip(&b.px)
            .map(|(p, q)| (0..3).map(|i| ((p.0 >> (i * 8)) & 0xff).abs_diff((q.0 >> (i * 8)) & 0xff)).max().unwrap())
            .max()
            .unwrap()
    }

    #[test]
    fn matches_the_float_fill() {
        let (w, h) = (96, 160);
        let rect = (0., 0., w as f32, h as f32);
        for (name, s, angle, under) in [
            ("vertical, dark", stops([8, 10, 20, 255], [30, 34, 60, 255]), 180., 0),
            ("diagonal", stops([200, 20, 40, 255], [10, 90, 250, 255]), 135., 0),
            ("horizontal", stops([0, 0, 0, 255], [255, 255, 255, 255]), 90., 0),
            ("right to left", stops([0, 0, 0, 255], [255, 255, 255, 255]), 270., 0),
            ("backdrop", stops([8, 10, 20, 255], [30, 34, 60, 255]), 170., 0),
            ("up, leaning", stops([8, 10, 20, 255], [30, 34, 60, 255]), 10., 0),
            ("translucent", stops([255, 255, 255, 64], [0, 0, 0, 200]), 160., 0x3050a0),
        ] {
            let (mut a, mut b) = (frame(w, h, under), frame(w, h, under));
            reference(&mut a, &s, angle, rect);
            Gradient::new(&s, angle, rect).fill(&mut b, (0, 0, w as i32, h as i32));
            let d = max_diff(&a, &b);
            assert!(d <= 1, "{name}: differs by {d}");
        }
    }

    #[test]
    fn a_partial_fill_matches_the_whole() {
        let (w, h) = (64, 64);
        let s = stops([10, 20, 30, 255], [90, 60, 30, 255]);
        for angle in [200., 170., 180., 90., 270.] {
            let g = Gradient::new(&s, angle, (0., 0., w as f32, h as f32));
            let (mut whole, mut part) = (frame(w, h, 0), frame(w, h, 0));
            g.fill(&mut whole, (0, 0, w as i32, h as i32));
            g.fill(&mut part, (0, 0, w as i32, h as i32));
            g.fill(&mut part, (13, 7, 41, 29));
            assert_eq!(max_diff(&whole, &part), 0, "{angle}");
        }
    }

    /// cargo test --release -- --ignored --nocapture
    #[test]
    #[ignore]
    fn timing() {
        let (w, h) = (1080, 2340);
        let rect = (0., 0., w as f32, h as f32);
        let dark = stops([8, 10, 20, 255], [30, 34, 60, 255]);
        let scrim = stops([0, 0, 0, 0], [0, 0, 0, 136]);
        for (name, s, angle) in [("vertical", &dark, 180.), ("170deg", &dark, 170.), ("scrim", &scrim, 180.)] {
            let mut f = frame(w, h, 0);
            let t = Instant::now();
            for _ in 0..10 {
                reference(&mut f, &s, angle, rect);
            }
            let old = t.elapsed() / 10;
            let t = Instant::now();
            for _ in 0..10 {
                Gradient::new(&s, angle, rect).fill(&mut f, (0, 0, w as i32, h as i32));
            }
            let new = t.elapsed() / 10;
            println!("{name} {w}x{h}: float {old:?}, fixed point {new:?}");
        }
    }
}
