package main

import (
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
)

// Thumbnails for the file list: fs.thumb {dev, path, size} decodes a JPEG,
// PNG or GIF (the standard library's decoders) and returns it shrunk to fit
// size x size, as RGBA bytes, which the UI turns straight into an image.
//
// Limits keep a large photo from stalling the stage: the file and the
// pixel count (read from the header before decoding) are capped.

const (
	maxThumbFile   = 32 << 20
	maxThumbPixels = 40_000_000
)

func fsThumb(args []byte) (any, error) {
	a, err := decode[struct {
		Dev, Path string
		Size      int
	}](args)
	if err != nil {
		return nil, err
	}
	if a.Size <= 0 || a.Size > 512 {
		a.Size = 96
	}
	f, _, err := openBrowsed(a.Dev, a.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || st.Size() > maxThumbFile {
		return nil, errors.New("too large for a thumbnail")
	}
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return nil, err
	}
	if cfg.Width*cfg.Height > maxThumbPixels {
		return nil, errors.New("too many pixels for a thumbnail")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	w, h, rgba := shrink(img, a.Size)
	return map[string]any{"width": w, "height": h, "rgba": rgba}, nil
}

// shrink fits img into size x size, each output pixel the average of a 4x4
// grid of samples across the source area it covers: cheap, and smooth
// enough at thumbnail size. The result is straight (not premultiplied)
// RGBA, which encoding/json sends as base64.
func shrink(img image.Image, size int) (int, int, []byte) {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	w, h := size, size
	if sw >= sh {
		h = max(1, sh*size/sw)
	} else {
		w = max(1, sw*size/sh)
	}
	w, h = min(w, sw), min(h, sh)
	const n = 4
	out := make([]byte, 0, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, bl, al uint32
			for j := 0; j < n; j++ {
				sy := b.Min.Y + ((2*y+1)*n+2*j-n+1)*sh/(2*h*n)
				for i := 0; i < n; i++ {
					sx := b.Min.X + ((2*x+1)*n+2*i-n+1)*sw/(2*w*n)
					cr, cg, cb, ca := img.At(clamp(sx, b.Min.X, b.Max.X-1), clamp(sy, b.Min.Y, b.Max.Y-1)).RGBA()
					r, g, bl, al = r+cr, g+cg, bl+cb, al+ca
				}
			}
			// Averages of premultiplied 16-bit values; un-premultiply.
			if al == 0 {
				out = append(out, 0, 0, 0, 0)
				continue
			}
			out = append(out,
				byte(r*255/al), byte(g*255/al), byte(bl*255/al),
				byte(al/(n*n)>>8))
		}
	}
	return w, h, out
}

func clamp(v, lo, hi int) int {
	return max(lo, min(v, hi))
}
