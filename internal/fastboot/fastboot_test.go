package fastboot

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"strconv"
	"strings"
	"testing"
)

type fakeDevice struct {
	maxDownload int64
	replies     [][]byte
	expect      int64
	buf         bytes.Buffer
	staged      []byte
	flashed     map[string][]byte
	refuse      string
}

func (d *fakeDevice) push(s string) { d.replies = append(d.replies, []byte(s)) }

func (d *fakeDevice) Write(p []byte) (int, error) {
	if d.expect > 0 {
		d.buf.Write(p)
		d.expect -= int64(len(p))
		if d.expect < 0 {
			return 0, errors.New("overrun")
		}
		if d.expect == 0 {
			d.staged = append([]byte(nil), d.buf.Bytes()...)
			d.buf.Reset()
			d.push("OKAY")
		}
		return len(p), nil
	}
	cmd := string(p)
	switch {
	case cmd == "getvar:max-download-size":
		d.push(fmt.Sprintf("OKAY0x%x", d.maxDownload))
	case strings.HasPrefix(cmd, "download:"):
		n, _ := strconv.ParseInt(cmd[9:], 16, 64)
		if n > d.maxDownload {
			d.push("FAILdata too large")
			break
		}
		d.expect = n
		d.push(fmt.Sprintf("DATA%08x", n))
	case strings.HasPrefix(cmd, "flash:"):
		part := cmd[6:]
		if part == d.refuse {
			d.push("FAILpartition is locked")
			break
		}
		d.push("INFOwriting " + part)
		d.apply(part)
		d.push("OKAY")
	default:
		d.push("FAILunknown command")
	}
	return len(p), nil
}

func (d *fakeDevice) apply(part string) {
	img := d.staged
	if len(img) < 4 || binary.LittleEndian.Uint32(img) != sparseMagic {
		d.flashed[part] = img
		return
	}
	blocks := binary.LittleEndian.Uint32(img[16:])
	chunks := binary.LittleEndian.Uint32(img[20:])
	out, ok := d.flashed[part]
	if !ok || len(out) != int(blocks)*BlockSize {
		out = make([]byte, int(blocks)*BlockSize)
	}
	pos, at := headerSize, 0
	for i := uint32(0); i < chunks; i++ {
		kind := binary.LittleEndian.Uint16(img[pos:])
		n := int(binary.LittleEndian.Uint32(img[pos+4:])) * BlockSize
		size := int(binary.LittleEndian.Uint32(img[pos+8:]))
		if kind == chunkRaw {
			copy(out[at:], img[pos+chunkHeaderSize:pos+chunkHeaderSize+n])
		}
		at += n
		pos += size
	}
	d.flashed[part] = out
}

func (d *fakeDevice) Read(p []byte) (int, error) {
	if len(d.replies) == 0 {
		return 0, io.EOF
	}
	r := d.replies[0]
	d.replies = d.replies[1:]
	return copy(p, r), nil
}

func (d *fakeDevice) Close() error { return nil }

func payload(n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(int64(n))).Read(b)
	return b
}

func TestFlashSingle(t *testing.T) {
	dev := &fakeDevice{maxDownload: 1 << 20, flashed: map[string][]byte{}}
	c := New(dev)
	var info []string
	c.Info = func(s string) { info = append(info, s) }
	data := payload(300000)
	if err := c.Flash("efi", bytes.NewReader(data), int64(len(data)), nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(dev.flashed["efi"], data) {
		t.Fatal("partition content differs")
	}
	if len(info) != 1 || info[0] != "writing efi" {
		t.Fatalf("info = %q", info)
	}
}

func TestFlashSparse(t *testing.T) {
	dev := &fakeDevice{maxDownload: 64 * 1024, flashed: map[string][]byte{}}
	c := New(dev)
	data := payload(10*BlockSize*7 + 1234)
	var last int64
	if err := c.Flash("rootfs", bytes.NewReader(data), int64(len(data)), func(done, total int64) { last = done }); err != nil {
		t.Fatal(err)
	}
	got := dev.flashed["rootfs"]
	if !bytes.Equal(got[:len(data)], data) || len(got)%BlockSize != 0 {
		t.Fatal("reassembled partition differs")
	}
	if last != int64(len(data)) {
		t.Fatalf("progress ended at %d", last)
	}
}

func TestSplitRespectsLimit(t *testing.T) {
	const max = 3*BlockSize + headerSize + 3*chunkHeaderSize
	pieces, err := Split(10*BlockSize, max)
	if err != nil {
		t.Fatal(err)
	}
	if len(pieces) != 4 {
		t.Fatalf("%d pieces", len(pieces))
	}
	for _, p := range pieces {
		if p.Size() > max {
			t.Fatalf("piece %+v is %d bytes, limit %d", p, p.Size(), max)
		}
		n, err := io.Copy(io.Discard, p.Reader(bytes.NewReader(make([]byte, 10*BlockSize))))
		if err != nil || n != p.Size() {
			t.Fatalf("reader gave %d bytes, Size says %d", n, p.Size())
		}
	}
	if _, err := Split(10, 16); err == nil {
		t.Fatal("tiny limit accepted")
	}
}

func TestFailure(t *testing.T) {
	dev := &fakeDevice{maxDownload: 1 << 20, flashed: map[string][]byte{}, refuse: "xbl"}
	c := New(dev)
	err := c.Flash("xbl", bytes.NewReader([]byte("x")), 1, nil)
	var f *Failure
	if !errors.As(err, &f) || f.Message != "partition is locked" {
		t.Fatalf("err = %v", err)
	}
}
