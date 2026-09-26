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
// run as a child; telnet and the remote UI are served from this process.
type service struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Detail  string `json:"detail"`
	Running bool   `json:"running"`

	start func() (stop func(), err error)
	stop  func()
}

var (
	svcMu    sync.Mutex
	services = []*service{
		{
			Name:   "ssh",
			Title:  "SSH",
			Detail: "port 22, public key only",
			start: func() (func(), error) {
				return startProcess("sshd", "-port", "22",
					"-keys", "/etc/ssh/authorized_keys",
					"-privatekey", "/etc/ssh/ssh_host_rsa_key")
			},
		},
		{
			Name:   "telnet",
			Title:  "Telnet",
			Detail: "port 23, no password",
			start:  func() (func(), error) { return startTelnet(":23") },
		},
		{
			Name:   "remote",
			Title:  "Remote UI",
			Detail: "http://172.16.42.1:8080  ·  app :7777",
			start:  startRemote,
		},
	}
)

// sshd is on at boot, as it was before the UI existed: it is how the stage
// is reached when the panel does not come up.
func startDefaultServices() {
	if _, err := setService("ssh", true); err != nil {
		log.Printf("ssh: %v", err)
	}
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
			stop, err := s.start()
			if err != nil {
				return s, fmt.Errorf("%s: %w", s.Title, err)
			}
			s.stop, s.Running = stop, true
			log.Printf("service %s started", name)
		} else {
			s.stop()
			s.stop, s.Running = nil, false
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

func startProcess(name string, args ...string) (func(), error) {
	cmd := exec.Command(name, args...)
	f, _ := os.OpenFile("/tmp/"+name+".log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		cmd.Wait()
		close(done)
	}()
	return func() {
		cmd.Process.Signal(syscall.SIGTERM)
		<-done
	}, nil
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
