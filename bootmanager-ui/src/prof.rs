//! A sampling profiler for a dev stage: SIGPROF every millisecond of CPU
//! time records the interrupted program counter. While /tmp/ui-prof exists,
//! `dump` writes the counts to /tmp/ui-prof.out once a second as
//! `base <hex load address>` and `<hex pc> <count>` lines; resolve them on
//! the host against an unstripped build (addr2line -f -i -C -e BIN).
//! Idle polling costs no CPU time, so only drawing and event handling show.

use std::collections::HashMap;
use std::sync::atomic::{AtomicUsize, Ordering};

const CAP: usize = 1 << 16;
static NEXT: AtomicUsize = AtomicUsize::new(0);
static SAMPLES: [AtomicUsize; CAP] = [const { AtomicUsize::new(0) }; CAP];

extern "C" fn on_prof(_: libc::c_int, _: *mut libc::siginfo_t, ctx: *mut libc::c_void) {
    // SAFETY: the kernel passes a ucontext_t for an SA_SIGINFO handler. The
    // program counter field is named per architecture.
    #[cfg(target_arch = "arm")]
    let pc = unsafe { (*(ctx as *mut libc::ucontext_t)).uc_mcontext.arm_pc } as usize;
    #[cfg(not(target_arch = "arm"))]
    let pc = unsafe { (*(ctx as *mut libc::ucontext_t)).uc_mcontext.pc } as usize;
    let i = NEXT.fetch_add(1, Ordering::Relaxed);
    if i < CAP {
        SAMPLES[i].store(pc, Ordering::Relaxed);
    }
}

/// Start sampling (once, in a dev stage).
pub fn start() {
    unsafe {
        let mut sa: libc::sigaction = std::mem::zeroed();
        sa.sa_sigaction = on_prof as *const () as usize;
        sa.sa_flags = libc::SA_SIGINFO | libc::SA_RESTART;
        libc::sigaction(libc::SIGPROF, &sa, std::ptr::null_mut());
        let tv = libc::timeval { tv_sec: 0, tv_usec: 1000 };
        let it = libc::itimerval { it_interval: tv, it_value: tv };
        libc::setitimer(libc::ITIMER_PROF, &it, std::ptr::null_mut());
    }
}

/// Write and clear the samples, when /tmp/ui-prof asks for them.
pub fn dump() {
    let n = NEXT.swap(0, Ordering::Relaxed).min(CAP);
    thread_local!(static TOTAL: std::cell::RefCell<HashMap<usize, u32>> = Default::default());
    if !std::path::Path::new("/tmp/ui-prof").exists() {
        TOTAL.with(|t| t.borrow_mut().clear());
        return;
    }
    // Cumulative while /tmp/ui-prof exists; remove it to start over.
    let counts = TOTAL.with(|t| {
        let mut t = t.borrow_mut();
        for s in &SAMPLES[..n] {
            *t.entry(s.load(Ordering::Relaxed)).or_default() += 1;
        }
        t.clone()
    });
    let base = std::fs::read_to_string("/proc/self/maps")
        .ok()
        .and_then(|m| m.lines().next().and_then(|l| usize::from_str_radix(l.split('-').next()?, 16).ok()))
        .unwrap_or(0);
    let mut out = format!("base {base:x}\n");
    for (pc, c) in counts {
        out += &format!("{pc:x} {c}\n");
    }
    let _ = std::fs::write("/tmp/ui-prof.out", out);
}
