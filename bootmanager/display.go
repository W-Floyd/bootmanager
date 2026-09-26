package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"math"
	"os"
	"strconv"
	"strings"
)

// display.info: the panel's shape, from a gmobile display-panel file
// (data/devices/display-panels/<dt-compatible>.json upstream), looked up by
// this device's device-tree compatibles as gmobile does. gmobile compiles
// those files into its library, so there is nothing to read off the
// rootfs; bootmanager/build.sh copies fogona's (from the aports overlay) into
// assets/display-panels/ for the embed.
//
// Each cutout's SVG path is reduced to a bounding box, which is all the UI
// needs to keep text clear of it. Arcs are boxed conservatively (their
// endpoints widened by the radii), never smaller than the real shape.

type cutoutJSON struct {
	Name string `json:"name"`
	Path string `json:"path"`
	X    int    `json:"x"`
	Y    int    `json:"y"`
	W    int    `json:"w"`
	H    int    `json:"h"`
}

type panelJSON struct {
	Name         string       `json:"name"`
	XRes         int          `json:"x-res"`
	YRes         int          `json:"y-res"`
	BorderRadius int          `json:"border-radius"`
	Cutouts      []cutoutJSON `json:"cutouts"`
}

var compatOverride = flag.String("compatible", "", "space-separated DT compatibles to look the panel up by, instead of /proc/device-tree (for -scan)")

func compatibles() []string {
	if *compatOverride != "" {
		return strings.Fields(*compatOverride)
	}
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

func displayInfo([]byte) (any, error) {
	for _, c := range compatibles() {
		if strings.ContainsAny(c, "/") {
			continue
		}
		b, err := assets.ReadFile("assets/display-panels/" + c + ".json")
		if err != nil {
			continue
		}
		var p panelJSON
		if err := json.Unmarshal(b, &p); err != nil {
			return nil, err
		}
		for i := range p.Cutouts {
			x0, y0, x1, y1, err := pathBounds(p.Cutouts[i].Path)
			if err != nil {
				return nil, err
			}
			c := &p.Cutouts[i]
			c.X, c.Y = int(math.Floor(x0)), int(math.Floor(y0))
			c.W, c.H = int(math.Ceil(x1))-c.X, int(math.Ceil(y1))-c.Y
		}
		return map[string]any{"compatible": c, "panel": p}, nil
	}
	return map[string]any{"panel": nil}, nil
}

// pathBounds is the bounding box of an SVG path: M L H V C S Q T A Z and
// their relative forms. Control points count, so curves are boxed by their
// hulls; arcs by their endpoints widened by the radii.
func pathBounds(d string) (x0, y0, x1, y1 float64, err error) {
	toks := pathTokens(d)
	x0, y0 = math.Inf(1), math.Inf(1)
	x1, y1 = math.Inf(-1), math.Inf(-1)
	add := func(x, y float64) {
		x0, y0 = math.Min(x0, x), math.Min(y0, y)
		x1, y1 = math.Max(x1, x), math.Max(y1, y)
	}
	var cx, cy, sx, sy float64
	var cmd byte
	i := 0
	num := func() (float64, bool) {
		if i >= len(toks) || isCmd(toks[i]) {
			return 0, false
		}
		v, e := strconv.ParseFloat(toks[i], 64)
		i++
		return v, e == nil
	}
	for i < len(toks) {
		if isCmd(toks[i]) {
			cmd = toks[i][0]
			i++
		} else if cmd == 0 {
			return 0, 0, 0, 0, errors.New("path: number before a command")
		}
		rel := cmd >= 'a'
		up := cmd &^ 0x20
		off := func(x, y float64) (float64, float64) {
			if rel {
				return cx + x, cy + y
			}
			return x, y
		}
		// How many numbers each command takes.
		n := map[byte]int{'M': 2, 'L': 2, 'T': 2, 'H': 1, 'V': 1, 'C': 6, 'S': 4, 'Q': 4, 'A': 7, 'Z': 0}[up]
		if up == 'Z' {
			cx, cy = sx, sy
			continue
		}
		if n == 0 {
			return 0, 0, 0, 0, errors.New("path: unknown command " + string(cmd))
		}
		v := make([]float64, n)
		for k := range v {
			var ok bool
			if v[k], ok = num(); !ok {
				return 0, 0, 0, 0, errors.New("path: short arguments to " + string(cmd))
			}
		}
		switch up {
		case 'H':
			if rel {
				cx += v[0]
			} else {
				cx = v[0]
			}
		case 'V':
			if rel {
				cy += v[0]
			} else {
				cy = v[0]
			}
		case 'A':
			ex, ey := off(v[5], v[6])
			rx, ry := math.Abs(v[0]), math.Abs(v[1])
			add(cx-rx, cy-ry)
			add(cx+rx, cy+ry)
			add(ex-rx, ey-ry)
			add(ex+rx, ey+ry)
			cx, cy = ex, ey
		default:
			// Every pair is a point: control points, then the end point.
			for k := 0; k < n; k += 2 {
				px, py := off(v[k], v[k+1])
				add(px, py)
				if k == n-2 {
					cx, cy = px, py
				}
			}
			if up == 'M' {
				sx, sy = cx, cy
				// Further pairs after M are implicit L (l after m).
				cmd = map[bool]byte{true: 'l', false: 'L'}[rel]
			}
		}
		add(cx, cy)
	}
	if math.IsInf(x0, 1) {
		return 0, 0, 0, 0, errors.New("path: empty")
	}
	return x0, y0, x1, y1, nil
}

func isCmd(t string) bool {
	return len(t) == 1 && strings.ContainsRune("MmLlHhVvCcSsQqTtAaZz", rune(t[0]))
}

// pathTokens splits a path into commands and numbers ("M325,0L-3.5e1,0").
func pathTokens(d string) []string {
	var toks []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			toks = append(toks, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(d); i++ {
		ch := d[i]
		switch {
		case isCmd(string(ch)) && !(ch == 'e' || ch == 'E'):
			flush()
			toks = append(toks, string(ch))
		case ch == ',' || ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r':
			flush()
		case ch == '-' || ch == '+':
			// A sign starts a new number unless it follows an exponent.
			s := cur.String()
			if len(s) > 0 && s[len(s)-1] != 'e' && s[len(s)-1] != 'E' {
				flush()
			}
			cur.WriteByte(ch)
		case ch == '.' && strings.Contains(cur.String(), ".") && !strings.ContainsAny(cur.String(), "eE"):
			flush() // ".5.5" is two numbers
			cur.WriteByte(ch)
		default:
			cur.WriteByte(ch)
		}
	}
	flush()
	return toks
}
