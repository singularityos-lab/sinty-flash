package main

import (
	"context"
	"fmt"

	"github.com/singularityos-lab/sinty-flash/internal/image"
	"github.com/singularityos-lab/sinty-flash/internal/stick"
)

func cmdStick(args []string) error {
	yes, rest, err := parseYes("stick", args)
	if err != nil || len(rest) != 3 {
		return usageError("stick [-y] <id> <image> <disk>")
	}
	id, imagePath, disk := rest[0], rest[1], rest[2]
	p, err := loadProfile(context.Background(), id)
	if err != nil {
		return err
	}
	if p.Install.Method != "installer-stick" {
		return fmt.Errorf("%s installs with %s, not from a stick: use sinty-flash flash", id, p.Install.Method)
	}
	w, err := stick.Connect()
	if err != nil {
		return err
	}
	t, err := w.Inspect(disk)
	if err != nil {
		return err
	}
	if !t.Removable {
		return fmt.Errorf("%s (%s) is not a removable drive", t.Device, t.Model)
	}
	fmt.Fprintf(stderr(), "reading %s\n", imagePath)
	size, err := image.Size(imagePath)
	if err != nil {
		return err
	}
	if size > t.Size {
		return fmt.Errorf("the image needs %d bytes, %s holds %d", size, t.Device, t.Size)
	}
	fmt.Printf("target: %s, %s, serial %s, %d MB\n", t.Device, t.Model, t.Serial, t.Size/1000000)
	if !yes && !confirm("Everything on "+t.Device+" will be erased.") {
		return errDeclined
	}
	src, err := image.Open(imagePath)
	if err != nil {
		return err
	}
	defer src.Close()
	bar := newProgress("writing", size)
	if err := w.Write(t, src, size, bar.update); err != nil {
		return err
	}
	fmt.Printf("%s is ready for the %s %s, written and read back\n", t.Device, p.Vendor, p.Model)
	for _, n := range p.Notes {
		fmt.Println("  " + n)
	}
	return nil
}
