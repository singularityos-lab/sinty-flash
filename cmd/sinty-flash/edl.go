package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/singularityos-lab/sinty-devices/profile"
	"github.com/singularityos-lab/sinty-flash/internal/edl"
)

const (
	dataLabel        = "atom-data"
	greetdConfig     = "etc/greetd/config.toml"
	officialMain     = "gpt_main0.bin"
	officialBackup   = "gpt_backup0.bin"
	espImage         = "efi.simg"
	dataImage        = "atom-data.simg"
	dataHeadroomMiB  = 512
	dataSlackDivisor = 10
)

// cmdEDLPackage builds the flash package that installs a signed release on a
// board flashed over Qualcomm EDL: the board's own partition table with the
// partitions the profile replaces merged into the data partition, the ESP and
// the data partition as sparse images, and the rawprogram and patch files the
// programmer runs. Every other partition, attributes included, stays as the
// board's firmware package ships it.
func cmdEDLPackage(args []string) error {
	trust, args, err := parseTrustHost("edl-package", args)
	if err != nil || len(args) != 4 {
		return usageError("edl-package [-trust-host <cert>] <id> <release> <firmware> <out>")
	}
	id, release, firmware, out := args[0], args[1], args[2], args[3]
	p, err := loadProfile(context.Background(), id)
	if err != nil {
		return err
	}
	if p.Install.Method != "installer-stick" || p.Install.ESP == "" || len(p.Install.Replace) == 0 {
		return fmt.Errorf("%s does not install by replacing board partitions", id)
	}
	if !hasEDL(p) {
		return fmt.Errorf("%s has no EDL mode", id)
	}
	installer, err := imageTools("mkfs.fat", "mcopy")
	if err != nil {
		return err
	}

	primary, err := os.ReadFile(filepath.Join(firmware, officialMain))
	if err != nil {
		return err
	}
	backup, err := os.ReadFile(filepath.Join(firmware, officialBackup))
	if err != nil {
		return err
	}
	table, err := edl.ParseTable(primary, backup)
	if err != nil {
		return fmt.Errorf("%s: %w", firmware, err)
	}
	esp, err := table.Find(p.Install.ESP)
	if err != nil {
		return err
	}
	last := table.LastEntry()
	data, err := table.Merge(p.Install.Replace, edl.DataType, dataLabel)
	if err != nil {
		return err
	}
	if data.Last != last.Last {
		return fmt.Errorf("the replaced partitions do not end the disk, %q does", last.Name)
	}

	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	work, err := os.MkdirTemp(out, ".work-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	stage := filepath.Join(work, "stage")
	if err := stageRelease(installer, "--stage", stage, release, work); err != nil {
		return err
	}
	bridge := ""
	if trust != "" {
		if bridge, err = seedBridge(filepath.Join(stage, "data"), trust); err != nil {
			return err
		}
	}

	espRaw := filepath.Join(work, "efi.img")
	if err := makeESP(espRaw, filepath.Join(stage, "esp"), int64(esp.Last-esp.First+1)*edl.Sector); err != nil {
		return err
	}
	dataRaw := filepath.Join(work, "data.img")
	if err := makeData(dataRaw, filepath.Join(stage, "data"), int64(data.Last-data.First+1)*edl.Sector); err != nil {
		return err
	}

	var images []edl.Program
	for _, img := range []struct {
		raw, file string
		part      edl.Entry
	}{{espRaw, espImage, esp}, {dataRaw, dataImage, data}} {
		size, err := edl.WriteSparse(img.raw, filepath.Join(out, img.file))
		if err != nil {
			return err
		}
		if err := checkSparse(filepath.Join(out, img.file), img.raw, size); err != nil {
			return err
		}
		images = append(images, edl.Program{Label: img.part.Name, File: img.file, Start: fmt.Sprint(img.part.First), Sectors: size / edl.Sector, Sparse: true})
	}
	if err := os.WriteFile(filepath.Join(out, officialMain), table.Main(), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, officialBackup), table.Backup(), 0o644); err != nil {
		return err
	}
	var raw, patch bytes.Buffer
	if err := edl.WriteRawProgram(&raw, edl.Programs(images)); err != nil {
		return err
	}
	if err := edl.WritePatches(&patch, data, table.ArrayBytes()); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "rawprogram0.xml"), raw.Bytes(), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "patch0.xml"), patch.Bytes(), 0o644); err != nil {
		return err
	}
	if err := writeSums(out, []string{officialMain, officialBackup, espImage, dataImage, "rawprogram0.xml", "patch0.xml"}); err != nil {
		return err
	}
	fmt.Printf("%s %s flash package in %s: %s replaced by %s, every other partition as the firmware ships it\n",
		p.Vendor, p.Model, out, strings.Join(p.Install.Replace, " and "), dataLabel)
	printBridge(bridge)
	return nil
}

