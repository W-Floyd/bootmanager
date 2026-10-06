package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"sync"
	"time"

	"bootmanager.local/bootmanager/internal/fsck"
	"bootmanager.local/bootmanager/internal/rpc"
	"bootmanager.local/bootmanager/internal/storage"
)

var fsckDir = flag.String("fsck-dir", "/usr/libexec/fsck", "where the file system checkers are (e2fsck, fsck.f2fs, fsck.fat, fsck.exfat, xfs_repair); Check and Repair are left out of the UI when there are none (or this is empty)")

// fsckBusy: one run at a time, and no boot meanwhile.
var fsckBusy sync.Mutex

type fsckArgs struct {
	Dev    string
	Repair bool
}

// registerFsck adds disk.fsck. A check (the default) changes nothing on the
// partition; a repair is for removable media only, an SD card being what
// this is for: internal storage holds the phone's own Android.
//
// The partition is let go first, since a checker wants it unmounted: the
// browsing mount and the boot scan's. The scan finds its entries again
// afterwards, whatever the outcome. Progress goes out as "fsck" events (the
// latest line, at most a few a second), the whole result as the reply.
func (a *app) registerFsck() {
	rpc.Handle(a.rpc, "disk.fsck", afterReady(a, func(x fsckArgs) (any, error) {
		if !fsck.Available(*fsckDir) {
			return nil, errors.New("this stage has no file system checkers")
		}
		fstype := storage.FSType(x.Dev)
		if !fsck.Supported(fstype) {
			return nil, fmt.Errorf("%s: no checker for %q", x.Dev, fstype)
		}
		if x.Repair && !storage.Removable(x.Dev) {
			return nil, errors.New("repair is only offered on removable media")
		}
		if !fsckBusy.TryLock() {
			return nil, errors.New("a check is already running")
		}
		defer fsckBusy.Unlock()

		a.boot.CancelBy("a file system check")
		a.disks.Unmount(x.Dev)
		if err := a.boot.Release(); err != nil {
			return nil, err
		}
		defer a.boot.Rescan()

		var last time.Time
		emit := func(line string) {
			if time.Since(last) > 200*time.Millisecond {
				last = time.Now()
				a.rpc.Broadcast("fsck", map[string]string{"dev": x.Dev, "line": line})
			}
		}
		log.Printf("fsck %s (%s, repair=%v)", x.Dev, fstype, x.Repair)
		res, err := fsck.Runner{Dir: *fsckDir}.Run(x.Dev, fstype, x.Repair, emit)
		log.Printf("fsck %s: %s (exit %d)", x.Dev, res.Status, res.Code)
		return res, err
	}))
}
