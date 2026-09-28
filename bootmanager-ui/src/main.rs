//! bootmanager-ui on the device's own panel (the fbdev build).
//!
//! Started by bootmanager:
//!     bootmanager-ui [--socket /tmp/bootmanager.sock] [--fb /dev/fb0]
//!
//! Headless, for layout work anywhere: render to a PNG after feeding it
//! keys, against any backend (bootmanager -scan DIR -compatible C works).
//! The picture takes the panel's size, else --size's:
//!     bootmanager-ui --socket S --screenshot out.png [--keys down,down,enter] [--size 720x1600]
//!         keys: up down enter back wait (each step 400 ms apart), and
//!         shot (save the frame as out-N.png) or tick (100 ms, for
//!         filmstrips of an animation: tick,shot,tick,shot,...); a finger:
//!         press:X:Y move:X:Y lift:X:Y (logical pixels, 8 ms apart, as a
//!         touchscreen reports), a long press being press, ticks, lift

use bootmanager_ui::app::{App, Mode};
use bootmanager_ui::platform::{self, Fb, FbPlatform, Frame, Target};
use bootmanager_ui::{transport, AppWindow};
use slint::ComponentHandle;
use std::cell::RefCell;
use std::rc::Rc;

fn main() {
    let mut socket = "/tmp/bootmanager.sock".to_string();
    let mut fb = "/dev/fb0".to_string();
    let mut screenshot = None::<String>;
    let mut keys = String::new();
    let mut size = slint::PhysicalSize::new(720, 1600);
    let mut args = std::env::args().skip(1);
    while let Some(a) = args.next() {
        let mut val = || args.next().unwrap_or_else(|| usage());
        match a.as_str() {
            "--socket" => socket = val(),
            "--fb" => fb = val(),
            "--screenshot" => screenshot = Some(val()),
            "--keys" => keys = val(),
            "--size" => {
                let v = val();
                let (w, h) = v.split_once('x').unwrap_or_else(|| usage());
                size = slint::PhysicalSize::new(w.parse().unwrap_or_else(|_| usage()), h.parse().unwrap_or_else(|_| usage()));
            }
            _ => usage(),
        }
    }

    let target = match &screenshot {
        Some(_) => Target::Memory,
        None => Target::Fb(Fb::open(&fb).unwrap_or_else(|e| {
            eprintln!("bootmanager-ui: {fb}: {e}");
            std::process::exit(1)
        })),
    };
    if let Target::Fb(_) = target {
        platform::pin_to_fastest_cpus();
    }
    let mode = match &target {
        Target::Fb(f) => Mode::Panel { fb_width_mm: f.width_mm },
        Target::Memory => Mode::Headless,
    };
    let p = FbPlatform::new(target, size).expect("platform");
    let frame = p.frame.clone();
    slint::platform::set_platform(Box::new(p)).expect("platform");

    let ui = AppWindow::new().expect("create window");
    App::install(&ui, mode);
    transport::start(socket);

    if let Some(out) = &screenshot {
        let steps: Vec<String> = keys.split(',').filter(|s| !s.is_empty()).map(String::from).collect();
        run_script(ui.as_weak(), steps, 0, frame.clone(), out.clone());
        ui.show().unwrap();
        slint::run_event_loop().unwrap();
        save_png(out, &frame.borrow());
        return;
    }
    ui.run().expect("run");
}

fn usage() -> ! {
    eprintln!("usage: bootmanager-ui [--socket PATH] [--fb DEV] [--screenshot PNG [--keys LIST] [--size WxH]]");
    std::process::exit(2)
}

fn run_script(ui: slint::Weak<AppWindow>, steps: Vec<String>, i: usize, frame: Rc<RefCell<Frame>>, out: String) {
    use slint::platform::{Key, WindowEvent};
    // The first step waits for the connection and the first replies; a
    // shot is taken of the frame drawn so far, at once.
    let delay = match (i, steps.get(i).map(String::as_str)) {
        (0, _) => 800,
        (_, Some("shot")) => 0,
        (_, Some("tick")) => 100,
        (_, Some(s)) if touch_step(s).is_some() => 8,
        _ => 400,
    };
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
        // As the panel's touchscreen sends them (platform.rs).
        if let Some((kind, x, y)) = touch_step(step) {
            let position = slint::LogicalPosition::new(x, y);
            let button = slint::platform::PointerEventButton::Left;
            match kind {
                "press" => u.window().dispatch_event(WindowEvent::PointerPressed { position, button }),
                "move" => u.window().dispatch_event(WindowEvent::PointerMoved { position }),
                _ => {
                    u.window().dispatch_event(WindowEvent::PointerReleased { position, button });
                    u.window().dispatch_event(WindowEvent::PointerExited);
                }
            }
        }
        if step == "shot" {
            let n = steps[..i].iter().filter(|s| *s == "shot").count();
            save_png(&format!("{}-{n}.png", out.trim_end_matches(".png")), &frame.borrow());
        }
        run_script(ui, steps, i + 1, frame, out);
    });
}

/// press:X:Y, move:X:Y or lift:X:Y.
fn touch_step(step: &str) -> Option<(&str, f32, f32)> {
    let mut it = step.split(':');
    let kind = it.next().filter(|k| matches!(*k, "press" | "move" | "lift"))?;
    Some((kind, it.next()?.parse().ok()?, it.next()?.parse().ok()?))
}

fn save_png(path: &str, frame: &Frame) {
    let file = std::fs::File::create(path).expect("create png");
    let mut enc = png::Encoder::new(std::io::BufWriter::new(file), frame.width as u32, frame.height as u32);
    enc.set_color(png::ColorType::Rgb);
    let mut writer = enc.write_header().unwrap();
    let data: Vec<u8> = frame.px.iter().flat_map(|p| [(p.0 >> 16) as u8, (p.0 >> 8) as u8, p.0 as u8]).collect();
    writer.write_image_data(&data).unwrap();
}
