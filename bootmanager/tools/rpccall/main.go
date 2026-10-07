// rpccall makes one call to a running Boot Manager's Remote UI, for a stage
// that cannot be reached by SSH:
//
//	go run ./tools/rpccall http://172.16.42.1:8080 log.tail '{"lines":80}'
//	go run ./tools/rpccall http://172.16.42.1:8080 service.list
//
// It prints the reply, then exits; events that arrive first are skipped.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: rpccall URL CMD [JSON-ARGS]")
		os.Exit(2)
	}
	args := json.RawMessage("{}")
	if len(os.Args) > 3 {
		args = json.RawMessage(os.Args[3])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(strings.TrimSuffix(os.Args[1], "/"), "http") + "/ws"
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer c.CloseNow()
	c.SetReadLimit(8 << 20)
	req, _ := json.Marshal(map[string]any{"id": 1, "cmd": os.Args[2], "args": args})
	if err := c.Write(ctx, websocket.MessageText, req); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		var r struct {
			ID    int64           `json:"id"`
			Data  json.RawMessage `json:"data"`
			Error string          `json:"error"`
		}
		if json.Unmarshal(b, &r) != nil || r.ID != 1 {
			continue
		}
		if r.Error != "" {
			fmt.Fprintln(os.Stderr, "error:", r.Error)
			os.Exit(1)
		}
		var s string
		if json.Unmarshal(r.Data, &s) == nil {
			fmt.Println(s)
		} else {
			fmt.Println(string(r.Data))
		}
		return
	}
}
