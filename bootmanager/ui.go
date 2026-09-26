package main

import (
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

var (
	uiMu       sync.Mutex
	uiProc     *os.Process
	uiStopping bool
	uiRestart  bool
)

// superviseUI runs the UI for as long as the backend lives, from
// uiExecutable() each time, so a restart picks up an upload. A UI that dies
// straight away five times in a row is given up on: the backend and its
// autoboot carry on without it. adopt is a UI already running as our child,
// left by the image this one replaced (dev.go).
func superviseUI(adopt int) {
	quick := 0
	for quick < 5 {
		start := time.Now()
		var p *os.Process
		if adopt > 0 {
			p, _ = os.FindProcess(adopt)
			adopt = 0
			log.Printf("ui: adopted pid %d", p.Pid)
		} else {
			path := uiExecutable()
			cmd := exec.Command(path, "--socket", *socketPath)
			// Into the log (and the console) with the rest, so what the UI
			// found -- fb0 format, input devices -- is on the Log screen.
			cmd.Stdout = log.Writer()
			cmd.Stderr = log.Writer()
			if err := cmd.Start(); err != nil {
				log.Printf("ui: %s: %v", path, err)
				return
			}
			p = cmd.Process
			log.Printf("ui: started %s, pid %d", path, p.Pid)
		}
		uiMu.Lock()
		uiProc = p
		uiMu.Unlock()

		st, err := p.Wait()

		uiMu.Lock()
		uiProc = nil
		stopping, restart := uiStopping, uiRestart
		uiRestart = false
		uiMu.Unlock()
		if stopping {
			return
		}
		if restart {
			quick = 0
			continue
		}
		log.Printf("ui exited: %v %v", st, err)
		if time.Since(start) < 10*time.Second {
			quick++
		} else {
			quick = 0
		}
		time.Sleep(time.Second)
	}
	log.Printf("ui: giving up after %d quick exits", quick)
}

func uiPid() int {
	uiMu.Lock()
	defer uiMu.Unlock()
	if uiProc == nil {
		return 0
	}
	return uiProc.Pid
}

// signalUI sends SIGTERM (the UI hands the console back to text mode on it)
// and waits up to a second before killing.
func signalUI() {
	uiMu.Lock()
	p := uiProc
	uiMu.Unlock()
	if p == nil {
		return
	}
	p.Signal(syscall.SIGTERM)
	for i := 0; i < 20; i++ {
		time.Sleep(50 * time.Millisecond)
		if uiPid() == 0 {
			return
		}
	}
	p.Kill()
}

// stopUI ends the UI for good, before a handover, so the next kernel
// inherits the display in the state the stage without a UI leaves it.
func stopUI() {
	uiMu.Lock()
	uiStopping = true
	uiMu.Unlock()
	signalUI()
}

// restartUI replaces the running UI with a fresh start of uiExecutable().
func restartUI() {
	uiMu.Lock()
	uiRestart = true
	uiMu.Unlock()
	signalUI()
}
