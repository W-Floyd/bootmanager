//! Browser transport: the page is served by the phone's Remote UI service,
//! so the backend is the page's own host, at /ws. One text frame per JSON
//! line each way.

use crate::app::UrlMode;
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
    app.set_url_opener(Box::new(open_url));
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
                let _ = s.send_with_str(&line);
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

/// Preview fills index.html's #preview overlay: the file in a frame, shown
/// by the browser's own viewer (the server sandboxes it). Downloads go
/// through an `<a download>` click, which leaves this page alone even if
/// the server answers with an error.
fn open_url(url: &str, mode: UrlMode) {
    let Some(window) = web_sys::window() else { return };
    let Some(doc) = window.document() else { return };
    let name = url.rsplit('/').next().unwrap_or("file");
    let download = format!("{url}?download=1");
    match mode {
        UrlMode::Tab => {
            let _ = window.open_with_url_and_target(url, "_blank");
        }
        UrlMode::Download => {
            let Ok(el) = doc.create_element("a") else { return };
            let a: web_sys::HtmlAnchorElement = el.unchecked_into();
            a.set_href(&download);
            a.set_download(name);
            a.click();
        }
        UrlMode::Preview => {
            let set = |id: &str, attr: &str, val: &str| {
                if let Some(el) = doc.get_element_by_id(id) {
                    let _ = el.set_attribute(attr, val);
                }
            };
            if let Some(el) = doc.get_element_by_id("preview-name") {
                el.set_text_content(Some(&decode_name(name)));
            }
            set("preview-tab", "href", url);
            set("preview-download", "href", &download);
            set("preview-frame", "src", url);
            if let Some(el) = doc.get_element_by_id("preview") {
                let _ = el.remove_attribute("hidden");
            }
        }
    }
}

/// Undoes the percent-encoding of a URL's last segment, for the title.
fn decode_name(s: &str) -> String {
    let b = s.as_bytes();
    let mut out = Vec::with_capacity(b.len());
    let mut i = 0;
    while i < b.len() {
        if b[i] == b'%' && i + 2 < b.len() {
            if let Ok(v) = u8::from_str_radix(&s[i + 1..i + 3], 16) {
                out.push(v);
                i += 3;
                continue;
            }
        }
        out.push(b[i]);
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

fn retry(url: String) {
    slint::Timer::single_shot(std::time::Duration::from_secs(1), move || connect(url));
}
