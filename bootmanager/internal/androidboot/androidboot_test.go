package androidboot

import (
	"bytes"
	"encoding/binary"
	"io"
	"strings"
	"testing"
)

// build makes an image: the header from fields, then the parts in pages.
type spec struct {
	version              uint32 // header_version, or the QCDT size when above 4
	page                 uint32
	kernel, ramdisk      []byte
	second, dtb, recov   []byte
	qcdt                 []byte
	cmdline, extra, name string
	os                   uint32
}

func (s spec) build() []byte {
	le := binary.LittleEndian
	hdr := make([]byte, 1660)
	copy(hdr, Magic)
	pad := func(b []byte, pg int) []byte {
		out := append([]byte(nil), b...)
		for len(out)%pg != 0 {
			out = append(out, 0)
		}
		return out
	}
	if s.version == 3 || s.version == 4 {
		hdr = make([]byte, 4096)
		copy(hdr, Magic)
		le.PutUint32(hdr[8:], uint32(len(s.kernel)))
		le.PutUint32(hdr[12:], uint32(len(s.ramdisk)))
		le.PutUint32(hdr[16:], s.os)
		le.PutUint32(hdr[20:], 4096)
		le.PutUint32(hdr[40:], s.version)
		copy(hdr[44:], s.cmdline)
		out := append(hdr, pad(s.kernel, 4096)...)
		return append(out, pad(s.ramdisk, 4096)...)
	}
	pg := int(s.page)
	le.PutUint32(hdr[8:], uint32(len(s.kernel)))
	le.PutUint32(hdr[12:], 0x80008000)
	le.PutUint32(hdr[16:], uint32(len(s.ramdisk)))
	le.PutUint32(hdr[20:], 0x81000000)
	le.PutUint32(hdr[24:], uint32(len(s.second)))
	le.PutUint32(hdr[28:], 0x80f00000)
	le.PutUint32(hdr[32:], 0x80000100)
	le.PutUint32(hdr[36:], s.page)
	le.PutUint32(hdr[40:], s.version)
	le.PutUint32(hdr[44:], s.os)
	copy(hdr[48:], s.name)
	copy(hdr[64:], s.cmdline)
	copy(hdr[608:], s.extra)
	body := append(pad(s.kernel, pg), pad(s.ramdisk, pg)...)
	body = append(body, pad(s.second, pg)...)
	if len(s.qcdt) > 0 {
		le.PutUint32(hdr[40:], uint32(len(s.qcdt)))
		body = append(body, pad(s.qcdt, pg)...)
	}
	hdrPages := (len(hdr) + pg - 1) / pg * pg
	if s.version >= 1 {
		le.PutUint32(hdr[1632:], uint32(len(s.recov)))
		off := hdrPages + len(body)
		le.PutUint64(hdr[1636:], uint64(off))
		le.PutUint32(hdr[1644:], 1648)
		body = append(body, pad(s.recov, pg)...)
	}
	if s.version >= 2 {
		le.PutUint32(hdr[1644:], 1660)
		le.PutUint32(hdr[1648:], uint32(len(s.dtb)))
		le.PutUint64(hdr[1652:], 0x83000000)
		body = append(body, pad(s.dtb, pg)...)
	}
	size := 1632
	switch s.version {
	case 1:
		size = 1648
	case 2:
		size = 1660
	}
	return append(pad(hdr[:size], pg), body...)
}

func fill(n int, c byte) []byte { return bytes.Repeat([]byte{c}, n) }

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestV0(t *testing.T) {
	// Sizes and header as the Kia EV6 head unit's boot partition has them:
	// page 2048, kernel 0x68fa38, ramdisk 0x1f8399, version 0.
	img := spec{page: 2048, kernel: fill(0x68fa38/64, 'K'), ramdisk: fill(0x1f8399/64, 'R'),
		cmdline: "console=null androidboot.selinux=disabled", name: "tcc803x"}.build()
	m, err := Parse(bytes.NewReader(img))
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != 0 || m.PageSize != 2048 || m.Cmdline != "console=null androidboot.selinux=disabled" || m.Name != "tcc803x" {
		t.Errorf("header = %+v", m.Header)
	}
	if got := readAll(t, m.Kernel()); got != string(fill(0x68fa38/64, 'K')) {
		t.Error("kernel differs")
	}
	if got := readAll(t, m.Ramdisk()); got != string(fill(0x1f8399/64, 'R')) {
		t.Error("ramdisk differs")
	}
	if m.DTB() != nil || m.QCDT() != nil {
		t.Error("v0 has no DTB")
	}
	if m.KernelAddr != 0x80008000 || m.RamdiskAddr != 0x81000000 {
		t.Errorf("addresses %x %x", m.KernelAddr, m.RamdiskAddr)
	}
	if err := m.Check(); err != nil {
		t.Error(err)
	}
}