func hasEDL(p *profile.Profile) bool {
	for _, u := range p.Match.USB {
		if u.Mode == "edl" {
			return true
		}
	}
	return false
}

func runTool(name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stdout, c.Stderr = os.Stderr, os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(name), err)
	}
	return nil
}

// makeESP formats an image the size of the board's ESP and copies the staged
// files into it.
func makeESP(img, dir string, size int64) error {
	if err := truncated(img, size); err != nil {
		return err
	}
	if err := runTool("mkfs.fat", "-F", "32", "-n", "SINGEFI", img); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	args := []string{"-s", "-p", "-i", img}
	for _, e := range entries {
		args = append(args, filepath.Join(dir, e.Name()))
	}
	return runTool("mcopy", append(args, "::/")...)
}

// imageTools checks for the programs that build the images of an install and
// finds atom-install, which stages them.
func imageTools(extra ...string) (string, error) {
	for _, t := range append([]string{"mkfs.f2fs", "sload.f2fs", "fsck.f2fs", "dump.erofs"}, extra...) {
		if _, err := exec.LookPath(t); err != nil {
			return "", fmt.Errorf("%s is needed to build the images", t)
		}
	}
	if installer := os.Getenv("SINTY_ATOM_INSTALL"); installer != "" {
		return installer, nil
	}
	installer, err := exec.LookPath("atom-install")
	if err != nil {
		return "", fmt.Errorf("set SINTY_ATOM_INSTALL to an atom-install binary")
	}
	return installer, nil
}

// stageRelease lays out an install of the release in dir with atom-install,
// in the given mode, taking the login settings from the release's own root.
func stageRelease(installer, mode, dir, release, work string) error {
	sysroot := filepath.Join(work, "sysroot")
	if err := os.MkdirAll(filepath.Join(sysroot, filepath.Dir(greetdConfig)), 0o755); err != nil {
		return err
	}
	greetd, err := exec.Command("dump.erofs", "--cat", "--path=/"+greetdConfig, filepath.Join(release, "rootfs.erofs")).Output()
	if err != nil || len(greetd) == 0 {
		return fmt.Errorf("the release root has no %s: %v", greetdConfig, err)
	}
	if err := os.WriteFile(filepath.Join(sysroot, greetdConfig), greetd, 0o644); err != nil {
		return err
	}
	return runTool(installer, mode, dir, "--from", release, "--sysroot", sysroot)
}

// makeData packs the staged data partition into an f2fs image as small as its
// contents, within limit when limit is set. The init grows it to the whole
// partition on the first boot.
func makeData(img, dir string, limit int64) error {
	used, err := treeSize(dir)
	if err != nil {
		return err
	}
	mib := used>>20 + used>>20/dataSlackDivisor + dataHeadroomMiB
	if limit > 0 && mib<<20 > limit {
		return fmt.Errorf("the data partition holds %d MiB, the system needs %d MiB", limit>>20, mib)
	}
	if err := truncated(img, mib<<20); err != nil {
		return err
	}
	if err := runTool("mkfs.f2fs", "-q", "-f", "-l", dataLabel, "-O", "encrypt,extra_attr", img); err != nil {
		return err
	}
	if err := runTool("sload.f2fs", "-f", dir, "-t", "/", img); err != nil {
		return err
	}
	return runTool("fsck.f2fs", "-f", img)
}

func truncated(path string, size int64) error {
	os.Remove(path)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func treeSize(dir string) (int64, error) {
	var n int64
	err := filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			fi, err := d.Info()
			if err != nil {
				return err
			}
			n += fi.Size()
		}
		return nil
	})
	return n, err
}

// checkSparse unpacks the image over a copy of nothing and compares it with
// the file it was made from: the holes the image skips must be holes there.
func checkSparse(sparse, raw string, size int64) error {
	back := raw + ".check"
	if err := truncated(back, size); err != nil {
		return err
	}
	defer os.Remove(back)
	f, err := os.OpenFile(back, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := edl.Unsparse(sparse, f); err != nil {
		f.Close()
		return err
	}
	f.Close()
	a, err := fileSum(raw)
	if err != nil {
		return err
	}
	b, err := fileSum(back)
	if err != nil {
		return err
	}
	if a != b {
		return fmt.Errorf("%s does not unpack to the image it was made from", filepath.Base(sparse))
	}
	return nil
}

func fileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeSums(dir string, files []string) error {
	var b strings.Builder
	for _, n := range files {
		s, err := fileSum(filepath.Join(dir, n))
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s  %s\n", s, n)
	}
	return os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(b.String()), 0o644)
}
