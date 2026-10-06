package boot

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
	"time"
)

func captureLog(t *testing.T) *bytes.Buffer {
	var b bytes.Buffer
	log.SetOutput(&b)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &b
}

func newTestManager() *Manager {
	return New(Config{Emit: func(string, any) {}})
}

func TestCancelByLogsOnlyTheFirst(t *testing.T) {
	b := captureLog(t)
	m := newTestManager()
	m.CancelBy("ui: event2 EV_KEY BTN_TOUCH 1")
	m.CancelBy("second")
	m.Cancel()
	got := b.String()
	if !strings.Contains(got, "autoboot: cancelled by ui: event2 EV_KEY BTN_TOUCH 1 (countdown not started)") {
		t.Errorf("log: %q", got)
	}
	if strings.Count(got, "cancelled by") != 1 {
		t.Errorf("more than the first cancel logged: %q", got)
	}
	if !m.Cancelled() {
		t.Error("not cancelled")
	}
}

func TestCancelByDuringTheCountdown(t *testing.T) {
	b := captureLog(t)
	m := newTestManager()
	m.mu.Lock()
	m.countStart = time.Now().Add(-1500 * time.Millisecond)
	m.left = 2
	m.mu.Unlock()
	m.CancelBy("x")
	if got := b.String(); !strings.Contains(got, "autoboot: cancelled by x (1.5 s into the countdown)") {
		t.Errorf("log: %q", got)
	}
}

func TestSilentCancelAtTheEnd(t *testing.T) {
	b := captureLog(t)
	m := newTestManager()
	m.cancel("countdown over", false)
	m.CancelBy("boot start") // after it: not the first, not logged
	if strings.Contains(b.String(), "cancelled by") {
		t.Errorf("log: %q", b.String())
	}
}
