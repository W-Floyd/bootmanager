package luks

import (
	"bytes"
	"io"
	"testing"

	"github.com/u-root/u-root/pkg/cpio"
)

func TestKeyring(t *testing.T) {
	var k Keyring
	k.Put("sde27", "abc", []byte("secret"))
	k.Put("sde28", "", []byte("no uuid, not kept"))
	if got := k.Take("nope"); got != nil {
		t.Errorf("Take(unknown) = %q", got)
	}
	if got := k.Take("abc"); string(got) != "secret" {
		t.Errorf("Take(abc) = %q", got)
	}
	if got := k.Take("abc"); got != nil {
		t.Errorf("a second Take(abc) = %q: it goes to one boot", got)
	}
	k.Put("sde27", "abc", []byte("again"))
	k.Forget("sde27")
	if got := k.Take("abc"); got != nil {
		t.Errorf("Take after Forget = %q", got)
	}
}

// The archive is a valid newc cpio with the passphrase's exact bytes in a
// root-only file, whatever it holds (no newline added, no trimming).
func TestKeyInitrd(t *testing.T) {
	for _, pass := range []string{"fogona", " spaced pass\n", "ünï\x00code"} {
		b, err := KeyInitrd([]byte(pass))
		if err != nil {
			t.Fatal(err)
		}
		r := cpio.Newc.Reader(bytes.NewReader(b))
		rec, err := r.ReadRecord()
		if err != nil {
			t.Fatalf("%q: %v", pass, err)
		}
		if rec.Info.Name != KeyFile {
			t.Errorf("name %q, want %q", rec.Info.Name, KeyFile)
		}
		if rec.Info.Mode&0o777 != 0o400 {
			t.Errorf("mode %o, want 0400", rec.Info.Mode&0o777)
		}
		got, _ := io.ReadAll(rec.ReaderAt.(io.Reader))
		if string(got) != pass {
			t.Errorf("content %q, want %q", got, pass)
		}
		if _, err := r.ReadRecord(); err != io.EOF {
			t.Errorf("after the file: %v, want EOF (the trailer)", err)
		}
	}
}
