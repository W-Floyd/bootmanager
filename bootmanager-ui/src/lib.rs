//! bootmanager-ui: the boot manager's Slint UI. See Cargo.toml for the two
//! builds; src/main.rs is the panel's entry point and `web_main` below the
//! browser's.

slint::include_modules!();

pub mod app;
pub mod proto;
pub mod routes;
pub mod screens;

#[cfg(feature = "fbdev")]
pub mod platform;
#[cfg(feature = "fbdev")]
pub mod transport;

#[cfg(target_arch = "wasm32")]
mod web;

#[cfg(target_arch = "wasm32")]
#[wasm_bindgen::prelude::wasm_bindgen(start)]
pub fn web_main() {
    console_error_panic_hook::set_once();
    web::run();
}
