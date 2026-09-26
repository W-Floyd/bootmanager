// Package panel describes the device's display panel: its resolution,
// physical size, rounded corners and cutouts, from gmobile's display-panel
// files (data/devices/display-panels/<dt-compatible>.json upstream),
// looked up by the device tree's compatibles as gmobile does.
//
// gmobile compiles those files into its library, so there is nothing to
// read off a rootfs. panels.json is gmobile's set, reduced by
// tools/panelgen from a pinned release (go generate). A device whose panel
// is not upstream yet passes its own gmobile-format file with -panel.
package panel

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
)

//go:generate go run ../../tools/panelgen -o panels.json

//go:embed panels.json
var table []byte

// Panel is a display panel in physical pixels, its cutouts reduced to
// bounding boxes: all the UI needs to keep text clear of them.
type Panel struct {
	Name string `json:"name"`
	XRes int    `json:"x-res"`
	YRes int    `json:"y-res"`
	// Physical size in mm; 0 when gmobile does not know it.
	WidthMM  int `json:"width-mm,omitempty"`
	HeightMM int `json:"height-mm,omitempty"`
	// Top-left, top-right, bottom-right, bottom-left.
	CornerRadii [4]int `json:"corner-radii"`
	Cutouts     []Box  `json:"cutouts"`
}

type Box struct {
	Name string `json:"name"`
	X    int    `json:"x"`
	Y    int    `json:"y"`
	W    int    `json:"w"`
	H    int    `json:"h"`
}

// gmobileFile is gmobile's format.
type gmobileFile struct {
	Name         string `json:"name"`
	XRes         int    `json:"x-res"`
	YRes         int    `json:"y-res"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	BorderRadius int    `json:"border-radius"`
	CornerRadii  []int  `json:"corner-radii"`
	Cutouts      []struct {
		Name string `json:"name"`
		Path string `json:"path"`
	} `json:"cutouts"`
}

// Parse reads a gmobile display-panel file. Each cutout's SVG path is
// reduced to a box; arcs are boxed conservatively, never smaller than the
// real shape.
func Parse(b []byte) (Panel, error) {
	var g gmobileFile
	if err := json.Unmarshal(b, &g); err != nil {
		return Panel{}, err
	}
	if g.XRes <= 0 || g.YRes <= 0 {
		return Panel{}, errors.New("no resolution")
	}
	p := Panel{Name: g.Name, XRes: g.XRes, YRes: g.YRes, WidthMM: g.Width, HeightMM: g.Height, Cutouts: []Box{}}
	switch len(g.CornerRadii) {
	case 0:
		p.CornerRadii = [4]int{g.BorderRadius, g.BorderRadius, g.BorderRadius, g.BorderRadius}
	case 4:
		copy(p.CornerRadii[:], g.CornerRadii)
	default:
		return Panel{}, fmt.Errorf("corner-radii: %d values, want 4", len(g.CornerRadii))
	}
	for _, c := range g.Cutouts {
		x0, y0, x1, y1, err := pathBounds(c.Path)
		if err != nil {
			return Panel{}, fmt.Errorf("cutout %s: %w", c.Name, err)
		}
		x, y := int(math.Floor(x0)), int(math.Floor(y0))
		p.Cutouts = append(p.Cutouts, Box{c.Name, x, y, int(math.Ceil(x1)) - x, int(math.Ceil(y1)) - y})
	}
	return p, nil
}

// Table is gmobile's set, by compatible.
func Table() (map[string]Panel, error) {
	var t map[string]Panel
	err := json.Unmarshal(table, &t)
	return t, err
}

// Lookup finds the panel for the first of compatibles that has one. A
// non-empty override (a gmobile-format file) wins over the table.
func Lookup(compatibles []string, override string) (p *Panel, compatible string, err error) {
	if override != "" {
		b, err := os.ReadFile(override)
		if err != nil {
			return nil, "", err
		}
		p, err := Parse(b)
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", override, err)
		}
		return &p, "", nil
	}
	t, err := Table()
	if err != nil {
		return nil, "", err
	}
	for _, c := range compatibles {
		if p, ok := t[c]; ok {
			return &p, c, nil
		}
	}
	return nil, "", nil
}

// Compatibles reads the running device tree's compatible list.
func Compatibles() []string {
	b, err := os.ReadFile("/proc/device-tree/compatible")
	if err != nil {
		return nil
	}
	var out []string
	for _, c := range bytes.Split(bytes.TrimRight(b, "\x00"), []byte{0}) {
		if len(c) > 0 {
			out = append(out, string(c))
		}
	}
	return out
}
