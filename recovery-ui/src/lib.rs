//! recovery-ui: the Slint front end of the fogona LinuxBoot stage. See
//! Cargo.toml for the three builds; src/main.rs is the native entry point
//! and `web_main` below the browser's.

slint::include_modules!();

pub mod app;
pub mod proto;

#[cfg(not(target_arch = "wasm32"))]
pub mod transport;

#[cfg(feature = "fbdev")]
pub mod platform;

#[cfg(target_arch = "wasm32")]
mod web;

use std::cell::RefCell;
use std::rc::Rc;

thread_local! {
    /// The one App, on the UI thread, where transports deliver lines.
    pub static APP: RefCell<Option<Rc<app::App>>> = const { RefCell::new(None) };
}

pub fn with_app(f: impl FnOnce(&Rc<app::App>)) {
    APP.with(|a| {
        let app = a.borrow().clone();
        if let Some(app) = app {
            f(&app)
        }
    });
}

#[cfg(target_arch = "wasm32")]
#[wasm_bindgen::prelude::wasm_bindgen(start)]
pub fn web_main() {
    console_error_panic_hook::set_once();
    web::run();
}
