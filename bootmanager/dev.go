package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/u-root/u-root/pkg/boot"
	"github.com/u-root/u-root/pkg/mount"
)

// Live restarts, for working on the boot manager without a reflash.
//
// The backend restarts by exec'ing itself (or an uploaded build) in place:
// the PID stays, so sshd and the UI stay its children and keep running --
// an SSH session survives -- and a state file hands the new image what it
// cannot rediscover: those PIDs, which services were on, and whether the
// autoboot was already stopped. The sockets close on exec; the UI's
// transport reconnects by itself.
//
// Uploads, with the Dev uploads service on (it rides on Remote UI's HTTP
// port, so both must be on), from the container:
//
//	curl -T bootmanager-ui http://172.16.42.1:8080/dev/bootmanager-ui   # restarts the UI
//	curl -T bootmanager    http://172.16.42.1:8080/dev/bootmanager      # re-execs the backend
//	curl -T index.html     http://172.16.42.1:8080/dev/web/index.html   # served next reload
//
// tools/linuxboot/bootmanager/dev.sh builds and sends them. Uploads live in
// /tmp/dev, in RAM, and win over the built-in copies until the next boot.

const (
	devDir    = "/tmp/dev"
	statePath = "/tmp/bootmanager-state.json"
)

type handoff struct {
	Cancelled bool           `json:"cancelled"`
	UIPid     int            `json:"ui_pid"`
	Services  map[string]int `json:"services"` // name -> pid (0: in-process)
}

// loadHandoff returns the state an exec left, once.
func loadHandoff() *handoff {
	b, err := os.ReadFile(statePath)
	if err != nil {
		return nil
	}
	os.Remove(statePath)
	var h handoff
	if err := json.Unmarshal(b, &h); err != nil {
		log.Printf("handoff: %v", err)
		return nil
	}
	return &h
}

// reexec replaces this process with the uploaded backend, or with itself.
func reexec() error {
	h := handoff{UIPid: uiPid(), Services: map[string]int{}}
	cdMu.Lock()
	h.Cancelled = cancelled || remaining < 0
	cdMu.Unlock()
	svcMu.Lock()
	for _, s := range services {
		if !s.Running {
			continue
		}
		h.Services[s.Name] = s.pid
		if s.pid == 0 {
			// In-process: stopped here, started again by the new image.
			s.stop()
		}
	}
	svcMu.Unlock()
	b, _ := json.Marshal(h)
	if err := os.WriteFile(statePath, b, 0o600); err != nil {
		return err
	}

	unmountBrowsed()
	mountPool.UnmountAll(mount.MNT_DETACH)

	bin := filepath.Join(devDir, "bootmanager")
	argv := os.Args
	if !isFile(bin) {
		// /proc/self/exe is u-root's bb for a built-in backend, which picks
		// the command from argv[0]; os.Args keeps that.
		bin = "/proc/self/exe"
	}
	log.Printf("re-exec %s (ui pid %d, services %v)", bin, h.UIPid, h.Services)
	return syscall.Exec(bin, argv, os.Environ())
}

