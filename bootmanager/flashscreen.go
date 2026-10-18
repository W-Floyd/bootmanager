package main

import (
	"sync"
	"time"

	"github.com/W-Floyd/bootmanager/bootmanager/internal/fastboot"
)

// flashEvent is what the UI's flash screen shows: a fastboot.Progress, an
// empty one to clear the screen.
type flashEvent struct {
	fastboot.Progress
}

// flashScreen turns the fastboot server's progress into "flash" events: about
// eight a second while data moves, every change of phase at once.
//
// An image comes as pieces with gaps between them, and the host does not say
// when the last one is out. After a piece, with nothing more for quiet, the
// image counts as flashed: the screen says so ("finished") and stays up until
// the UI dismisses it. While a flash is under way the UI holds this screen,
// and the only way out is to switch fastboot off.
type flashScreen struct {
	emit func(name string, data any)

	mu    sync.Mutex
	last  fastboot.Progress
	sent  time.Time
	timer *time.Timer
}

const (
	flashEvery = 120 * time.Millisecond
	flashQuiet = 6 * time.Second
)

func newFlashScreen(emit func(name string, data any)) *flashScreen {
	return &flashScreen{emit: emit}
}

// Progress is fastboot.Server.Progress.
func (f *flashScreen) Progress(p fastboot.Progress) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.timer != nil {
		f.timer.Stop()
	}
	changed := p.Phase != f.last.Phase || p.Piece != f.last.Piece
	f.last = p
	if changed || time.Since(f.sent) >= flashEvery {
		f.sent = time.Now()
		f.emit("flash", flashEvent{p})
	}
	if p.Phase == "waiting" || p.Phase == "done" {
		f.timer = time.AfterFunc(flashQuiet, f.finish)
	}
}

func (f *flashScreen) finish() {
	f.mu.Lock()
	defer f.mu.Unlock()
	fin := f.last
	fin.Phase, fin.End = "finished", fin.Total
	f.last = fin
	f.emit("flash", flashEvent{fin})
}

// Active is whether a flash is under way: some data has come in, and the
// image is not yet counted as flashed.
func (f *flashScreen) Active() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last.Phase != "" && f.last.Phase != "finished"
}

// Reset clears the screen and forgets the flash: fastboot is off.
func (f *flashScreen) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.timer != nil {
		f.timer.Stop()
	}
	was := f.last.Phase
	f.last = fastboot.Progress{}
	if was != "" {
		f.emit("flash", flashEvent{})
	}
}
