package distro

import (
	"slices"
	"testing"
)

const pmosCmdline = "console=tty0 pmos_boot_uuid=aaa pmos_root_uuid=5CE4863D-2736 androidboot.slot_suffix=_a"

func TestFor(t *testing.T) {
	d := For(pmosCmdline)
	if d == nil || d.Name() != "Nura" {
		t.Fatalf("For(Nura cmdline) = %v", d)
	}
	if got := d.EncryptedRoot(pmosCmdline); got != "5ce4863d-2736" {
		t.Errorf("EncryptedRoot = %q, want the UUID in lower case", got)
	}
	for _, cl := range []string{"", "console=tty0 root=/dev/sda2", "pmos_root_uuid="} {
		if d := For(cl); d != nil {
			t.Errorf("For(%q) = %v, want none", cl, d.Name())
		}
	}
}

func TestNuraKey(t *testing.T) {
	files, words := Nura{}.UnlockKey([]byte("fogona"))
	if len(files) != 1 || files[0].Path != "bman-unlock.key" || files[0].Mode != 0o400 || string(files[0].Data) != "fogona" {
		t.Errorf("files %+v", files)
	}
	if !slices.Equal(words, []string{"cryptkey=/bman-unlock.key"}) {
		t.Errorf("words %v", words)
	}
}

func TestWords(t *testing.T) {
	if !HasWord("a b=1 c", "b") || !HasWord("a b=1 c", "c") || HasWord("a b=1 c", "d") || HasWord("abc=1", "ab") {
		t.Error("HasWord")
	}
	if Value("x=1 y=2=3", "y") != "2=3" || Value("x=1", "z") != "" {
		t.Error("Value")
	}
}
