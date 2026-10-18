package boot

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// legacyImage is a version 0 image: page 2048, kernel and ramdisk.
func legacyImage(kernel, ramdisk, cmdline string) []byte {
	le := binary.LittleEndian
	h := make([]byte, 2048)
	copy(h, "ANDROID!")
	le.PutUint32(h[8:], uint32(len(kernel)))
	le.PutUint32(h[16:], uint32(len(ramdisk)))
	le.PutUint32(h[36:], 2048)
	copy(h[64:], cmdline)
	pad := func(s string) []byte {
		b := []byte(s)
		for len(b)%2048 != 0 {
			b = append(b, 0)
		}
		return b
	}
	return append(append(h, pad(kernel)...), pad(ramdisk)...)
}

func TestLegacyAndroidImageEntry(t *testing.T) {
	dir := t.TempDir()
	dev := filepath.Join(dir, "boot")
	if err := os.WriteFile(dev, legacyImage("KERNEL", "RAMDISK", "console=null"), 0o644); err != nil {
		t.Fatal(err)
	}
	dtb := bytes.NewReader([]byte("DTB"))
	img, err := legacyAndroidImage(dev, "boot", dtb, "androidboot.mode=emmc", KexecTools{})
	if err != nil {
		t.Fatal(err)
	}
	li, ok := asLinux(img)
	if !ok {
		t.Fatal("no LinuxImage")
	}
	if li.Name != "Android (boot)" || li.Cmdline != "console=null androidboot.mode=emmc" || li.DTB == nil || li.Initrd == nil {
		t.Errorf("entry = %q %q dtb=%v initrd=%v", li.Name, li.Cmdline, li.DTB, li.Initrd)
	}
	if _, isK := img.(*kexecToolsImage); isK {
		t.Error("kexec-tools image without kexec-tools")
	}
	img, err = legacyAndroidImage(dev, "boot", dtb, "", KexecTools{Argv: []string{"kexec"}})
	if err != nil {
		t.Fatal(err)
	}
	if k, ok := img.(*kexecToolsImage); !ok {
		t.Error("not a kexec-tools image")
	} else if li2, _ := asLinux(k); li2 != k.LinuxImage {
		t.Error("asLinux does not unwrap")
	}
}

func TestLegacyAndroidImageRejects(t *testing.T) {
	dir := t.TempDir()
	v3 := make([]byte, 4096)
	copy(v3, "ANDROID!")
	binary.LittleEndian.PutUint32(v3[8:], 100)
	binary.LittleEndian.PutUint32(v3[40:], 3)
	for name, content := range map[string][]byte{"empty": make([]byte, 4096), "v3": v3} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, content, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := legacyAndroidImage(p, name, nil, "", KexecTools{}); err == nil {
			t.Errorf("%s was taken as a legacy image", name)
		}
	}
}

// KEXEC_HELPER_OUT makes the test binary stand in for kexec-tools: it
// records its arguments and exits.
func init() {
	if out := os.Getenv("KEXEC_HELPER_OUT"); out != "" && os.Getenv("KEXEC_HELPER_RUN") == "1" {
		os.WriteFile(out, []byte(strings.Join(os.Args[1:], "\n")), 0o644)
		os.Exit(0)
	}
}

func TestKexecToolsLoad(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "args")
	dev := filepath.Join(dir, "boot")
	if err := os.WriteFile(dev, legacyImage("KERNEL", "RAMDISK", "console=null"), 0o644); err != nil {
		t.Fatal(err)
	}
	kx := KexecTools{Argv: []string{os.Args[0]}, Prep: []string{os.Args[0], "prep"}}
	img, err := legacyAndroidImage(dev, "boot", bytes.NewReader([]byte("DTB")), "", kx)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KEXEC_HELPER_OUT", out)
	t.Setenv("KEXEC_HELPER_RUN", "1")
	if err := img.Load(); err != nil {
		t.Fatal(err)
	}
	// The prep command ran last and overwrote the record.
	if b, _ := os.ReadFile(out); string(b) != "prep" {
		t.Fatalf("after Load the last command was %q, want prep", b)
	}
	// Run the load alone, to see its arguments.
	kx.Prep = nil
	img, _ = legacyAndroidImage(dev, "boot", bytes.NewReader([]byte("DTB")), "", kx)
	if err := img.Load(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(string(b), "\n")
	if args[0] != "-l" || args[2] != "--append=console=null" {
		t.Fatalf("args = %q", args)
	}
	if got, _ := os.ReadFile(args[1]); !strings.HasPrefix(string(got), "KERNEL") {
		t.Errorf("kernel holds %q", got)
	}
	for _, a := range args {
		if p, ok := strings.CutPrefix(a, "--initrd="); ok {
			if got, _ := os.ReadFile(p); !strings.HasPrefix(string(got), "RAMDISK") {
				t.Errorf("initrd holds %q", got)
			}
		}
		if p, ok := strings.CutPrefix(a, "--dtb="); ok {
			if got, _ := os.ReadFile(p); string(got) != "DTB" {
				t.Errorf("dtb holds %q", got)
			}
		}
	}
}
