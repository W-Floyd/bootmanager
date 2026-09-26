package storage

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"testing"
)

func TestShrink(t *testing.T) {
	for _, tc := range []struct{ sw, sh, size, w, h int }{
		{400, 200, 96, 96, 48},
		{200, 400, 96, 48, 96},
		{50, 30, 96, 50, 30}, // never enlarged
		{1, 1, 96, 1, 1},
		{4000, 3, 96, 96, 1},
	} {
		src := image.NewNRGBA(image.Rect(10, 20, 10+tc.sw, 20+tc.sh)) // offset bounds
		for y := 20; y < 20+tc.sh; y++ {
			for x := 10; x < 10+tc.sw; x++ {
				src.Set(x, y, color.NRGBA{200, 100, 50, 255})
			}
		}
		dst := shrink(src, tc.size)
		if w, h := dst.Rect.Dx(), dst.Rect.Dy(); w != tc.w || h != tc.h || len(dst.Pix) != w*h*4 {
			t.Fatalf("%dx%d -> %dx%d (%d bytes), want %dx%d", tc.sw, tc.sh, w, h, len(dst.Pix), tc.w, tc.h)
		}
		for i, px := 0, dst.Pix; i < len(px); i += 4 {
			if px[i] != 200 || px[i+1] != 100 || px[i+2] != 50 || px[i+3] != 255 {
				t.Fatalf("%dx%d: pixel %d = %v", tc.sw, tc.sh, i/4, px[i:i+4])
			}
		}
	}
}

func TestThumb(t *testing.T) {
	var ok bytes.Buffer
	png.Encode(&ok, image.NewNRGBA(image.Rect(0, 0, 200, 100)))
	if th, err := thumb(bytes.NewReader(ok.Bytes()), 50); err != nil || th.Width != 50 || th.Height != 25 {
		t.Errorf("200x100 png: %+v %v", th, err)
	}
	// A 0x0 GIF decodes to a 0x0 image, which shrink would divide by.
	var empty bytes.Buffer
	if err := gif.Encode(&empty, image.NewPaletted(image.Rect(0, 0, 0, 0), color.Palette{color.Black}), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := thumb(bytes.NewReader(empty.Bytes()), 50); err == nil {
		t.Error("0x0 gif: no error")
	}
}
