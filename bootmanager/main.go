// Command bootmanager is a boot manager for a LinuxBoot stage: it owns every privileged action (boot, services, mounts, power)
// and serves them to bootmanager-ui, the Slint front end: on the device's
// panel, which it starts and restarts, and its web build in a browser.
//
// The protocol is internal/rpc's; any client works. By hand over SSH:
// `netcat -U /tmp/bootmanager.sock` (keep its stdin open until the reply:
// u-root's netcat quits at EOF).
//
// Autoboot lives here, not in the UI, so the stage still boots its default
// entry if the UI never draws.
//
// -scan DIR parses DIR's extlinux.conf instead of probing block devices,
// so the backend runs anywhere for UI work; -compatible then stands in
// for the device tree's compatibles.
//
// -wait-ready lets the stage start this first, for the UI's splash to be
// up while the stage sets itself up: the UI and the socket start at once,
// and what needs that setup (the card, the keys, usb0) waits for
// `bootmanager -send stage.ready`, or readyTimeout.
package main

import (
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"bootmanager.local/bootmanager/internal/boot"
	"bootmanager.local/bootmanager/internal/rpc"
	"bootmanager.local/bootmanager/internal/storage"
	"bootmanager.local/bootmanager/internal/svc"
	"bootmanager.local/bootmanager/internal/uiproc"
)

var (
	socketPath  = flag.String("socket", "/tmp/bootmanager.sock", "IPC socket")
	uiPath      = flag.String("ui", "/bin/bootmanager-ui", "UI binary; empty for none")
	webDir      = flag.String("web", "/usr/share/bootmanager-web", "web build of bootmanager-ui, served by Remote UI")
	autoboot    = flag.Int("autoboot", 10, "seconds before booting the default entry; 0 disables")
	holdFile    = flag.String("hold", "/tmp/hold", "stay in the stage when this file exists as the countdown starts or ends")
	scanDir     = flag.String("scan", "", "parse this directory's boot config instead of probing block devices")
	blDir       = flag.String("bootloader", "/sys/firmware/bootloader", "the device tree and initramfs the bootloader handed the stage (fdt, initrd), if it keeps them; with them, an Android boot image at "+boot.AndroidImage+" on a partition is an entry")
	logPath     = flag.String("log", "/tmp/bootmanager.log", "log file, also shown on the Log screen")
	extraLogs   = flag.String("logs", "", "comma-separated files shown on the Log screen before the log (the stage's own, say)")
	listen      = flag.String("listen", "127.0.0.1", "address Remote UI listens on: a USB network's, say, never one reachable from elsewhere; the default needs an SSH tunnel")
	rebootModes = flag.String("reboot-modes", "bootloader,fastboot,recovery,download", "comma-separated reboot modes offered when the device tree maps them; leave out any that strand the device (edl)")
	panelFile   = flag.String("panel", "", "gmobile display-panel file for this device, instead of looking it up in gmobile's set")
	compatible  = flag.String("compatible", "", "space-separated DT compatibles to look the panel up by, instead of /proc/device-tree (for -scan)")
	sshKeys     = flag.String("ssh-keys", "/etc/ssh/authorized_keys", "sshd's authorized_keys")
	sshHostKey  = flag.String("ssh-hostkey", "/etc/ssh/ssh_host_rsa_key", "sshd's host key")
	waitReady   = flag.Bool("wait-ready", false, "hold the boot scan, disks, services and dev-once until stage.ready (or readyTimeout)")
	sendCmd     = flag.String("send", "", "send this command to -socket, print the reply and exit: a client, not the backend")
)

// readyTimeout: a stage that never sends stage.ready still boots.
const readyTimeout = 20 * time.Second

type app struct {
	rpc      *rpc.Server
	boot     *boot.Manager
	services []*svc.Service // until svcs is made from them
	svcs     *svc.Set
	disks    *storage.Browser
	ui       *uiproc.Supervisor
	web      *webServer

	ready     chan struct{} // closed once the stage is set up
	readyOnce sync.Once
}

// markReady is stage.ready: idempotent, and a no-op without -wait-ready.
func (a *app) markReady() { a.readyOnce.Do(func() { close(a.ready) }) }

