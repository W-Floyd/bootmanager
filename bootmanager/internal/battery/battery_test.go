package battery

import (
	"os"
	"path/filepath"
	"testing"
)

func supply(t *testing.T, root, name, uevent string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name, "uevent"), []byte(uevent), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSuppliesAndSummary(t *testing.T) {
	root := t.TempDir()
	supply(t, root, "bq25890-charger", "POWER_SUPPLY_NAME=bq25890-charger\nPOWER_SUPPLY_TYPE=USB\nPOWER_SUPPLY_ONLINE=1\nPOWER_SUPPLY_CURRENT_MAX=2000000\n")
	supply(t, root, "cw221x", "POWER_SUPPLY_NAME=cw221x\nPOWER_SUPPLY_TYPE=Battery\nPOWER_SUPPLY_STATUS=Charging\nPOWER_SUPPLY_CAPACITY=78\nPOWER_SUPPLY_VOLTAGE_NOW=3987000\nPOWER_SUPPLY_PRESENT=1\n")
	ss := Supplies(root)
	if len(ss) != 2 || ss[0].Name != "cw221x" || ss[0].Props["voltage_now"] != "3987000" || ss[1].Type != "USB" {
		t.Fatalf("supplies: %+v", ss)
	}
	if l := Summary(ss); !l.Present || l.Percent != 78 || !l.Charging || l.Status != "Charging" {
		t.Errorf("summary: %+v", l)
	}
	// A gauge that does not follow the charger: its status lags "Discharging"
	// while the charger is online, and the bolt follows the charger, until full.
	supply(t, root, "cw221x", "POWER_SUPPLY_TYPE=Battery\nPOWER_SUPPLY_STATUS=Discharging\nPOWER_SUPPLY_CAPACITY=60\n")
	if l := Summary(Supplies(root)); !l.Plugged || !l.Charging {
		t.Errorf("plugged, gauge says discharging: %+v", l)
	}
	supply(t, root, "bq25890-charger", "POWER_SUPPLY_TYPE=USB\nPOWER_SUPPLY_ONLINE=0\n")
	if l := Summary(Supplies(root)); l.Plugged || l.Charging {
		t.Errorf("unplugged: %+v", l)
	}
	supply(t, root, "bq25890-charger", "POWER_SUPPLY_TYPE=USB\nPOWER_SUPPLY_ONLINE=1\n")
	supply(t, root, "cw221x", "POWER_SUPPLY_TYPE=Battery\nPOWER_SUPPLY_STATUS=Full\nPOWER_SUPPLY_CAPACITY=100\n")
	if l := Summary(Supplies(root)); !l.Plugged || l.Charging {
		t.Errorf("full on the charger: %+v", l)
	}
	// Restored for the rest.
	supply(t, root, "cw221x", "POWER_SUPPLY_NAME=cw221x\nPOWER_SUPPLY_TYPE=Battery\nPOWER_SUPPLY_STATUS=Charging\nPOWER_SUPPLY_CAPACITY=78\nPOWER_SUPPLY_VOLTAGE_NOW=3987000\nPOWER_SUPPLY_PRESENT=1\n")
	// No battery, or one with no reading.
	if l := Summary(ss[1:]); l.Present || l.Percent != -1 {
		t.Errorf("no battery: %+v", l)
	}
	supply(t, root, "cw221x", "POWER_SUPPLY_TYPE=Battery\nPOWER_SUPPLY_CAPACITY=abc\nPOWER_SUPPLY_PRESENT=0\n")
	if l := Summary(Supplies(root)); l.Present || l.Percent != -1 {
		t.Errorf("unreadable: %+v", l)
	}
	if len(Supplies(filepath.Join(root, "none"))) != 0 {
		t.Error("a missing root has supplies")
	}
}
