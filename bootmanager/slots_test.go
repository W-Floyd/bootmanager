package main

import "testing"

func TestBootedSlot(t *testing.T) {
	for _, c := range []struct {
		name, want, cmdline string
		bootconfig          map[string]string
	}{
		{"bootconfig wins", "b", "androidboot.slot_suffix=_a", map[string]string{"androidboot.slot_suffix": "_b"}},
		{"bootconfig without underscore", "a", "", map[string]string{"androidboot.slot_suffix": "a"}},
		{"no bootconfig: the command line", "a", "console=tty0 androidboot.slot_suffix=_a panic=5", nil},
		{"bootconfig without a slot: the command line", "b", "androidboot.slot_suffix=_b", map[string]string{"androidboot.serialno": "x"}},
		{"nothing: no slot, nothing is marked", "", "console=tty0", nil},
		{"an unknown suffix is no slot", "", "androidboot.slot_suffix=_c", nil},
	} {
		if got := bootedSlot(c.bootconfig, c.cmdline); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
