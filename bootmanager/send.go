package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"
)

// send is -send: one command to a running backend, for scripts (the
// stage's uinit: stage.ready, or -toast). The socket may not be up yet
// when the script gets here, so it retries for a while. The exit status is
// 0 only for a reply without an error. args, when non-empty, is raw JSON
// sent as the request's "args" (-send-args).
func send(socket, cmd, args string) int {
	var conn net.Conn
	var err error
	for deadline := time.Now().Add(5 * time.Second); ; {
		if conn, err = net.Dial("unix", socket); err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "bootmanager -send: %v\n", err)
		return 1
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	req := map[string]any{"id": 1, "cmd": cmd}
	if args != "" {
		req["args"] = json.RawMessage(args)
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		fmt.Fprintf(os.Stderr, "bootmanager -send: %v\n", err)
		return 1
	}
	// Events come first (the greeting), and may come between.
	dec := json.NewDecoder(conn)
	for {
		var m struct {
			ID    int             `json:"id"`
			Data  json.RawMessage `json:"data"`
			Error string          `json:"error"`
		}
		if err := dec.Decode(&m); err != nil {
			fmt.Fprintf(os.Stderr, "bootmanager -send %s: no reply: %v\n", cmd, err)
			return 1
		}
		if m.ID != 1 {
			continue
		}
		if m.Error != "" {
			fmt.Fprintf(os.Stderr, "bootmanager -send %s: %s\n", cmd, m.Error)
			return 1
		}
		if len(m.Data) > 0 && string(m.Data) != "null" {
			fmt.Println(string(m.Data))
		}
		return 0
	}
}
