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
sinty-flash data-image <id> <release> <out> build the data image of a phone
sinty-flash edl-package <id> <release> <firmware> <out>
                                            build the EDL flash package of a board
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
`<image>` is a release directory instead: the signed release, with its unified
kernel image `kernelcache.efi`. When the directory has no data image
`userdata.img`, the tool builds one from the release in the user's cache, the
way `data-image` does, and removes it after the flash. The tool builds the boot
images from the kernel, initramfs and device tree in the signed image with
[sinty-bootimg](https://github.com/singularityos-lab/sinty-bootimg), writes them
to the slot the profile names, writes the data image, makes that slot active
and reboots. The AVB footers are signed with a key made for each flash, which
an unlocked bootloader accepts.

`data-image` builds that data image ahead of time: the release laid out by
`atom-install` in an f2fs image as small as its contents, which the phone grows
to the whole partition on its first boot. Building it needs `mkfs.f2fs`,
`sload.f2fs`, `fsck.f2fs`, `dump.erofs`, and an `atom-install` named by
`SINTY_ATOM_INSTALL` or found on `PATH`.

`edl-package` is for boards flashed over Qualcomm EDL, such as the Arduino UNO
Q. From a signed release and the board's own flash package (`<firmware>`, the
directory with its `gpt_main0.bin` and `gpt_backup0.bin`) it builds a package
that installs Sinty the way the installer does on the board: the partitions the
profile replaces become one `atom-data` partition, and every other entry of the
board's table, slot attributes included, stays exactly as the board package
ships it. The ESP and the data partition are sparse images that carry only the
data they hold, and the data partition grows to the whole disk on the first
boot. `rawprogram0.xml` and `patch0.xml` fit the board's programmer, so the
package flashes with any EDL tool that reads them. Building it needs
`mkfs.fat`, `mcopy`, `mkfs.f2fs`, `sload.f2fs`, `fsck.f2fs`, `dump.erofs`, and
an `atom-install` named by `SINTY_ATOM_INSTALL` or found on `PATH`.

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
