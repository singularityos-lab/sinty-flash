package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	devices "github.com/singularityos-lab/sinty-devices"
	"github.com/singularityos-lab/sinty-devices/profile"
)

func pixel(t *testing.T) *profile.Profile {
	ps, err := devices.Profiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.ID == "google/pixel-7-pro" {
			return p
		}
	}
	t.Fatal("no Pixel 7 Pro in the catalog")
	return nil
}

func TestActiveSlotFollowsTheBootPartition(t *testing.T) {
	if s := activeSlot(pixel(t)); s != "a" {
		t.Fatalf("slot %q", s)
	}
}

func TestAndroidPlanNeedsTheSignedKernelImage(t *testing.T) {
	if _, _, err := androidPlan(pixel(t), t.TempDir()); err == nil || !strings.Contains(err.Error(), releaseUKI) {
		t.Fatalf("got %v", err)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, releaseUKI), []byte("MZ"), 0o644)
	if _, _, err := androidPlan(pixel(t), dir); err == nil {
		t.Fatal("a broken kernel image must be refused")
	}
}

func TestAndroidPlanFromARelease(t *testing.T) {
	dir := os.Getenv("SINTY_FLASH_ANDROID_RELEASE")
	if dir == "" {
		t.Skip("SINTY_FLASH_ANDROID_RELEASE names no release directory")
	}
	p := pixel(t)
	plan, done, err := androidPlan(p, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	for i, part := range p.Install.Partitions {
		if plan[i].size == 0 {
			t.Fatalf("%s gets nothing", part.Name)
		}
	}
}
