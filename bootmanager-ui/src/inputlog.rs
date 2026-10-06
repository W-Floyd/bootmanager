//! What the UI saw of the input devices, for the stage's log: the first
//! events of each device and every event while an autoboot countdown runs
//! (at most MAX_LINES lines in all), and the most recent event, which the UI
//! sends with `autoboot.cancel` so the backend can log what stopped the
//! countdown.
//!
//! Lines go to stderr, which the stage's uinit keeps:
//!   bootmanager-ui: ev t=3.041 event2 gt9916 EV_KEY BTN_TOUCH 1
//! t is seconds since the UI started; the device is its event node and name.

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex, OnceLock};
use std::time::Instant;

/// Events logged from each device whatever else is going on.
pub const FIRST_PER_DEVICE: u32 = 40;
/// Event lines in all; after them one line says so and no more are written.
pub const MAX_LINES: u32 = 200;

static START: OnceLock<Instant> = OnceLock::new();
static COUNTDOWN: AtomicBool = AtomicBool::new(false);
static LAST: Mutex<Option<Last>> = Mutex::new(None);

#[derive(Clone)]
struct Last {
    at: Instant,
    dev: Arc<str>,
    kind: u16,
    code: u16,
    value: i32,
}

/// Starts the clock the log's t is on.
pub fn init() {
    START.get_or_init(Instant::now);
}

/// Seconds since init, for log lines. 0 in the web build, where there is no
/// std clock (and nothing is logged).
#[cfg(feature = "fbdev")]
pub fn t() -> f64 {
    START.get_or_init(Instant::now).elapsed().as_secs_f64()
}

#[cfg(not(feature = "fbdev"))]
pub fn t() -> f64 {
    0.0
}

/// The countdown is running (the backend says so); every event is logged
/// while it does.
pub fn set_countdown(running: bool) {
    if COUNTDOWN.swap(running, Ordering::Relaxed) != running {
        eprintln!("bootmanager-ui: t={:.3} autoboot countdown {}", t(), if running { "shown" } else { "gone" });
    }
}

#[derive(Default)]
pub struct Log {
    lines: u32,
    suppressed: bool,
}

impl Log {
    /// Whether to write this device's `count`th event, counting it.
    fn want(&mut self, count: &mut u32) -> bool {
        *count = count.saturating_add(1);
        if *count > FIRST_PER_DEVICE && !COUNTDOWN.load(Ordering::Relaxed) {
            return false;
        }
        if self.lines >= MAX_LINES {
            if !self.suppressed {
                self.suppressed = true;
                eprintln!("bootmanager-ui: input log suppressed (over {MAX_LINES} lines)");
            }
            return false;
        }
        self.lines += 1;
        true
    }
}

/// One event from a device, `dev` being "event2 gt9916". Cheap unless it is
/// logged: the last-event record is a few words, and the names are only
/// looked up for a line that is written.
pub fn event(log: &mut Log, count: &mut u32, dev: &Arc<str>, kind: u16, code: u16, value: i32) {
    // Sync and misc events say nothing about what the user did.
    if kind != 0 && kind != 4 {
        if let Ok(mut l) = LAST.lock() {
            *l = Some(Last { at: Instant::now(), dev: dev.clone(), kind, code, value });
        }
    }
    if log.want(count) {
        eprintln!("bootmanager-ui: ev t={:.3} {dev} {} {} {value}", t(), type_name(kind), code_name(kind, code));
    }
}

/// What the most recent input event was, and how long ago: the cause given
/// for cancelling the countdown.
pub fn last_event() -> Option<(String, u64)> {
    let l = LAST.lock().ok()?.clone()?;
    let text = format!("{} {} {} {}", l.dev, type_name(l.kind), code_name(l.kind, l.code), l.value);
    Some((text, l.at.elapsed().as_millis() as u64))
}

fn type_name(kind: u16) -> String {
    match kind {
        0 => "EV_SYN".into(),
        1 => "EV_KEY".into(),
        2 => "EV_REL".into(),
        3 => "EV_ABS".into(),
        4 => "EV_MSC".into(),
        k => format!("EV_{k:#x}"),
    }
}

#[cfg(feature = "fbdev")]
fn code_name(kind: u16, code: u16) -> String {
    use evdev::{AbsoluteAxisCode, KeyCode, SynchronizationCode};
    match kind {
        0 => format!("{:?}", SynchronizationCode(code)),
        1 => format!("{:?}", KeyCode(code)),
        3 => format!("{:?}", AbsoluteAxisCode(code)),
        _ => format!("{code:#x}"),
    }
}

#[cfg(not(feature = "fbdev"))]
fn code_name(_kind: u16, code: u16) -> String {
    format!("{code:#x}")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn logs_the_first_events_of_a_device_then_stops() {
        let mut log = Log::default();
        let mut count = 0;
        let logged = (0..100).filter(|_| log.want(&mut count)).count();
        assert_eq!(logged, FIRST_PER_DEVICE as usize);
    }

    #[test]
    fn caps_the_total() {
        let mut log = Log::default();
        let mut counts = [0u32; 10];
        let mut logged = 0;
        for i in 0..1000 {
            if log.want(&mut counts[i % 10]) {
                logged += 1;
            }
        }
        assert!(logged <= MAX_LINES as usize, "{logged}");
        assert!(log.suppressed);
    }

    #[test]
    fn remembers_the_last_event_but_not_syncs() {
        let dev: Arc<str> = "event2 panel".into();
        let (mut log, mut n) = (Log::default(), 0);
        event(&mut log, &mut n, &dev, 1, 330, 1);
        event(&mut log, &mut n, &dev, 0, 0, 0);
        let (text, age) = last_event().unwrap();
        assert!(text.starts_with("event2 panel EV_KEY ") && text.ends_with(" 1"), "{text}");
        assert!(age < 5000);
    }
}
