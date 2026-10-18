// Package fsck checks and repairs the file system on a partition by running
// the file system's own checker, which a stage packs as a separate binary.
package fsck

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Status is how a run ended.
const (
	Clean    = "clean"     // nothing wrong
	Fixed    = "fixed"     // errors found, repaired, and a second check finds none
	Errors   = "errors"    // errors found, not repaired (a check, or too damaged)
	DirtyLog = "dirty-log" // XFS: the log needs replaying, which only a mount does
	Failed   = "failed"    // the checker could not run to the end
)

type Result struct {
	Dev    string   `json:"dev"`
	FSType string   `json:"fstype"`
	Repair bool     `json:"repair"`
	Status string   `json:"status"`
	Code   int      `json:"code"`
	Lines  []string `json:"lines"`
}

// maxLines is how much output a Result keeps (the tail); all of it goes to
// the emit callback.
const maxLines = 400

type checker struct {
	bin    string
	check  []string
	repair []string // nil: no repair
	// status classifies one run by its exit code: Clean, Errors (anything
	// found, fixed or not: a repair is judged by checking again), DirtyLog
	// or Failed.
	status func(code int) string
}

// bitmask: the exit codes of fsck(8), which e2fsck and exfatprogs follow:
// 1 corrected, 2 corrected and a reboot wanted, 4 left uncorrected, 8
// operational error, 16 usage, 32 cancelled, 128 library error. A check of
// a damaged ext4 exits 12, 4 with the 8 its abort adds, so 8 counts as a
// failure only without the 4.
func bitmask(code int) string {
	switch {
	case code == 0:
		return Clean
	case code&(16|32|128) != 0:
		return Failed
	case code&(1|2|4) != 0:
		return Errors
	}
	return Failed
}

// fsck.fat: 0 clean, 1 errors found (or fixed with -a), others failure.
func fat(code int) string {
	switch code {
	case 0:
		return Clean
	case 1:
		return Errors
	}
	return Failed
}

// fsck.f2fs: 0 clean, anything else is damage, including 255 for what it
// cannot find a checkpoint in. It exits 1 after a repair that worked, so
// only a second check says.
func f2fs(code int) string {
	if code == 0 {
		return Clean
	}
	return Errors
}

// xfs_repair: 0 clean, 1 corruption found (-n), 2 a dirty log it will not
// zero without -L, which would discard it.
func xfs(code int) string {
	switch code {
	case 0:
		return Clean
	case 1:
		return Errors
	case 2:
		return DirtyLog
	}
	return Failed
}

// btrfs check: 0 clean, 1 errors.
func btrfs(code int) string {
	switch code {
	case 0:
		return Clean
	case 1:
		return Errors
	}
	return Failed
}

var e2fsck = checker{"e2fsck", []string{"-f", "-n"}, []string{"-f", "-y"}, bitmask}

var checkers = map[string]checker{
	"ext2": e2fsck,
	"ext3": e2fsck,
	"ext4": e2fsck,
	"vfat": {"fsck.fat", []string{"-n", "-v"}, []string{"-a", "-v"}, fat},
	// -y, not -p: -p leaves a corrupt boot region alone ("Fix (y/N)? n").
	"exfat": {"fsck.exfat", []string{"-n", "-v"}, []string{"-y", "-v"}, bitmask},
	"f2fs":  {"fsck.f2fs", []string{"-f", "--dry-run"}, []string{"-f", "-a"}, f2fs},
	// No repair: it would zero a dirty log, discarding what a mount would
	// replay.
	"xfs": {"xfs_repair", []string{"-n"}, nil, xfs},
	// No repair: btrfs check --repair is documented as dangerous, and can
	// make a damaged file system worse.
	"btrfs": {"btrfs", []string{"check", "--readonly"}, nil, btrfs},
}

// Supported reports whether a checker is known for fstype.
func Supported(fstype string) bool { _, ok := checkers[fstype]; return ok }

// Available reports whether dir holds any checker. Check and Repair are an
// optional feature: a stage built without the tools has none, and the UI
// leaves them out.
func Available(dir string) bool {
	if dir == "" {
		return false
	}
	for _, c := range checkers {
		if _, err := os.Stat(filepath.Join(dir, c.bin)); err == nil {
			return true
		}
	}
	return false
}

// Runner runs the checkers found in Dir.
type Runner struct {
	Dir string
}

// Mounts lists where /dev/<dev> is mounted, per /proc/mounts.
func Mounts(dev string) []string {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil
	}
	defer f.Close()
	var at []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if fs := strings.Fields(sc.Text()); len(fs) > 1 && fs[0] == "/dev/"+dev {
			at = append(at, fs[1])
		}
	}
	return at
}

// Run checks /dev/<dev>, which holds a file system of type fstype, and
// repairs it when repair is set; a repair is followed by a check, whose
// verdict is the result's (Fixed or Errors). Each line the checker prints
// goes to emit. The partition must not be mounted: a checker on a mounted
// file system reads a moving target, and a repair of one corrupts it.
func (r Runner) Run(dev, fstype string, repair bool, emit func(string)) (Result, error) {
	res := Result{Dev: dev, FSType: fstype, Repair: repair, Status: Failed}
	c, ok := checkers[fstype]
	if !ok {
		return res, fmt.Errorf("no checker for %s", fstype)
	}
	if repair && c.repair == nil {
		return res, fmt.Errorf("%s: no repair; run a check, and mount it to replay the log", fstype)
	}
	if dev == "" || strings.ContainsAny(dev, "/.") {
		return res, fmt.Errorf("bad device %q", dev)
	}
	if at := Mounts(dev); len(at) > 0 {
		return res, fmt.Errorf("%s is mounted at %s", dev, strings.Join(at, ", "))
	}
	bin := filepath.Join(r.Dir, c.bin)
	if _, err := os.Stat(bin); err != nil {
		return res, fmt.Errorf("this stage has no %s", c.bin)
	}
	run := func(args []string) (int, error) {
		cmd := exec.Command(bin, append(append([]string{}, args...), "/dev/"+dev)...)
		// One pipe for both streams keeps their order.
		out, w, err := os.Pipe()
		if err != nil {
			return 0, err
		}
		cmd.Stdout, cmd.Stderr = w, w
		if err := cmd.Start(); err != nil {
			out.Close()
			w.Close()
			return 0, err
		}
		w.Close()
		sc := bufio.NewScanner(out)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			line := sc.Text()
			emit(line)
			res.Lines = append(res.Lines, line)
			if len(res.Lines) > 2*maxLines {
				res.Lines = append([]string(nil), res.Lines[len(res.Lines)-maxLines:]...)
			}
		}
		out.Close()
		var ee *exec.ExitError
		switch werr := cmd.Wait(); {
		case werr == nil:
			return 0, nil
		case errors.As(werr, &ee):
			return ee.ExitCode(), nil
		default:
			return 0, werr
		}
	}

	args := c.check
	if repair {
		args = c.repair
	}
	code, err := run(args)
	if err != nil {
		return res, err
	}
	res.Code, res.Status = code, c.status(code)
	// A repair that found damage is judged by checking again.
	if repair && res.Status == Errors {
		emit("-- checking again")
		res.Lines = append(res.Lines, "-- checking again")
		code, err = run(c.check)
		if err != nil {
			return res, err
		}
		res.Code = code
		switch c.status(code) {
		case Clean:
			res.Status = Fixed
		case Failed:
			res.Status = Failed
		}
	}
	if len(res.Lines) > maxLines {
		res.Lines = res.Lines[len(res.Lines)-maxLines:]
	}
	return res, nil
}
