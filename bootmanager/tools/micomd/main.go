// micomd takes over the SoC side of the head unit's micom serial link from
// Android's daemon of the same name: it decodes what the micom sends and
// sends the routine frames Android did, so the micom does not take the SoC
// for hung. It is a thin wrapper around internal/micom, which the Boot
// Manager can embed instead.
//
//	micomd -dev /dev/ttyAMA1 [-baud 115200] [-dump] [-clock]
//
// What it sends is the micom package's DefaultProfile and nothing else.
// -clock adds the two frames that carry the time, from this machine's
// clock: only use it when that clock is right, as the car may store it.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/W-Floyd/bootmanager/bootmanager/internal/micom"
)

func main() {
	dev := flag.String("dev", "", "the micom's serial device (the vendor's /dev/ttyTCC2, a PL011 at 0x16620000)")
	baud := flag.Int("baud", 115200, "baud rate of the link; 115200 8N1 raw is what Android's micomd sets (its tcsetattr: c_cflag 0x1002 = B115200 | CS8)")
	dump := flag.Bool("dump", false, "print every frame received")
	clock := flag.Bool("clock", false, "also send the two clock frames, from this machine's clock")
	noAlive := flag.Bool("noalive", false, "do not answer the micom's CPU-alive requests (it resets the SoC if they go unanswered)")
	every := flag.Duration("stats", 10*time.Second, "how often to print the link's counters; 0 for never")
	flag.Parse()
	if *dev == "" {
		flag.Usage()
		os.Exit(2)
	}

	port, err := micom.OpenTTY(*dev, *baud)
	if err != nil {
		log.Fatal(err)
	}
	profile := micom.DefaultProfile()
	if *clock {
		profile = append(profile, micom.ClockProfile()...)
		log.Printf("sending the clock frames from this machine's clock: %s", time.Now().Format(time.RFC3339))
	}
	cfg := micom.Config{Port: port, Profile: profile, Logf: log.Printf, NoAlive: *noAlive}
	if *dump {
		cfg.OnFrame = func(f micom.Frame) {
			if s := micom.Describe(f); s != "" {
				fmt.Printf("rx %v  # %s\n", f, s)
				return
			}
			fmt.Printf("rx %v\n", f)
		}
	}
	d := micom.New(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *every > 0 {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(*every):
					printStats(d.Stats())
				}
			}
		}()
	}
	if err := d.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}

func printStats(s micom.Stats) { log.Print(s) }
