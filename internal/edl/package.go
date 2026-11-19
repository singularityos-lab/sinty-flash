package edl

import (
	"encoding/xml"
	"fmt"
	"io"
)

// Program is one image the flash package writes, at a sector of the disk or
// at an expression of its size the programmer resolves.
type Program struct {
	Label    string
	File     string
	Start    string
	Sectors  int64
	Sparse   bool
	startHex string
}

type xmlProgram struct {
	SectorSize   int    `xml:"SECTOR_SIZE_IN_BYTES,attr"`
	FileOffset   int    `xml:"file_sector_offset,attr"`
	Filename     string `xml:"filename,attr"`
	Label        string `xml:"label,attr"`
	Sectors      int64  `xml:"num_partition_sectors,attr"`
	SingleImage  string `xml:"partofsingleimage,attr"`
	Physical     int    `xml:"physical_partition_number,attr"`
	ReadBack     string `xml:"readbackverify,attr"`
	SizeKB       string `xml:"size_in_KB,attr"`
	Sparse       string `xml:"sparse,attr"`
	StartByteHex string `xml:"start_byte_hex,attr"`
	StartSector  string `xml:"start_sector,attr"`
}

type xmlPatch struct {
	SectorSize int    `xml:"SECTOR_SIZE_IN_BYTES,attr"`
	ByteOffset int    `xml:"byte_offset,attr"`
	Filename   string `xml:"filename,attr"`
	Physical   int    `xml:"physical_partition_number,attr"`
	Size       int    `xml:"size_in_bytes,attr"`
	Start      string `xml:"start_sector,attr"`
	Value      string `xml:"value,attr"`
	What       string `xml:"what,attr"`
}

// Programs lists the writes of a flash package: the primary table, the given
// images, and the backup table at the end of the disk.
func Programs(images []Program) []Program {
	out := []Program{{Label: "PrimaryGPT", File: "gpt_main0.bin", Start: "0", Sectors: MainSectors, startHex: "0x0"}}
	out = append(out, images...)
	return append(out, Program{
		Label: "BackupGPT", File: "gpt_backup0.bin", Start: "NUM_DISK_SECTORS-33.", Sectors: BackupSectors,
		startHex: fmt.Sprintf("(%d*NUM_DISK_SECTORS)-%d.", Sector, BackupSectors*Sector),
	})
}

func WriteRawProgram(w io.Writer, programs []Program) error {
	type data struct {
		XMLName  xml.Name     `xml:"data"`
		Programs []xmlProgram `xml:"program"`
	}
	var d data
	for _, p := range programs {
		hex := p.startHex
		if hex == "" {
			var s int64
			if _, err := fmt.Sscan(p.Start, &s); err != nil {
				return fmt.Errorf("%s starts at %q: %w", p.Label, p.Start, err)
			}
			hex = fmt.Sprintf("%#x", s*Sector)
		}
		d.Programs = append(d.Programs, xmlProgram{
			SectorSize: Sector, Filename: p.File, Label: p.Label, Sectors: p.Sectors,
			SingleImage: fmt.Sprint(p.Label == "PrimaryGPT" || p.Label == "BackupGPT"), ReadBack: "false",
			SizeKB: fmt.Sprintf("%.1f", float64(p.Sectors*Sector)/1024), Sparse: fmt.Sprint(p.Sparse),
			StartByteHex: hex, StartSector: p.Start,
		})
	}
	return writeXML(w, d)
}

// WritePatches writes the patches the programmer applies once the tables are
// on the disk: the last partition grows to the end of this disk, and the
// headers get the disk's real size and their checksums, as the board's own
// flash package does.
func WritePatches(w io.Writer, last Entry, arrayBytes int) error {
	type patches struct {
		XMLName xml.Name   `xml:"patches"`
		Patches []xmlPatch `xml:"patch"`
	}
	entryOff := (last.Num - 1) * entrySize
	p := func(start string, off, size int, value, what string) xmlPatch {
		return xmlPatch{SectorSize: Sector, ByteOffset: off, Filename: "DISK", Size: size, Start: start, Value: value, What: what}
	}
	tableCRC := fmt.Sprintf("CRC32(%%s,%d)", arrayBytes)
	d := patches{Patches: []xmlPatch{
		p(fmt.Sprint(2+entryOff/Sector), entryOff%Sector+40, 8, "NUM_DISK_SECTORS-34.", fmt.Sprintf("Grow partition %d '%s' to the end of the disk in the primary table.", last.Num, last.Name)),
		p("NUM_DISK_SECTORS-"+fmt.Sprint(BackupSectors-entryOff/Sector)+".", entryOff%Sector+40, 8, "NUM_DISK_SECTORS-34.", fmt.Sprintf("Grow partition %d '%s' to the end of the disk in the backup table.", last.Num, last.Name)),
		p("1", 48, 8, "NUM_DISK_SECTORS-34.", "Last usable sector in the primary header."),
		p("NUM_DISK_SECTORS-1.", 48, 8, "NUM_DISK_SECTORS-34.", "Last usable sector in the backup header."),
		p("1", 32, 8, "NUM_DISK_SECTORS-1.", "Backup header location in the primary header."),
		p("NUM_DISK_SECTORS-1.", 24, 8, "NUM_DISK_SECTORS-1.", "Own location in the backup header."),
		p("NUM_DISK_SECTORS-1.", 72, 8, "NUM_DISK_SECTORS-33.", "Partition array location in the backup header."),
		p("1", 88, 4, fmt.Sprintf(tableCRC, "2"), "Partition array checksum in the primary header."),
		p("NUM_DISK_SECTORS-1.", 88, 4, fmt.Sprintf(tableCRC, "NUM_DISK_SECTORS-33."), "Partition array checksum in the backup header."),
		p("1", 16, 4, "0", "Clear the primary header checksum."),
		p("1", 16, 4, fmt.Sprintf("CRC32(1,%d)", headerSize), "Primary header checksum."),
		p("NUM_DISK_SECTORS-1.", 16, 4, "0", "Clear the backup header checksum."),
		p("NUM_DISK_SECTORS-1.", 16, 4, fmt.Sprintf("CRC32(NUM_DISK_SECTORS-1.,%d)", headerSize), "Backup header checksum."),
	}}
	return writeXML(w, d)
}

func writeXML(w io.Writer, v any) error {
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}
