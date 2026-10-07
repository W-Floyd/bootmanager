package fastboot

import (
	"errors"
	"fmt"
)

// Android sparse images (AOSP system/core/libsparse/sparse_format.h): what
// img2simg makes, and what the host splits an image larger than
// max-download-size into. Each piece starts with a don't-care chunk over
// what earlier pieces wrote, so every piece is written from offset 0.
const (
	sparseMagic    = 0xed26ff3a
	sparseHeader   = 28
	chunkHeader    = 12
	chunkRaw       = 0xcac1
	chunkFill      = 0xcac2
	chunkDontCare  = 0xcac3
	chunkCRC32     = 0xcac4
	fillBufferSize = 1 << 20
)

type sparseImage struct {
	blockSize int64
	blocks    int64
	chunks    []sparseChunk
}

type sparseChunk struct {
	kind   uint16
	block  int64 // first block written
	blocks int64
	data   []byte // raw: the blocks; fill: the 4-byte pattern
}

func isSparse(b []byte) bool { return len(b) >= sparseHeader && le.Uint32(b) == sparseMagic }

// parseSparse checks the whole image before anything is written, so a
// malformed one writes nothing.
func parseSparse(b []byte) (*sparseImage, error) {
	major, hdrSize, chunkSize := le.Uint16(b[4:]), int(le.Uint16(b[8:])), int(le.Uint16(b[10:]))
	bs := int64(le.Uint32(b[12:]))
	switch {
	case major != 1:
		return nil, fmt.Errorf("sparse image version %d", major)
	case hdrSize < sparseHeader || chunkSize < chunkHeader || hdrSize > len(b):
		return nil, errors.New("sparse image: bad header sizes")
	case bs == 0 || bs%4 != 0 || bs > 1<<20: // the cap keeps blocks*bs in an int64
		return nil, fmt.Errorf("sparse image: block size %d", bs)
	}
	sp := &sparseImage{blockSize: bs, blocks: int64(le.Uint32(b[16:]))}
	nchunks := int(le.Uint32(b[20:]))
	off, block := hdrSize, int64(0)
	for i := range nchunks {
		if len(b)-off < chunkSize {
			return nil, fmt.Errorf("sparse image: chunk %d past the end", i)
		}
		kind, blocks, total := le.Uint16(b[off:]), int64(le.Uint32(b[off+4:])), int64(le.Uint32(b[off+8:]))
		if total < int64(chunkSize) || total > int64(len(b)-off) {
			return nil, fmt.Errorf("sparse image: chunk %d size %d", i, total)
		}
		data := b[off+chunkSize : off+int(total)]
		want := int64(-1)
		switch kind {
		case chunkRaw:
			want = blocks * bs
		case chunkFill, chunkCRC32:
			want = 4
		case chunkDontCare:
			want = 0
		}
		if want < 0 {
			return nil, fmt.Errorf("sparse image: chunk %d type 0x%x", i, kind)
		}
		if int64(len(data)) != want {
			return nil, fmt.Errorf("sparse image: chunk %d holds %d bytes, not %d", i, len(data), want)
		}
		if kind != chunkCRC32 {
			sp.chunks = append(sp.chunks, sparseChunk{kind, block, blocks, data})
			block += blocks
		}
		if block > sp.blocks {
			return nil, fmt.Errorf("sparse image: chunk %d past its %d blocks", i, sp.blocks)
		}
		off += int(total)
	}
	return sp, nil
}

// size is how much of the partition the image covers.
func (sp *sparseImage) size() int64 { return sp.blocks * sp.blockSize }

func (sp *sparseImage) writeTo(w Target) error { return sp.writeToProgress(w, nil) }

// writeToProgress writes the image, telling step after each chunk how many
// bytes it has written so far and where in the image that left it. An
// error from step stops the write and is returned.
func (sp *sparseImage) writeToProgress(w Target, step func(wrote, end int64) error) error {
	var fill []byte
	var wrote, end int64
	done := func(off, n int64) error {
		wrote, end = wrote+n, off+n
		if step != nil {
			return step(wrote, end)
		}
		return nil
	}
	for _, c := range sp.chunks {
		// The same news again, before the next write: an error from step
		// (a stop) takes effect before it, not after it.
		if step != nil && wrote > 0 && (c.kind == chunkRaw || c.kind == chunkFill) {
			if err := step(wrote, end); err != nil {
				return err
			}
		}
		off, n := c.block*sp.blockSize, c.blocks*sp.blockSize
		switch c.kind {
		case chunkRaw:
			if _, err := w.WriteAt(c.data, off); err != nil {
				return err
			}
			if err := done(off, n); err != nil {
				return err
			}
		case chunkFill:
			start, size := off, n
			if z, ok := w.(zeroer); ok && le.Uint32(c.data) == 0 {
				if err := z.ZeroRange(off, n); err == nil {
					if err := done(start, size); err != nil {
						return err
					}
					continue
				}
			}
			if fill == nil {
				fill = make([]byte, fillBufferSize)
			}
			for i := 0; i < len(fill); i += 4 {
				copy(fill[i:], c.data)
			}
			for n > 0 {
				k := min(n, int64(len(fill)))
				if _, err := w.WriteAt(fill[:k], off); err != nil {
					return err
				}
				off, n = off+k, n-k
			}
			if err := done(start, size); err != nil {
				return err
			}
		}
	}
	return nil
}

// dataBytes is how many bytes the image writes: its raw and fill chunks,
// not the holes it skips.
func (sp *sparseImage) dataBytes() int64 {
	var n int64
	for _, c := range sp.chunks {
		if c.kind == chunkRaw || c.kind == chunkFill {
			n += c.blocks * sp.blockSize
		}
	}
	return n
}

// end is where the last chunk that writes anything ends: how far into the
// whole image a piece got. A sparse piece usually ends in a hole up to the
// image's size, which is no progress.
func (sp *sparseImage) end() int64 {
	var e int64
	for _, c := range sp.chunks {
		if c.kind == chunkRaw || c.kind == chunkFill {
			e = (c.block + c.blocks) * sp.blockSize
		}
	}
	return e
}
