// Package uiproc keeps the UI process running.
package uiproc

import (
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type Supervisor struct {
	// Path is the UI binary to start, asked afresh at each start so a
	// restart picks up a new build.
	Path func() string
	Args []string
	// Output gets the UI's stdout and stderr: what it found (fb format,
	// input devices) belongs on the Log screen.
	Output io.Writer

	mu       sync.Mutex
	proc     *os.Process
	exited   chan struct{}
	stopping bool
	restart  bool
	// resumeCh is non-nil while the UI is suspended: Run waits on it.
	resumeCh chan struct{}
}

// Suspend ends the UI and keeps it down until Resume, so a program can have
// the panel and the touch screen: the UI's end puts the console back to text
// mode, and its restart then draws the whole screen afresh, which a stopped
// and continued UI could not.
func (s *Supervisor) Suspend() {
	s.mu.Lock()
	if s.resumeCh != nil {
		s.mu.Unlock()
		return
	}
	s.resumeCh = make(chan struct{})
	p, exited := s.proc, s.exited
	s.mu.Unlock()
	if p == nil {
		return
	}
	p.Signal(syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(time.Second):
		p.Kill()
		<-exited
	}
}

// Resume starts the UI again after Suspend.
func (s *Supervisor) Resume() {
	s.mu.Lock()
	ch := s.resumeCh
	s.resumeCh = nil
	s.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// Run runs the UI for as long as the backend lives. A UI that dies within
// 10 s five times in a row is given up on: the backend and its autoboot
// carry on without it. adopt is a UI already running as this process's
// child (left by the image it exec'd from), or 0.
func (s *Supervisor) Run(adopt int) {
	for quick := 0; quick < 5; {
		p, err := s.start(adopt)
		adopt = 0
		if err != nil {
			log.Printf("ui: %v", err)
			return
		}
		exited := make(chan struct{})
		s.mu.Lock()
		s.proc, s.exited = p, exited
		s.mu.Unlock()

		started := time.Now()
		st, err := p.Wait()
		close(exited)

		s.mu.Lock()
		s.proc = nil
		stopping, restart := s.stopping, s.restart
		s.restart = false
		suspended := s.resumeCh
		s.mu.Unlock()
		switch {
		case stopping:
			return
		case suspended != nil:
			<-suspended
			quick = 0
			continue
		case restart:
			quick = 0
			continue
		case time.Since(started) < 10*time.Second:
			quick++
		default:
			quick = 0
		}
		log.Printf("ui exited: %v %v", st, err)
		time.Sleep(time.Second)
	}
	log.Printf("ui: giving up after 5 quick exits")
}

func (s *Supervisor) start(adopt int) (*os.Process, error) {
	if adopt > 0 {
		log.Printf("ui: adopted pid %d", adopt)
		return os.FindProcess(adopt)
	}
	path := s.Path()
	cmd := exec.Command(path, s.Args...)
	cmd.Stdout, cmd.Stderr = s.Output, s.Output
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	log.Printf("ui: started %s, pid %d", path, cmd.Process.Pid)
	return cmd.Process, nil
}

// Pid is the running UI's, or 0.
func (s *Supervisor) Pid() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proc == nil {
		return 0
	}
	return s.proc.Pid
}

// Stop ends the UI for good, before a handover: SIGTERM hands the console
// back to text mode, so the next kernel inherits the display as a stage
// without a UI leaves it.
func (s *Supervisor) Stop() { s.signal(func() { s.stopping = true }) }

// Restart replaces the running UI with a fresh start.
func (s *Supervisor) Restart() { s.signal(func() { s.restart = true }) }

// signal sets a flag for Run, sends SIGTERM and waits up to a second
// before killing.
func (s *Supervisor) signal(set func()) {
	s.mu.Lock()
	set()
	p, exited := s.proc, s.exited
	s.mu.Unlock()
	if p == nil {
		return
	}
	p.Signal(syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(time.Second):
		p.Kill()
		<-exited
	}
}
