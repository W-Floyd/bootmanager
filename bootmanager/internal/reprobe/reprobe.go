// Package reprobe asks the kernel to try drivers again for devices that have
// none. A driver that gave up at probe time because a firmware file was not
// there yet ("Direct firmware load ... failed", -ENOENT) is not retried by
// the kernel when the file appears; writing the device's name to its bus's
// drivers_probe makes the kernel probe it again.
//
// It only touches devices with no driver bound, and each gets one write. A
// device that is bound is never written to, so nothing running is rebound.
package reprobe

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// Summary is what Run did.
type Summary struct {
	Buses     int // buses looked at
	Unbound   int // devices with no driver
	Probed    int // names written to drivers_probe
	NoProbe   int // unbound devices on a bus with no drivers_probe file
	WriteErrs int // writes that failed
}

func (s Summary) String() string {
	return fmt.Sprintf("reprobe: %d buses, %d devices without a driver, %d probed, %d on a bus without drivers_probe, %d write errors",
		s.Buses, s.Unbound, s.Probed, s.NoProbe, s.WriteErrs)
}

// Run does it for every bus under sysRoot (/sys), logging each device to
// log. With dryRun it only lists the devices it would write.
func Run(sysRoot string, dryRun bool, log io.Writer) (Summary, error) {
	var s Summary
	buses, err := os.ReadDir(filepath.Join(sysRoot, "bus"))
	if err != nil {
		return s, err
	}
	for _, b := range buses {
		bus := filepath.Join(sysRoot, "bus", b.Name())
		devs, err := os.ReadDir(filepath.Join(bus, "devices"))
		if err != nil {
			continue // not a bus with devices
		}
		s.Buses++
		names := make([]string, 0, len(devs))
		for _, d := range devs {
			names = append(names, d.Name())
		}
		sort.Strings(names)
		probe := filepath.Join(bus, "drivers_probe")
		_, probeErr := os.Stat(probe)
		for _, n := range names {
			if _, err := os.Lstat(filepath.Join(bus, "devices", n, "driver")); err == nil {
				continue // bound
			}
			s.Unbound++
			switch {
			case probeErr != nil:
				s.NoProbe++
				fmt.Fprintf(log, "%s/%s: no driver, and the bus has no drivers_probe\n", b.Name(), n)
			case dryRun:
				fmt.Fprintf(log, "%s/%s: no driver, would probe\n", b.Name(), n)
			default:
				if err := writeProbe(probe, n); err != nil {
					s.WriteErrs++
					fmt.Fprintf(log, "%s/%s: probe: %v\n", b.Name(), n, err)
					continue
				}
				s.Probed++
				fmt.Fprintf(log, "%s/%s: probed\n", b.Name(), n)
			}
		}
	}
	return s, nil
}

// writeProbe is one write of name, as the kernel wants (no truncation).
func writeProbe(path, name string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteString(name)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
