# iot-gateway official drivers

[简体中文](README.md) | English

Official out-of-process protocol drivers for
[iot-gateway](https://github.com/Ellisata/iot-gateway).

Each directory is one plugin binary (`iot-driver-<name>`), built with the
[driver SDK](https://github.com/Ellisata/iot-gateway-sdk).

## License

The official drivers are licensed under **AGPL-3.0-only**. A commercial/OEM
license is available for closed-source distribution; see [CLA.md](CLA.md) and
the main project's licensing FAQ.

The SDK they depend on is **Apache-2.0** and lives in a separate repository.
Third-party drivers can use the SDK with any license; these official drivers
remain AGPL-3.0.

## Layout

| Directory | Protocols |
|---|---|
| `modbus/` | ModBus.TCP / ModBus.RTU |
| `s7/` | Siemens.S7 |
| `mitsubishi/` | Mitsubishi.MC.TCP / .Serial |
| `fins/` | Omron.FINS.UDP / .TCP / .Serial / .HostLinkTCP |
| `omroncip/` | Omron.CIP |
| `rockwellcip/` | Rockwell.CIP |
| `opcua/` | OPC.UA |
| `abb/` | ABB.RWS |
| `dlt645/` | DLT645.Serial / DLT645.TCP |

Tests use the fake PLC servers from the SDK's
[`testkit`](https://github.com/Ellisata/iot-gateway-sdk/tree/main/testkit)
module; they are not part of this repository.

## Build

The SDK and testkit are ordinary Go module dependencies — no sibling checkout
is needed:

```bash
go test ./...
```

`build.sh` cross-compiles every driver for the gateway's target platforms.
Plugins must match the gateway binary's GOOS/GOARCH (and GOARM) — a mismatched
plugin is silently skipped at runtime — so each platform gets its own output
directory:

```bash
./build.sh                      # all platforms (default)
./build.sh windows              # windows/amd64 only
./build.sh linux                # linux/amd64 only
./build.sh arm64                # linux/arm64 only
./build.sh arm32                # linux/arm (GOARM=7) only
./build.sh native               # current platform -> plugins/
./build.sh linux /tmp/plugins   # single platform, explicit directory
```

On Windows, `build.bat` provides the same targets and options:

```bat
build.bat
build.bat windows
build.bat native
set VERSION=v0.2.0 & build.bat
```

| Target | Output directory |
|---|---|
| `linux` (amd64) | `plugins-linux/` |
| `arm64` | `plugins-linux-arm64/` |
| `arm32` (GOARM=7) | `plugins-linux-arm/` |
| `windows` (amd64) | `plugins-windows/` |
| `native` | `plugins/` |

`Meta.Version` is injected from the latest git tag; set `VERSION=v0.2.0` to
override it. `arm32` is pinned to GOARM=7; for ARMv6/ARMv5 boards (Raspberry Pi
1 / Zero and older) use `GOARM=5 ./build.sh arm32`.

To develop against an unreleased SDK checkout, point a workspace at it:

```bash
go work use .
go work edit -replace github.com/Ellisata/iot-gateway-sdk=../iot-gateway-sdk
go work edit -replace github.com/Ellisata/iot-gateway-sdk/testkit=../iot-gateway-sdk/testkit
```

Copy the matching directory's contents into the gateway's `plugins/` directory
(same directory as the gateway executable). A single driver can also be built
by hand — see the versioning section for the `-ldflags` needed.

## Adding a driver

1. Create `<name>/main.go` implementing `driver.Driver` and call
   `driversdk.Main(...)`.
2. Keep the directory name equal to the plugin suffix
   (`iot-driver-<name>`), with no nesting.
3. Add a fake device to the SDK testkit
   (`github.com/Ellisata/iot-gateway-sdk/testkit/fake`) if the protocol can be
   tested without hardware.
4. The gateway and plugins are version-paired by `driversdk.APIVersion`; a
   plugin built against an incompatible SDK is rejected at handshake time.

## Versioning

This repository is tagged independently (e.g. `v0.1.0`). The gateway release
pins a driver tag and packages the matching plugin binaries. Do not mix
arbitrary gateway and driver versions in production.

The `Meta.Version` a plugin reports is injected at build time from the tag.
`build.sh` does this automatically; for a manual single-driver build:

```bash
go build -ldflags "-X main.version=$(git describe --tags --abbrev=0)" -o plugins/iot-driver-modbus ./modbus
```
