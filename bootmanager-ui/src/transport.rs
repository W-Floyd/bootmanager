//! The backend's Unix socket. One thread reads lines and hands them to the
//! UI thread; one writes. It reconnects forever, so the UI can start before
//! the backend and outlive a backend restart.

use crate::app::{with_app, App};
use std::io::{BufRead, BufReader, Write};
use std::os::unix::net::UnixStream;
use std::rc::Rc;
use std::sync::mpsc;
use std::time::Duration;

/// Runs f on the UI thread, with the App.
fn on_ui(f: impl FnOnce(&Rc<App>) + Send + 'static) {
    let _ = slint::invoke_from_event_loop(move || with_app(f));
}

pub fn start(socket: String) {
    std::thread::spawn(move || loop {
        let Ok(stream) = UnixStream::connect(&socket) else {
            std::thread::sleep(Duration::from_millis(500));
            continue;
        };
        let Ok(mut w) = stream.try_clone() else { continue };
        let (tx, rx) = mpsc::channel::<String>();
        let writer = std::thread::spawn(move || {
            for line in rx {
                if w.write_all(line.as_bytes()).is_err() {
                    break;
                }
            }
        });
        on_ui(move |a| a.connected(Box::new(move |line| drop(tx.send(line)))));
        for line in BufReader::new(stream).lines() {
            let Ok(line) = line else { break };
            on_ui(move |a| a.line(&line));
        }
        // Dropping the sender (in disconnected) ends the writer thread.
        on_ui(|a| a.disconnected());
        let _ = writer.join();
        std::thread::sleep(Duration::from_millis(500));
    });
}
