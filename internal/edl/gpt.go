package edl

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"slices"
	"unicode/utf16"
)

const (
	Sector     = 512
	entrySize  = 128
	headerSize = 92
	// MainSectors and BackupSectors are the sizes of the primary and backup
	// table files a Qualcomm flash package carries.
	MainSectors   = 34
	BackupSectors = 33
	entrySectors  = 32
)

// DataType is the partition type the installer gives the data partition.
var DataType = guid("0FC63DAF-8483-4772-8E79-3D69D8477DE4")

// Entry is one partition of a table, numbered from 1 by its slot.
type Entry struct {
	Num   int
	Name  string
	First uint64
	Last  uint64
	Attrs uint64
}

// Table is a GPT read from the primary and backup files of a flash package.
// Every entry keeps the bytes it was read with, attributes included, unless
// it is replaced.
type Table struct {
	main, backup []byte
	count        int
}

func ParseTable(main, backup []byte) (*Table, error) {
	if len(main) != MainSectors*Sector || len(backup) != BackupSectors*Sector {
		return nil, fmt.Errorf("the tables are %d and %d bytes, want %d and %d", len(main), len(backup), MainSectors*Sector, BackupSectors*Sector)
	}
	t := &Table{main: bytes.Clone(main), backup: bytes.Clone(backup)}
	for _, h := range [][]byte{t.mainHeader(), t.backupHeader()} {
		if string(h[:8]) != "EFI PART" {
			return nil, errors.New("a table has no GPT header")
		}
		n, size := int(binary.LittleEndian.Uint32(h[80:])), binary.LittleEndian.Uint32(h[84:])
		if size != entrySize || n == 0 || n*entrySize > entrySectors*Sector || (t.count != 0 && n != t.count) {
			return nil, fmt.Errorf("the table holds %d entries of %d bytes, want at most %d of %d", n, size, entrySectors*Sector/entrySize, entrySize)
		}
		t.count = n
	}
	if !bytes.Equal(t.mainEntries(), t.backupEntries()) {
		return nil, errors.New("the primary and backup tables list different partitions")
	}
	return t, nil
}

func (t *Table) mainHeader() []byte    { return t.main[Sector : Sector+headerSize] }
func (t *Table) mainEntries() []byte   { return t.main[2*Sector : 2*Sector+t.ArrayBytes()] }
func (t *Table) backupEntries() []byte { return t.backup[:t.ArrayBytes()] }

// ArrayBytes is the size of the partition array the headers checksum.
func (t *Table) ArrayBytes() int { return t.count * entrySize }
func (t *Table) backupHeader() []byte {
	return t.backup[entrySectors*Sector : entrySectors*Sector+headerSize]
}

func (t *Table) Main() []byte   { return bytes.Clone(t.main) }
func (t *Table) Backup() []byte { return bytes.Clone(t.backup) }

func (t *Table) Entries() []Entry {
	var out []Entry
	raw := t.mainEntries()
	for i := 0; i < len(raw)/entrySize; i++ {
		e := raw[i*entrySize : (i+1)*entrySize]
		if allZero(e[:16]) {
			continue
		}
		le := binary.LittleEndian
		out = append(out, Entry{Num: i + 1, Name: entryName(e[56:]), First: le.Uint64(e[32:]), Last: le.Uint64(e[40:]), Attrs: le.Uint64(e[48:])})
	}
	return out
}

func (t *Table) Find(name string) (Entry, error) {
	var found []Entry
	for _, e := range t.Entries() {
		if e.Name == name {
			found = append(found, e)
		}
	}
	switch len(found) {
	case 0:
		return Entry{}, fmt.Errorf("the table has no %q partition", name)
	case 1:
		return found[0], nil
	}
	return Entry{}, fmt.Errorf("the table has %d partitions named %q", len(found), name)
}

// Merge replaces the named partitions, which must be contiguous, with one
// partition of the given type and name in the slot of the first of them, as
// the installer does on the board. Every other entry stays byte for byte.
func (t *Table) Merge(names []string, typ [16]byte, name string) (Entry, error) {
	var gone []Entry
	for _, n := range names {
		e, err := t.Find(n)
		if err != nil {
			return Entry{}, err
		}
		gone = append(gone, e)
	}
	if len(gone) == 0 {
		return Entry{}, errors.New("no partition to replace")
	}
	slices.SortFunc(gone, func(a, b Entry) int { return int(a.First) - int(b.First) })
	for i := 1; i < len(gone); i++ {
		if gone[i-1].Last+1 != gone[i].First {
			return Entry{}, fmt.Errorf("%q and %q are not contiguous", gone[i-1].Name, gone[i].Name)
		}
	}
	merged := Entry{Num: gone[0].Num, Name: name, First: gone[0].First, Last: gone[len(gone)-1].Last}
	for _, e := range t.Entries() {
		if !slices.ContainsFunc(gone, func(g Entry) bool { return g.Num == e.Num }) && e.First <= merged.Last && merged.First <= e.Last {
			return Entry{}, fmt.Errorf("%q overlaps the partitions it replaces", e.Name)
		}
	}
	raw := t.mainEntries()
	for _, g := range gone {
		clear(raw[(g.Num-1)*entrySize : g.Num*entrySize])
	}
	e := raw[(merged.Num-1)*entrySize : merged.Num*entrySize]
	copy(e, typ[:])
	if _, err := rand.Read(e[16:32]); err != nil {
		return Entry{}, err
	}
	e[16+7] = e[16+7]&0x0f | 0x40
	e[16+8] = e[16+8]&0x3f | 0x80
	binary.LittleEndian.PutUint64(e[32:], merged.First)
	binary.LittleEndian.PutUint64(e[40:], merged.Last)
	for i, c := range utf16.Encode([]rune(name)) {
		binary.LittleEndian.PutUint16(e[56+2*i:], c)
	}
	copy(t.backupEntries(), raw)
	t.seal()
	return merged, nil
}

// LastEntry is the entry that ends at the end of the disk, which the flash
// package patches with the real disk size.
func (t *Table) LastEntry() Entry {
	var last Entry
	for _, e := range t.Entries() {
		if e.Last >= last.Last {
			last = e
		}
	}
	return last
}

func (t *Table) seal() {
	for _, h := range []struct{ header, entries []byte }{{t.mainHeader(), t.mainEntries()}, {t.backupHeader(), t.backupEntries()}} {
		binary.LittleEndian.PutUint32(h.header[88:], crc32.ChecksumIEEE(h.entries))
		binary.LittleEndian.PutUint32(h.header[16:], 0)
		binary.LittleEndian.PutUint32(h.header[16:], crc32.ChecksumIEEE(h.header))
	}
}

func entryName(b []byte) string {
	u := make([]uint16, 0, 36)
	for i := 0; i+1 < len(b); i += 2 {
		c := binary.LittleEndian.Uint16(b[i:])
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u))
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

func guid(s string) [16]byte {
	var hex []byte
	for _, c := range []byte(s) {
		if c != '-' {
			hex = append(hex, c)
		}
	}
	var raw [16]byte
	for i := range raw {
		fmt.Sscanf(string(hex[2*i:2*i+2]), "%02x", &raw[i])
	}
	var g [16]byte
	g[0], g[1], g[2], g[3] = raw[3], raw[2], raw[1], raw[0]
	g[4], g[5] = raw[5], raw[4]
	g[6], g[7] = raw[7], raw[6]
	copy(g[8:], raw[8:])
	return g
}
