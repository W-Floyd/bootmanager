//! The screen as a URL path, for the web build: each screen change is a
//! history entry, and a loaded URL opens its screen with the Back stack
//! its path implies. Paths inside partitions are percent-encoded.

use crate::screens::{Load, Screen};
use percent_encoding::{percent_decode_str, utf8_percent_encode, AsciiSet, NON_ALPHANUMERIC};

/// Everything but unreserved characters and '/'.
const PATH: &AsciiSet = &NON_ALPHANUMERIC.remove(b'-').remove(b'.').remove(b'_').remove(b'~').remove(b'/');

pub fn encode(p: &str) -> String {
    utf8_percent_encode(p, PATH).to_string()
}

pub fn route_of(s: &Screen) -> String {
    match s {
        Screen::Main => "/menu".into(),
        Screen::Boot => "/boot".into(),
        Screen::Autoboot => "/autoboot".into(),
        Screen::BootOptions => "/boot-options".into(),
        Screen::Services => "/services".into(),
        Screen::Disks(_) => "/partitions".into(),
        Screen::Log(_) => "/log".into(),
        Screen::Diagnostics => "/diagnostics".into(),
        Screen::About => "/about".into(),
        Screen::Battery => "/diagnostics/battery".into(),
        Screen::BootInfo(_) => "/diagnostics/boot".into(),
        Screen::Files { dev, path, .. } => format!("/partitions/{dev}{}", encode(path.trim_end_matches('/'))),
        // The browser previews images itself; the viewer is the panel's.
        Screen::File { dev, path, .. } | Screen::Image { dev, path, .. } => format!("/file/{dev}{}", encode(path)),
        Screen::Text { dev, path, .. } => format!("/text/{dev}{}", encode(path)),
        Screen::Fsck { dev, .. } => format!("/check/{dev}"),
        Screen::Unlock { dev, .. } => format!("/unlock/{dev}"),
    }
}

/// A route's screen and the stack of screens Back walks from it. Anything
/// unknown is the main screen.
pub fn screens_of(route: &str) -> (Vec<Screen>, Screen) {
    let route = percent_decode_str(route).decode_utf8_lossy();
    let parts: Vec<&str> = route.split('/').filter(|p| !p.is_empty()).collect();
    let files = |dev: &str, path: String| Screen::Files { dev: dev.into(), name: String::new(), path, listing: Load::Waiting };
    let dir_stack = |dev: &str, dirs: &[&str]| {
        let mut st = vec![Screen::Main, Screen::Disks(Load::Waiting), files(dev, "/".into())];
        let mut p = String::new();
        for d in dirs {
            p = format!("{p}/{d}");
            st.push(files(dev, p.clone()));
        }
        st
    };
    match parts.as_slice() {
        ["boot"] => (vec![Screen::Main], Screen::Boot),
        ["autoboot"] => (vec![Screen::Main], Screen::Autoboot),
        ["boot-options"] => (vec![Screen::Main], Screen::BootOptions),
        ["services"] => (vec![Screen::Main], Screen::Services),
        ["log"] => (vec![Screen::Main], Screen::Log(Load::Waiting)),
        ["diagnostics"] => (vec![Screen::Main], Screen::Diagnostics),
        ["about"] => (vec![Screen::Main], Screen::About),
        ["diagnostics", "battery"] => (vec![Screen::Main, Screen::Diagnostics], Screen::Battery),
        ["diagnostics", "boot"] => (vec![Screen::Main, Screen::Diagnostics], Screen::BootInfo(Load::Waiting)),
        ["partitions"] => (vec![Screen::Main], Screen::Disks(Load::Waiting)),
        ["unlock", dev] => (vec![Screen::Main, Screen::Disks(Load::Waiting)], Screen::Unlock { dev: dev.to_string(), name: String::new() }),
        ["check", dev] => (dir_stack(dev, &[]), Screen::Fsck { dev: dev.to_string(), disk: Load::Waiting, confirm: false }),
        ["partitions", dev, rest @ ..] => {
            let mut st = dir_stack(dev, rest);
            let screen = st.pop().unwrap();
            (st, screen)
        }
        [kind @ ("file" | "text"), dev, rest @ ..] if !rest.is_empty() => {
            let path = format!("/{}", rest.join("/"));
            let mut st = dir_stack(dev, &rest[..rest.len() - 1]);
            let file = Screen::File { dev: dev.to_string(), path: path.clone(), size: 0 };
            if *kind == "file" {
                (st, file)
            } else {
                st.push(file);
                (st, Screen::Text { dev: dev.to_string(), path, text: Load::Waiting })
            }
        }
        _ => (vec![], Screen::Main),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn routes_round_trip() {
        for (route, stack_len) in [
            ("/menu", 0),
            ("/boot", 1),
            ("/autoboot", 1),
            ("/diagnostics/battery", 2),
            ("/diagnostics/boot", 2),
            ("/diagnostics", 1),
            ("/partitions", 1),
            ("/partitions/mmcblk0p2", 2),
            ("/check/mmcblk0p2", 3),
            ("/unlock/mmcblk0p2", 2),
            ("/partitions/mmcblk0p2/usr/share", 4),
            ("/file/mmcblk0p2/etc/os-release", 4),
            ("/text/mmcblk0p2/etc/os-release", 5),
            ("/file/mmcblk0p2/my%20photo%20%C3%A9.jpg", 3),
        ] {
            let (stack, screen) = screens_of(route);
            assert_eq!(stack.len(), stack_len, "{route}");
            assert_eq!(route_of(&screen), route, "{route} back to a route");
        }
        let (_, screen) = screens_of("/file/mmcblk0p2/my%20photo%20%C3%A9.jpg");
        assert!(matches!(screen, Screen::File { path, .. } if path == "/my photo é.jpg"));
        // The stack under a file is its directories, parent last.
        let (stack, _) = screens_of("/text/mmcblk0p2/a/b/c.txt");
        assert_eq!(route_of(&stack[3]), "/partitions/mmcblk0p2/a");
        assert_eq!(route_of(&stack[4]), "/partitions/mmcblk0p2/a/b");
        assert!(matches!(stack[5], Screen::File { .. }));
        assert!(matches!(screens_of("/nope/x").1, Screen::Main));
        assert!(matches!(screens_of("/file/mmcblk0p2").1, Screen::Main));
    }
}
