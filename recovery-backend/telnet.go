package main

import (
	"io"
	"log"
	"net"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// startTelnet is a minimal telnetd: each connection gets gosh on a pty.
// pmOS's initramfs offers the same (a telnet debug shell, no password) on
// its USB network; this is that for the stage, off until switched on.
//
// Option negotiation is the least that puts a client in character mode:
// the server offers ECHO and SUPPRESS-GO-AHEAD and then discards every IAC
// sequence the client sends.
func startTelnet(addr string) (func(), error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go telnetSession(c)
		}
	}()
	return func() { ln.Close() }, nil
}

const (
	iac  = 255
	will = 251
	sb   = 250
	se   = 240
)

func telnetSession(c net.Conn) {
	defer c.Close()
	log.Printf("telnet: connection from %s", c.RemoteAddr())
	c.Write([]byte{iac, will, 1, iac, will, 3}) // WILL ECHO, WILL SGA

	cmd := exec.Command("gosh")
	cmd.Env = append(os.Environ(), "TERM=vt100")
	tty, err := pty.Start(cmd)
	if err != nil {
		io.WriteString(c, "telnet: "+err.Error()+"\r\n")
		return
	}
	defer func() {
		tty.Close()
		cmd.Process.Kill()
		cmd.Wait()
	}()
	go io.Copy(c, tty)

	buf := make([]byte, 1024)
	state := 0 // 0 data, 1 after IAC, 2 option byte, 3 in SB, 4 IAC in SB
	for {
		n, err := c.Read(buf)
		if err != nil {
			return
		}
		out := buf[:0]
		for _, b := range buf[:n] {
			switch state {
			case 0:
				if b == iac {
					state = 1
				} else if b != 0 { // CR NUL -> CR
					out = append(out, b)
				}
			case 1:
				switch {
				case b == iac:
					out, state = append(out, iac), 0
				case b == sb:
					state = 3
				case b >= will: // WILL WONT DO DONT take an option byte
					state = 2
				default:
					state = 0
				}
			case 2:
				state = 0
			case 3:
				if b == iac {
					state = 4
				}
			case 4:
				if b == se {
					state = 0
				} else {
					state = 3
				}
			}
		}
		if _, err := tty.Write(out); err != nil {
			return
		}
	}
}
