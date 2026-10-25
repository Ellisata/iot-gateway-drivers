# Changelog

All notable changes to the official iot-gateway drivers are documented here.
This repository is versioned independently from the gateway and the SDK.

## [0.1.0] - 2026-10-25

### Added

- Extracted all nine official protocol drivers from the gateway repository:
  Modbus, Siemens S7, Mitsubishi MC, Omron FINS, Omron CIP, Rockwell CIP,
  OPC UA, ABB RWS, DL/T 645.
- Migrated the drivers to the Apache-2.0 driver SDK
  (`github.com/Ellisata/iot-gateway-sdk`).
- Tests use the SDK testkit (`github.com/Ellisata/iot-gateway-sdk/testkit`);
  the parity goldens live in the gateway repository.
- Plugin `Meta.Version` is injected at build time (`-X main.version=<tag>`)
  instead of being hard-coded per driver.
- Official drivers remain AGPL-3.0-only; commercial/OEM licensing is available.
