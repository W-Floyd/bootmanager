package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// A service is something the UI can switch on and off. sshd is u-root's,
// run as a child; telnet, the remote UI and dev uploads are served from
// this process (pid 0).
type service struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Detail  string `json:"detail"`
	Running bool   `json:"running"`

	start func() (stop func(), pid int, err error)
	stop  func()
	pid   int
}

func inProcess(f func() (func(), error)) func() (func(), int, error) {
	return func() (func(), int, error) {
		stop, err := f()
		return stop, 0, err
	}
}

var (
	svcMu    sync.Mutex
	services []*service
)

// Filled in init, as handlers are: as a literal it is an initialisation
// cycle (Remote UI serves dev uploads, which re-exec, which reads services).
func init() {
	services = []*service{
		{
			Name:   "ssh",
			Title:  "SSH",
			Detail: "port 22, public key only",
			start: func() (func(), int, error) {
				return startProcess("sshd", "-port", "22",
					"-keys", "/etc/ssh/authorized_keys",
					"-privatekey", "/etc/ssh/ssh_host_rsa_key")
			},
		},
		{
			Name:   "telnet",
			Title:  "Telnet",
			Detail: "port 23, no password",
			start:  inProcess(func() (func(), error) { return startTelnet(":23") }),
		},
		{
			Name:   "remote",
			Title:  "Remote UI",
			Detail: "http://172.16.42.1:8080  ·  app :7777",
			start:  inProcess(startRemote),
		},
		{
			Name:   "dev",
			Title:  "Dev uploads",
			Detail: "PUT to :8080/dev/ replaces the UI or backend",
			// Only a gate: devHandler checks it, on Remote UI's server.
			start: inProcess(func() (func(), error) { return func() {}, nil }),
		},
	}
}

// sshd is on at boot, as it was before the UI existed: it is how the stage
// is reached when the panel does not come up.
func startDefaultServices() {
	if _, err := setService("ssh", true); err != nil {
		log.Printf("ssh: %v", err)
	}
}

// adoptServices takes over what an exec'd predecessor was running (dev.go):
// child processes by PID, in-process servers by starting them again.
func adoptServices(running map[string]int) {
	for name, pid := range running {
		if pid == 0 {
			if _, err := setService(name, true); err != nil {
				log.Printf("adopt %s: %v", name, err)
			}
			continue
		}
		p, err := os.FindProcess(pid)
		if err == nil {
			err = p.Signal(syscall.Signal(0))
		}
		if err != nil {
			log.Printf("adopt %s: pid %d: %v; starting afresh", name, pid, err)
			setService(name, true)
			continue
		}
		svcMu.Lock()
		for _, s := range services {
			if s.Name == name {
				s.stop, s.pid, s.Running = waitable(p), pid, true
				log.Printf("service %s adopted, pid %d", name, pid)
			}
		}
		svcMu.Unlock()
	}
}

func serviceRunning(name string) bool {
	svcMu.Lock()
	defer svcMu.Unlock()
	for _, s := range services {
		if s.Name == name {
			return s.Running
		}
	}
	return false
}

func stopServices() {
	for _, s := range services {
		setService(s.Name, false)
	}
}

func setService(name string, on bool) (*service, error) {
	svcMu.Lock()
	defer svcMu.Unlock()
	for _, s := range services {
		if s.Name != name {
			continue
		}
		if on == s.Running {
			return s, nil
		}
		if on {
			stop, pid, err := s.start()
			if err != nil {
				return s, fmt.Errorf("%s: %w", s.Title, err)
			}
			s.stop, s.pid, s.Running = stop, pid, true
			log.Printf("service %s started", name)
		} else {
			s.stop()
			s.stop, s.pid, s.Running = nil, 0, false
			log.Printf("service %s stopped", name)
		}
		return s, nil
	}
	return nil, fmt.Errorf("no service %q", name)
}

func serviceListData() []service {
	svcMu.Lock()
	defer svcMu.Unlock()
	out := make([]service, 0, len(services))
	for _, s := range services {
		out = append(out, *s)
	}
	return out
}

func serviceList([]byte) (any, error) { return serviceListData(), nil }

func serviceSet(args []byte) (any, error) {
	a, err := decode[struct {
		Name    string
		Running bool
	}](args)
	if err != nil {
		return nil, err
	}
	_, err = setService(a.Name, a.Running)
	list := serviceListData()
	broadcast("services", list)
	return list, err
}

func startProcess(name string, args ...string) (func(), int, error) {
	cmd := exec.Command(name, args...)
	f, _ := os.OpenFile("/tmp/"+name+".log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		return nil, 0, err
	}
	return waitable(cmd.Process), cmd.Process.Pid, nil
}

// waitable reaps p in the background and returns its stop function.
func waitable(p *os.Process) func() {
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

// startRemote serves the UI to other machines: the same JSON-lines protocol
// raw over TCP for a native recovery-ui (--connect), and over a WebSocket
// next to the web build of recovery-ui for a browser. There is no
// authentication; the stage's only network is the USB link.
func startRemote() (func(), error) {
	tcp, err := net.Listen("tcp", ":7777")
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			c, err := tcp.Accept()
			if err != nil {
				return
			}
			go serve(c)
		}
	}()

	httpLn, err := net.Listen("tcp", ":8080")
	if err != nil {
		tcp.Close()
		return nil, err
	}
	srv := &http.Server{Handler: webHandler()}
	go srv.Serve(httpLn)
	return func() {
		tcp.Close()
		srv.Close()
	}, nil
}
