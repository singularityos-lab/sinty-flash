package edl

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"
)

var testType = guid("EBD0A0A2-B9E5-4433-87C0-68B6B72699C7")

func putEntry(raw []byte, num int, name string, first, last, attrs uint64) {
	e := raw[(num-1)*entrySize : num*entrySize]
	copy(e, testType[:])
	e[16] = byte(num)
	le := binary.LittleEndian
	le.PutUint64(e[32:], first)
	le.PutUint64(e[40:], last)
	le.PutUint64(e[48:], attrs)
	for i, c := range utf16.Encode([]rune(name)) {
		le.PutUint16(e[56+2*i:], c)
	}
}

// boardTables builds the tables of a board package the way its tools ship
// them: the last partition and the disk size left for the programmer to patch.
func boardTables(t *testing.T) (main, backup []byte) {
	t.Helper()
	main = make([]byte, MainSectors*Sector)
	backup = make([]byte, BackupSectors*Sector)
	entries := main[2*Sector:]
	putEntry(entries, 1, "xbl_a", 34, 99, 1<<60)
	putEntry(entries, 2, "boot_a", 100, 199, 0x6f<<48)
	putEntry(entries, 3, "efi", 200, 299, 0)
	putEntry(entries, 4, "rootfs", 300, 499, 0)
	putEntry(entries, 5, "userdata", 500, 600, 0)
	copy(backup, entries)
	for _, h := range [][]byte{main[Sector:], backup[entrySectors*Sector:]} {
		copy(h, "EFI PART")
		le := binary.LittleEndian
		le.PutUint32(h[8:], 0x10000)
		le.PutUint32(h[12:], headerSize)
		le.PutUint64(h[24:], 1)
		le.PutUint64(h[40:], 34)
		le.PutUint64(h[72:], 2)
		le.PutUint32(h[80:], 72)
		le.PutUint32(h[84:], entrySize)
	}
	return main, backup
}

func TestMergeKeepsEveryOtherEntryByteForByte(t *testing.T) {
	main, backup := boardTables(t)
	tb, err := ParseTable(main, backup)
	if err != nil {
		t.Fatal(err)
	}
	e, err := tb.Merge([]string{"userdata", "rootfs"}, DataType, "atom-data")
	if err != nil {
		t.Fatal(err)
	}
	if e.Num != 4 || e.First != 300 || e.Last != 600 {
		t.Fatalf("merged %+v", e)
	}
	got := tb.Main()[2*Sector:]
	if !bytes.Equal(got[:3*entrySize], main[2*Sector:2*Sector+3*entrySize]) {
		t.Fatal("the entries before the data partition changed")
	}
	if !bytes.Equal(got[4*entrySize:], make([]byte, len(got)-4*entrySize)) {
		t.Fatal("the replaced userdata entry is still listed")
	}
	names := []string{}
	for _, x := range tb.Entries() {
		names = append(names, x.Name)
	}
	if strings.Join(names, " ") != "xbl_a boot_a efi atom-data" {
		t.Fatalf("entries %v", names)
	}
	if xbl, _ := tb.Find("xbl_a"); xbl.Attrs != 1<<60 {
		t.Fatalf("xbl attributes %#x", xbl.Attrs)
	}
	if _, err := ParseTable(tb.Main(), tb.Backup()); err != nil {
		t.Fatal(err)
	}
}

func TestMergeRefusesAGap(t *testing.T) {
	main, backup := boardTables(t)
	tb, _ := ParseTable(main, backup)
	if _, err := tb.Merge([]string{"efi", "userdata"}, DataType, "atom-data"); err == nil || !strings.Contains(err.Error(), "contiguous") {
		t.Fatalf("got %v", err)
	}
}

// applyPatches does what the programmer does with the DISK patches of a
// package on a disk of the given number of sectors.
func applyPatches(t *testing.T, disk []byte, patchXML []byte) {
	t.Helper()
	var d struct {
		Patches []xmlPatch `xml:"patch"`
	}
	if err := xml.Unmarshal(patchXML, &d); err != nil {
		t.Fatal(err)
	}
	n := int64(len(disk) / Sector)
	eval := func(s string) int64 {
		s = strings.TrimSuffix(s, ".")
		s = strings.ReplaceAll(s, "NUM_DISK_SECTORS", strconv.FormatInt(n, 10))
		if a, b, ok := strings.Cut(s, "-"); ok {
			x, _ := strconv.ParseInt(a, 10, 64)
			y, _ := strconv.ParseInt(strings.TrimSuffix(b, "."), 10, 64)
			return x - y
		}
		x, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatalf("expression %q", s)
		}
		return x
	}
	for _, p := range d.Patches {
		if p.Filename != "DISK" {
			continue
		}
		at := eval(p.Start)*Sector + int64(p.ByteOffset)
		var v uint64
		if inner, ok := strings.CutPrefix(p.Value, "CRC32("); ok {
			a, b, _ := strings.Cut(strings.TrimSuffix(inner, ")"), ",")
			from := eval(a) * Sector
			size, _ := strconv.ParseInt(b, 10, 64)
			v = uint64(crc32.ChecksumIEEE(disk[from : from+size]))
		} else {
			v = uint64(eval(p.Value))
		}
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], v)
		copy(disk[at:at+int64(p.Size)], buf[:p.Size])
	}
}

