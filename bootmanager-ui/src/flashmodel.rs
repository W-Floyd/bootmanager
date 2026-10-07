//! The flash screen's estimate of how far along a flash is, in time.
//!
//! An image comes in pieces; each is received (seconds) and then written
//! (about a second). What the server reports is stepwise: how much of this
//! piece has come in, how much has been written, and how far into the image
//! the last finished piece reached. Drawn as they are, the bar creeps while a
//! piece comes in and then leaps when it is written, and a sparse piece spans
//! more of the image than it holds (the holes it skips).
//!
//! This keeps what has been seen of how long a piece takes to receive and to
//! write, and what image span a piece covers, and from them says how far
//! through the piece the flash is, in time, and so how far through the image:
//! one number that only moves forward, at the pace the flash does. It is
//! learned as it goes: the first piece uses defaults, the rest what the
//! pieces before them took.

use crate::proto::Flash;

/// How much of a piece's time goes on receiving it, before any piece has
/// been timed: the host sends at about 35 MB/s and the device writes at
/// about 200 MB/s.
const DEFAULT_RECV_SHARE: f64 = 0.85;

/// The most the bar moves for one event, of the whole: events come about
/// eight a second, so this is 6% a second at most, against about 1.3% a second
/// that a flash of this size goes at.
const MAX_STEP: f32 = 0.008;

#[derive(Default)]
pub struct FlashModel {
    name: String,
    total: i64,
    piece: i64,
    /// Where in the image the finished pieces reach.
    prev_end: i64,
    /// What a piece covers of the image, as last seen (an average of them).
    span: Option<f64>,
    /// How long a piece took to receive and to write, as last seen.
    recv_ms: Option<f64>,
    write_ms: Option<f64>,
    /// The event clock (Flash::t) at which this piece's phases began.
    recv_t0: Option<i64>,
    write_t0: Option<i64>,
    recv_bytes: i64,
    /// What this piece has been measured as, once it is.
    measured_recv: bool,
    /// Never goes back: 0 to 1.
    overall: f32,
    /// Something has been reported of this image.
    seen: bool,
    eta_ms: Option<i64>,
}

/// The EMA that follows the pieces: a new one counts for a third.
fn ema(old: Option<f64>, new: f64) -> f64 {
    old.map_or(new, |o| o * 0.66 + new * 0.34)
}

impl FlashModel {
    pub fn overall(&self) -> f32 {
        self.overall
    }

    /// About how many milliseconds the flash has left, once a piece has been
    /// timed.
    pub fn eta_ms(&self) -> Option<i64> {
        self.eta_ms
    }

    pub fn reset(&mut self) {
        *self = FlashModel::default();
    }

    /// Takes the next event.
    pub fn update(&mut self, f: &Flash) {
        if f.phase.is_empty() {
            self.reset();
            return;
        }
        // Another image: another partition, or a piece count that went back.
        if f.name != self.name || f.total != self.total || f.piece < self.piece {
            self.reset();
            self.name = f.name.clone();
            self.total = f.total;
        }
        self.piece = f.piece;
        let total = self.total.max(1) as f64;
        if f.phase == "finished" {
            self.overall = 1.0;
            self.eta_ms = Some(0);
            return;
        }
        let share = self.recv_share();
        // How far through this piece, 0 to 1, in time.
        let frac = match f.phase.as_str() {
            "receiving" => {
                // While a piece comes in, the server's `end` is where the
                // pieces before it reach: a screen that came in late has no
                // other way to know.
                self.prev_end = self.prev_end.max(f.end);
                if self.recv_t0.is_none() {
                    self.recv_t0 = Some(f.t);
                    self.recv_bytes = f.recv_total;
                    self.measured_recv = false;
                }
                share * ratio(f.recv, f.recv_total)
            }
            "writing" => {
                if self.write_t0.is_none() {
                    self.write_t0 = Some(f.t);
                    self.recv_bytes = f.recv_total.max(self.recv_bytes);
                }
                if let (Some(t0), false) = (self.recv_t0, self.measured_recv) {
                    if let Some(w0) = self.write_t0 {
                        self.recv_ms = Some(ema(self.recv_ms, (w0 - t0).max(1) as f64));
                        self.measured_recv = true;
                    }
                }
                let share = self.recv_share();
                share + (1.0 - share) * ratio(f.wrote, f.wrote_total)
            }
            // The piece is out: its real span, and how long it took to write.
            _ => {
                if let Some(w0) = self.write_t0 {
                    self.write_ms = Some(ema(self.write_ms, (f.t - w0).max(1) as f64));
                }
                let span = (f.end - self.prev_end).max(0) as f64;
                if span > 0.0 {
                    self.span = Some(ema(self.span, span));
                }
                self.prev_end = f.end.max(self.prev_end);
                self.recv_t0 = None;
                self.write_t0 = None;
                self.measured_recv = false;
                1.0
            }
        };
        let left = (self.total - self.prev_end).max(0) as f64;
        // What the next piece will cover, until one has shown it: as many
        // image bytes as it holds, and no more than is left.
        let span = self.span.unwrap_or(self.recv_bytes.max(1) as f64).min(left.max(0.0));
        let at = if f.phase == "waiting" || f.phase == "done" {
            self.prev_end as f64
        } else {
            self.prev_end as f64 + span * frac
        };
        // Only forward, and not to the end before the end: that is "finished".
        // A step is capped: an estimate that turned out low (a piece that
        // spanned more of the image than it held) is closed over some
        // events, not in one leap.
        let now = (at / total).min(0.995) as f32;
        if self.seen {
            self.overall += (now - self.overall).clamp(0.0, MAX_STEP);
        } else {
            // The first report: where it is, as a screen that came in late
            // finds it, not a climb up to it.
            (self.overall, self.seen) = (now, true);
        }
        self.eta_ms = match (self.recv_ms, self.write_ms) {
            (Some(r), Some(w)) if span > 0.0 => {
                let rest = (self.total as f64 - at).max(0.0);
                Some((rest / span * (r + w)) as i64)
            }
            _ => None,
        };
    }

