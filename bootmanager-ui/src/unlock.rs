//! `bootmanager-ui --unlock`: ask for a passphrase and print it on stdout,
//! as an initramfs's unlocker does, to be piped to cryptsetup:
//!
//! ```text
//! bootmanager-ui --unlock | cryptsetup open /dev/X root -
//! ```
//!
//! The passphrase is written as typed, with no newline, and the exit status
//! is 0 once one was entered. Nothing else is read or written: no backend,
//! no socket. Like pmOS's own fde-unlock script, it takes the volume from
//! CRYPTTAB_SOURCE and the number of failed tries from CRYPTTAB_TRIED, to
//! say so on screen.

use crate::{UnlockWindow, VirtualKeyboardHandler};
use slint::platform::WindowEvent;
use slint::ComponentHandle;
use std::io::Write;

/// The window, wired: the keyboard's presses become key events on it (as in
/// app.rs), and Return in the field prints and quits.
pub fn window(volume: &str, tried: u32) -> UnlockWindow {
    let ui = UnlockWindow::new().expect("create window");
    ui.set_volume(volume.into());
    if tried > 0 {
        ui.set_message("Incorrect passphrase, try again".into());
    }
    let weak = ui.as_weak();
    ui.global::<VirtualKeyboardHandler>().on_key_pressed(move |key| {
        if let Some(ui) = weak.upgrade() {
            ui.window().dispatch_event(WindowEvent::KeyPressed { text: key.clone() });
            ui.window().dispatch_event(WindowEvent::KeyReleased { text: key });
        }
    });
    let weak = ui.as_weak();
    ui.on_accepted(move || {
        let Some(ui) = weak.upgrade() else { return };
        let pass = ui.get_passphrase();
        if pass.is_empty() {
            return;
        }
        let mut out = std::io::stdout();
        if out.write_all(pass.as_bytes()).and_then(|_| out.flush()).is_err() {
            std::process::exit(1);
        }
        ui.set_passphrase("".into());
        let _ = slint::quit_event_loop();
    });
    ui
}

/// The volume and tries from the environment, as pmOS's fde-unlock gets them.
pub fn from_env() -> (String, u32) {
    let volume = std::env::var("CRYPTTAB_SOURCE").unwrap_or_default();
    let tried = std::env::var("CRYPTTAB_TRIED").ok().and_then(|t| t.parse().ok()).unwrap_or(0);
    (volume, tried)
}
