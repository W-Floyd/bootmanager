package fastboot

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

type chunk struct {
	kind   uint16
	blocks uint32
	data   []byte
}

func sparse(bs, blocks uint32, chunks ...chunk) []byte {
	b := le.AppendUint32(nil, sparseMagic)
	b = le.AppendUint16(b, 1)
	b = le.AppendUint16(b, 0)
	b = le.AppendUint16(b, sparseHeader)
	b = le.AppendUint16(b, chunkHeader)
	b = le.AppendUint32(b, bs)
	b = le.AppendUint32(b, blocks)
	b = le.AppendUint32(b, uint32(len(chunks)))
	b = le.AppendUint32(b, 0)
	for _, c := range chunks {
		b = le.AppendUint16(b, c.kind)
		b = le.AppendUint16(b, 0)
		b = le.AppendUint32(b, c.blocks)
		b = le.AppendUint32(b, uint32(chunkHeader+len(c.data)))
		b = append(b, c.data...)
	}
	return b
}

type zeroTarget struct {
	memTarget
	zeroed int64
}

func (z *zeroTarget) ZeroRange(off, n int64) error {
	clear(z.b[off : off+n])
	z.zeroed += n
	return nil
}

func TestSparse(t *testing.T) {
	raw := bytes.Repeat([]byte("abcd"), 8) // two 16-byte blocks
	img := sparse(16, 7,
		chunk{chunkDontCare, 1, nil},
		chunk{chunkRaw, 2, raw},
		chunk{chunkFill, 2, []byte{1, 2, 3, 4}},
		chunk{chunkCRC32, 0, []byte{0, 0, 0, 0}},
		chunk{chunkFill, 2, []byte{0, 0, 0, 0}},
	)
	sp, err := parseSparse(img)
	if err != nil {
		t.Fatal(err)
	}
	if sp.size() != 112 {
		t.Errorf("size %d, want 112", sp.size())
	}
	w := &zeroTarget{memTarget: memTarget{b: bytes.Repeat([]byte{0xee}, 112)}}
	if err := sp.writeTo(w); err != nil {
		t.Fatal(err)
	}
	want := slices.Concat(bytes.Repeat([]byte{0xee}, 16), raw, bytes.Repeat([]byte{1, 2, 3, 4}, 8), make([]byte, 32))
	if !bytes.Equal(w.b, want) {
		t.Errorf("wrote\n%x\nwant\n%x", w.b, want)
	}
	if w.zeroed != 32 {
		t.Errorf("zeroed %d bytes by ZeroRange, want 32", w.zeroed)
	}
}

func TestSparseRejects(t *testing.T) {
	for name, img := range map[string][]byte{
		"raw short":      sparse(16, 2, chunk{chunkRaw, 2, make([]byte, 16)}),
		"past blocks":    sparse(16, 1, chunk{chunkDontCare, 2, nil}),
		"unknown chunk":  sparse(16, 1, chunk{0x1234, 1, nil}),
		"block size":     sparse(6, 1),
		"missing chunks": sparse(16, 1)[:sparseHeader-4],
	} {
		if !isSparse(img) {
			if name != "missing chunks" {
				t.Errorf("%s: not seen as sparse", name)
			}
			continue
		}
		if _, err := parseSparse(img); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	// A chunk count past the image's end.
	img := sparse(16, 1)
	le.PutUint32(img[20:], 1)
	if _, err := parseSparse(img); err == nil || !strings.Contains(err.Error(), "past the end") {
		t.Errorf("chunk past the end: %v", err)
	}
}
