package main

import (
	"fmt"
	"strconv"
	"strings"

	micom "github.com/W-Floyd/go-micomd"
)

// The micom's hard keys move around the UI. Their frames become key events of
// a virtual keyboard (uinput), which bootmanager-ui reads from /dev/input like
// any other keyboard: the keys need no path of their own into it, and its
// rescan finds the device. Which Func carries the key frames is not known in
// advance; the -micom-log capture shows it.

// navActions are the UI's four moves, each with the Linux key that
// bootmanager-ui's map_key turns into its Up, Down, Return or Escape.
var navActions = map[string]uint16{
	"up":    103, // KEY_UP
	"down":  108, // KEY_DOWN
	"enter": 28,  // KEY_ENTER
	"back":  1,   // KEY_ESC
}

// navKeyMap is -micom-keys: a hard key, by [type, code], to the Linux key of
// the move it makes.
type navKeyMap map[[2]int]uint16

// parseNavKeys reads "type.code=move,...": type 0 to 7 (the push keys), code 0
// to 15, move one of navActions.
func parseNavKeys(s string) (navKeyMap, error) {
	m := navKeyMap{}
	for _, kv := range strings.Split(s, ",") {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		key, action, ok := strings.Cut(kv, "=")
		ts, cs, dot := strings.Cut(key, ".")
		t, terr := strconv.Atoi(ts)
		c, cerr := strconv.Atoi(cs)
		if !ok || !dot || terr != nil || cerr != nil {
			return nil, fmt.Errorf("%q: want type.code=move", kv)
		}
		if t < micom.TypeFrontCol1 || t > micom.TypeEject || c < 0 || c > 15 {
			return nil, fmt.Errorf("%q: type is 0 to 7 and code 0 to 15", kv)
		}
		linux, found := navActions[action]
		if !found {
			return nil, fmt.Errorf("%q: %q is not one of up, down, enter, back", kv, action)
		}
		m[[2]int{t, c}] = linux
	}
	return m, nil
}

// codes are the Linux keys the virtual keyboard has to offer, once each.
func (m navKeyMap) codes() []uint16 {
	var out []uint16
	seen := map[uint16]bool{}
	for _, c := range m {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// keySink is a virtual keyboard: micom.Uinput.
type keySink interface {
	Key(code uint16, value int32) error
}

// navKeys turns the micom's key frames into moves.
type navKeys struct {
	fn   uint16 // the Func of the key frames
	keys navKeyMap
	out  keySink
	logf func(format string, args ...any)
}

// frame is a micom frame's hook. A key press is one frame (short), a long
// press another and the release a third, and the UI moves on every press or
// repeat it sees, so only the short press is sent, as a press and a release:
// a long press or a release would otherwise move it again.
func (n *navKeys) frame(f micom.Frame) {
	if micom.Func(f) != n.fn || f.Hdr[0] != 0x0c {
		return
	}
	e, ok := micom.ParseKey(f.Payload)
	if !ok {
		return
	}
	code, ok := n.keys[[2]int{e.Type, e.Code}]
	if !ok {
		// How a key's type and code are found: press it, and read this.
		n.logf("micom: key type %d code %d (press %d) is not in -micom-keys", e.Type, e.Code, e.Press)
		return
	}
	if e.Press != micom.PressShort {
		return
	}
	for _, v := range []int32{1, 0} {
		if err := n.out.Key(code, v); err != nil {
			n.logf("micom: uinput: %v", err)
			return
		}
	}
}
