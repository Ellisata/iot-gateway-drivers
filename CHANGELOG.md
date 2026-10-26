# 更新日志

本文件记录 iot-gateway 官方驱动的所有重要变更。每个版本先列中文条目，再列英文条目。本仓库的版本独立于网关和 SDK。

All notable changes to the official iot-gateway drivers are documented here.
Chinese entries come first under each release, followed by English entries.
This repository is versioned independently from the gateway and the SDK.

## [0.1.0] - 2026-10-25

### 新增

- 从网关仓库中拆出全部九个官方协议驱动：Modbus、西门子 S7、三菱 MC、欧姆龙 FINS、欧姆龙 CIP、罗克韦尔 CIP、OPC UA、ABB RWS、DL/T 645。
- 将驱动迁移到 Apache-2.0 授权的驱动 SDK（`github.com/Ellisata/iot-gateway-sdk`）。
- 测试改用 SDK testkit（`github.com/Ellisata/iot-gateway-sdk/testkit`）；parity goldens（一致性基准）保留在网关仓库。
- 插件 `Meta.Version` 改为构建时注入（`-X main.version=<tag>`），不再按驱动硬编码。
- 官方驱动仍为 AGPL-3.0-only；提供商业/OEM 授权。

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
