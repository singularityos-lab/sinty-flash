package edl

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

const (
	BlockSize     = 4096
	sparseMagic   = 0xed26ff3a
	chunkRaw      = 0xcac1
	chunkDontCare = 0xcac3
	maxRawBlocks  = 16384
)

type extent struct{ start, end int64 }

// dataExtents lists the regions of f the filesystem holds data for, rounded
// out to whole blocks. Everything else is a hole the image leaves untouched.
func dataExtents(f *os.File, size int64) ([]extent, error) {
	var out []extent
	for off := int64(0); off < size; {
		start, err := f.Seek(off, 3)
		if errors.Is(err, syscall.ENXIO) {
			break
		}
		if err != nil {
			return nil, err
		}
		end, err := f.Seek(start, 4)
		if err != nil {
			return nil, err
		}
		start = start / BlockSize * BlockSize
		end = min((end+BlockSize-1)/BlockSize*BlockSize, (size+BlockSize-1)/BlockSize*BlockSize)
		if n := len(out); n > 0 && out[n-1].end >= start {
			out[n-1].end = max(out[n-1].end, end)
		} else {
			out = append(out, extent{start, end})
		}
		off = end
	}
	return out, nil
}

// WriteSparse writes the file at src as an Android sparse image: the data it
// holds as raw chunks, its holes as chunks the flasher skips. It returns the
// size of the unpacked image.
func WriteSparse(src, dst string) (int64, error) {
	f, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if fi.Size()%BlockSize != 0 {
		return 0, fmt.Errorf("%s is %d bytes, not a multiple of %d", src, fi.Size(), BlockSize)
	}
	total := fi.Size() / BlockSize
	ext, err := dataExtents(f, fi.Size())
	if err != nil {
		return 0, fmt.Errorf("%s: %w", src, err)
	}
	type chunk struct {
		kind          uint16
		block, blocks int64
	}
	var chunks []chunk
	at := int64(0)
	for _, e := range ext {
		if b := e.start / BlockSize; b > at {
			chunks = append(chunks, chunk{chunkDontCare, at, b - at})
		}
		for b := e.start / BlockSize; b < e.end/BlockSize; b += maxRawBlocks {
			chunks = append(chunks, chunk{chunkRaw, b, min(maxRawBlocks, e.end/BlockSize-b)})
		}
		at = e.end / BlockSize
	}
	if at < total {
		chunks = append(chunks, chunk{chunkDontCare, at, total - at})
	}

	out, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	w := bufio.NewWriterSize(out, 1<<20)
	le := binary.LittleEndian
	hdr := make([]byte, 28)
	le.PutUint32(hdr[0:], sparseMagic)
	le.PutUint16(hdr[4:], 1)
	le.PutUint16(hdr[8:], 28)
	le.PutUint16(hdr[10:], 12)
	le.PutUint32(hdr[12:], BlockSize)
	le.PutUint32(hdr[16:], uint32(total))
	le.PutUint32(hdr[20:], uint32(len(chunks)))
	w.Write(hdr)
	for _, c := range chunks {
		ch := make([]byte, 12)
		le.PutUint16(ch[0:], c.kind)
		le.PutUint32(ch[4:], uint32(c.blocks))
		size := int64(12)
		if c.kind == chunkRaw {
			size += c.blocks * BlockSize
		}
		le.PutUint32(ch[8:], uint32(size))
		w.Write(ch)
		if c.kind == chunkRaw {
			if _, err := io.Copy(w, io.NewSectionReader(f, c.block*BlockSize, c.blocks*BlockSize)); err != nil {
				out.Close()
				return 0, err
			}
		}
	}
	if err := w.Flush(); err != nil {
		out.Close()
		return 0, err
	}
	return fi.Size(), out.Close()
}

// Unsparse expands a sparse image the way the flasher writes it, onto dst,
// skipping the chunks it would skip. It is the check that an image packs
// what it was made from.
func Unsparse(src string, dst io.WriterAt) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	le := binary.LittleEndian
	hdr := make([]byte, 28)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return err
	}
	if le.Uint32(hdr) != sparseMagic || le.Uint16(hdr[8:]) != 28 || le.Uint16(hdr[10:]) != 12 {
		return errors.New("not a sparse image")
	}
	blk := int64(le.Uint32(hdr[12:]))
	at := int64(0)
	for i := uint32(0); i < le.Uint32(hdr[20:]); i++ {
		ch := make([]byte, 12)
		if _, err := io.ReadFull(r, ch); err != nil {
			return err
		}
		n := int64(le.Uint32(ch[4:])) * blk
		switch le.Uint16(ch) {
		case chunkRaw:
			buf := make([]byte, n)
			if _, err := io.ReadFull(r, buf); err != nil {
				return err
			}
			if _, err := dst.WriteAt(buf, at); err != nil {
				return err
			}
		case chunkDontCare:
		default:
			return fmt.Errorf("chunk type %#x", le.Uint16(ch))
		}
		at += n
	}
	if at != int64(le.Uint32(hdr[16:]))*blk {
		return fmt.Errorf("the chunks cover %d bytes, the header says %d", at, int64(le.Uint32(hdr[16:]))*blk)
	}
	return nil
}