// devHandler takes the uploads.
func devHandler(w http.ResponseWriter, r *http.Request) {
	if !serviceRunning("dev") {
		http.Error(w, "Dev uploads is off (Services on the phone)", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		http.Error(w, "PUT a file", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/dev/")
	switch {
	case name == "bootmanager-ui", name == "bootmanager":
	case name == "kernel-stage", name == "kernel-pmos":
	case name == "android-kernel", name == "android-initrd", name == "android-dtb", name == "kexec-android":
	case strings.HasPrefix(name, "web/") && !strings.Contains(name[4:], "/"):
	default:
		http.Error(w, "upload bootmanager-ui, bootmanager, kernel-stage, kernel-pmos or web/<file>", http.StatusBadRequest)
		return
	}
	dst := filepath.Join(devDir, name)
	if err := saveUpload(dst, r.Body); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("dev: received %s", dst)

	switch name {
	case "bootmanager-ui":
		restartUI()
		fmt.Fprintln(w, "ui restarted")
	case "bootmanager":
		fmt.Fprintln(w, "backend re-exec")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		go func() {
			time.Sleep(200 * time.Millisecond)
			if err := reexec(); err != nil {
				log.Printf("re-exec: %v", err)
			}
		}()
	case "android-kernel", "android-initrd", "android-dtb":
		fmt.Fprintln(w, "saved; PUT the command line to kexec-android to boot it")
	case "kernel-stage", "kernel-pmos", "kexec-android":
		fmt.Fprintln(w, "kexec into it")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		go func() {
			time.Sleep(200 * time.Millisecond)
			if err := kexecTest(name); err != nil {
				log.Printf("kexec %s: %v", name, err)
				broadcast("error", map[string]string{"message": "kexec " + name + ": " + err.Error()})
			}
		}()
	default:
		fmt.Fprintln(w, "saved; reload the page")
	}
}

// devOnce acts on a one-shot request the uinit found on the boot partition
// (dev.sh stage-hold, from pmOS): /tmp/hold means stay here, so the
// countdown is not even started, and /tmp/dev-once names the services to
// switch on -- Remote UI and Dev uploads, so the next upload is accepted.
func devOnce() {
	if isFile("/tmp/hold") {
		cdMu.Lock()
		cancelled = true
		cdMu.Unlock()
		log.Printf("dev: held for kexec testing, no autoboot")
	}
	b, err := os.ReadFile("/tmp/dev-once")
	if err != nil {
		return
	}
	for _, name := range strings.Fields(string(b)) {
		if name == "hold" {
			continue
		}
		if _, err := setService(name, true); err != nil {
			log.Printf("dev-once: %v", err)
		}
	}
}

// kexecTest boots an uploaded kernel straight from RAM, for testing a
// kernel with no reflash and no install (dev.sh kexec-stage, kexec-pmos):
//
//   - kernel-stage, a LinuxBoot stage Image (its DTB and initramfs are
//     built in), with this stage's command line. It is loaded with the
//     running tree as its DTB, which takes the fork's kexec_load path --
//     the one the pmOS handover proves on this SoC -- and the stage kernel
//     uses its built-in tree regardless.
//   - kernel-pmos, a pmOS Image or Image.gz, booted as the first boot entry
//     with only its kernel replaced: the entry's initramfs, DTB and
//     command line, and the rootfs's modules, so its version string must
//     match the installed kernel's.
//   - kexec-android, stock Android: the android-kernel, android-initrd and
//     android-dtb uploads and this upload's body as the command line, as
//     android-payload.py assembles them from what ABL would hand it. The
//     tree is the live stock one, so the loader keeps out of every region
//     Android reserves (its /reserved-memory and /memreserve/).
//
// Nothing is written: a kernel that does not come up costs a reboot.
func kexecTest(name string) error {
	if name == "kexec-android" {
		return kexecAndroid()
	}
	k, err := os.Open(filepath.Join(devDir, name))
	if err != nil {
		return err
	}
	switch name {
	case "kernel-stage":
		dtb, err := os.Open("/sys/firmware/fdt")
		if err != nil {
			return err
		}
		cmdline, _ := os.ReadFile("/proc/cmdline")
		return handover(&boot.LinuxImage{
			Name:    "stage test kernel",
			Kernel:  k,
			DTB:     dtb,
			Cmdline: strings.TrimSpace(string(cmdline)),
		})
	case "kernel-pmos":
		bootMu.Lock()
		var li *boot.LinuxImage
		if len(images) > 0 {
			li, _ = images[0].(*boot.LinuxImage)
		}
		bootMu.Unlock()
		if li == nil {
			return errors.New("no Linux boot entry to take the initramfs and DTB from")
		}
		test := *li
		test.Name = li.Label() + " (test kernel)"
		test.Kernel = k
		return handover(&test)
	}
	return fmt.Errorf("not a test kernel: %s", name)
}

func kexecAndroid() error {
	open := func(n string) (*os.File, error) {
		f, err := os.Open(filepath.Join(devDir, n))
		if err != nil {
			return nil, fmt.Errorf("%s not uploaded: %w", n, err)
		}
		return f, nil
	}
	k, err := open("android-kernel")
	if err != nil {
		return err
	}
	i, err := open("android-initrd")
	if err != nil {
		return err
	}
	d, err := open("android-dtb")
	if err != nil {
		return err
	}
	cmdline, err := os.ReadFile(filepath.Join(devDir, "kexec-android"))
	if err != nil {
		return err
	}
	return handover(&boot.LinuxImage{
		Name:    "stock Android",
		Kernel:  k,
		Initrd:  i,
		DTB:     d,
		Cmdline: strings.TrimSpace(string(cmdline)),
	})
}

func saveUpload(dst string, body io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n == 0 {
		err = errors.New("empty upload")
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	// A rename, so a running copy of the old file is never half-overwritten.
	return os.Rename(tmp, dst)
}

// devRestart is the socket command: {"what": "ui"|"backend"}, for restarting
// without an upload (after one sent another way, or just to reset state).
func devRestart(args []byte) (any, error) {
	a, err := decode[struct{ What string }](args)
	if err != nil {
		return nil, err
	}
	switch a.What {
	case "ui":
		restartUI()
	case "backend":
		go func() {
			time.Sleep(200 * time.Millisecond)
			if err := reexec(); err != nil {
				log.Printf("re-exec: %v", err)
			}
		}()
	default:
		return nil, fmt.Errorf("restart ui or backend, not %q", a.What)
	}
	return nil, nil
}
