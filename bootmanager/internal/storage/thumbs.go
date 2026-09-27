package storage

import (
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"

	"golang.org/x/image/draw"
)

// Caps that keep a large photo from stalling or exhausting the stage: the
// file size and pixel count (from the header, before decoding), and how
// many decode at once (a 40-megapixel decode holds 160 MB).
const (
	maxThumbFile   = 32 << 20
	maxThumbPixels = 40_000_000
	maxThumbSide   = 4096
)

var thumbSlots = make(chan struct{}, 2)

type Thumb struct {
	Width  int `json:"width"`
	Height int `json:"height"`
	// Straight (not premultiplied) RGBA, which encoding/json sends as
	// base64.
	RGBA []byte `json:"rgba"`
}

// Thumb decodes a JPEG, PNG or GIF and shrinks it to fit w x h: a row's
// picture, or the whole screen for the image viewer.
func (b *Browser) Thumb(dev, p string, w, h int) (Thumb, error) {
	if w <= 0 || h <= 0 || w > maxThumbSide || h > maxThumbSide {
		w, h = 96, 96
	}
	f, err := b.Open(dev, p)
	if err != nil {
		return Thumb{}, err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || st.Size() > maxThumbFile {
		return Thumb{}, errors.New("too large for a thumbnail")
	}
	return thumb(f, w, h)
}

func thumb(f io.ReadSeeker, w, h int) (Thumb, error) {
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return Thumb{}, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return Thumb{}, errors.New("an empty image")
	}
	if cfg.Width*cfg.Height > maxThumbPixels {
		return Thumb{}, errors.New("too many pixels for a thumbnail")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Thumb{}, err
	}
	thumbSlots <- struct{}{}
	defer func() { <-thumbSlots }()
	img, _, err := image.Decode(f)
	if err != nil {
		return Thumb{}, err
	}
	dst := shrink(img, w, h)
	return Thumb{dst.Rect.Dx(), dst.Rect.Dy(), dst.Pix}, nil
}

// shrink fits img into bw x bh, keeping its aspect ratio; it never
// enlarges.
func shrink(img image.Image, bw, bh int) *image.NRGBA {
	sw, sh := img.Bounds().Dx(), img.Bounds().Dy()
	w, h := bw, bh
	if sw*bh >= sh*bw {
		h = max(1, sh*bw/sw)
	} else {
		w = max(1, sw*bh/sh)
	}
	dst := image.NewNRGBA(image.Rect(0, 0, min(w, sw), min(h, sh)))
	draw.BiLinear.Scale(dst, dst.Rect, img, img.Bounds(), draw.Src, nil)
	return dst
}
