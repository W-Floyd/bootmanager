package boot

import "testing"

func TestForeignUserdata(t *testing.T) {
	for kind, want := range map[string]bool{
		"":            false, // random bytes: Android's, metadata-encrypted
		"f2fs":        false,
		"crypto_LUKS": true,
		"ext4":        true,
		"vfat":        true,
	} {
		if got := foreignUserdata(kind); got != want {
			t.Errorf("foreignUserdata(%q) = %v, want %v", kind, got, want)
		}
	}
}
