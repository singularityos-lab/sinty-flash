package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/singularityos-lab/sinty-devices/profile"
	"github.com/singularityos-lab/sinty-flash/internal/fastboot"
	"github.com/singularityos-lab/sinty-flash/internal/image"
	"github.com/singularityos-lab/sinty-flash/internal/usb"
)

func stderr() io.Writer { return os.Stderr }

func cmdFlash(args []string) error {
	yes, rest, err := parseYes("flash", args)
	if err != nil || len(rest) != 2 {
		return usageError("flash [-y] <id> <image>")
	}
	id, imagePath := rest[0], rest[1]
	p, err := loadProfile(context.Background(), id)
	if err != nil {
		return err
	}
	if p.Install.Method != "fastboot" {
		return fmt.Errorf("%s installs with %s: use sinty-flash stick", id, p.Install.Method)
	}
	if p.Boot != nil {
		return flashAndroid(p, imagePath, yes)
	}
	dev, err := findFastboot(p)
	if err != nil {
		return err
	}
	img, cleanup, err := randomAccess(imagePath)
	if err != nil {
		return err
	}
	defer cleanup()
	parts, err := image.Partitions(img)
	if err != nil {
		return err
	}
	plan := make([]image.Partition, len(p.Install.Partitions))
	for i, part := range p.Install.Partitions {
		if part.Source == "data" {
			continue
		}
		src, err := image.Find(parts, part.Source)
		if err != nil {
			return err
		}
		plan[i] = src
	}
	fmt.Printf("%s %s in fastboot mode on %s\n", p.Vendor, p.Model, dev.Node())
	for _, part := range p.Install.Partitions {
		if part.Source == "data" {
			fmt.Printf("  erase  %s\n", part.Name)
		} else {
			fmt.Printf("  write  %s from the image %s\n", part.Name, part.Source)
		}
	}
	if !yes && !confirm("These partitions will be overwritten, every other one stays as shipped.") {
		return errDeclined
	}
	t, err := fastboot.Open(filepath.Join(usb.SysRoot, dev.Path), dev.Node())
	if err != nil {
		return err
	}
	c := fastboot.New(t)
	defer c.Close()
	c.Info = func(s string) { fmt.Fprintln(os.Stderr, "device:", s) }
	if err := unlock(c, p, yes); err != nil {
		return err
	}
	for i, part := range p.Install.Partitions {
		if part.Source == "data" {
			if _, err := c.Command("erase:" + part.Name); err != nil {
				return fmt.Errorf("erase %s: %w", part.Name, err)
			}
			continue
		}
		src := plan[i]
		bar := newProgress("writing "+part.Name, src.Size)
		if err := c.Flash(part.Name, io.NewSectionReader(img, src.Offset, src.Size), src.Size, func(done, _ int64) { bar.update(done) }); err != nil {
			return fmt.Errorf("flash %s: %w", part.Name, err)
		}
	}
	if _, err := c.Command("reboot"); err != nil {
		return err
	}
	fmt.Printf("%s %s flashed, rebooting into Sinty\n", p.Vendor, p.Model)
	return nil
}

func findFastboot(p *profile.Profile) (usb.Device, error) {
	devs, err := usb.List(usb.SysRoot)
	if err != nil {
		return usb.Device{}, err
	}
	var hits []usb.Device
	for _, d := range devs {
		if mode, ok := p.MatchesUSB(d.VID, d.PID); ok && mode == "fastboot" {
			hits = append(hits, d)
		}
	}
	switch len(hits) {
	case 0:
		return usb.Device{}, fmt.Errorf("no %s %s in fastboot mode is connected", p.Vendor, p.Model)
	case 1:
		return hits[0], nil
	default:
		return usb.Device{}, fmt.Errorf("%d %s %s devices are in fastboot mode, connect only one", len(hits), p.Vendor, p.Model)
	}
}

func unlock(c *fastboot.Client, p *profile.Profile, yes bool) error {
	if p.Install.Unlock == "" {
		return nil
	}
	state, err := c.GetVar("unlocked")
	if err != nil {
		var f *fastboot.Failure
		if !errors.As(err, &f) {
			return err
		}
	}
	if strings.EqualFold(state, "yes") {
		return nil
	}
	if !yes && !confirm("The bootloader is locked. Unlocking it erases the device and must be confirmed on its screen.") {
		return errDeclined
	}
	if _, err := c.Command(p.Install.Unlock); err != nil {
		return fmt.Errorf("unlock: %w", err)
	}
	return nil
}

func randomAccess(path string) (*os.File, func(), error) {
	if !strings.HasSuffix(path, ".xz") {
		f, err := os.Open(path)
		if err != nil {
			return nil, nil, err
		}
		return f, func() { f.Close() }, nil
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil, nil, err
	}
	dir = filepath.Join(dir, "sinty-flash")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, err
	}
	tmp, err := os.CreateTemp(dir, "image-*.img")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}
	src, err := image.Open(path)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	defer src.Close()
	fmt.Fprintf(os.Stderr, "unpacking %s\n", path)
	if _, err := io.Copy(tmp, src); err != nil {
		cleanup()
		return nil, nil, err
	}
	return tmp, cleanup, nil
}
