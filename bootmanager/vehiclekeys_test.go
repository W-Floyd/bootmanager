package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	micom "github.com/W-Floyd/go-micomd"
)

func TestParseNavKeys(t *testing.T) {
	m, err := parseNavKeys("5.1=up, 5.2=down,2.0=enter,2.1=back,")
	if err != nil {
		t.Fatal(err)
	}
	want := navKeyMap{{5, 1}: 103, {5, 2}: 108, {2, 0}: 28, {2, 1}: 1}
	if fmt.Sprint(m) != fmt.Sprint(want) {
		t.Errorf("map %v, want %v", m, want)
	}
	if m, err := parseNavKeys(""); err != nil || len(m) != 0 {
		t.Errorf("empty: %v, %v", m, err)
	}
	for _, bad := range []string{"5.1", "5.1=left", "5=up", "9.1=up", "5.16=up", "a.b=up", "5.1=", "-1.0=up", "5.1.2=up"} {
		if m, err := parseNavKeys(bad); err == nil {
			t.Errorf("%q accepted as %v", bad, m)
		}
	}
}

// Two keys making the same move are one key on the virtual keyboard.
func TestNavKeyMapCodes(t *testing.T) {
	m, _ := parseNavKeys("5.1=up,2.3=up,5.2=down")
	got := m.codes()
	slices.Sort(got)
	if !slices.Equal(got, []uint16{103, 108}) {
		t.Errorf("codes %v", got)
	}
}

type fakeKeys struct {
	got []string
	err error
}

func (f *fakeKeys) Key(code uint16, value int32) error {
	if f.err != nil {
		return f.err
	}
	f.got = append(f.got, fmt.Sprintf("%d=%d", code, value))
	return nil
}

func TestNavKeysFrame(t *testing.T) {
	// The unit's key frames, as captured: hdr 07 84 01 87, cmd 01.
	const fn = 0x8701
	hdr := [4]byte{0x07, 0x84, 0x01, 0x87}
	keyFrame := func(typ, code, press byte) micom.Frame {
		return micom.Frame{Hdr: hdr, Cmd: 0x01, Payload: []byte{typ, press<<4 | code, 0}}
	}
	for _, tc := range []struct {
		name string
		f    micom.Frame
		keys []string
		log  string
	}{
		{"short press moves, as a press and a release", keyFrame(5, 1, micom.PressShort), []string{"103=1", "103=0"}, ""},
		{"another key", keyFrame(2, 0, micom.PressShort), []string{"28=1", "28=0"}, ""},
		{"a long press would move it twice", keyFrame(5, 1, micom.PressLong), nil, ""},
		{"a release is not a move", keyFrame(5, 1, micom.PressRelease), nil, ""},
		{"a key that is not listed is logged", keyFrame(5, 9, micom.PressShort), nil, "key type 5 code 9 (press 1)"},
		{"a knob is not a push key", micom.Frame{Hdr: hdr, Cmd: 0x01, Payload: []byte{9, 0x11, 0}}, nil, ""},
		{"another Func", micom.Frame{Hdr: hdr, Cmd: 0x02, Payload: []byte{5, 0x11}}, nil, ""},
		{"no payload", micom.Frame{Hdr: hdr, Cmd: 0x01}, nil, ""},
	} {
		sink := &fakeKeys{}
		var logged []string
		n := &navKeys{
			fn:   fn,
			keys: navKeyMap{{5, 1}: 103, {2, 0}: 28},
			out:  sink,
			logf: func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
		}
		n.frame(tc.f)
		if !slices.Equal(sink.got, tc.keys) {
			t.Errorf("%s: sent %v, want %v", tc.name, sink.got, tc.keys)
		}
		if tc.log == "" && len(logged) > 0 || tc.log != "" && !strings.Contains(strings.Join(logged, "\n"), tc.log) {
			t.Errorf("%s: logged %q, want %q", tc.name, logged, tc.log)
		}
	}
}

// A device that stops taking events is reported once, not for the release too:
// the link, which keeps the SoC alive, never depends on it.
func TestNavKeysSinkError(t *testing.T) {
	var logged []string
	n := &navKeys{
		fn:   0x8701,
		keys: navKeyMap{{5, 1}: 103},
		out:  &fakeKeys{err: errors.New("no device")},
		logf: func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}
	n.frame(micom.Frame{Hdr: [4]byte{0x07, 0x84, 0x01, 0x87}, Cmd: 0x01, Payload: []byte{5, 0x11, 0}})
	if len(logged) != 1 || !strings.Contains(logged[0], "no device") {
		t.Errorf("logged %q", logged)
	}
}
