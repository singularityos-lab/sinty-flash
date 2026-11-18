package fastboot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

const (
	usbdevfsBulk           = 0xc0185502
	usbdevfsClaimInterface = 0x8004550f
	usbdevfsReleaseIntf    = 0x80045510
	timeoutMillis          = 30000
)

type usbfs struct {
	f     *os.File
	iface uint32
	in    uint32
	out   uint32
}

type bulkTransfer struct {
	ep      uint32
	len     uint32
	timeout uint32
	_       uint32
	data    unsafe.Pointer
}

func Open(sysDir, node string) (Transport, error) {
	iface, in, out, err := findInterface(sysDir)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(node, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	n := iface
	if err := ioctl(f, usbdevfsClaimInterface, uintptr(unsafe.Pointer(&n))); err != nil {
		f.Close()
		return nil, fmt.Errorf("claim fastboot interface: %w", err)
	}
	return &usbfs{f: f, iface: iface, in: in, out: out}, nil
}

func findInterface(sysDir string) (iface, in, out uint32, err error) {
	matches, _ := filepath.Glob(filepath.Join(sysDir, filepath.Base(sysDir)+":*"))
	for _, dir := range matches {
		if attr(dir, "bInterfaceClass") != "ff" || attr(dir, "bInterfaceSubClass") != "42" || attr(dir, "bInterfaceProtocol") != "03" {
			continue
		}
		num, perr := strconv.ParseUint(attr(dir, "bInterfaceNumber"), 16, 32)
		if perr != nil {
			continue
		}
		eps, _ := filepath.Glob(filepath.Join(dir, "ep_*"))
		in, out = 0, 0
		for _, ep := range eps {
			if attr(ep, "type") != "Bulk" {
				continue
			}
			addr, aerr := strconv.ParseUint(attr(ep, "bEndpointAddress"), 16, 32)
			if aerr != nil {
				continue
			}
			if addr&0x80 != 0 {
				in = uint32(addr)
			} else {
				out = uint32(addr)
			}
		}
		if in != 0 && out != 0 {
			return uint32(num), in, out, nil
		}
	}
	return 0, 0, 0, errors.New("no fastboot interface on this device")
}

func attr(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func ioctl(f *os.File, req, arg uintptr) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), req, arg)
	if e != 0 {
		return e
	}
	return nil
}

func (u *usbfs) bulk(ep uint32, p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	t := bulkTransfer{ep: ep, len: uint32(len(p)), timeout: timeoutMillis, data: unsafe.Pointer(&p[0])}
	n, _, e := syscall.Syscall(syscall.SYS_IOCTL, u.f.Fd(), usbdevfsBulk, uintptr(unsafe.Pointer(&t)))
	runtime.KeepAlive(p)
	if e != 0 {
		return 0, e
	}
	return int(n), nil
}

func (u *usbfs) Read(p []byte) (int, error) { return u.bulk(u.in, p) }

func (u *usbfs) Write(p []byte) (int, error) {
	done := 0
	for done < len(p) {
		end := done + chunk
		if end > len(p) {
			end = len(p)
		}
		n, err := u.bulk(u.out, p[done:end])
		if err != nil {
			return done, err
		}
		done += n
	}
	return done, nil
}

func (u *usbfs) Close() error {
	n := u.iface
	_ = ioctl(u.f, usbdevfsReleaseIntf, uintptr(unsafe.Pointer(&n)))
	return u.f.Close()
}
