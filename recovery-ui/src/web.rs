//! Browser transport: the page is served by the phone's Remote UI service,
//! so the backend is the page's own host, at /ws. One text frame per JSON
//! line each way.

use crate::{with_app, AppWindow, APP};
use slint::ComponentHandle;
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;
use web_sys::{MessageEvent, WebSocket};

pub fn run() {
    let ui = AppWindow::new().expect("create window");
    let host = web_sys::window().and_then(|w| w.location().host().ok()).unwrap_or_default();
    ui.set_hint("Arrow keys and Enter, Esc for back, or click".into());
    let app = crate::app::App::new(&ui, &host);
    APP.with(|a| *a.borrow_mut() = Some(app));
    connect(format!("ws://{host}/ws"));
    ui.run().expect("run");
}

fn connect(url: String) {
    let ws = match WebSocket::new(&url) {
        Ok(ws) => ws,
        Err(_) => return retry(url),
    };

    let sender = ws.clone();
    let onopen = Closure::<dyn FnMut()>::new(move || {
        let s = sender.clone();
        with_app(|a| {
            a.connected(Box::new(move |line: String| {
                let _ = s.send_with_str(line.trim_end());
            }))
        });
    });
    ws.set_onopen(Some(onopen.as_ref().unchecked_ref()));
    onopen.forget();

    let onmessage = Closure::<dyn FnMut(MessageEvent)>::new(move |e: MessageEvent| {
        if let Some(text) = e.data().as_string() {
            for line in text.lines().filter(|l| !l.is_empty()) {
                with_app(|a| a.line(line));
            }
        }
    });
    ws.set_onmessage(Some(onmessage.as_ref().unchecked_ref()));
    onmessage.forget();

    let onclose = Closure::<dyn FnMut()>::new(move || {
        with_app(|a| a.disconnected());
        retry(url.clone());
    });
    ws.set_onclose(Some(onclose.as_ref().unchecked_ref()));
    onclose.forget();
}

fn retry(url: String) {
    slint::Timer::single_shot(std::time::Duration::from_secs(1), move || connect(url));
}
