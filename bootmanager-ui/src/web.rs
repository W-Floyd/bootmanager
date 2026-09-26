//! The browser build: the page is served by the backend, so the backend is
//! the page's own host, at /ws. One text frame per JSON message each way.

use crate::app::{with_app, App, Mode};
use crate::screens::UrlMode;
use crate::AppWindow;
use slint::ComponentHandle;
use std::rc::Rc;
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;
use web_sys::{MessageEvent, WebSocket};

pub fn run() {
    let ui = AppWindow::new().expect("create window");
    let host = web_sys::window().and_then(|w| w.location().host().ok()).unwrap_or_default();
    ui.set_hint("Arrow keys and Enter, Esc for back, or click".into());
    let app = App::install(&ui, Mode::Browser { host: host.clone() });
    app.set_url_opener(Box::new(open_url));
    sync_url(&app);
    connect(format!("ws://{host}/ws"));
    ui.run().expect("run");
}

fn path() -> String {
    web_sys::window().and_then(|w| w.location().pathname().ok()).unwrap_or_default()
}

/// The screen is the URL path (routes.rs): each screen change is a history
/// entry, so the browser's Back and Forward walk what was visited; a loaded
/// URL opens its screen, Back stack included; and the backend answers any
/// such path with this page.
fn sync_url(app: &Rc<App>) {
    let Some(window) = web_sys::window() else { return };
    if let Ok(history) = window.history() {
        app.set_route_listener(Box::new(move |route| {
            if path() != route {
                let _ = history.push_state_with_url(&wasm_bindgen::JsValue::NULL, "", Some(route));
            }
        }));
    }
    app.navigate_to(&path());

    let onpop = Closure::<dyn FnMut()>::new(|| with_app(|a| a.navigate_to(&path())));
    let _ = window.add_event_listener_with_callback("popstate", onpop.as_ref().unchecked_ref());
    onpop.forget();
}

fn connect(url: String) {
    let Ok(ws) = WebSocket::new(&url) else { return retry(url) };

    let sender = ws.clone();
    let onopen = Closure::<dyn FnMut()>::new(move || {
        let s = sender.clone();
        with_app(|a| a.connected(Box::new(move |line: String| drop(s.send_with_str(&line)))));
    });
    ws.set_onopen(Some(onopen.as_ref().unchecked_ref()));
    onopen.forget();

    let onmessage = Closure::<dyn FnMut(MessageEvent)>::new(|e: MessageEvent| {
        if let Some(text) = e.data().as_string() {
            with_app(|a| a.line(&text));
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

/// Files come from the backend's files port, a separate origin from this
/// page. Preview fills index.html's #preview overlay with the file in a
/// frame, shown by the browser's own viewer; downloads go through an
/// `<a download>` click, which leaves this page alone even on an error.
fn open_url(port: &str, path: &str, mode: UrlMode) {
    let Some(window) = web_sys::window() else { return };
    let Some(doc) = window.document() else { return };
    let loc = window.location();
    let (proto, host) = (loc.protocol().unwrap_or_default(), loc.hostname().unwrap_or_default());
    let url = format!("{proto}//{host}:{port}{path}");
    let download = format!("{url}?download=1");
    let name = percent_encoding::percent_decode_str(path.rsplit('/').next().unwrap_or("file")).decode_utf8_lossy().into_owned();
    match mode {
        UrlMode::Tab => {
            let _ = window.open_with_url_and_target_and_features(&url, "_blank", "noopener");
        }
        UrlMode::Download => {
            let Ok(el) = doc.create_element("a") else { return };
            let a: web_sys::HtmlAnchorElement = el.unchecked_into();
            a.set_href(&download);
            a.set_download(&name);
            a.click();
        }
        UrlMode::Preview => {
            let set = |id: &str, attr: &str, val: &str| {
                if let Some(el) = doc.get_element_by_id(id) {
                    let _ = el.set_attribute(attr, val);
                }
            };
            if let Some(el) = doc.get_element_by_id("preview-name") {
                el.set_text_content(Some(&name));
            }
            set("preview-tab", "href", &url);
            set("preview-download", "href", &download);
            set("preview-frame", "src", &url);
            if let Some(el) = doc.get_element_by_id("preview") {
                let _ = el.remove_attribute("hidden");
            }
        }
    }
}
