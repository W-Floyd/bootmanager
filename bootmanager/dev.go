//go:build dev

// Dev builds (go build -tags dev) add what working on the boot manager
// and kernels needs without a reflash, behind the Dev uploads service
// (which rides on Remote UI's HTTP port, so both must be on):
//
//	PUT /dev/bootmanager-ui   restarts the UI with it
//	PUT /dev/bootmanager      re-execs the backend with it
//	PUT /dev/web/<file>       served from the next reload
//	POST /dev/kexec           boots a kernel from RAM (kexec below)
//
// dev.sh sends them. Uploads live in /tmp/dev, in RAM, and win over the
// built-in copies until the next boot. A normal build has none of this:
// no remote path to run code as root.
//
// The backend restarts by exec'ing itself (or an upload) in place: the
// PID stays, so sshd and the UI stay its children and keep running -- an
// SSH session survives -- and a state file hands the new image what it
// cannot rediscover: those PIDs, which services were on, and whether the
// autoboot was already stopped. The sockets close on exec; the UI
// reconnects by itself.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	uboot "github.com/u-root/u-root/pkg/boot"

	"bootmanager.local/bootmanager/internal/fastboot"
	"bootmanager.local/bootmanager/internal/rpc"
	"bootmanager.local/bootmanager/internal/svc"
)

const (
	devDir    = "/tmp/dev"
	statePath = "/tmp/bootmanager-state.json"
	maxUpload = 512 << 20
)

var devOnce = flag.String("dev-once", "", "file naming services to switch on at start, one-shot (the stage's dev.sh stage-hold)")

func init() {
	devExt = dev{}
	// Words in /tmp/cmdline-add (RAM, gone at reboot) go onto the command line of
	// whatever is booted next: `echo pmos.debug-shell > /tmp/cmdline-add`.
	cmdlineExtra = func() string {
		b, _ := os.ReadFile("/tmp/cmdline-add")
		return string(b)
	}
}

type dev struct{}

type handoff struct {
	Cancelled bool           `json:"cancelled"`
	UIPid     int            `json:"ui_pid"`
	Services  map[string]int `json:"services"` // name -> pid (0: in-process)
}

func (dev) register(a *app) {
	a.services = append(a.services, &svc.Service{
		Name:   "dev",
		Title:  "Dev uploads",
		Detail: "PUT to :" + webPort + "/dev/ replaces the UI or backend",
		// Only a gate: upload checks it.
		Start: svc.InProcess(func() (func(), error) { return func() {}, nil }),
	})
	a.web.mux.HandleFunc("/dev/", func(w http.ResponseWriter, r *http.Request) { upload(a, w, r) })
	a.web.static = overlay{http.Dir(filepath.Join(devDir, "web")), a.web.static}
	builtin := a.ui.Path
	a.ui.Path = func() string {
		if p := filepath.Join(devDir, "bootmanager-ui"); isFile(p) {
			return p
		}
		return builtin()
	}
	// A fake flash event, to see the flash screen without flashing: the
	// body is a fastboot.Progress (phase, name, piece, recv, recv_total,
	// wrote, wrote_total, end, total); an empty phase clears the screen.
	rpc.Handle(a.rpc, "dev.flash", func(p fastboot.Progress) (any, error) {
		a.rpc.Broadcast("flash", flashEvent{p})
		return nil, nil
	})
	rpc.Handle(a.rpc, "dev.restart", func(x struct{ What string }) (any, error) {
		switch x.What {
		case "ui":
			a.ui.Restart()
		case "backend":
			go reexec(a)
		default:
			return nil, fmt.Errorf("restart ui or backend, not %q", x.What)
		}
		return nil, nil
	})
}

