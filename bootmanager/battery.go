package main

import (
	"flag"
	"sync"
	"time"

	"github.com/W-Floyd/bootmanager/bootmanager/internal/battery"
	"github.com/W-Floyd/bootmanager/bootmanager/internal/rpc"
)

var powerSupplyDir = flag.String("power-supply", battery.Root, "where the kernel lists power supplies, for the battery indicator and Diagnostics")

// batteryPollEvery: the level moves slowly, but a plug or unplug should show
// within a moment; a few small sysfs reads this often cost nothing.
const batteryPollEvery = 2 * time.Second

// batteryLevel is the last level pushed, for a client that connects later.
type batteryLevel struct {
	mu   sync.Mutex
	last battery.Level
}

func (b *batteryLevel) get() battery.Level {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.last
}

// watchBattery reads the battery now and then and pushes a "battery" event
// whenever it changed. A device with no battery, or a stage whose kernel
// lists no supplies, pushes one event saying none is present.
func (a *app) watchBattery() {
	a.battery.last = battery.Summary(battery.Supplies(*powerSupplyDir))
	go func() {
		for range time.Tick(batteryPollEvery) {
			l := battery.Summary(battery.Supplies(*powerSupplyDir))
			a.battery.mu.Lock()
			changed := l != a.battery.last
			a.battery.last = l
			a.battery.mu.Unlock()
			if changed {
				a.rpc.Broadcast("battery", l)
			}
		}
	}()
	rpc.Handle(a.rpc, "power.supplies", func(none) (any, error) {
		return battery.Supplies(*powerSupplyDir), nil
	})
}
