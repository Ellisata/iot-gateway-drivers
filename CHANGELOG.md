# 更新日志

本文件记录 iot-gateway 官方驱动的所有重要变更。每个版本先列中文条目，再列英文条目。本仓库的版本独立于网关和 SDK。

All notable changes to the official iot-gateway drivers are documented here.
Chinese entries come first under each release, followed by English entries.
This repository is versioned independently from the gateway and the SDK.

## [0.6.0] - 2026-11-24

### 新增

- Modbus（TCP / RTU）：点位级字节/字排列布局 `ByteOrderLayout`（16 位
  `ABCD` / `BADC`，32 位 `ABCD` / `CDAB` / `BADC` / `DCBA`，64 位 `ABCDEFGH` /
  `CDABGHEF` / `BADCFEHG` / `HGFEDCBA` / `GHEFCDAB`），读写共用同一份；空串
  沿用设备级 `byteOrder` / `wordOrder`，存量点位行为不变。

### 修复

- S7：类型宽度超过地址声明宽度（如 `DB1.DBW0` 配 Float）被设备拒绝时，按声明
  宽度探读一次：读得通改判 `QualityBadParse`（「解析失败」），不再误报
  `QualityBadAddress`（「地址无效」）；读不通维持原判。探读只发生在已确认读不到
  的单点上。

### 变更

- 依赖 SDK **v0.6.0**。线协议 `APIVersion` 4 → 5，新旧插件不兼容：需同步升级
  网关并重编全部插件，新插件在旧网关上握手即被拒绝。

### Added

- Modbus (TCP / RTU): point-level byte/word layout `ByteOrderLayout` — 16-bit
  `ABCD` / `BADC`; 32-bit `ABCD` / `CDAB` / `BADC` / `DCBA`; 64-bit `ABCDEFGH` /
  `CDABGHEF` / `BADCFEHG` / `HGFEDCBA` / `GHEFCDAB`. Reads and writes share the
  same permutation; an empty string keeps the device-level `byteOrder` /
  `wordOrder`, so existing points behave as before.

### Fixed

