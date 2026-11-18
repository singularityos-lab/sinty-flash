package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/singularityos-lab/sinty-devices/profile"
	"github.com/singularityos-lab/sinty-flash/internal/catalog"
	"github.com/singularityos-lab/sinty-flash/internal/usb"
)

var version = "dev"

func main() { os.Exit(run(os.Args[1:])) }

const usage = `sinty-flash puts Sinty OS on a device the way its profile says.

usage:
  sinty-flash devices                        list the devices the catalog knows
  sinty-flash show <id>                      print a device profile
  sinty-flash detect                         find connected devices the catalog knows
  sinty-flash stick [-y] <id> <image> <disk> write the installer stick for a device
  sinty-flash flash [-y] <id> <image>        flash a device in fastboot mode
  sinty-flash version                        print the version

The catalog is read from SINTY_FLASH_CATALOG, a URL or a local checkout of
sinty-devices, and defaults to the public catalog.
`

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "devices":
		err = cmdDevices(args[1:])
	case "show":
		err = cmdShow(args[1:])
	case "detect":
		err = cmdDetect(args[1:])
	case "stick":
		err = cmdStick(args[1:])
	case "flash":
		err = cmdFlash(args[1:])
	case "version":
		fmt.Println(version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	if err != nil {
		var usageErr usageError
		if errors.As(err, &usageErr) {
			fmt.Fprintln(os.Stderr, "usage: sinty-flash", string(usageErr))
			return 2
		}
		fmt.Fprintln(os.Stderr, "sinty-flash:", err)
		return 1
	}
	return 0
}

type usageError string

func (u usageError) Error() string { return string(u) }

func openCatalog() *catalog.Catalog {
	source := os.Getenv("SINTY_FLASH_CATALOG")
	if source == "" {
		source = catalog.DefaultSource
	}
	cache := ""
	if dir, err := os.UserCacheDir(); err == nil {
		cache = filepath.Join(dir, "sinty-flash", "catalog")
	}
	return catalog.New(source, cache)
}

func toolVersion() (profile.Version, bool) {
	v, err := profile.ParseVersion(version)
	if err != nil {
		return profile.Version{}, true
	}
	return v, false
}

func loadProfile(ctx context.Context, id string) (*profile.Profile, error) {
	c := openCatalog()
	ix, err := c.Index(ctx)
	if err != nil {
		return nil, err
	}
	have, dev := toolVersion()
	return c.Profile(ctx, ix, id, have, dev)
}

func cmdDevices(args []string) error {
	if len(args) != 0 {
		return usageError("devices")
	}
	ix, err := openCatalog().Index(context.Background())
	if err != nil {
		return err
	}
	have, dev := toolVersion()
	for _, e := range ix.Devices {
		note := ""
		if need, err := profile.ParseVersion(e.MinFlashVersion); err == nil && !dev && have.Less(need) {
			note = "  (needs sinty-flash " + need.String() + ")"
		}
		fmt.Printf("%-28s %s %s%s\n", e.ID, e.Vendor, e.Model, note)
	}
	return nil
}

func cmdShow(args []string) error {
	if len(args) != 1 {
		return usageError("show <id>")
	}
	p, err := loadProfile(context.Background(), args[0])
	if err != nil {
		return err
	}
	fmt.Printf("%s %s (%s)\n", p.Vendor, p.Model, p.ID)
	fmt.Printf("  architecture   %s\n", p.Arch)
	fmt.Printf("  install        %s\n", p.Install.Method)
	if p.Install.Method == "installer-stick" {
		fmt.Printf("  keeps as ESP   %s\n", p.Install.ESP)
		fmt.Printf("  replaces only  %s\n", strings.Join(p.Install.Replace, ", "))
	}
	for _, part := range p.Install.Partitions {
		fmt.Printf("  writes         %s from the image %s\n", part.Name, part.Source)
	}
	if p.Firmware != nil && p.Firmware.Restore != "" {
		fmt.Printf("  back to stock  %s\n", p.Firmware.Restore)
	}
	for _, n := range p.Notes {
		fmt.Printf("  note           %s\n", n)
	}
	return nil
}

func cmdDetect(args []string) error {
	if len(args) != 0 {
		return usageError("detect")
	}
	ctx := context.Background()
	c := openCatalog()
	ix, err := c.Index(ctx)
	if err != nil {
		return err
	}
	devs, err := usb.List(usb.SysRoot)
	if err != nil {
		return err
	}
	have, dev := toolVersion()
	found := 0
	for _, e := range ix.Devices {
		p, err := c.Profile(ctx, ix, e.ID, profile.Version{}, true)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skipping %s: %v\n", e.ID, err)
			continue
		}
		need, _ := profile.ParseVersion(p.MinFlashVersion)
		for _, d := range devs {
			mode, ok := p.MatchesUSB(d.VID, d.PID)
			if !ok {
				continue
			}
			found++
			fmt.Printf("%s %s in %s mode on %s\n", p.Vendor, p.Model, mode, d.Node())
			if !dev && have.Less(need) {
				fmt.Printf("  needs sinty-flash %s or newer, this is %s\n", need, have)
			}
		}
	}
	if found == 0 {
		fmt.Println("no known device connected")
	}
	return nil
}

func confirm(prompt string) bool {
	fmt.Printf("%s Type yes to continue: ", prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(line) == "yes"
}

type progressBar struct {
	label string
	total int64
	last  int
}

func (p *progressBar) update(done int64) {
	if p.total <= 0 {
		return
	}
	pct := int(done * 100 / p.total)
	if pct == p.last {
		return
	}
	p.last = pct
	fmt.Fprintf(os.Stderr, "\r%s %3d%%", p.label, pct)
	if done >= p.total {
		fmt.Fprintln(os.Stderr)
	}
}

func newProgress(label string, total int64) *progressBar {
	return &progressBar{label: label, total: total, last: -1}
}

var errDeclined = errors.New("cancelled")

func parseYes(name string, args []string) (bool, []string, error) {
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	yes := fl.Bool("y", false, "")
	if err := fl.Parse(args); err != nil {
		return false, nil, err
	}
	return *yes, fl.Args(), nil
}
