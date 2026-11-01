# 更新日志

本文件记录 iot-gateway 官方驱动的所有重要变更。每个版本先列中文条目，再列英文条目。本仓库的版本独立于网关和 SDK。

All notable changes to the official iot-gateway drivers are documented here.
Chinese entries come first under each release, followed by English entries.
This repository is versioned independently from the gateway and the SDK.

## [0.3.0] - 2026-10-31

### 新增

- 控制下发能力（`driver.Writer` / `Writable: true`）覆盖 Modbus 与第二阶段协议。
- Modbus（TCP / RTU）：线圈（0x）与保持寄存器（4x）可写，输入寄存器（3x）与
  离散输入（1x）仍为只读；支持 FC05/06/16，按现有 byteOrder/wordOrder 编码，
  uint8/int8 按字节序补齐。
- S7：DB / M / Q 区可写，I 输入区返回 `WriteCodeNotWritable`；位地址
  （`M0.3`、`DB1.DBX0.0`）采用读-改-写一个字节，其余类型按 S7 大端编码。
- 三菱 MC（TCP / Serial）：位设备 bool 走位单位批量写（nibble 打包，前一点在
  高半字节）；字设备走字单位写；字设备的 bool 点位读-改-写所在字；位设备的
  非 bool 类型按 16 位对齐做字访问。
- FINS（UDP / TCP / Serial / HostLinkTCP）：CIO / WR / HR / DM 区可写；带
  `.bit` 后缀的 bool 使用命令 0x0102 的位写入，其余按字写入并复用现有
  byteOrder/wordOrder 约定。
- Omron CIP：实现 0x4D Write Service for Variables，类型码按 W506 的
  CIP Common 表下发。
- Rockwell CIP：实现 0x4D Write Tag，按点位类型把值转换成对应宽度的 Go
  原始类型，支持数组元素与 `Tag.N` 位访问路径。
- 写结果逐点返回（`driver.WriteCodeXxx`），协议层否定应答不升级为整批错误；
  链路错误返回 error 并标记连接断开。批量写不是事务。

### 修复

- 修正 Omron CIP 的类型码表：此前 BOOL/SINT/USINT/UINT/UDINT/WORD/DWORD/
  REAL/LREAL/STRING 与 CIP Common 码存在错位，批量读会按错误长度切分响应；
  0x4D 写服务必须显式发送类型码，错位值会导致设备按错误类型解析数据。

### 限制

- S7 的 STRING / S5TIME / DATE / DATE_AND_TIME 尚无编码函数，写入返回
  `WriteCodeBadValue`；Omron CIP 与 Rockwell CIP 的 STRING 写入同样暂不支持。
- 控制下发仍要求网关 `control.enabled`、协议 `Writable` 与点位 `W` 权限
  三层同时满足；`APIVersion` 仍为 4。

### 依赖

- 需要 SDK v0.3.0（`driver.Writer` / `Protocol.Writable` / `MethodWrite`）。
  线协议 `APIVersion` 仍为 4。

### Added

- Control writes (`driver.Writer` / `Writable: true`) now cover Modbus and the
  phase-2 protocols.
- Modbus (TCP / RTU): coils (0x) and holding registers (4x) are writable; input
  registers (3x) and discrete inputs (1x) remain read-only. Supports FC05/06/16,
  using the existing byteOrder/wordOrder encoding; sub-register types (uint8/int8)
  are padded according to byte order.
- S7: DB / M / Q are writable and the I input area returns
  `WriteCodeNotWritable`. Bit addresses use a byte read-modify-write; other
  types use S7 big-endian encoding.
- Mitsubishi MC (TCP / Serial): bool points on bit devices use bit-unit batch
  writes (nibble packed, first point in the high nibble); word devices use
  word-unit writes; bool points on word devices read-modify-write the containing
  word; non-bool types on bit devices use 16-bit aligned word access.
- FINS (UDP / TCP / Serial / HostLinkTCP): CIO / WR / HR / DM are writable;
  `.bit` bool points use the bit form of command 0x0102, while word writes reuse
  the existing byteOrder/wordOrder conventions.
- Omron CIP: implements the 0x4D Write Service for Variables with type codes
  from the W506 CIP Common table.
- Rockwell CIP: implements 0x4D Write Tag, converting values to the exact Go
  type width for the point's data type; array elements and `Tag.N` bit paths are
  supported.
- Per-point write results (`driver.WriteCodeXxx`); protocol-level rejections do
  not fail the whole batch, while transport errors return an error and mark the
  link disconnected. Batched writes are not transactional.

### Fixed

- Corrected the Omron CIP type-code table. Several codes (BOOL/SINT/USINT/UINT/
  UDINT/WORD/DWORD/REAL/LREAL/STRING) were misaligned with CIP Common, which
  made batched-read response splitting use the wrong length; the 0x4D write
  service sends the type code explicitly, so a wrong code makes the device parse
  the payload as a different type.

### Limitations

- S7 STRING / S5TIME / DATE / DATE_AND_TIME have no encoder yet and return
  `WriteCodeBadValue`; STRING writes are also not supported for Omron CIP and
  Rockwell CIP.
- Control writes still require the gateway's `control.enabled`, protocol
  `Writable` and point `W` permission. `APIVersion` remains 4.

### Dependencies

- Requires SDK v0.3.0 (`driver.Writer` / `Protocol.Writable` / `MethodWrite`).
  Wire `APIVersion` remains 4.

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