- S7: when a data type is wider than the address suffix (e.g. `DB1.DBW0` as
  Float) and the device rejects the read, probe once at the declared width: a
  successful probe reclassifies the point as `QualityBadParse` ("parse
  failure") instead of the misleading `QualityBadAddress` ("invalid address");
  a failed probe keeps the old verdict. Only already-confirmed bad single
  points are probed.

### Changed

- Depends on SDK **v0.6.0**. Wire `APIVersion` goes 4 → 5; old and new plugins
  are incompatible — upgrade the gateway and rebuild every plugin, as a new
  plugin is rejected at handshake on an old gateway.

## [0.5.0] - 2026-11-20

### 修复

- **不同插件进程配到同一个物理口（issue #2 的终局修复）**：DL/T 645、FINS、
  三菱 MC 与 Modbus RTU 现在都声明 `SerialProxy` 并实现
  `driver.SerialTransportProvider`。网关判定同端口上的协议**全部**支持代理时，
  物理句柄改由网关主进程持有，插件经 `serial.*` RPC 收发字节、共用网关的跨进程
  总线锁；不再出现两个进程各持一个 fd、互相瓜分应答与打断 termios 的局面。
  Modbus RTU 的代理路径用自定义 `Transporter` 走字节流（CRC 校验仍由 goburrow
  的 Packager 完成）。旧网关、或同端口混有不支持代理的插件时保持直连，网关告警。
- **多台设备共用一个串口会互相打断（Linux 上表现为"只有一台表能采集"）**：
  DL/T 645、FINS（Host Link）、三菱 MC（4C Format5）与 Modbus RTU 的串口传输改为
  通过 SDK 的 `driversdk/serialbus` 按物理端口共享句柄与总线锁。此前每个会话各开
  一个句柄，同一个 tty 上两个读端会瓜分应答字节（日志里的「已收 7/9 字节」）、在
  select 竞争失败时拿到 EAGAIN（`resource temporarily unavailable`），且任一方
  `Close` 恢复 termios 都会打断另一方，最终表现为两台设备交替超时、串口反复
  「连接已关闭/已连接」。现在一个物理端口只有一个句柄，最后一个会话关闭时才关端口；
  端口名按 `driver.CanonicalSerialPort` 归一（Windows 大小写与 `\\.\`、Linux 软链）。
- 同一端口但波特率/数据位/停止位/校验位不同，现在报明确的配置错误，而不是悄悄再开
  一个句柄把上述问题原样复现（同一条总线上的设备必须参数一致）。
- 读超时不再判死共享句柄 —— 它只说明本次没有应答；只有真正的链路 I/O 错误才换新句柄，
  避免一台设备掉线把整条总线拖进反复重连。
- Modbus RTU 的 `IdleTimeout` 在共享句柄下禁用：它会静默关闭端口并恢复 termios，
  打断仍在收发其它从站的会话。「测试连接」的临时连接也改走同一张注册表，采集会话
  占着端口时直接共享，不再二次打开（Windows 的 `Access is denied`）。

### 变更

- 依赖 SDK **v0.5.0**（`driver.CanonicalSerialPort`、`driversdk/serialbus`、
  `driversdk/serialproxy` 与 `driver.SerialTransportProvider`）。
  线协议 `APIVersion` 保持 4，旧网关无需升级；插件需按新 SDK 重新编译。

- **Different plugin processes on one physical port** (the end-game fix for
  issue #2): DL/T 645, FINS, Mitsubishi MC and Modbus RTU now declare
  `SerialProxy` and implement `driver.SerialTransportProvider`. When every
  protocol on a port supports proxying, the gateway process owns the only
  physical handle and plugins exchange bytes over `serial.*` RPC, sharing the
  gateway's cross-process bus lock. Two processes can no longer hold one fd
  each and split answers or restore termios under one another. The Modbus RTU
  proxy path uses a custom `Transporter` over the byte stream (CRC verification
  still comes from goburrow's Packager). Old gateways, or a port that mixes in
  a plugin without proxy support, keep the direct path with a gateway warning.

### Fixed

- **Several devices sharing one serial port corrupted each other's traffic**
  (on Linux this showed up as "only one meter can be read"): the serial
  transports of DL/T 645, FINS (Host Link), Mitsubishi MC (4C Format5) and Modbus
  RTU now share one handle and one bus lock per physical port through the SDK's
  `driversdk/serialbus`. Previously every session opened its own handle, so two
  readers on the same tty split the answer bytes (the "received 7/9 bytes"
  fragments in the log), lost the select race with `EAGAIN` ("resource
  temporarily unavailable"), and either side's `Close` restored termios under
  the other — producing alternating timeouts and the "connection closed /
  connected" churn. There is now exactly one handle per port and it is closed
  only by the last session; port names are canonicalized with
  `driver.CanonicalSerialPort` (Windows case / `\\.\`, Linux symlinks).
- A different baud rate / data bits / stop bits / parity on the same port is now
  an explicit configuration error instead of silently opening a second handle
  and reproducing the same failure (devices on one bus must share parameters).
- Read timeouts no longer mark the shared handle dead — they only mean this
  request got no answer; only real link I/O errors force a new handle, so one
  offline device no longer drags the whole bus into reconnect churn.
- Modbus RTU's `IdleTimeout` is disabled for shared handles: it used to close
  the port (restoring termios) behind the back of sessions still talking to
  other slaves. The temporary connection used by "test connection" now goes
  through the same registry and shares an existing handle instead of opening a
  second one (`Access is denied` on Windows).

### Changed

- Depends on SDK **v0.5.0** (`driver.CanonicalSerialPort` and
  `driversdk/serialbus`). Wire `APIVersion` stays 4, so old gateways need no
  upgrade; plugins must be rebuilt against the new SDK.

## [0.4.1] - 2026-11-19

### 新增

- 新增 FANUC 机器人 EtherNet/IP（CIP 显式消息）驱动 `fanuc/`，协议名
  `FANUC.CIP`：支持 R-30iA / R-30iB / R-30iB Plus 的数值寄存器 R[]，
  整型走对象 0x6B、实型走 0x6C，读/写均支持。连接前用 ListIdentity 校验
  厂商号 591；地址级、通信级、解码级失败按点位降级，不拖垮整台设备；
  请求默认限流 10 次/秒，单轮预算 45s。
- 数值寄存器 R[] 增加块读（`Get_Attribute_All`，服务 0x01）：同一 class 下
  连续（间隙 ≤16）的点位合并成一次请求，单块默认 100 个、上限 124 个，
  设为 1 即关闭。首次块读用单点读交叉校验；设备拒绝、响应长度不符或数据
  错位时当场退回单读，并暂停该 class 的块读 5 分钟。相邻 100 点的帧数从
  100 降到 1。
- FANUC.CIP 新增局域网设备扫描：用 EtherNet/IP 标准的 ListIdentity
  （44818/UDP）发现网段内的 FANUC 机器人（厂商号 591），广播与逐主机两种
  目标都扫，型号与版本取自设备自述的身份信息；非 FANUC 的 EtherNet/IP
  设备不上报。部分控制器或现场网络不回应 UDP ListIdentity，扫不到不等于
  网段里没有设备。
- FANUC.CIP 新增点位扫描：按所选类型（Long / DWord / Float）扫描一段数值
  寄存器号，列出读得到的 R[n] 与当前值。连续有效区间用块读快速扫过，块读
  被拒或中间夹着另一类寄存器时二分降级到单点；首次块读会用单点读交叉校验，
  发现设备忽略起始号时改走单读。控制器不提供寄存器总数，范围由用户指定，
  产出的是候选清单；整型与实型是两个不同的对象，一次扫描只覆盖其中一类。
  超出预算时返回已完成的部分并把「未扫完」标出来。
- OPC.UA 新增控制下发：实现 `driver.Writer` 并声明 `Writable: true`，
  经 OPC UA Write 服务写 Variable 的 Value 属性。值按点位内部类型编码为
  对应的 Variant（bool / 整型 / 浮点 / string / byteString(hex) / datetime）；
  节点只读或当前用户无权限返回 `WriteCodeNotWritable`，类型或范围错误返回
  `WriteCodeBadValue`。Method 调用与数组写入暂不支持。
- P0 范围**不含**位置寄存器 PR/CURPOS、DI/DO/GI/GO 与字符串寄存器 SR。
  这些对象的载荷布局与 Assembly 实例需要真机核对后再开放。
- 线协议 `APIVersion` 保持 4；新增驱动只依赖既有接口，旧网关无需升级，
  插件需按当前 SDK（v0.4.0）重新编译。

### Added

- New FANUC robot EtherNet/IP (CIP explicit messaging) driver under `fanuc/`,
  protocol `FANUC.CIP`: reads and writes numeric registers R[] on R-30iA /
  R-30iB / R-30iB Plus, using object 0x6B for integers and 0x6C for reals.
  The driver verifies vendor ID 591 via ListIdentity before connecting and
  degrades address-, comms- and decode-level failures per point instead of
  failing the whole device. Requests are rate-limited to 10/s by default with
  a 45s read budget.
- Block reads for numeric registers R[] (`Get_Attribute_All`, service 0x01):
  contiguous points of the same class (gap ≤16) are merged into one request,
  100 registers per block by default (max 124; set 1 to disable). The first
  block read is cross-checked with single reads; on rejection, wrong length or
  shifted data the driver falls back to single reads immediately and pauses
  block reads for that class for 5 minutes. 100 adjacent points go from 100
  frames to 1.
- Device discovery for `FANUC.CIP`: uses the standard EtherNet/IP ListIdentity
  (44818/UDP) to find FANUC robots (vendor ID 591) on the subnet, scanning both
  broadcast and per-host targets. Model and version come from the device's
  self-reported identity; non-FANUC EtherNet/IP devices are not reported. Some
  controllers or site networks do not answer UDP ListIdentity, so an empty
  result does not prove there are no devices.
- Point discovery for `FANUC.CIP`: scans a range of numeric register numbers
  of the selected type (Long / DWord / Float) and lists the R[n] that can be
  read with their current values. Contiguous valid ranges are read in blocks;
  when a block is rejected or the range contains registers of the other type,
  the scan degrades to binary splits down to single reads. The first block
  read is cross-checked with single reads, and the scan switches to single
  reads if the controller ignores the start number. The controller does not
  expose a register count, so the range is user-specified and the result is a
  candidate list; integer and real registers are different objects, and one
  scan covers only one type. On timeout the completed part is returned with
  the scan marked incomplete.
- Control writes for `OPC.UA`: implements `driver.Writer` and declares
  `Writable: true`, writing a Variable's Value attribute through the OPC UA
  Write service. Values are encoded as the Variant matching the point's
  internal data type (bool / integer / float / string / byteString (hex) /
  datetime). Read-only nodes or insufficient user access return
  `WriteCodeNotWritable`; type or range errors return `WriteCodeBadValue`.
  Method calls and array writes are not supported yet.
- Out of P0 scope: position registers (PR/CURPOS), DI/DO/GI/GO and string
  registers (SR). Their payload layouts and Assembly instances need
  on-hardware verification first.
- Wire `APIVersion` stays 4; the new driver only uses existing interfaces, so
  no gateway upgrade is required. Rebuild plugins with the current SDK
  (v0.4.0).

## [0.4.0] - 2026-11-05

### 新增

- 全部声明扫描能力的协议补上 `Discovery.Transport`：Modbus TCP、S7、FINS/TCP、
  OPC UA、三菱 MC 声明 `tcp`；FINS/UDP、Omron CIP、Rockwell CIP 声明 `udp`。
  网关据此对 TCP 协议的单播目标先做一次短超时的 TCP 可达性预筛，大网段扫描
  从小时级降到分钟级。广播目标永远绕过预筛，CIP 的 ListIdentity 不受影响。
- 依赖 SDK `v0.4.0`（`driver.ProbeTransport` 随该版本引入）。

### 兼容性

- 线协议 `APIVersion` **保持 4**，本版本只新增可选的 describe 字段；旧网关忽略它，
  扫描行为不变。`Transport` 是**承诺**：声明 `tcp` 而实际探 UDP 会让预筛误判死。

### Added

- Every scan-capable protocol now declares `Discovery.Transport`: Modbus TCP,
  S7, FINS/TCP, OPC UA and Mitsubishi MC declare `tcp`; FINS/UDP, Omron CIP and
  Rockwell CIP declare `udp`. The gateway uses it to run a short-timeout TCP
  reachability prefilter for unicast targets of TCP protocols, cutting
  large-subnet scans from hours to minutes. Broadcast targets always bypass the
  prefilter, so CIP ListIdentity is unaffected.
- Requires SDK `v0.4.0` (which introduces `driver.ProbeTransport`).

### Compatibility

- Wire `APIVersion` **stays 4**; this release only adds an optional describe
  field. Old gateways ignore it and scanning behaves as before. `Transport` is a
  **promise**: declaring `tcp` for a probe that actually uses UDP makes the
  prefilter reject every target.

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
