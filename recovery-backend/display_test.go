package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPathBounds(t *testing.T) {
	for _, tc := range []struct {
		d              string
		x0, y0, x1, y1 float64
	}{
		// fogona (aports overlay): a rectangle.
		{"M 325,0 L 395,0 L 395,80 L 325,80 Z", 325, 0, 395, 80},
		// Relative, implicit lineto after m, packed numbers.
		{"m10,10 20,0 0,5-20,0z", 10, 10, 30, 15},
		{"M0,0H-5V7", -5, 0, 0, 7},
		// motorola,dubai: a circle as two arcs; boxed conservatively.
		{"M 498.5,65 a 41.5,41.5 0 1 0 83,0 a 41.5,41.5 0 1 0 -83,0 Z", 457, 23.5, 623, 106.5},
	} {
		x0, y0, x1, y1, err := pathBounds(tc.d)
		if err != nil || x0 != tc.x0 || y0 != tc.y0 || x1 != tc.x1 || y1 != tc.y1 {
			t.Errorf("%q: %v,%v %v,%v %v; want %v,%v %v,%v", tc.d, x0, y0, x1, y1, err, tc.x0, tc.y0, tc.x1, tc.y1)
		}
	}
}

// Every cutout in gmobile's own dataset parses and lands on its panel.
// GMOBILE_PANELS names data/devices/display-panels of a gmobile source tree.
func TestPathBoundsGmobileData(t *testing.T) {
	dir := os.Getenv("GMOBILE_PANELS")
	if dir == "" {
		t.Skip("GMOBILE_PANELS not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) == 0 {
		t.Fatalf("no panels in %s", dir)
	}
	n := 0
	for _, f := range files {
		b, _ := os.ReadFile(f)
		var p panelJSON
		if err := json.Unmarshal(b, &p); err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		for _, c := range p.Cutouts {
			x0, y0, x1, y1, err := pathBounds(c.Path)
			if err != nil {
				t.Errorf("%s %s: %v", filepath.Base(f), c.Name, err)
				continue
			}
			// Arcs are boxed with slack, so allow a margin off the panel.
			m := float64(p.XRes) / 4
			if x0 < -m || y0 < -m || x1 > float64(p.XRes)+m || y1 > float64(p.YRes)+m || x1 <= x0 || y1 <= y0 {
				t.Errorf("%s %s: box %v,%v %v,%v off a %dx%d panel", filepath.Base(f), c.Name, x0, y0, x1, y1, p.XRes, p.YRes)
			}
			n++
		}
	}
	t.Logf("%d cutouts in %d panels", n, len(files))
}
