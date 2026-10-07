package main

import "testing"

func TestCmdlineValue(t *testing.T) {
	cl := "console=tty0 pmos_boot_uuid=aaa pmos_root_uuid=BB-cc androidboot.slot_suffix=_a"
	for key, want := range map[string]string{"pmos_root_uuid": "BB-cc", "pmos_boot_uuid": "aaa", "nope": "", "root": ""} {
		if got := cmdlineValue(cl, key); got != want {
			t.Errorf("cmdlineValue(%q) = %q, want %q", key, got, want)
		}
	}
}
