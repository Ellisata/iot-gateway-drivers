# 更新日志

本文件记录 iot-gateway 官方驱动的所有重要变更。每个版本先列中文条目，再列英文条目。本仓库的版本独立于网关和 SDK。

All notable changes to the official iot-gateway drivers are documented here.
Chinese entries come first under each release, followed by English entries.
This repository is versioned independently from the gateway and the SDK.

## [0.2.0] - 2026-10-29

### 新增

- 全部 29 个动态表单（15 个协议配置、7 个局域网扫描、7 个点位扫描）改为数据驱动多语言：
  标题/提示/选项文案使用 `{{$t.key}}`，中文与英文译文存放在表单自带的
  `options.language` 里。表单在设计器里怎么改，都随数据一起切换，不依赖前端词典。
- 扫描能力声明的 `Title` / `Description` 新增可选 `TitleI18n` / `DescriptionI18n`
  （SDK v0.2.0），扫描页下拉与点位扫描弹窗的标题/说明可切换中英。
- 新增仓库级 `internal/formcheck` 测试：token 与译文双向完整、英文不含中文、
  协议表单不含会被迁移器切开的分号。

### Added

- All 29 dynamic forms (15 protocol config, 7 LAN discovery, 7 point discovery) are now
  data-driven multilingual: display text uses `{{$t.key}}` and the Chinese/English
  translations live in the form's own `options.language`. Form edits travel with the
  data, with no runtime dictionary in the gateway.
- Scan specs gained optional `TitleI18n` / `DescriptionI18n` (SDK v0.2.0), so the
  discovery dropdown and point-discovery dialog title/description switch language too.
- A repo-wide `internal/formcheck` test enforces token/translation symmetry, no CJK in
  English, and no semicolons in protocol forms (the migration splitter would break).

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
