# Sinty Flash

The host tool that puts Sinty OS on a device the way that device needs it,
keeping the firmware it shipped with.

Phones and boards such as the Arduino UNO Q keep their boot chain and radio
firmware on the same storage as the operating system. Writing a whole disk image
over it erases that firmware and can leave the device unbootable. `sinty-flash`
reads the device's profile and writes only what the profile allows.

## Profiles

Profiles live in [sinty-devices](https://github.com/singularityos-lab/sinty-devices),
one JSON file per device model. `sinty-flash` fetches the catalog when it runs,
checks every profile against the digest in the catalog index and keeps a copy for
offline use, so a newly merged device works without updating the tool.

Each profile names the oldest `sinty-flash` that handles it. An older tool lists
the device but refuses to flash it and asks to be updated, so a device that
depends on a fix in the tool is never flashed by a build without the fix.

## Commands

```sh
sinty-flash devices                         list the devices the catalog knows
sinty-flash show <id>                       print what a profile does to a device
sinty-flash detect                          find connected devices the catalog knows
sinty-flash stick [-y] <id> <image> <disk>  write the installer stick for a device
sinty-flash flash [-y] <id> <image|release> flash a device in fastboot mode
sinty-flash version                         print the version
```

`stick` is for devices that boot the Sinty installer from USB. It writes the
image to a removable drive through UDisks, so it needs no root, refuses any
drive that is not removable, and reads the drive back to check every byte.

`flash` is for devices with a fastboot bootloader. It writes each partition the
profile lists from the matching part of a Sinty image, splitting large
partitions into sparse pieces that fit the device's download limit, and leaves
every other partition untouched.

For a device whose profile has a `boot` section, such as the Pixel 7 Pro,
`<image>` is a release directory instead: the signed unified kernel image
`kernelcache.efi` and the data image `userdata.img`. The tool builds the boot
images from the kernel, initramfs and device tree in the signed image with
[sinty-bootimg](https://github.com/singularityos-lab/sinty-bootimg), writes them
to the slot the profile names, writes the data image, makes that slot active
and reboots. The AVB footers are signed with a key made for each flash, which
an unlocked bootloader accepts.

Images may be raw or `.xz` compressed. `SINTY_FLASH_CATALOG` points the tool at
another catalog, either a URL or a local checkout of sinty-devices, for testing
a profile before it is merged.

## Building

```sh
go build ./cmd/sinty-flash
go test ./...
```

The tool runs on Linux.

## License

GPL-3.0-or-later.
