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
	trust, args, err := parseTrustHost("data-image", args)
	if err != nil || len(args) != 3 {
		return usageError("data-image [-trust-host <cert>] <id> <release> <out>")
	}
	id, release, out := args[0], args[1], args[2]
	p, err := loadProfile(context.Background(), id)
	if err != nil {
		return err
	}
	if p.Boot == nil {
		return fmt.Errorf("%s boots from an ESP, not from its data partition", id)
	}
	bridge, err := buildDataImage(release, out, trust)
	if err != nil {
		return err
	}
	fmt.Printf("%s %s data image in %s\n", p.Vendor, p.Model, out)
	printBridge(bridge)
	return nil
}

func buildDataImage(release, out, trust string) (string, error) {
	installer, err := imageTools()
	if err != nil {
		return "", err
	}
	work, err := os.MkdirTemp(filepath.Dir(out), ".work-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	data := filepath.Join(work, "data")
	if err := stageRelease(installer, "--data-dir", data, release, work); err != nil {
		return "", err
	}
	bridge := ""
	if trust != "" {
		if bridge, err = seedBridge(data, trust); err != nil {
			return "", err
		}
	}
	return bridge, makeData(out, data, 0)
}
