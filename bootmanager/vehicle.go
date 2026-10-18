package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/W-Floyd/bootmanager/bootmanager/internal/rpc"
	micom "github.com/W-Floyd/go-micomd"
)

var (
	micomDev = flag.String("micom", "", "the micom's serial device (/dev/ttyAMA2 on the Kia EV6 head unit): answer its CPU-alive requests, which it takes the SoC for hung and resets without, and read the vehicle's state; empty for none")
	micomLog = flag.String("micom-log", "", "append every frame the micom sends, decoded where known, and the link's counters to this file")
	micomTap = flag.String("micom-tap", "", "serve every frame the micom sends, with the time, on this unix socket, so `micomd -attach SOCKET` can watch the live link without taking the port")

	micomKeyFunc = flag.Uint("micom-keyfunc", 0, "Func (header byte 3 << 8 | command, as the -micom-log capture shows it) of the micom's hard-key frames; with -micom-keys, the keys then move around the UI. 0 for none")
	micomKeys    = flag.String("micom-keys", "", "which hard keys make which move, type.code=up|down|enter|back,... (type 0-7, code 0-15; a key the log reports as not listed is found by pressing it), for -micom-keyfunc")
)

// micomRetry: the device may not exist yet at start, and a failed link is
// reopened; the micom resets the SoC after about 3.5 minutes without answers.
const micomRetry = 2 * time.Second

// startMicom runs the micom link in the background, from the first moment
// possible, and returns the daemon for the Diagnostics screen; nil without
// -micom. The daemon of a failed link is replaced, so a.vehicle is a holder.
func startMicom() *vehicleLink {
	if *micomDev == "" {
		return nil
	}
	v := &vehicleLink{}
	var tap *micom.Tap
	if *micomTap != "" {
		var err error
		if tap, err = micom.ListenTap(*micomTap); err != nil {
			log.Printf("micom: no tap: %v", err)
		}
	}
	go v.run(*micomDev, *micomLog, startNavKeys(), tap)
	return v
}

// startNavKeys opens the virtual keyboard for the hard keys, nil when they are
// not asked for, or when there is no uinput: the link goes on without them, as
// answering the micom is what keeps the SoC alive. Flags that cannot be read
// stop the start, as other flags do.
func startNavKeys() *navKeys {
	if *micomKeyFunc == 0 && *micomKeys == "" {
		return nil
	}
	km, err := parseNavKeys(*micomKeys)
	if err != nil {
		log.Fatalf("-micom-keys: %v", err)
	}
	if *micomKeyFunc == 0 || *micomKeyFunc > 0xffff || len(km) == 0 {
		log.Fatal("-micom-keyfunc (0x1 to 0xffff) and -micom-keys go together")
	}
	ui, err := micom.OpenUinput("bootmanager micom keys", km.codes())
	if err != nil {
		log.Printf("micom: hard keys are off: %v", err)
		return nil
	}
	log.Printf("micom: hard keys of Func %#04x drive the UI: %v", *micomKeyFunc, km)
	return &navKeys{fn: uint16(*micomKeyFunc), keys: km, out: ui, logf: log.Printf}
}

// vehicleLink is the current daemon, swapped when the link is reopened.
type vehicleLink struct {
	mu sync.Mutex
	d  *micom.Daemon
}

func (v *vehicleLink) daemon() *micom.Daemon {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.d
}

func (v *vehicleLink) run(dev, logPath string, keys *navKeys, tap *micom.Tap) {
	var lf *os.File
	var lmu sync.Mutex
	if logPath != "" {
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			lf = f
		}
	}
	line := func(format string, args ...any) {
		if lf == nil {
			return
		}
		lmu.Lock()
		defer lmu.Unlock()
		fmt.Fprintf(lf, format+"\n", args...)
	}
	for {
		port, err := micom.OpenTTY(dev, 115200)
		if err != nil {
			time.Sleep(micomRetry)
			continue
		}
		d := micom.New(micom.Config{
			Port: port,
			Logf: log.Printf,
			OnFrame: func(f micom.Frame) {
				if keys != nil {
					keys.frame(f)
				}
				text := fmt.Sprintf("rx %v", f)
				if s := micom.Describe(f); s != "" {
					text += "  # " + s
				}
				line("%s", text)
				if tap != nil {
					tap.Send(time.Now().Format("15:04:05.000 ") + text + "\n")
				}
			},
		})
		v.mu.Lock()
		v.d = d
		v.mu.Unlock()
		log.Printf("micom: link on %s", dev)
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(30 * time.Second):
					line("%s %s", time.Now().Format("15:04:05"), d.Stats())
				}
			}
		}()
		err = d.Run(context.Background())
		cancel()
		log.Printf("micom: link ended: %v", err)
		time.Sleep(micomRetry)
	}
}

// vehicleState is the vehicle.state reply.
type vehicleState struct {
	// Present is false without -micom, or before the link is open.
	Present bool `json:"present"`
	micom.Snapshot
}

func (a *app) vehicleNow() vehicleState {
	if a.vehicle == nil || a.vehicle.daemon() == nil {
		return vehicleState{Snapshot: micom.Snapshot{LastAliveAgeMs: -1, MarkedAgeMs: -1}}
	}
	return vehicleState{Present: true, Snapshot: a.vehicle.daemon().Snapshot()}
}

func (a *app) registerVehicle() {
	rpc.Handle(a.rpc, "vehicle.state", func(none) (any, error) { return a.vehicleNow(), nil })
	// vehicle.mark sets the baseline the byte diffs are counted from: the
	// Learn screen's Mark button.
	rpc.Handle(a.rpc, "vehicle.mark", func(none) (any, error) {
		if a.vehicle != nil && a.vehicle.daemon() != nil {
			a.vehicle.daemon().Mark()
		}
		return a.vehicleNow(), nil
	})
}
