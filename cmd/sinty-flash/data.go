package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// cmdDataImage builds the data image of a device that boots from its data
// partition and has no ESP, such as an Android phone: the signed release laid
// out by atom-install in an f2fs image as small as its contents.
func cmdDataImage(args []string) error {
	if len(args) != 3 {
		return usageError("data-image <id> <release> <out>")
	}
	id, release, out := args[0], args[1], args[2]
	p, err := loadProfile(context.Background(), id)
	if err != nil {
		return err
	}
	if p.Boot == nil {
		return fmt.Errorf("%s boots from an ESP, not from its data partition", id)
	}
	if err := buildDataImage(release, out); err != nil {
		return err
	}
	fmt.Printf("%s %s data image in %s\n", p.Vendor, p.Model, out)
	return nil
}

func buildDataImage(release, out string) error {
	installer, err := imageTools()
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp(filepath.Dir(out), ".work-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	data := filepath.Join(work, "data")
	if err := stageRelease(installer, "--data-dir", data, release, work); err != nil {
		return err
	}
	return makeData(out, data, 0)
}
