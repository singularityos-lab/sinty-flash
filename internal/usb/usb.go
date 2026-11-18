package usb

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const SysRoot = "/sys/bus/usb/devices"

type Device struct {
	Path         string
	Bus          string
	Number       string
	VID          string
	PID          string
	Manufacturer string
	Product      string
	Serial       string
}

func (d Device) Node() string {
	return filepath.Join("/dev/bus/usb", d.Bus, d.Number)
}

func List(root string) ([]Device, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var devs []Device
	for _, e := range entries {
		if strings.Contains(e.Name(), ":") {
			continue
		}
		dir := filepath.Join(root, e.Name())
		d := Device{
			Path:         e.Name(),
			VID:          attr(dir, "idVendor"),
			PID:          attr(dir, "idProduct"),
			Bus:          pad3(attr(dir, "busnum")),
			Number:       pad3(attr(dir, "devnum")),
			Manufacturer: attr(dir, "manufacturer"),
			Product:      attr(dir, "product"),
			Serial:       attr(dir, "serial"),
		}
		if d.VID == "" || d.PID == "" {
			continue
		}
		devs = append(devs, d)
	}
	sort.Slice(devs, func(i, j int) bool { return devs[i].Path < devs[j].Path })
	return devs, nil
}

func attr(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(string(b)))
}

func pad3(s string) string {
	for len(s) > 0 && len(s) < 3 {
		s = "0" + s
	}
	return s
}
