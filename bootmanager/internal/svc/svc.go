// Package svc holds the services the UI switches on and off: child
// processes (sshd) and servers in this process (pid 0).
package svc

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// StartFunc starts a service and returns how to stop it, and its pid when
// it is a child process (0 in-process).
type StartFunc func() (stop func(), pid int, err error)

type Service struct {
	Name   string
	Title  string
	Detail string
	Start  StartFunc
	// Log is the file a child process writes to, whose last line says why
	// it died; "" for none.
	Log string
	// Explain, when set, turns that last line into words for the person at
	// the screen.
	Explain func(line string) string

	running bool
	stop    func()
	pid     int
	failed  string
}

type Status struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Detail  string `json:"detail"`
	Running bool   `json:"running"`
	// Error is why a service that was switched on is not running: its
	// process died. Empty otherwise.
	Error string `json:"error,omitempty"`
}

type Set struct {
	mu   sync.Mutex
	list []*Service
	// Changed, when set, is called (without the lock) after a service
	// stopped by itself, so the UI can say so.
	Changed func()
}

func NewSet(services ...*Service) *Set { return &Set{list: services} }

func (s *Set) find(name string) (*Service, error) {
	for _, v := range s.list {
		if v.Name == name {
			return v, nil
		}
	}
	return nil, fmt.Errorf("no service %q", name)
}

// Set switches a service on or off.
func (s *Set) Set(name string, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.find(name)
	if err != nil || on == v.running {
		return err
	}
	if !on {
		v.stop()
		v.running, v.stop, v.pid, v.failed = false, nil, 0, ""
		log.Printf("service %s stopped", name)
		return nil
	}
	v.failed = ""
	stop, pid, err := v.Start()
	if err != nil {
		return fmt.Errorf("%s: %w", v.Title, err)
	}
	v.running, v.stop, v.pid = true, stop, pid
	log.Printf("service %s started", name)
	if pid > 0 {
		go s.watch(v, pid)
	}
	return nil
}

// watch notices a child process dying on its own: the service is marked
// off, with the last line of its log as the reason, rather than staying
// "on" with nothing behind it.
func (s *Set) watch(v *Service, pid int) {
	for {
		time.Sleep(500 * time.Millisecond)
		if syscall.Kill(pid, 0) == nil {
			s.mu.Lock()
			ours := v.running && v.pid == pid
			s.mu.Unlock()
			if !ours {
				return
			}
			continue
		}
		s.mu.Lock()
		if !v.running || v.pid != pid {
			s.mu.Unlock()
			return
		}
		v.running, v.stop, v.pid = false, nil, 0
		v.failed = lastLine(v.Log)
		if v.failed != "" && v.Explain != nil {
			v.failed = v.Explain(v.failed)
		}
		if v.failed == "" {
			v.failed = "the process exited"
		}
		log.Printf("service %s died: %s", v.Name, v.failed)
		changed := s.Changed
		s.mu.Unlock()
		if changed != nil {
			changed()
		}
		return
	}
}

// lastLine is the last non-empty line of a file, shortened, "" if there is
// none.
func lastLine(path string) string {
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	if len(l) > 160 {
		l = l[:160]
	}
	return l
}

func (s *Set) Running(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.find(name)
	return err == nil && v.running
}

func (s *Set) List() []Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Status, 0, len(s.list))
	for _, v := range s.list {
		out = append(out, Status{v.Name, v.Title, v.Detail, v.running, v.failed})
	}
	return out
}

func (s *Set) StopAll() {
	for _, v := range s.list {
		s.Set(v.Name, false)
	}
}

// Running services by name, with their pids (0 in-process).
func (s *Set) Snapshot() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[string]int{}
	for _, v := range s.list {
		if v.running {
			m[v.Name] = v.pid
		}
	}
	return m
}

// Adopt takes over a running child process as service name's, as left by
// the image this process exec'd from.
func (s *Set) Adopt(name string, pid int) error {
	p, err := os.FindProcess(pid)
	if err == nil {
		err = p.Signal(syscall.Signal(0))
	}
	if err != nil {
		return fmt.Errorf("pid %d: %w", pid, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.find(name)
	if err != nil {
		return err
	}
	v.running, v.stop, v.pid = true, Waitable(p), pid
	if pid > 0 {
		go s.watch(v, pid)
	}
	return nil
}

// Process is a StartFunc running name with args, logging to
// /tmp/<name>.log.
func Process(name string, args ...string) StartFunc {
	return func() (func(), int, error) {
		cmd := exec.Command(name, args...)
		if f, err := os.OpenFile("/tmp/"+name+".log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			cmd.Stdout, cmd.Stderr = f, f
			defer f.Close()
		}
		if err := cmd.Start(); err != nil {
			return nil, 0, err
		}
		return Waitable(cmd.Process), cmd.Process.Pid, nil
	}
}

// InProcess adapts a server started in this process to a StartFunc.
func InProcess(start func() (stop func(), err error)) StartFunc {
	return func() (func(), int, error) {
		stop, err := start()
		return stop, 0, err
	}
}

// Waitable reaps p in the background and returns its stop function:
// SIGTERM, then wait for it.
func Waitable(p *os.Process) func() {
	done := make(chan struct{})
	go func() {
		p.Wait()
		close(done)
	}()
	return func() {
		p.Signal(syscall.SIGTERM)
		<-done
	}
}
