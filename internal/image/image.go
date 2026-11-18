package image

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf16"

	"github.com/ulikunitz/xz"
)

const sector = 512

var Labels = map[string]string{
	"esp":  "esp",
	"root": "sing-root",
	"hash": "sing-hash",
}

type Stream struct {
	io.Reader
	file *os.File
}

func (s *Stream) Close() error { return s.file.Close() }

func Open(path string) (*Stream, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(path, ".xz") {
		return &Stream{Reader: f, file: f}, nil
	}
	r, err := xz.NewReader(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &Stream{Reader: r, file: f}, nil
}

func Size(path string) (int64, error) {
	s, err := Open(path)
	if err != nil {
		return 0, err
	}
	defer s.Close()
	return io.Copy(io.Discard, s)
}

type Partition struct {
	Name   string
	Offset int64
	Size   int64
}

func Partitions(r io.ReaderAt) ([]Partition, error) {
	hdr := make([]byte, 92)
	if _, err := r.ReadAt(hdr, sector); err != nil {
		return nil, fmt.Errorf("gpt header: %w", err)
	}
	if !bytes.Equal(hdr[:8], []byte("EFI PART")) {
		return nil, errors.New("no GPT in the image")
	}
	entriesLBA := int64(binary.LittleEndian.Uint64(hdr[72:]))
	count := binary.LittleEndian.Uint32(hdr[80:])
	size := binary.LittleEndian.Uint32(hdr[84:])
	if size < 128 || count == 0 || count > 1024 {
		return nil, errors.New("implausible GPT entry table")
	}
	table := make([]byte, int(count)*int(size))
	if _, err := r.ReadAt(table, entriesLBA*sector); err != nil {
		return nil, fmt.Errorf("gpt entries: %w", err)
	}
	var parts []Partition
	for i := 0; i < int(count); i++ {
		e := table[i*int(size) : (i+1)*int(size)]
		if bytes.Equal(e[:16], make([]byte, 16)) {
			continue
		}
		first := int64(binary.LittleEndian.Uint64(e[32:]))
		last := int64(binary.LittleEndian.Uint64(e[40:]))
		if last < first {
			return nil, fmt.Errorf("partition %d ends before it starts", i+1)
		}
		name := make([]uint16, 36)
		for j := range name {
			name[j] = binary.LittleEndian.Uint16(e[56+2*j:])
		}
		n := 0
		for n < len(name) && name[n] != 0 {
			n++
		}
		parts = append(parts, Partition{
			Name:   string(utf16.Decode(name[:n])),
			Offset: first * sector,
			Size:   (last - first + 1) * sector,
		})
	}
	return parts, nil
}

func Find(parts []Partition, source string) (Partition, error) {
	label, ok := Labels[source]
	if !ok {
		return Partition{}, fmt.Errorf("unknown image part %q", source)
	}
	for _, p := range parts {
		if p.Name == label {
			return p, nil
		}
	}
	return Partition{}, fmt.Errorf("the image has no %q partition", label)
}