func TestThePatchedDiskHoldsAValidTable(t *testing.T) {
	main, backup := boardTables(t)
	tb, _ := ParseTable(main, backup)
	data, err := tb.Merge([]string{"rootfs", "userdata"}, DataType, "atom-data")
	if err != nil {
		t.Fatal(err)
	}
	var patch bytes.Buffer
	if err := WritePatches(&patch, data, tb.ArrayBytes()); err != nil {
		t.Fatal(err)
	}
	const sectors = 8192
	disk := make([]byte, sectors*Sector)
	copy(disk, tb.Main())
	copy(disk[(sectors-BackupSectors)*Sector:], tb.Backup())
	applyPatches(t, disk, patch.Bytes())

	le := binary.LittleEndian
	for _, c := range []struct {
		header       int64
		self, other  uint64
		entriesStart int64
	}{{1, 1, sectors - 1, 2}, {sectors - 1, sectors - 1, 1, sectors - 33}} {
		h := disk[c.header*Sector : c.header*Sector+headerSize]
		if le.Uint64(h[24:]) != c.self || le.Uint64(h[32:]) != c.other && c.header == 1 {
			t.Fatalf("header at %d: current %d backup %d", c.header, le.Uint64(h[24:]), le.Uint64(h[32:]))
		}
		if le.Uint64(h[48:]) != sectors-34 || int64(le.Uint64(h[72:])) != c.entriesStart {
			t.Fatalf("header at %d: last usable %d entries at %d", c.header, le.Uint64(h[48:]), le.Uint64(h[72:]))
		}
		entries := disk[c.entriesStart*Sector : c.entriesStart*Sector+int64(tb.ArrayBytes())]
		if le.Uint32(h[88:]) != crc32.ChecksumIEEE(entries) {
			t.Fatalf("header at %d: entries checksum", c.header)
		}
		hc := bytes.Clone(h)
		le.PutUint32(hc[16:], 0)
		if le.Uint32(h[16:]) != crc32.ChecksumIEEE(hc) {
			t.Fatalf("header at %d: header checksum", c.header)
		}
		e := entries[(data.Num-1)*entrySize:]
		if le.Uint64(e[32:]) != 300 || le.Uint64(e[40:]) != sectors-34 {
			t.Fatalf("atom-data spans %d-%d", le.Uint64(e[32:]), le.Uint64(e[40:]))
		}
	}
}

// The board's own package patches its last partition, number 69 on the UNO
// Q; the generated patches must address the same bytes for the same entry.
func TestPatchesAddressTheEntriesTheBoardPackageDoes(t *testing.T) {
	var patch bytes.Buffer
	if err := WritePatches(&patch, Entry{Num: 69, Name: "userdata"}, 72*entrySize); err != nil {
		t.Fatal(err)
	}
	var d struct {
		Patches []xmlPatch `xml:"patch"`
	}
	xml.Unmarshal(patch.Bytes(), &d)
	want := map[string]bool{
		"19/40/8/NUM_DISK_SECTORS-34.":                              true,
		"NUM_DISK_SECTORS-16./40/8/NUM_DISK_SECTORS-34.":            true,
		"1/48/8/NUM_DISK_SECTORS-34.":                               true,
		"NUM_DISK_SECTORS-1./48/8/NUM_DISK_SECTORS-34.":             true,
		"1/32/8/NUM_DISK_SECTORS-1.":                                true,
		"NUM_DISK_SECTORS-1./24/8/NUM_DISK_SECTORS-1.":              true,
		"NUM_DISK_SECTORS-1./72/8/NUM_DISK_SECTORS-33.":             true,
		"1/88/4/CRC32(2,9216)":                                      true,
		"NUM_DISK_SECTORS-1./88/4/CRC32(NUM_DISK_SECTORS-33.,9216)": true,
		"1/16/4/0":                   true,
		"1/16/4/CRC32(1,92)":         true,
		"NUM_DISK_SECTORS-1./16/4/0": true,
		"NUM_DISK_SECTORS-1./16/4/CRC32(NUM_DISK_SECTORS-1.,92)": true,
	}
	for _, p := range d.Patches {
		k := fmt.Sprintf("%s/%d/%d/%s", p.Start, p.ByteOffset, p.Size, p.Value)
		if !want[k] {
			t.Errorf("unexpected patch %s", k)
		}
		delete(want, k)
	}
	for k := range want {
		t.Errorf("missing patch %s", k)
	}
}

