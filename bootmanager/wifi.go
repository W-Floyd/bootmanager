package main

import (
	"errors"
	"flag"
	"log"
	"sync"
	"time"

	"github.com/W-Floyd/bootmanager/bootmanager/internal/rpc"

	"github.com/W-Floyd/bootmanager/bootmanager/internal/wifi"
)

var (
	wifiDir   = flag.String("wifi-dir", "/opt/ap", "directory with bin/ and lib/ of hostapd, dnsmasq, wpa_supplicant, wpa_cli and busybox; Wi-Fi is left out of the UI without it (or when this is empty)")
	wifiIface = flag.String("wifi-iface", "wlan0", "the Wi-Fi interface")
	wifiSSID  = flag.String("wifi-ssid", "kia-ev6", "the access point's network name")
	wifiPSK   = flag.String("wifi-psk", "kiaev6boot", "the access point's key")
	wifiAddr  = flag.String("wifi-addr", "172.16.42.1", "the access point's address (/24)")
	wifiAP    = flag.Bool("wifi-ap", true, "start the access point when the interface appears")
)

func (a *app) wifiRunner() wifi.Runner {
	return wifi.Runner{Dir: *wifiDir, Iface: *wifiIface, SSID: *wifiSSID, PSK: *wifiPSK, APAddr: *wifiAddr}
}

// wifiStatus is wifi.status's reply: the radio's state, and the names of the
// networks saved (never their keys).
type wifiStatus struct {
	wifi.Status
	Saved []string `json:"saved"`
}

func statusOf(r wifi.Runner) wifiStatus {
	st := wifiStatus{Status: r.Status(), Saved: []string{}}
	saved, _ := r.Load()
	for _, s := range saved {
		st.Saved = append(st.Saved, s.SSID)
	}
	return st
}

// autoJoin joins the strongest saved network in range, so a boot near a
// known network comes up on it instead of as an access point. False when
// there is none or the join failed.
func autoJoin(r wifi.Runner) bool {
	var saved []wifi.Saved
	// The stick may be mounted a moment after the radio appears.
	for i := 0; i < 10; i++ {
		var err error
		if saved, err = r.Load(); err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if len(saved) == 0 {
		return false
	}
	nets, err := r.Scan()
	if err != nil {
		log.Printf("wifi: scan for saved networks: %v", err)
		return false
	}
	for _, n := range nets {
		s, ok := wifi.Find(saved, n.SSID)
		if !ok {
			continue
		}
		if err := r.Join(s.SSID, s.PSK); err != nil {
			log.Printf("wifi: joining saved %q: %v", s.SSID, err)
			continue
		}
		log.Printf("wifi: joined saved network %q, %s", s.SSID, r.Status().IP)
		return true
	}
	return false
}

// registerWifi adds wifi.status, wifi.scan, wifi.join, wifi.forget and
// wifi.ap. The radio does one thing at a time, so they take turns.
func (a *app) registerWifi() {
	r := a.wifiRunner()
	var mu sync.Mutex
	locked := func(f func() (any, error)) (any, error) {
		if *wifiDir == "" || !r.HasTools() {
			return nil, errors.New("this stage has no Wi-Fi")
		}
		if !r.Available() {
			return nil, errors.New(*wifiIface + " not found: the driver or its firmware is not loaded (see the Log)")
		}
		mu.Lock()
		defer mu.Unlock()
		return f()
	}
	rpc.Handle(a.rpc, "wifi.status", func(none) (any, error) {
		if *wifiDir == "" || !r.HasTools() {
			return nil, errors.New("this stage has no Wi-Fi")
		}
		mu.Lock()
		defer mu.Unlock()
		return statusOf(r), nil
	})
	rpc.Handle(a.rpc, "wifi.scan", func(none) (any, error) {
		return locked(func() (any, error) { return r.Scan() })
	})
	rpc.Handle(a.rpc, "wifi.join", func(x struct{ SSID, PSK string }) (any, error) {
		return locked(func() (any, error) {
			saved, _ := r.Load()
			// No key typed: a saved network's own (or none, for an open one).
			if s, ok := wifi.Find(saved, x.SSID); ok && x.PSK == "" {
				x.PSK = s.PSK
			}
			if err := r.Join(x.SSID, x.PSK); err != nil {
				return nil, err
			}
			if err := r.Save(wifi.Upsert(saved, wifi.Saved{SSID: x.SSID, PSK: x.PSK})); err != nil {
				log.Printf("wifi: not saved: %v", err)
			}
			return statusOf(r), nil
		})
	})
	// ssid "" forgets every saved network.
	rpc.Handle(a.rpc, "wifi.forget", func(x struct{ SSID string }) (any, error) {
		return locked(func() (any, error) {
			saved, err := r.Load()
			if err != nil {
				return nil, err
			}
			if err := r.Save(wifi.Forget(saved, x.SSID)); err != nil {
				return nil, err
			}
			return statusOf(r), nil
		})
	})
	rpc.Handle(a.rpc, "wifi.ap", func(on struct{ On bool }) (any, error) {
		return locked(func() (any, error) {
			if !on.On {
				r.Off()
				return statusOf(r), nil
			}
			if err := r.AP(); err != nil {
				return nil, err
			}
			return statusOf(r), nil
		})
	})
	if *wifiAP && *wifiDir != "" {
		go func() {
			// The driver loads its firmware after the stage starts.
			for i := 0; i < 90; i++ {
				if r.Available() {
					mu.Lock()
					defer mu.Unlock()
					if r.Status().Mode != "off" {
						return
					}
					if autoJoin(r) {
						return
					}
					if err := r.AP(); err != nil {
						log.Printf("wifi: access point: %v", err)
					} else {
						log.Printf("wifi: access point %q up on %s", *wifiSSID, *wifiAddr)
					}
					return
				}
				time.Sleep(time.Second)
			}
			log.Printf("wifi: %s did not appear", *wifiIface)
		}()
	}
}
