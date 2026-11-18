package stick

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"syscall"

	"github.com/godbus/dbus/v5"
)

const (
	udisks       = "org.freedesktop.UDisks2"
	blockIface   = udisks + ".Block"
	driveIface   = udisks + ".Drive"
	blockPrefix  = "/org/freedesktop/UDisks2/block_devices/"
	chunk        = 4 << 20
	fadvDontNeed = 4
)

type Target struct {
	Device    string
	Model     string
	Serial    string
	Size      int64
	Removable bool
	block     dbus.ObjectPath
	drive     dbus.ObjectPath
}

type Writer struct{ conn *dbus.Conn }

func Connect() (*Writer, error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return nil, fmt.Errorf("system bus: %w", err)
	}
	return &Writer{conn: conn}, nil
}

func (w *Writer) Inspect(device string) (*Target, error) {
	name := path.Base(device)
	if name == "" || strings.ContainsAny(name, "/.") {
		return nil, fmt.Errorf("%q is not a block device", device)
	}
	block := dbus.ObjectPath(blockPrefix + name)
	obj := w.conn.Object(udisks, block)
	drivePath, err := obj.GetProperty(blockIface + ".Drive")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", device, err)
	}
	dp, ok := drivePath.Value().(dbus.ObjectPath)
	if !ok || dp == "/" {
		return nil, fmt.Errorf("%s is not backed by a drive", device)
	}
	if v, err := obj.GetProperty(blockIface + ".PreferredDevice"); err == nil {
		if b, ok := v.Value().([]byte); ok && strings.TrimRight(string(b), "\x00") != "/dev/"+name {
			return nil, fmt.Errorf("%s is a partition or an alias, name the whole disk", device)
		}
	}
	drive := w.conn.Object(udisks, dp)
	t := &Target{Device: "/dev/" + name, block: block, drive: dp}
	if v, err := drive.GetProperty(driveIface + ".Model"); err == nil {
		t.Model, _ = v.Value().(string)
	}
	if v, err := drive.GetProperty(driveIface + ".Serial"); err == nil {
		t.Serial, _ = v.Value().(string)
	}
	if v, err := drive.GetProperty(driveIface + ".Size"); err == nil {
		if n, ok := v.Value().(uint64); ok {
			t.Size = int64(n)
		}
	}
	if v, err := drive.GetProperty(driveIface + ".Removable"); err == nil {
		t.Removable, _ = v.Value().(bool)
	}
	return t, nil
}

func (w *Writer) Write(t *Target, src io.Reader, size int64, progress func(done int64)) error {
	if !t.Removable {
		return fmt.Errorf("%s is not removable, refusing to overwrite it", t.Device)
	}
	if size > t.Size {
		return fmt.Errorf("the image is %d bytes and %s holds %d", size, t.Device, t.Size)
	}
	if err := w.unmount(t); err != nil {
		return err
	}
	var fd dbus.UnixFD
	obj := w.conn.Object(udisks, t.block)
	if err := obj.Call(blockIface+".OpenForRestore", 0, map[string]dbus.Variant{}).Store(&fd); err != nil {
		return fmt.Errorf("open %s: %w", t.Device, err)
	}
	f := os.NewFile(uintptr(fd), t.Device)
	defer f.Close()
	want := sha256.New()
	buf := make([]byte, chunk)
	var done int64
	for {
		n, err := io.ReadFull(src, buf)
		if n > 0 {
			want.Write(buf[:n])
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			done += int64(n)
			if progress != nil {
				progress(done)
			}
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	if done != size {
		return fmt.Errorf("wrote %d bytes, expected %d", done, size)
	}
	if err := f.Sync(); err != nil {
		return err
	}
	var rfd dbus.UnixFD
	if err := obj.Call(blockIface+".OpenForBackup", 0, map[string]dbus.Variant{}).Store(&rfd); err != nil {
		return fmt.Errorf("reopen %s: %w", t.Device, err)
	}
	r := os.NewFile(uintptr(rfd), t.Device)
	defer r.Close()
	if _, _, e := syscall.Syscall6(syscall.SYS_FADVISE64, r.Fd(), 0, 0, fadvDontNeed, 0, 0); e != 0 {
		return fmt.Errorf("drop cached pages of %s: %w", t.Device, e)
	}
	got := sha256.New()
	if _, err := io.CopyBuffer(got, io.LimitReader(r, size), buf); err != nil {
		return fmt.Errorf("read back: %w", err)
	}
	if string(got.Sum(nil)) != string(want.Sum(nil)) {
		return fmt.Errorf("%s does not read back what was written", t.Device)
	}
	return nil
}

func (w *Writer) unmount(t *Target) error {
	var objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	root := w.conn.Object(udisks, "/org/freedesktop/UDisks2")
	if err := root.Call("org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&objects); err != nil {
		return fmt.Errorf("list block devices: %w", err)
	}
	for path, ifaces := range objects {
		blk, ok := ifaces[blockIface]
		if !ok {
			continue
		}
		if d, ok := blk["Drive"].Value().(dbus.ObjectPath); !ok || d != t.drive {
			continue
		}
		fs, ok := ifaces[udisks+".Filesystem"]
		if !ok {
			continue
		}
		if mounts, ok := fs["MountPoints"].Value().([][]byte); !ok || len(mounts) == 0 {
			continue
		}
		if err := w.conn.Object(udisks, path).Call(udisks+".Filesystem.Unmount", 0, map[string]dbus.Variant{}).Err; err != nil {
			return fmt.Errorf("unmount %s: %w", path, err)
		}
	}
	return nil
}
