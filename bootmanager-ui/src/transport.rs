//! Native transport: a Unix socket on the phone, TCP from the host. One
//! thread reads lines and hands them to the UI thread; one writes. It
//! reconnects forever, so the UI can start before the backend and outlive a
//! backend restart or a replugged cable.

use std::io::{BufRead, BufReader, Write};
use std::sync::mpsc;
use std::time::Duration;

#[derive(Clone)]
pub enum Addr {
    Unix(String),
    Tcp(String),
}

type Stream = (Box<dyn std::io::Read + Send>, Box<dyn Write + Send>);

fn dial(addr: &Addr) -> std::io::Result<Stream> {
    Ok(match addr {
        Addr::Unix(p) => {
            let s = std::os::unix::net::UnixStream::connect(p)?;
            (Box::new(s.try_clone()?), Box::new(s))
        }
        Addr::Tcp(a) => {
            let s = std::net::TcpStream::connect(a)?;
            s.set_nodelay(true)?;
            (Box::new(s.try_clone()?), Box::new(s))
        }
    })
}

pub fn start(addr: Addr) {
    std::thread::spawn(move || loop {
        let (r, mut w) = match dial(&addr) {
            Ok(s) => s,
            Err(_) => {
                std::thread::sleep(Duration::from_millis(500));
                continue;
            }
        };
        let (tx, rx) = mpsc::channel::<String>();
        let writer = std::thread::spawn(move || {
            for line in rx {
                if w.write_all(line.as_bytes()).and_then(|_| w.flush()).is_err() {
                    break;
                }
            }
        });
        let _ = slint::invoke_from_event_loop(move || {
            crate::with_app(|a| {
                a.connected(Box::new(move |line| {
                    let _ = tx.send(line);
                }))
            })
        });
        for line in BufReader::new(r).lines() {
            let Ok(line) = line else { break };
            let _ = slint::invoke_from_event_loop(move || crate::with_app(|a| a.line(&line)));
        }
        // Dropping the sender (in disconnected) ends the writer thread.
        let _ = slint::invoke_from_event_loop(|| crate::with_app(|a| a.disconnected()));
        let _ = writer.join();
        std::thread::sleep(Duration::from_millis(500));
    });
}
