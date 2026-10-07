package boot

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

// The record as the package's shell writes it: dd at 4 MiB from the end.
func TestRawKeysAsAShellWritesThem(t *testing.T) {
	r := rawFile(t, 96<<20)
	if got, err := readRawKeys(r.path); err != nil || got != nil {
		t.Fatalf("none written: %q, %v", got, err)
	}
	keys := "ssh-ed25519 AAAAC3Nza...one a@b\nssh-rsa AAAAB3...two c@d\n"
	f, _ := os.OpenFile(r.path, os.O_RDWR, 0)
	f.WriteAt([]byte(rawKeysMagic+keys+"\x00junk after the end"), 96<<20-rawKeysOff)
	f.Close()
	got, err := readRawKeys(r.path)
	if err != nil || string(got) != keys[:len(keys)-1] {
		t.Fatalf("read %q, %v", got, err)
	}
}

// The keys do not touch the settings' copies, nor the other way round.
func TestRawKeysAndSettingsKeepApart(t *testing.T) {
	r := rawFile(t, 96<<20)
	f, _ := os.OpenFile(r.path, os.O_RDWR, 0)
	f.WriteAt([]byte(rawKeysMagic+"ssh-ed25519 K x\n\x00"), 96<<20-rawKeysOff)
	f.Close()
	if err := r.save(bytes.Repeat([]byte("s"), rawSlotSize-rawHeaderLen)); err != nil {
		t.Fatal(err)
	}
	if got, _ := readRawKeys(r.path); string(got) != "ssh-ed25519 K x" {
		t.Fatalf("saving settings changed the keys: %q", got)
	}
}

func TestHostKeyIsOneSSHCanRead(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ssh_host_rsa_key")
	if err := WriteHostKey(p); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	// The PEM type x/crypto's ssh.ParsePrivateKey (sshd's reader) takes for
	// a PKCS#8 key, which parses as an ed25519 key.
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "PRIVATE KEY" {
		t.Fatalf("not PKCS#8 PEM: %q", b)
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if _, ok := k.(ed25519.PrivateKey); err != nil || !ok {
		t.Fatalf("parsed as %T, %v", k, err)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
}
