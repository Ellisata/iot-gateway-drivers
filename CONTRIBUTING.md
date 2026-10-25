# Contributing to the official drivers

This repository contains the official protocol drivers for iot-gateway. They
are licensed under **AGPL-3.0-only**, with a commercial/OEM dual-licensing
option. By submitting a contribution you agree to the [CLA](CLA.md).

## Rules

1. **Drivers are AGPL-3.0-only.** Do not copy code from an incompatible
   license into this repository.
2. **Use the SDK contract.** Import
   `github.com/Ellisata/iot-gateway-sdk/driver` and
   `github.com/Ellisata/iot-gateway-sdk/driversdk`; do not import gateway core
   packages.
3. **One directory = one plugin.** The directory name determines the binary
   name (`iot-driver-<name>`); it must be unique and flat.
4. **Wire compatibility is a contract.** Do not change
   `driversdk.APIVersion` or the frame/payload layout without coordinating
   with the SDK and gateway releases.
5. **Tests must run without hardware.** Add a fake server to the SDK testkit
   (`github.com/Ellisata/iot-gateway-sdk/testkit/fake`) and cover reconnects,
   timeouts and bad addresses.

## Development

```bash
gofmt -l .
go vet ./...
go test ./...
```

The SDK and testkit are ordinary module dependencies. To work against an
unreleased SDK checkout, add workspace replacements:

```bash
go work use .
go work edit -replace github.com/Ellisata/iot-gateway-sdk=../iot-gateway-sdk
go work edit -replace github.com/Ellisata/iot-gateway-sdk/testkit=../iot-gateway-sdk/testkit
```

## Release

Each release is tagged independently. The gateway release pins a driver tag
and packages the matching plugin binaries for every supported platform.