func (dev) resume(a *app) (bool, int) {
	b, err := os.ReadFile(statePath)
	if err != nil {
		return false, 0
	}
	os.Remove(statePath)
	var h handoff
	if err := json.Unmarshal(b, &h); err != nil {
		log.Printf("handoff: %v", err)
		return false, 0
	}
	log.Printf("restarted in place: ui pid %d, services %v", h.UIPid, h.Services)
	if h.Cancelled {
		a.boot.CancelBy("an earlier run (restarted in place)")
	}
	// Children by pid; in-process servers, and children that died, anew.
	for name, pid := range h.Services {
		if pid > 0 {
			err := a.svcs.Adopt(name, pid)
			if err == nil {
				continue
			}
			log.Printf("adopt %s: %v; starting afresh", name, err)
		}
		if err := a.svcs.Set(name, true); err != nil {
			log.Printf("%s: %v", name, err)
		}
	}
	return true, h.UIPid
}

// oneShot switches on the services a one-shot request names: Remote UI
// and Dev uploads, so the next upload is accepted.
func (dev) oneShot(a *app) {
	if *devOnce == "" {
		return
	}
	b, err := os.ReadFile(*devOnce)
	if err != nil {
		return
	}
	for _, name := range strings.Fields(string(b)) {
		if name == "hold" {
			continue // the stage's own: it made the hold file
		}
		if err := a.svcs.Set(name, true); err != nil {
			log.Printf("dev-once: %v", err)
		}
	}
}

// reexec replaces this process with the uploaded backend, or with itself.
func reexec(a *app) {
	time.Sleep(200 * time.Millisecond) // let the reply go out
	h := handoff{Cancelled: a.boot.Cancelled(), UIPid: a.ui.Pid(), Services: a.svcs.Snapshot()}
	// In-process services die with the image; the new one starts them.
	for name, pid := range h.Services {
		if pid == 0 {
			a.svcs.Set(name, false)
		}
	}
	err := func() error {
		b, _ := json.Marshal(h)
		if err := os.WriteFile(statePath, b, 0o600); err != nil {
			return err
		}
		a.disks.UnmountAll()
		a.boot.Unmount()
		bin, argv := filepath.Join(devDir, "bootmanager"), os.Args
		if !isFile(bin) {
			// A built-in backend is u-root's busybox, which picks the
			// command from argv[0]; os.Args keeps that.
			bin = "/proc/self/exe"
		}
		log.Printf("re-exec %s (ui pid %d, services %v)", bin, h.UIPid, h.Services)
		return syscall.Exec(bin, argv, os.Environ())
	}()
	log.Printf("re-exec: %v", err)
	os.Remove(statePath)
	for name, pid := range h.Services {
		if pid == 0 {
			a.svcs.Set(name, true)
		}
	}
}