func TestV1V2(t *testing.T) {
	base := spec{page: 4096, kernel: []byte("kernel"), ramdisk: []byte("ramdisk"), second: []byte("second"),
		recov: []byte("recovery-dtbo"), dtb: []byte("the-dtb"), cmdline: "a=b", name: "dev",
		os: (uint32(11)<<14|0<<7|0)<<11 | (uint32(21)<<4 | 6)}
	for _, v := range []uint32{1, 2} {
		s := base
		s.version = v
		m, err := Parse(bytes.NewReader(s.build()))
		if err != nil {
			t.Fatalf("v%d: %v", v, err)
		}
		if m.Version != int(v) || readAll(t, m.Kernel()) != "kernel" || readAll(t, m.Ramdisk()) != "ramdisk" {
			t.Errorf("v%d: %+v", v, m.Header)
		}
		if m.RecoveryDTBOSize != uint32(len("recovery-dtbo")) {
			t.Errorf("v%d recovery dtbo size %d", v, m.RecoveryDTBOSize)
		}
		if v == 2 && readAll(t, m.DTB()) != "the-dtb" {
			t.Error("v2 dtb differs")
		}
		if v == 1 && m.DTB() != nil {
			t.Error("v1 has no DTB")
		}
		if m.OSVersion != "11.0.0" || m.OSPatch != "2021-06" || m.Label() != "dev Android 11.0.0" {
			t.Errorf("os %q %q label %q", m.OSVersion, m.OSPatch, m.Label())
		}
	}
}

func TestV3V4(t *testing.T) {
	for _, v := range []uint32{3, 4} {
		m, err := Parse(bytes.NewReader(spec{version: v, kernel: fill(5000, 'k'), ramdisk: fill(100, 'r'), cmdline: "x=y"}.build()))
		if err != nil {
			t.Fatalf("v%d: %v", v, err)
		}
		if m.Version != int(v) || m.PageSize != 4096 || m.Cmdline != "x=y" || m.KernelOff != 4096 || m.RamdiskOff != 4096+8192 {
			t.Errorf("v%d header = %+v", v, m.Header)
		}
		if got := readAll(t, m.Ramdisk()); got != string(fill(100, 'r')) {
			t.Errorf("v%d ramdisk differs", v)
		}
	}
}

func TestQCDTAndExtraCmdline(t *testing.T) {
	long := strings.Repeat("a", 512)
	m, err := Parse(bytes.NewReader(spec{page: 2048, kernel: []byte("k"), ramdisk: []byte("r"), second: []byte("s"),
		qcdt: []byte("QCDT-table"), cmdline: long, extra: " more=1"}.build()))
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != 0 || readAll(t, m.QCDT()) != "QCDT-table" {
		t.Errorf("qcdt: %+v", m.Header)
	}
	if m.Cmdline != long+" more=1" {
		t.Errorf("cmdline is %d bytes, ends %q", len(m.Cmdline), m.Cmdline[len(m.Cmdline)-12:])
	}
}

func TestRejects(t *testing.T) {
	if _, err := Parse(bytes.NewReader([]byte("not a boot image at all"))); err == nil {
		t.Error("garbage accepted")
	}
	if _, err := Parse(bytes.NewReader(make([]byte, 2000))); err == nil {
		t.Error("zeros accepted")
	}
	img := spec{page: 2048, kernel: fill(5000, 'k'), ramdisk: fill(3000, 'r')}.build()
	m, err := Parse(bytes.NewReader(img[:len(img)-4000]))
	if err != nil {
		t.Fatal(err)
	}
	if m.Check() == nil {
		t.Error("a truncated image passed Check")
	}
	bad := spec{page: 3000, kernel: []byte("k")}.build()
	if _, err := Parse(bytes.NewReader(bad)); err == nil {
		t.Error("a page size that is not a power of two was accepted")
	}
}
