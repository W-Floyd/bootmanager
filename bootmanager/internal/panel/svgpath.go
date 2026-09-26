package panel

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

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
		n := argCount[up]
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
				cmd = 'L' | cmd&0x20
			}
		}
		add(cx, cy)
	}
	if math.IsInf(x0, 1) {
		return 0, 0, 0, 0, errors.New("path: empty")
	}
	return x0, y0, x1, y1, nil
}

// argCount is how many numbers each command takes.
var argCount = map[byte]int{'M': 2, 'L': 2, 'T': 2, 'H': 1, 'V': 1, 'C': 6, 'S': 4, 'Q': 4, 'A': 7, 'Z': 0}

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