func upload(a *app, w http.ResponseWriter, r *http.Request) {
	if !a.svcs.Running("dev") {
		http.Error(w, "Dev uploads is off (Services, on the device)", http.StatusForbidden)
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/dev/")
	if name == "kexec" {
		kexec(a, w, r)
		return
	}
	// PUT only: a cross-site page can send a POST without the browser
	// asking this server first.
	if r.Method != http.MethodPut {
		http.Error(w, "PUT a file", http.StatusMethodNotAllowed)
		return
	}
	switch {
	case name == "bootmanager-ui", name == "bootmanager":
	case strings.HasPrefix(name, "web/") && !strings.Contains(name[4:], "/"):
	default:
		http.Error(w, "PUT bootmanager-ui, bootmanager or web/<file>", http.StatusBadRequest)
		return
	}
	dst := filepath.Join(devDir, name)
	if err := save(dst, http.MaxBytesReader(w, r.Body, maxUpload)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("dev: received %s", dst)
	switch name {
	case "bootmanager-ui":
		a.ui.Restart()
		fmt.Fprintln(w, "ui restarted")
	case "bootmanager":
		fmt.Fprintln(w, "backend re-exec")
		go reexec(a)
	default:
		fmt.Fprintln(w, "saved; reload the page")
	}
}

// kexec boots a kernel straight from RAM, for testing one with no reflash
// and no install. A multipart PUT with the parts:
//
//	kernel   the kernel (required)
//	initrd   its initramfs
//	dtb      its device tree
//	cmdline  its command line
//	inherit  where missing parts come from: "running" (this stage's tree
//	         and command line), or "entry:N" (boot entry N's initramfs,
//	         tree and command line); none by default
//
// "entry:N" with only a kernel boots that entry with its kernel replaced,
// so the kernel's release must match the entry's installed modules.
// Nothing is written: a kernel that does not come up costs a reboot.
func kexec(a *app, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "PUT multipart/form-data", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	img, err := kexecImage(a, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	fmt.Fprintln(w, "kexec into it")
	go func() {
		time.Sleep(200 * time.Millisecond)
		if err := a.boot.Handover(img); err != nil {
			log.Printf("kexec: %v", err)
			a.rpc.Broadcast("error", map[string]string{"message": "kexec: " + err.Error()})
		}
	}()
}

func kexecImage(a *app, r *http.Request) (*uboot.LinuxImage, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(devDir, "kexec")
	os.RemoveAll(dir)
	parts := map[string]string{}
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch n := p.FormName(); n {
		case "kernel", "initrd", "dtb":
			if err := save(filepath.Join(dir, n), p); err != nil {
				return nil, err
			}
			parts[n] = filepath.Join(dir, n)
		case "cmdline", "inherit":
			b, err := io.ReadAll(io.LimitReader(p, 64<<10))
			if err != nil {
				return nil, err
			}
			parts[n] = strings.TrimSpace(string(b))
		default:
			return nil, fmt.Errorf("unknown part %q", n)
		}
	}
	if parts["kernel"] == "" {
		return nil, errors.New("no kernel part")
	}

	img := &uboot.LinuxImage{Name: "test kernel"}
	switch inherit := parts["inherit"]; {
	case inherit == "":
	case inherit == "running":
		img.DTB, err = os.Open("/sys/firmware/fdt")
		if err != nil {
			return nil, err
		}
		b, _ := os.ReadFile("/proc/cmdline")
		img.Cmdline = strings.TrimSpace(string(b))
	case strings.HasPrefix(inherit, "entry:"):
		i, err := strconv.Atoi(inherit[len("entry:"):])
		if err != nil {
			return nil, fmt.Errorf("inherit: %w", err)
		}
		e, err := a.boot.Image(i)
		if err != nil {
			return nil, err
		}
		li, ok := e.(*uboot.LinuxImage)
		if !ok {
			return nil, fmt.Errorf("entry %d is not a Linux image", i)
		}
		copied := *li
		img = &copied
		img.Name = li.Label() + " (test kernel)"
	default:
		return nil, fmt.Errorf("inherit %q: want running or entry:N", inherit)
	}
	open := func(n string) (*os.File, error) { return os.Open(parts[n]) }
	if img.Kernel, err = open("kernel"); err != nil {
		return nil, err
	}
	if parts["initrd"] != "" {
		if img.Initrd, err = open("initrd"); err != nil {
			return nil, err
		}
	}
	if parts["dtb"] != "" {
		if img.DTB, err = open("dtb"); err != nil {
			return nil, err
		}
	}
	if c, ok := parts["cmdline"]; ok {
		img.Cmdline = c
	}
	return img, nil
}

// save writes body to dst by way of a rename, so a running copy of the
// old file is never half-overwritten.
func save(dst string, body io.Reader) error {
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
	return os.Rename(tmp, dst)
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// overlay is a stack of file systems, the first that has a file winning.
type overlay []http.FileSystem

func (o overlay) Open(name string) (http.File, error) {
	var first error
	for _, l := range o {
		f, err := l.Open(name)
		if err == nil {
			return f, nil
		}
		if first == nil {
			first = err
		}
	}
	return nil, first
}
