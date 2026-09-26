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
)

var thumbSlots = make(chan struct{}, 2)

type Thumb struct {
	Width  int `json:"width"`
	Height int `json:"height"`
	// Straight (not premultiplied) RGBA, which encoding/json sends as
	// base64.
	RGBA []byte `json:"rgba"`
}

// Thumb decodes a JPEG, PNG or GIF and shrinks it to fit size x size.
func (b *Browser) Thumb(dev, p string, size int) (Thumb, error) {
	if size <= 0 || size > 512 {
		size = 96
	}
	f, err := b.Open(dev, p)
	if err != nil {
		return Thumb{}, err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || st.Size() > maxThumbFile {
		return Thumb{}, errors.New("too large for a thumbnail")
	}
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return Thumb{}, err
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
	dst := shrink(img, size)
	return Thumb{dst.Rect.Dx(), dst.Rect.Dy(), dst.Pix}, nil
}

// shrink fits img into size x size, keeping its aspect ratio; it never
// enlarges.
func shrink(img image.Image, size int) *image.NRGBA {
	sw, sh := img.Bounds().Dx(), img.Bounds().Dy()
	w, h := size, size
	if sw >= sh {
		h = max(1, sh*size/sw)
	} else {
		w = max(1, sw*size/sh)
	}
	dst := image.NewNRGBA(image.Rect(0, 0, min(w, sw), min(h, sh)))
	draw.BiLinear.Scale(dst, dst.Rect, img, img.Bounds(), draw.Src, nil)
	return dst
}
