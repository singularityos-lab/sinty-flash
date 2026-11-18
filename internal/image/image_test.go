package image

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"

	"github.com/ulikunitz/xz"
)

func gptImage(t *testing.T, names ...string) []byte {
	t.Helper()
	img := make([]byte, 64*sector)
	copy(img[sector:], "EFI PART")
	binary.LittleEndian.PutUint64(img[sector+72:], 2)
	binary.LittleEndian.PutUint32(img[sector+80:], 4)
	binary.LittleEndian.PutUint32(img[sector+84:], 128)
	for i, n := range names {
		e := img[2*sector+i*128:]
		e[0] = 1
		binary.LittleEndian.PutUint64(e[32:], uint64(10+i*10))
		binary.LittleEndian.PutUint64(e[40:], uint64(10+i*10+9))
		for j, c := range utf16.Encode([]rune(n)) {
			binary.LittleEndian.PutUint16(e[56+2*j:], c)
		}
	}
	return img
}

func TestPartitions(t *testing.T) {
	img := gptImage(t, "esp", "sing-root", "sing-hash")
	parts, err := Partitions(bytes.NewReader(img))
	if err != nil {
		t.Fatal(err)
	}
	root, err := Find(parts, "root")
	if err != nil || root.Offset != 20*sector || root.Size != 10*sector {
		t.Fatalf("root = %+v err = %v", root, err)
	}
	if _, err := Find(parts, "data"); err == nil {
		t.Fatal("unknown source accepted")
	}
}

func TestNoGPT(t *testing.T) {
	if _, err := Partitions(bytes.NewReader(make([]byte, 4*sector))); err == nil {
		t.Fatal("blank image accepted")
	}
}

func TestOpenXZ(t *testing.T) {
	dir := t.TempDir()
	want := bytes.Repeat([]byte("sinty"), 1000)
	var buf bytes.Buffer
	w, err := xz.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(want)
	w.Close()
	p := filepath.Join(dir, "a.img.xz")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := Size(p)
	if err != nil || n != int64(len(want)) {
		t.Fatalf("size %d err %v", n, err)
	}
}
