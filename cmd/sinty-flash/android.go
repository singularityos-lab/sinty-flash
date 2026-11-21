package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/singularityos-lab/sinty-bootimg/bootimg"
	"github.com/singularityos-lab/sinty-devices/profile"
	"github.com/singularityos-lab/sinty-flash/internal/fastboot"
	"github.com/singularityos-lab/sinty-flash/internal/usb"
)

// A release directory for a device with a boot section holds the signed
// unified kernel image and the device's data image.
const (
	releaseUKI  = "kernelcache.efi"
	releaseData = "userdata.img"
)

type flashSource struct {
	name string
	r    io.ReaderAt
	size int64
}

func androidPlan(p *profile.Profile, dir string) ([]flashSource, func(), error) {
	uki, err := os.ReadFile(filepath.Join(dir, releaseUKI))
	if err != nil {
		return nil, nil, fmt.Errorf("the release has no %s: %w", releaseUKI, err)
	}
	if len(p.Match.Compatible) == 0 {
		return nil, nil, fmt.Errorf("%s names no device tree compatible", p.ID)
	}
	in := bootimg.Inputs{}
	if in.Kernel, in.Initramfs, in.DTB, err = bootimg.FromUKI(uki, p.Match.Compatible[0]); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", releaseUKI, err)
	}
	if p.Boot.AVB != nil {
		// The bootloader is unlocked, so it checks the footers' integrity but
		// trusts no particular key: a key made for this flash is enough.
		if in.Key, err = rsa.GenerateKey(rand.Reader, 4096); err != nil {
			return nil, nil, err
		}
	}
	imgs, err := bootimg.Android(p.Boot, in)
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() {}
	plan := make([]flashSource, len(p.Install.Partitions))
	for i, part := range p.Install.Partitions {
		switch {
		case slices.Contains(profile.BootImages, part.Source):
			img, ok := imgs[part.Source]
			if !ok {
				return nil, nil, fmt.Errorf("the boot section builds no %s image", part.Source)
			}
			plan[i] = flashSource{name: part.Source + " image", r: bytes.NewReader(img), size: int64(len(img))}
		case part.Source == "data":
			path, built, err := releaseDataImage(dir)
			if err != nil {
				return nil, nil, err
			}
			f, done, err := randomAccess(path)
			if err != nil {
				if built != "" {
					os.RemoveAll(built)
				}
				return nil, nil, fmt.Errorf("the release has no data image: %w", err)
			}
			cleanup = func() {
				done()
				if built != "" {
					os.RemoveAll(built)
				}
			}
			fi, err := f.Stat()
			if err != nil {
				done()
				return nil, nil, err
			}
			plan[i] = flashSource{name: "data image", r: f, size: fi.Size()}
		default:
			return nil, nil, fmt.Errorf("%s takes %s, which a release for this device does not carry", part.Name, part.Source)
		}
	}
	return plan, cleanup, nil
}

func flashAndroid(p *profile.Profile, dir string, yes bool) error {
	dev, err := findFastboot(p)
	if err != nil {
		return err
	}
	plan, cleanup, err := androidPlan(p, dir)
	if err != nil {
		return err
	}
	defer cleanup()
	fmt.Printf("%s %s in fastboot mode on %s\n", p.Vendor, p.Model, dev.Node())
	for i, part := range p.Install.Partitions {
		fmt.Printf("  write  %s from the %s\n", part.Name, plan[i].name)
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
		src := plan[i]
		bar := newProgress("writing "+part.Name, src.size)
		if err := c.Flash(part.Name, src.r, src.size, func(done, _ int64) { bar.update(done) }); err != nil {
			return fmt.Errorf("flash %s: %w", part.Name, err)
		}
	}
	if slot := activeSlot(p); slot != "" {
		if _, err := c.Command("set_active:" + slot); err != nil {
			return fmt.Errorf("set_active %s: %w", slot, err)
		}
	}
	if _, err := c.Command("reboot"); err != nil {
		return err
	}
	fmt.Printf("%s %s flashed, rebooting into Sinty\n", p.Vendor, p.Model)
	return nil
}

// activeSlot is the A/B slot the boot partitions were written to.
func activeSlot(p *profile.Profile) string {
	for _, part := range p.Install.Partitions {
		if part.Source == "boot" {
			if n := len(part.Name); n > 2 && part.Name[n-2] == '_' {
				return part.Name[n-1:]
			}
		}
	}
	return ""
}

// releaseDataImage finds the data image of a release, or builds it from the
// signed release itself when the directory carries none, in the user's cache
// rather than a temporary directory that may live in memory. built is the
// directory to remove once the image has been written.
func releaseDataImage(dir string) (path, built string, err error) {
	for _, name := range []string{releaseData, releaseData + ".xz"} {
		path = filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			return path, "", nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", "", err
		}
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(filepath.Join(cache, "sinty-flash"), 0o755); err != nil {
		return "", "", err
	}
	built, err = os.MkdirTemp(filepath.Join(cache, "sinty-flash"), "data-")
	if err != nil {
		return "", "", err
	}
	path = filepath.Join(built, releaseData)
	if _, err := buildDataImage(dir, path, ""); err != nil {
		os.RemoveAll(built)
		return "", "", err
	}
	return path, built, nil
}
