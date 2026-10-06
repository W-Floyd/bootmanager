//! The frame rate a dev stage shows on screen. The fbdev platform publishes
//! the frames drawn in the last second (it draws only damaged frames, so an
//! idle UI draws none); the app reads it on a timer.

use std::sync::atomic::{AtomicBool, AtomicU32, Ordering};

/// Set by the platform in a dev stage (/etc/bootmanager/dev).
pub static ON: AtomicBool = AtomicBool::new(false);
static FPS_X10: AtomicU32 = AtomicU32::new(0);
static TICK: AtomicU32 = AtomicU32::new(0);

pub fn enabled() -> bool {
    ON.load(Ordering::Relaxed)
}

/// `frames` drawn in `span_ms`.
pub fn publish(frames: u32, span_ms: u128) {
    FPS_X10.store((frames as u128 * 10_000 / span_ms.max(1)) as u32, Ordering::Relaxed);
    TICK.fetch_add(1, Ordering::Relaxed);
}

/// The last published rate in tenths of a frame per second, and a counter
/// that moves with each publish: when it stops moving the UI is idle.
pub fn read() -> (u32, u32) {
    (FPS_X10.load(Ordering::Relaxed), TICK.load(Ordering::Relaxed))
}