func (a *app) isReady() bool {
	select {
	case <-a.ready:
		return true
	default:
		return false
	}
}

// errNotReady refuses what needs the stage's setup until it is done.
var errNotReady = errors.New("the stage is still starting")

// devExt is the dev build's extension (dev.go): live restarts and test
// kernels. nil in a normal build.
var devExt interface {
	// register adds the dev service, routes and commands.
	register(*app)
	// resume takes over what an exec'd predecessor left running.
	resume(*app) (resumed bool, uiPid int)
	// oneShot applies the stage's one-shot requests (-dev-once).
	oneShot(*app)
}

func main() {
	flag.Parse()
	if *sendCmd != "" {
		os.Exit(send(*socketPath, *sendCmd))
	}
	logOut := io.Writer(os.Stderr)
	if f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		logOut = io.MultiWriter(os.Stderr, f)
	}
	log.SetOutput(logOut)
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	log.Printf("bootmanager starting")

	a := &app{rpc: rpc.NewServer(), disks: storage.NewBrowser(), ready: make(chan struct{})}
	if !*waitReady {
		a.markReady()
	}
	a.boot = boot.New(boot.Config{
		ScanDir:       *scanDir,
		Autoboot:      *autoboot,
		HoldFile:      *holdFile,
		BootloaderDir: *blDir,
		Emit:          a.rpc.Broadcast,
		Teardown:      a.teardown,
	})
	a.ui = &uiproc.Supervisor{
		Path:   func() string { return *uiPath },
		Args:   []string{"--socket", *socketPath},
		Output: logOut,
	}
	a.web = newWebServer(a)
	a.services = []*svc.Service{
		{
			Name:   "ssh",
			Title:  "SSH",
			Detail: "port 22, public key only",
			Start:  svc.Process("sshd", "-port", "22", "-keys", *sshKeys, "-privatekey", *sshHostKey),
		},
		{
			Name:   "remote",
			Title:  "Remote UI",
			Detail: "http://" + net.JoinHostPort(*listen, webPort),
			Start:  svc.InProcess(a.startRemote),
		},
	}
	a.rpc.Greeting = func() []rpc.Event { return []rpc.Event{{Event: "autoboot", Data: a.boot.Autoboot()}} }
	a.register()
	if devExt != nil {
		devExt.register(a)
	}
	a.svcs = svc.NewSet(a.services...)

	os.Remove(*socketPath)
	ln, err := net.Listen("unix", *socketPath)
	if err != nil {
		log.Fatalf("listen %s: %v", *socketPath, err)
	}

	resumed, uiPid := false, 0
	if devExt != nil {
		resumed, uiPid = devExt.resume(a)
	}
	if resumed {
		// An exec in place: the stage was set up before, and argv still
		// says -wait-ready.
		a.markReady()
	}
	if *uiPath != "" || uiPid > 0 {
		go a.ui.Run(uiPid)
	} else {
		log.Printf("-ui empty, running headless")
	}
	// Nothing closes the socket, so this does not return; if it ever did,
	// the countdown and the Remote UI still need this process.
	go a.rpc.ServeListener(ln)

	select {
	case <-a.ready:
	case <-time.After(readyTimeout):
		log.Printf("no stage.ready in %v; going ahead", readyTimeout)
		a.markReady()
	}
	if !resumed {
		if devExt != nil {
			devExt.oneShot(a)
		}
		// On at boot, as it was before the UI existed: it is how the stage
		// is reached when the panel does not come up.
		if err := a.svcs.Set("ssh", true); err != nil {
			log.Printf("ssh: %v", err)
		}
	}
	go a.boot.Run()
	select {}
}

// teardown ends whatever must not outlive the stage, before a handover or
// a power action.
func (a *app) teardown() {
	a.boot.Cancel()
	a.ui.Stop()
	a.svcs.StopAll()
	a.disks.UnmountAll()
	a.boot.Unmount()
}

func (a *app) logFiles() []string {
	var files []string
	for _, f := range strings.Split(*extraLogs, ",") {
		if f != "" {
			files = append(files, f)
		}
	}
	return append(files, *logPath)
}
