//! The carousel's cards: a boot entry's look, guessed from its label and
//! command line, and the menu's card after them.

use crate::proto::Entry;
use crate::Card;
use slint::Color;

struct Look {
    tag: &'static str,
    icon: &'static str,
    a: u32,
    b: u32,
}

// Matched in order against the words of the label, then of the command
// line, lowercased, as prefixes ("lineage" finds "lineageos"); the first
// match wins. The label first: a command line can carry another system's
// words (Android's, from its bootloader, the stage's own pmos_* ones).
const LOOKS: &[(&[&str], Look)] = &[
    (&["recovery", "twrp", "rescue", "fallback"], Look { tag: "RECOVERY", icon: "recovery", a: 0xf0a030, b: 0x8a3a12 }),
    // postmarketOS's green (artwork, docs/assets/attributes.svg).
    (&["postmarketos", "pmos"], Look { tag: "POSTMARKETOS", icon: "pmos", a: 0x009900, b: 0x003300 }),
    // Android's green, #3ddc84, and a darker one of its hue.
    (&["android", "lineage", "grapheneos", "calyx"], Look { tag: "ANDROID", icon: "android", a: 0x3ddc84, b: 0x0e4d2c }),
    (&["ubuntu"], Look { tag: "UBUNTU", icon: "linux", a: 0xe95420, b: 0x5a1f3f }),
    (&["debian", "mobian"], Look { tag: "DEBIAN", icon: "debian", a: 0xce0056, b: 0x48001e }),
    (&["fedora"], Look { tag: "FEDORA", icon: "linux", a: 0x3c6eb4, b: 0x1b2a55 }),
    (&["arch"], Look { tag: "ARCH", icon: "linux", a: 0x1793d1, b: 0x0a3550 }),
    (&["alpine"], Look { tag: "ALPINE", icon: "linux", a: 0x2a8fc4, b: 0x0a2f4a }),
    (&["nixos"], Look { tag: "NIXOS", icon: "linux", a: 0x7ebae4, b: 0x2c3f86 }),
];

// Any other entry with a command line is Linux (u-root finds Linux images).
const LINUX: Look = Look { tag: "LINUX", icon: "linux", a: 0xf7b733, b: 0x7a2e10 };
const OTHER: Look = Look { tag: "SYSTEM", icon: "layers", a: 0x8a7cf0, b: 0x2a2466 };
const MENU: Look = Look { tag: "", icon: "gear", a: 0x6a6f9e, b: 0x1c1d30 };

fn look(e: &Entry) -> &'static Look {
    let find = |text: &str| {
        let text = text.to_lowercase();
        let words: Vec<&str> = text.split(|c: char| !c.is_alphanumeric()).filter(|w| !w.is_empty()).collect();
        LOOKS.iter().find(|(keys, _)| keys.iter().any(|k| words.iter().any(|w| w.starts_with(k))))
    };
    match find(&e.label).or_else(|| find(&e.detail)) {
        Some((_, l)) => l,
        None if !e.detail.is_empty() => &LINUX,
        None => &OTHER,
    }
}

fn rgb(c: u32) -> Color {
    Color::from_rgb_u8((c >> 16) as u8, (c >> 8) as u8, c as u8)
}

/// What the entry boots from, briefly: its root= device.
fn subtitle(cmdline: &str) -> String {
    let Some(root) = cmdline.split_whitespace().find_map(|w| w.strip_prefix("root=")) else {
        return String::new();
    };
    match root.split_once('=') {
        // root=UUID=… / PARTUUID=…: the start of the id is enough to tell.
        Some((kind, id)) => format!("{kind} {}…", &id[..id.len().min(8)]),
        None => root.to_string(),
    }
}

/// The subtitle with where the entry lives ahead of it: two installs of the
/// same system differ in nothing else a person can see.
fn with_source(source: &str, subtitle: String) -> String {
    match (source.is_empty(), subtitle.is_empty()) {
        (true, _) => subtitle,
        (false, true) => source.to_string(),
        (false, false) => format!("{source} · {subtitle}"),
    }
}

fn card(title: &str, subtitle: String, tag: &str, l: &Look, variant: i32) -> Card {
    Card { title: title.into(), subtitle: subtitle.into(), tag: tag.into(), icon: l.icon.into(), a: rgb(l.a), b: rgb(l.b), variant }
}

/// A card per entry, the default marked, then the menu's.
pub fn build(entries: &[Entry], default: usize) -> Vec<Card> {
    let mut cards: Vec<Card> = entries
        .iter()
        .enumerate()
        .map(|(i, e)| {
            let l = look(e);
            // A tag that only repeats the title says nothing.
            let tag = match l.tag {
                _ if i == default => "DEFAULT",
                t if t.eq_ignore_ascii_case(&e.label) => "",
                t => t,
            };
            card(&e.label, with_source(&e.source, subtitle(&e.detail)), tag, l, (i % 3) as i32)
        })
        .collect();
    cards.push(card("Tools", "Services, partitions, log, power".into(), MENU.tag, &MENU, 2));
    cards
}

#[cfg(test)]
mod tests {
    use super::*;

    fn entry(label: &str, detail: &str) -> Entry {
        Entry { label: label.into(), detail: detail.into(), source: String::new() }
    }

    #[test]
    fn two_installs_of_one_system() {
        let mut sd = entry("postmarketOS", "pmos_root_uuid=8e7b");
        sd.source = "microSD card".into();
        let mut ufs = entry("postmarketOS", "pmos_root_uuid=5ce4");
        ufs.source = "UFS userdata (encrypted)".into();
        let cards = build(&[sd, ufs, entry("Android", "")], 0);
        assert_eq!(cards[0].subtitle, "microSD card");
        assert_eq!(cards[1].subtitle, "UFS userdata (encrypted)");
        assert_eq!(cards[2].subtitle, "");
        assert_eq!(with_source("microSD card", "/dev/sda2".into()), "microSD card · /dev/sda2");
    }

    #[test]
    fn looks() {
        assert_eq!(look(&entry("postmarketOS", "root=/dev/mmcblk0p2")).icon, "pmos");
        assert_eq!(look(&entry("LineageOS 21", "")).icon, "android");
        assert_eq!(look(&entry("pmos (recovery)", "")).icon, "recovery");
        assert_eq!(look(&entry("Something", "quiet")).icon, "linux");
        assert_eq!(look(&entry("Mobian", "")).icon, "debian");
        // Android's command line, as ABL builds it, carries the stage's pmOS words.
        assert_eq!(look(&entry("Android", "console=null pmos.nosplash pmos_boot_uuid=3777f3f3 init=/init")).icon, "android");
        assert_eq!(look(&entry("Kernel 7.3", "pmos_root_uuid=8e7b")).icon, "pmos");
        assert_eq!(look(&entry("Something", "")).icon, "layers");
        assert_eq!(subtitle("console=ttyS0 root=PARTUUID=0123456789ab rw"), "PARTUUID 01234567…");
        assert_eq!(subtitle("root=/dev/sda2"), "/dev/sda2");
        let cards = build(&[entry("a", ""), entry("b", ""), entry("Android", ""), entry("Android 14", "")], 1);
        assert_eq!(cards.len(), 5);
        assert_eq!(cards[1].tag, "DEFAULT");
        assert_eq!(cards[2].tag, "", "the title says it already");
        assert_eq!(cards[3].tag, "ANDROID");
        assert_eq!((cards[4].icon.as_str(), cards[4].tag.as_str()), ("gear", ""));
    }
}
