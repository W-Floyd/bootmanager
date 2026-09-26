package panel

import "testing"

func TestPathBounds(t *testing.T) {
	for _, tc := range []struct {
		d              string
		x0, y0, x1, y1 float64
	}{
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

// Every panel in the embedded table has a size, and its cutouts land on
// it (arcs are boxed with slack, so allow a margin).
func TestTable(t *testing.T) {
	tbl, err := Table()
	if err != nil || len(tbl) == 0 {
		t.Fatalf("table: %d panels, %v", len(tbl), err)
	}
	for c, p := range tbl {
		if p.XRes <= 0 || p.YRes <= 0 {
			t.Errorf("%s: no resolution", c)
		}
		m := p.XRes / 4
		for _, b := range p.Cutouts {
			if b.X < -m || b.Y < -m || b.X+b.W > p.XRes+m || b.Y+b.H > p.YRes+m || b.W <= 0 || b.H <= 0 {
				t.Errorf("%s %s: box %+v off a %dx%d panel", c, b.Name, b, p.XRes, p.YRes)
			}
		}
	}
}

func TestParse(t *testing.T) {
	p, err := Parse([]byte(`{"name":"x","x-res":720,"y-res":1600,"border-radius":42,"width":68,"height":151,
		"cutouts":[{"name":"camera","path":"M 325,0 L 395,0 L 395,80 L 325,80 Z"}]}`))
	want := Box{"camera", 325, 0, 70, 80}
	if err != nil || p.CornerRadii != [4]int{42, 42, 42, 42} || p.WidthMM != 68 || len(p.Cutouts) != 1 || p.Cutouts[0] != want {
		t.Fatalf("%+v %v", p, err)
	}
	p, err = Parse([]byte(`{"x-res":1080,"y-res":2280,"corner-radii":[80,80,66,66]}`))
	if err != nil || p.CornerRadii != [4]int{80, 80, 66, 66} {
		t.Fatalf("%+v %v", p, err)
	}
}
