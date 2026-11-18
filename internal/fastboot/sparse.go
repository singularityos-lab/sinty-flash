package fastboot

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	BlockSize       = 4096
	headerSize      = 28
	chunkHeaderSize = 12
	sparseMagic     = 0xed26ff3a
	chunkRaw        = 0xcac1
	chunkDontCare   = 0xcac3
)

type Piece struct {
	Total  int64
	Offset int64
	Length int64
}

func Split(size, maxDownload int64) ([]Piece, error) {
	if size <= 0 {
		return nil, errors.New("nothing to flash")
	}
	room := (maxDownload - headerSize - 3*chunkHeaderSize) / BlockSize * BlockSize
	if room <= 0 {
		return nil, fmt.Errorf("max-download-size %d is too small", maxDownload)
	}
	total := (size + BlockSize - 1) / BlockSize * BlockSize
	var pieces []Piece
	for off := int64(0); off < size; off += room {
		n := room
		if off+n > size {
			n = size - off
		}
		pieces = append(pieces, Piece{Total: total, Offset: off, Length: n})
	}
	return pieces, nil
}

func (p Piece) rawBlocks() int64 { return (p.Length + BlockSize - 1) / BlockSize }

func (p Piece) chunks() (before, after int64) {
	before = p.Offset / BlockSize
	after = p.Total/BlockSize - before - p.rawBlocks()
	return
}

func (p Piece) header() []byte {
	before, after := p.chunks()
	count := uint32(1)
	if before > 0 {
		count++
	}
	if after > 0 {
		count++
	}
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, struct {
		Magic       uint32
		Major       uint16
		Minor       uint16
		FileHeader  uint16
		ChunkHeader uint16
		BlockSize   uint32
		Blocks      uint32
		Chunks      uint32
		Checksum    uint32
	}{sparseMagic, 1, 0, headerSize, chunkHeaderSize, BlockSize, uint32(p.Total / BlockSize), count, 0})
	if before > 0 {
		writeChunk(&b, chunkDontCare, before, 0)
	}
	writeChunk(&b, chunkRaw, p.rawBlocks(), p.rawBlocks()*BlockSize)
	return b.Bytes()
}

func (p Piece) trailer() []byte {
	_, after := p.chunks()
	if after == 0 {
		return nil
	}
	var b bytes.Buffer
	writeChunk(&b, chunkDontCare, after, 0)
	return b.Bytes()
}

func writeChunk(b *bytes.Buffer, kind uint16, blocks, data int64) {
	binary.Write(b, binary.LittleEndian, struct {
		Kind     uint16
		Reserved uint16
		Blocks   uint32
		Size     uint32
	}{kind, 0, uint32(blocks), uint32(chunkHeaderSize + data)})
}

func (p Piece) Size() int64 {
	pad := p.rawBlocks()*BlockSize - p.Length
	return int64(len(p.header())) + p.Length + pad + int64(len(p.trailer()))
}

func (p Piece) Reader(r io.ReaderAt) io.Reader {
	pad := p.rawBlocks()*BlockSize - p.Length
	return io.MultiReader(
		bytes.NewReader(p.header()),
		io.NewSectionReader(r, p.Offset, p.Length),
		bytes.NewReader(make([]byte, pad)),
		bytes.NewReader(p.trailer()),
	)
}
