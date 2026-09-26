//! bootmanager-ui, native.
//!
//! On the phone (default build), started by bootmanager:
//!     bootmanager-ui [--socket /tmp/bootmanager.sock] [--fb /dev/fb0]
//!
//! Headless, for layout work anywhere (fbdev build): render to a PNG after
//! feeding it keys, against any backend (bootmanager -scan DIR works):
//!     bootmanager-ui --socket S --screenshot out.png [--keys down,down,enter]
//!         keys: up down enter back wait (each step 400 ms apart)
//!
//! On the host (desktop build), against the phone's Remote UI service:
//!     cargo run --release --no-default-features --features desktop -- \
//!         --connect 172.16.42.1:7777

#[cfg(target_arch = "wasm32")]
fn main() {}

#[cfg(not(target_arch = "wasm32"))]
fn main() {
    use bootmanager_ui::transport::{self, Addr};
    use bootmanager_ui::{app::App, AppWindow, APP};
    use slint::ComponentHandle;

    let mut socket = "/tmp/bootmanager.sock".to_string();
    let mut connect: Option<String> = None;
    #[allow(unused_mut, unused_variables)]
    let (mut fb, mut screenshot, mut keys) = ("/dev/fb0".to_string(), None::<String>, String::new());
    let mut args = std::env::args().skip(1);
    while let Some(a) = args.next() {
        let mut val = || args.next().unwrap_or_else(|| panic!("{a} needs a value"));
        match a.as_str() {
            "--socket" => socket = val(),
            "--connect" => connect = Some(val()),
            "--fb" => fb = val(),
            "--screenshot" => screenshot = Some(val()),
            "--keys" => keys = val(),
            _ => {
                eprintln!("usage: bootmanager-ui [--socket PATH | --connect HOST:PORT] [--fb DEV] [--screenshot PNG [--keys LIST]]");
                std::process::exit(2);
            }
        }
    }

    #[cfg(feature = "fbdev")]
    let buffer = {
        use bootmanager_ui::platform::{Fb, FbPlatform, Target};
        let size = slint::PhysicalSize::new(720, 1600);
        let target = if screenshot.is_some() {
            Target::Memory
        } else {
            match Fb::open(&fb) {
                Ok(f) => Target::Fb(f),
                Err(e) => {
                    eprintln!("bootmanager-ui: {fb}: {e}");
                    std::process::exit(1);
                }
            }
        };
        let p = FbPlatform::new(target, size);
        let buffer = p.buffer.clone();
        slint::platform::set_platform(Box::new(p)).expect("platform");
        buffer
    };

    let ui = AppWindow::new().expect("create window");
    let (addr, where_) = match connect {
        Some(c) => (Addr::Tcp(c.clone()), c),
        None => (Addr::Unix(socket), String::new()),
    };
    if !cfg!(feature = "fbdev") {
        ui.set_hint("Arrow keys and Enter, Esc for back, or click".into());
    }
    let app = App::new(&ui, &where_);
    // On the phone's own panel (and headless, which renders its size),
    // lay out around its cutout; a remote window is not that shape.
    if cfg!(feature = "fbdev") && where_.is_empty() {
        app.use_panel_shape();
    }
    APP.with(|a| *a.borrow_mut() = Some(app));
    transport::start(addr);

    #[cfg(feature = "fbdev")]
    if let Some(out) = &screenshot {
        let steps: Vec<String> = keys.split(',').filter(|s| !s.is_empty()).map(String::from).collect();
        run_script(ui.as_weak(), steps, 0);
        ui.show().unwrap();
        slint::run_event_loop().unwrap();
        save_png(out, &buffer.borrow(), 720, 1600);
        return;
    }

    ui.run().expect("run");
}

#[cfg(feature = "fbdev")]
fn run_script(ui: slint::Weak<bootmanager_ui::AppWindow>, steps: Vec<String>, i: usize) {
    use slint::platform::{Key, WindowEvent};
    use slint::ComponentHandle;
    // The first step waits for the connection and the first replies.
    let delay = if i == 0 { 800 } else { 400 };
    slint::Timer::single_shot(std::time::Duration::from_millis(delay), move || {
        let Some(u) = ui.upgrade() else { return };
        let Some(step) = steps.get(i) else {
            let _ = slint::quit_event_loop();
            return;
        };
        let key = match step.as_str() {
            "up" => Some(Key::UpArrow),
            "down" => Some(Key::DownArrow),
            "enter" => Some(Key::Return),
            "back" => Some(Key::Escape),
            _ => None,
        };
        if let Some(k) = key {
            let text: slint::SharedString = k.into();
            u.window().dispatch_event(WindowEvent::KeyPressed { text: text.clone() });
            u.window().dispatch_event(WindowEvent::KeyReleased { text });
        }
        run_script(ui, steps, i + 1);
    });
}

#[cfg(feature = "fbdev")]
fn save_png(path: &str, buf: &[bootmanager_ui::platform::Px], w: u32, h: u32) {
    let file = std::fs::File::create(path).expect("create png");
    let mut enc = png::Encoder::new(std::io::BufWriter::new(file), w, h);
    enc.set_color(png::ColorType::Rgb);
    let mut writer = enc.write_header().unwrap();
    let data: Vec<u8> = buf.iter().flat_map(|p| [(p.0 >> 16) as u8, (p.0 >> 8) as u8, p.0 as u8]).collect();
    writer.write_image_data(&data).unwrap();
}
