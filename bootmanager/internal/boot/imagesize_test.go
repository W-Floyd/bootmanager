package boot

import "testing"

func TestParseImageSize(t *testing.T) {
	// kexec-tools 2.0.32's -d output for the G5W's kernel before and after
	// Bluetooth was built in: the second overran 0x3200000.
	const before = `zImage header: 0x016f2818 0x00000000 0x00d19200
zImage requires 0x00d2a200 bytes
Decompressed kernel sizes:
 text+data 0x021c2a50 bss 0x00066634 total 0x02229084
`
	const after = `zImage requires 0x00d6a200 bytes
Decompressed kernel sizes:
 text+data 0x022c3bfc bss 0x00066588 total 0x0232a184
`
	for _, tc := range []struct{ name, out, want string }{
		{"before", before, "0x3300000"},
		{"after", after, "0x3500000"},
		{"zImage, nothing to read", "zImage\n", "0x3800000"},
		{"not a zImage", "kernel: 0x1\n", ""},
		{"empty", "", ""},
	} {
		if got := parseImageSize(tc.out); got != tc.want {
			t.Errorf("%s: parseImageSize = %q, want %q", tc.name, got, tc.want)
		}
	}
}