    fn recv_share(&self) -> f64 {
        match (self.recv_ms, self.write_ms) {
            (Some(r), Some(w)) => r / (r + w).max(1.0),
            _ => DEFAULT_RECV_SHARE,
        }
    }
}

fn ratio(a: i64, b: i64) -> f64 {
    if b <= 0 {
        0.0
    } else {
        (a as f64 / b as f64).clamp(0.0, 1.0)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const MB: i64 = 1 << 20;

    fn ev(phase: &str, piece: i64, t: i64) -> Flash {
        Flash { phase: phase.into(), name: "sde27".into(), piece, total: 3000 * MB, t, ..Default::default() }
    }

    /// Nine pieces as the handset showed: about 250 MB each, 7.5 s to receive
    /// and 1.2 s to write, spanning more of the image than they hold (holes).
    #[test]
    fn a_flash_moves_forward_smoothly_to_the_end() {
        let mut m = FlashModel::default();
        let (mut t, mut end, mut last, mut worst_step) = (0i64, 0i64, 0f32, 0f32);
        let mut seen = vec![];
        for piece in 1..=9 {
            let span = 340 * MB;
            // Receiving, an event every 120 ms.
            let n = 7500 / 120;
            for k in 1..=n {
                t += 120;
                m.update(&Flash { recv: 250 * MB * k / n, recv_total: 250 * MB, end, ..ev("receiving", piece, t) });
                seen.push(m.overall());
            }
            let m_writes = 1200 / 120;
            for k in 1..=m_writes {
                t += 120;
                m.update(&Flash {
                    recv: 250 * MB,
                    recv_total: 250 * MB,
                    wrote: 250 * MB * k / m_writes,
                    wrote_total: 250 * MB,
                    end: end + span * k / m_writes,
                    ..ev("writing", piece, t)
                });
                seen.push(m.overall());
            }
            end += span;
            t += 100;
            m.update(&Flash { end, wrote: 250 * MB, wrote_total: 250 * MB, ..ev("waiting", piece, t) });
            seen.push(m.overall());
        }
        for o in &seen {
            assert!(*o >= last, "went back: {last} -> {o}");
            worst_step = worst_step.max(*o - last);
            last = *o;
        }
        assert!(worst_step < 0.01, "a step of {worst_step} in one event is not smooth");
        assert!(last > 0.9, "ends at {last} before it is finished");
        m.update(&Flash { end, ..ev("finished", 9, t + 6000) });
        assert_eq!(m.overall(), 1.0);
    }

    #[test]
    fn it_learns_how_long_the_rest_takes() {
        let mut m = FlashModel::default();
        assert_eq!(m.eta_ms(), None, "nothing is known before a piece has been timed");
        let mut t = 0;
        for piece in 1..=2 {
            for k in 1..=10 {
                t += 750;
                m.update(&Flash { recv: 25 * MB * k, recv_total: 250 * MB, end: 340 * MB * (piece - 1), ..ev("receiving", piece, t) });
            }
            t += 100;
            m.update(&Flash { recv_total: 250 * MB, wrote: 0, wrote_total: 250 * MB, ..ev("writing", piece, t) });
            t += 1200;
            m.update(&Flash { end: 340 * MB * piece, wrote: 250 * MB, wrote_total: 250 * MB, ..ev("waiting", piece, t) });
        }
        let eta = m.eta_ms().expect("two pieces in");
        // About 7.5 more pieces of 8.8 s: some tens of seconds, not hours.
        assert!((40_000..90_000).contains(&eta), "eta {eta} ms");
    }

    #[test]
    fn another_image_starts_again() {
        let mut m = FlashModel::default();
        m.update(&Flash { recv: 100 * MB, recv_total: 200 * MB, end: 2000 * MB, ..ev("receiving", 7, 1000) });
        assert!(m.overall() > 0.6);
        m.update(&Flash { name: "boot_a".into(), total: 50 * MB, recv: MB, recv_total: 10 * MB, ..ev("receiving", 1, 2000) });
        assert!(m.overall() < 0.2, "the new image's bar started over: {}", m.overall());
        m.update(&Flash::default());
        assert_eq!(m.overall(), 0.0);
    }
}
