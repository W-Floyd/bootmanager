//! The backend protocol, one JSON object per line (bootmanager/main.go
//! has the spec). Transport-free: a transport hands complete lines to
//! `handle_line` and supplies the function that sends one.

use serde_json::{json, Value};
use std::cell::{Cell, RefCell};
use std::collections::HashMap;

type Reply = Box<dyn FnOnce(Result<Value, String>)>;

#[derive(Default)]
pub struct Client {
    send: RefCell<Option<Box<dyn Fn(String)>>>,
    next_id: Cell<u64>,
    pending: RefCell<HashMap<u64, Reply>>,
}

impl Client {
    pub fn set_sender(&self, send: Option<Box<dyn Fn(String)>>) {
        *self.send.borrow_mut() = send;
        if self.send.borrow().is_none() {
            // Nothing is coming back on a dead connection.
            let dropped: Vec<Reply> = self.pending.borrow_mut().drain().map(|(_, r)| r).collect();
            for r in dropped {
                r(Err("disconnected".into()));
            }
        }
    }

    pub fn connected(&self) -> bool {
        self.send.borrow().is_some()
    }

    pub fn call(&self, cmd: &str, args: Value, reply: impl FnOnce(Result<Value, String>) + 'static) {
        let id = self.next_id.get() + 1;
        self.next_id.set(id);
        let line = json!({"id": id, "cmd": cmd, "args": args}).to_string() + "\n";
        match &*self.send.borrow() {
            Some(send) => {
                self.pending.borrow_mut().insert(id, Box::new(reply));
                send(line);
            }
            None => reply(Err("not connected".into())),
        }
    }

    /// Settles a reply, or returns an event for the caller to act on.
    pub fn handle_line(&self, line: &str) -> Option<(String, Value)> {
        let mut v: Value = match serde_json::from_str(line) {
            Ok(v) => v,
            Err(e) => {
                eprintln!("bootmanager-ui: bad line from backend: {e}");
                return None;
            }
        };
        if let Some(name) = v.get("event").and_then(Value::as_str) {
            return Some((name.to_string(), v["data"].take()));
        }
        let id = v.get("id").and_then(Value::as_u64)?;
        // Take the callback out before calling it: it may well send again.
        let reply = self.pending.borrow_mut().remove(&id)?;
        match v.get("error").and_then(Value::as_str) {
            Some(e) => reply(Err(e.to_string())),
            None => reply(Ok(v["data"].take())),
        }
        None
    }
}