func TestSparseSkipsTheHoles(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "img")
	f, _ := os.Create(src)
	f.Truncate(64 * BlockSize)
	f.WriteAt(bytes.Repeat([]byte{1}, 100), 3*BlockSize+10)
	f.WriteAt(bytes.Repeat([]byte{2}, 2*BlockSize), 40*BlockSize)
	f.Close()
	dst := filepath.Join(dir, "img.simg")
	size, err := WriteSparse(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if size != 64*BlockSize {
		t.Fatalf("size %d", size)
	}
	if fi, _ := os.Stat(dst); fi.Size() > 8*BlockSize {
		t.Fatalf("the sparse image is %d bytes for 3 blocks of data", fi.Size())
	}
	back, _ := os.Create(filepath.Join(dir, "back"))
	back.Truncate(size)
	if err := Unsparse(dst, back); err != nil {
		t.Fatal(err)
	}
	back.Close()
	a, _ := os.ReadFile(src)
	b, _ := os.ReadFile(filepath.Join(dir, "back"))
	if !bytes.Equal(a, b) {
		t.Fatal("the image does not unpack to its source")
	}
}

// With the board's real package at hand, the merge must give the layout the
// installer gives the board: atom-data in the rootfs slot over rootfs and
// userdata, every firmware partition as shipped.
func TestTheBoardPackage(t *testing.T) {
	dir := os.Getenv("SINTY_EDL_FIRMWARE")
	if dir == "" {
		t.Skip("SINTY_EDL_FIRMWARE names no board flash package")
	}
	main, err := os.ReadFile(filepath.Join(dir, "gpt_main0.bin"))
	if err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(filepath.Join(dir, "gpt_backup0.bin"))
	if err != nil {
		t.Fatal(err)
	}
	tb, err := ParseTable(main, backup)
	if err != nil {
		t.Fatal(err)
	}
	before := tb.Entries()
	last := tb.LastEntry()
	data, err := tb.Merge([]string{"rootfs", "userdata"}, DataType, "atom-data")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("last %+v, data %+v, %d entries before, %d after", last, data, len(before), len(tb.Entries()))
	if data.Last != last.Last {
		t.Fatalf("atom-data ends at %d, the disk's last partition at %d", data.Last, last.Last)
	}
	kept := 0
	for _, b := range before {
		if b.Name == "rootfs" || b.Name == "userdata" {
			continue
		}
		a, err := tb.Find(b.Name)
		if err != nil || a != b {
			t.Fatalf("%s changed: %+v -> %+v", b.Name, b, a)
		}
		kept++
	}
	if kept != len(before)-2 {
		t.Fatalf("kept %d of %d", kept, len(before))
	}
}

func TestActivateQcomSlotMarksTheSlotBooted(t *testing.T) {
	main, backup := boardTables(t)
	putEntry(main[2*Sector:], 6, "boot_b", 601, 700, 1<<60|0x87<<48)
	copy(backup, main[2*Sector:])
	tb, err := ParseTable(main, backup)
	if err != nil {
		t.Fatal(err)
	}
	if err := tb.ActivateQcomSlot("boot_a"); err != nil {
		t.Fatal(err)
	}
	again, err := ParseTable(tb.Main(), tb.Backup())
	if err != nil {
		t.Fatalf("the table no longer parses: %v", err)
	}
	a, _ := again.Find("boot_a")
	if a.Attrs != 0x7f<<48 {
		t.Fatalf("boot_a attributes %#x, want active, priority 3, 7 retries, successful", a.Attrs)
	}
	b, _ := again.Find("boot_b")
	if b.Attrs != 1<<60 {
		t.Fatalf("boot_b attributes %#x, want only the shipped bits", b.Attrs)
	}
	x, _ := again.Find("xbl_a")
	if x.Attrs != 1<<60 {
		t.Fatalf("xbl_a attributes changed to %#x", x.Attrs)
	}
}

func TestActivateQcomSlotNeedsThePartition(t *testing.T) {
	main, backup := boardTables(t)
	tb, _ := ParseTable(main, backup)
	if err := tb.ActivateQcomSlot("boot_c"); err == nil {
		t.Fatal("a missing partition was accepted")
	}
}
