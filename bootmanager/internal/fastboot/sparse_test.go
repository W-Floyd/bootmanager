package fastboot

import (
	"bytes"
	"fmt"
	"io"
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

// A big image comes as several sparse pieces, each the whole image's size
// with holes where the others go: the progress counts the pieces and
// carries on from where the last ended.
func TestProgress(t *testing.T) {
	s, mem := testDevice()
	var events []Progress
	s.Progress = func(p Progress) { events = append(events, p) }
	// 16-byte blocks; the partition holds 64 KiB, the image is 8 blocks.
	a := bytes.Repeat([]byte("A"), 32)
	b := bytes.Repeat([]byte("B"), 32)
	piece1 := sparse(16, 8, chunk{chunkRaw, 2, a}, chunk{chunkDontCare, 6, nil})
	piece2 := sparse(16, 8, chunk{chunkDontCare, 4, nil}, chunk{chunkRaw, 2, b}, chunk{chunkDontCare, 2, nil})
	var in [][]byte
	for _, pc := range [][]byte{piece1, piece2} {
		in = append(in, []byte(fmt.Sprintf("download:%08x", len(pc))), pc, []byte("flash:boot_b"))
	}
	if err := s.Serve(&pipeTransport{in: in}); err != io.EOF {
		t.Fatalf("Serve: %v", err)
	}
	var phases []string
	for _, p := range events {
		if len(phases) == 0 || phases[len(phases)-1] != p.Phase {
			phases = append(phases, p.Phase)
		}
	}
	if got := strings.Join(phases, " "); got != "receiving writing waiting receiving writing waiting" {
		t.Errorf("phases: %s", got)
	}
	last := events[len(events)-1]
	if last.Piece != 2 || last.Total != 128 || last.End != 96 || last.Wrote != 32 || last.WroteTotal != 32 {
		t.Errorf("last event %+v, want piece 2 of an image of 128 bytes, ended at 96, 32 of 32 written", last)
	}
	// Between the pieces, the second one's download shows how far the first got.
	for _, p := range events {
		if p.Phase == "receiving" && p.Piece == 2 && p.End != 32 {
			t.Errorf("piece 2 receiving: End %d, want 32 (where piece 1 ended)", p.End)
			break
		}
	}
	got := mem["sda2"].bytes()
	if !bytes.Equal(got[:32], a) || !bytes.Equal(got[64:96], b) {
		t.Error("the two pieces are not both on the partition")
	}
}

// Abort stops a flash after the chunk being written: the rest of the piece
// is not.
func TestAbort(t *testing.T) {
	s, mem := testDevice()
	s.Progress = func(p Progress) {
		if p.Phase == "writing" && p.Wrote > 0 {
			s.Abort()
		}
	}
	a := bytes.Repeat([]byte("A"), 32)
	b := bytes.Repeat([]byte("B"), 32)
	img := sparse(16, 4, chunk{chunkRaw, 2, a}, chunk{chunkRaw, 2, b})
	p := &pipeTransport{in: [][]byte{[]byte(fmt.Sprintf("download:%08x", len(img))), img, []byte("flash:boot_b")}}
	if err := s.Serve(p); err != io.EOF {
		t.Fatalf("Serve: %v", err)
	}
	if last := p.replies[len(p.replies)-1]; !strings.HasPrefix(last, "FAIL") || !strings.Contains(last, "stopped") {
		t.Errorf("flash reply %q, want a FAIL saying it stopped", last)
	}
	got := mem["sda2"].bytes()
	if !bytes.Equal(got[:32], a) {
		t.Error("the chunk before the abort is not written")
	}
	if bytes.Equal(got[32:64], b) {
		t.Error("the chunk after the abort was written")
	}
}
