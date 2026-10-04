// Package battery reads the kernel's power supplies (/sys/class/power_supply):
// the battery and what charges it, for the battery indicator and the
// Diagnostics screen.
package battery

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Root is where the kernel lists power supplies.
const Root = "/sys/class/power_supply"

// Supply is one power supply: a battery, a charger (type "Mains" or "USB")
// or anything else the kernel lists, with every property it reports, as
// the kernel spells them in the supply's uevent, lowercased and without the
// POWER_SUPPLY_ prefix ("capacity", "voltage_now", "status", ...). Values
// are the kernel's units: µV, µA, µAh, tenths of °C.
type Supply struct {
	Name  string            `json:"name"`
	Type  string            `json:"type"`
	Props map[string]string `json:"props"`
}

// Supplies lists the supplies under root, batteries first, then by name.
func Supplies(root string) []Supply {
	ents, _ := os.ReadDir(root)
	var out []Supply
	for _, e := range ents {
		s := Supply{Name: e.Name(), Props: map[string]string{}}
		f, err := os.Open(filepath.Join(root, e.Name(), "uevent"))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if k, v, ok := strings.Cut(sc.Text(), "="); ok {
				if k, ok = strings.CutPrefix(k, "POWER_SUPPLY_"); ok {
					s.Props[strings.ToLower(k)] = v
				}
			}
		}
		f.Close()
		s.Type = s.Props["type"]
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		bi, bj := out[i].Type == "Battery", out[j].Type == "Battery"
		if bi != bj {
			return bi
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Level is what the indicator shows.
type Level struct {
	// Present: a battery was found and is in.
	Present bool `json:"present"`
	// Percent is the charge, 0 to 100; -1 when unknown.
	Percent int `json:"percent"`
	// Status is the kernel's: Charging, Discharging, Full, Not charging,
	// Unknown.
	Status string `json:"status"`
	// Plugged: a charger or USB supply is online.
	Plugged bool `json:"plugged"`
	// Charging is what the icon's bolt shows: the battery says so, or a
	// charger is online and the battery is not full. A fuel gauge's own
	// status can lag a plug or unplug, or not follow it at all (the
	// CW2217 knows the charge, not the charger), where the charger's
	// "online" is the plain fact.
	Charging bool `json:"charging"`
}

// Summary is the first battery's level; Present false with none.
func Summary(ss []Supply) Level {
	plugged := false
	for _, s := range ss {
		if s.Type != "Battery" && s.Props["online"] == "1" {
			plugged = true
		}
	}
	for _, s := range ss {
		if s.Type != "Battery" {
			continue
		}
		l := Level{Present: s.Props["present"] != "0", Percent: -1, Status: s.Props["status"], Plugged: plugged}
		if p, err := strconv.Atoi(s.Props["capacity"]); err == nil && p >= 0 && p <= 100 {
			l.Percent = p
		}
		l.Charging = l.Status == "Charging" || (plugged && l.Status != "Full")
		return l
	}
	return Level{Percent: -1}
}
