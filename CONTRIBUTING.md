# Contributing to sinty-flash

Thanks for your interest in contributing!

## Quick Start

```bash
git clone https://github.com/singularityos-lab/sinty-flash
cd sinty-flash
go build ./...
go test ./...
```

## Guidelines

- Keep changes additive and tested. sinty-flash writes to the storage of real
  devices, so a regression can brick one; prefer small, verifiable commits over
  large rewrites.
- A fix that a device profile depends on ships in a tagged release first. The
  profile then raises its `min_flash_version` in sinty-devices.
- Match the surrounding style. Comments explain WHY, not WHAT.
- By submitting a contribution you agree to the [CLA](CLA.md).

## License

GPL-3.0-or-later.
