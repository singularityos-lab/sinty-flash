package usb

import (
	"os"
	"path/filepath"
	"testing"
)

func TestList(t *testing.T) {
	root := t.TempDir()
	write := func(dev string, attrs map[string]string) {
		dir := filepath.Join(root, dev)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for k, v := range attrs {
			if err := os.WriteFile(filepath.Join(dir, k), []byte(v+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	write("1-2", map[string]string{"idVendor": "05C6", "idProduct": "9008", "busnum": "1", "devnum": "7", "product": "QUSB__BULK"})
	write("1-2:1.0", map[string]string{"bInterfaceClass": "ff"})
	write("usb1", map[string]string{"busnum": "1"})
	devs, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 1 {
		t.Fatalf("devs = %+v", devs)
	}
	d := devs[0]
	if d.VID != "05c6" || d.PID != "9008" || d.Node() != "/dev/bus/usb/001/007" {
		t.Fatalf("device = %+v node %s", d, d.Node())
	}
}
