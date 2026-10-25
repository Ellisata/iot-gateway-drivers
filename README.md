# iot-gateway official drivers

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
[`testkit`](https://github.com/Ellisata/iot-gateway-sdk/tree/master/testkit)
module; they are not part of this repository.

## Build

The SDK and testkit are ordinary Go module dependencies — no sibling checkout
is needed:

```bash
go test ./...
go build -o plugins/iot-driver-modbus ./modbus
```

To develop against an unreleased SDK checkout, point a workspace at it:

```bash
go work use .
go work edit -replace github.com/Ellisata/iot-gateway-sdk=../iot-gateway-sdk
go work edit -replace github.com/Ellisata/iot-gateway-sdk/testkit=../iot-gateway-sdk/testkit
```

Copy the resulting `iot-driver-*` binaries into the gateway's `plugins/`
directory (same directory as the gateway executable).

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

The `Meta.Version` a plugin reports is injected at build time from the tag:

```bash
go build -ldflags "-X main.version=$(git describe --tags --abbrev=0)" -o plugins/iot-driver-modbus ./modbus
```
