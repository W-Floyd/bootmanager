package boot

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
)

// The SSH keys allowed to log in to the stage, kept where the stage can read
// them with no boot partition: after the image in the tail of the stage's
// own partition (boot_a), beside the settings (rawrecord.go). An encrypted
// install keeps its linuxboot/ directory inside a root that is closed when
// the stage starts, so the keys it needs at once have to be outside it.
//
// The record is plain text so a shell can write it (the package's install
// script): the magic line, the authorized_keys text, a NUL. It sits 4 MiB
// from the end, clear of the settings' copies (2 and 3 MiB) and the AVB
// footer. There is no checksum: a damaged key is a key that matches nobody.
const (
	rawKeysOff   = 4 << 20
	rawKeysMax   = 60 << 10
	rawKeysMagic = "BMKEYS01\n"
)

// readRawKeys is the authorized_keys text kept in the file or device at
// path; none, with no error, when no record is there.
func readRawKeys(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	size, err := sizeOf(f)
	if err != nil {
		return nil, err
	}
	if size < rawMinSize {
		return nil, fmt.Errorf("%s: %d bytes is too small to hold keys", path, size)
	}
	buf := make([]byte, len(rawKeysMagic)+rawKeysMax)
	if _, err := f.ReadAt(buf, size-rawKeysOff); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	rest, ok := bytes.CutPrefix(buf, []byte(rawKeysMagic))
	if !ok {
		return nil, nil
	}
	if i := bytes.IndexByte(rest, 0); i >= 0 {
		rest = rest[:i]
	}
	return bytes.TrimSpace(rest), nil
}

// RawKeys is the keys kept in the partition named name (boot_a).
func RawKeys(name string) ([]byte, error) {
	dev, err := rawDevice(name)
	if err != nil {
		return nil, err
	}
	return readRawKeys(dev)
}

// WriteHostKey makes an SSH host key at path, an ed25519 key as PKCS#8 PEM,
// which the stage's sshd reads. The stage makes one each boot when none was
// kept: an identity that changes with every boot, which is the price of not
// leaving a private key on an unencrypted partition.
func WriteHostKey(path string) error {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600)
}
