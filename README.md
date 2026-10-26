# iot-gateway 官方驱动

[English](README.en.md) | 简体中文

[iot-gateway](https://github.com/Ellisata/iot-gateway) 的官方进程外协议驱动。

每个目录对应一个插件二进制（`iot-driver-<name>`），使用[驱动 SDK](https://github.com/Ellisata/iot-gateway-sdk) 构建。

## 许可证

官方驱动采用 **AGPL-3.0-only** 授权。如需闭源分发，可获取商业/OEM 授权；参见 [CLA.md](CLA.md) 以及主项目的许可 FAQ。

驱动所依赖的 SDK 采用 **Apache-2.0**，位于独立仓库。第三方驱动可用任意许可证使用该 SDK；本仓库中的官方驱动仍保持 AGPL-3.0-only。

## 目录结构

| 目录 | 协议 |
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

测试使用 SDK [`testkit`](https://github.com/Ellisata/iot-gateway-sdk/tree/main/testkit) 模块中的假 PLC 服务器；它们不在本仓库内。

## 构建

SDK 与 testkit 是普通的 Go module 依赖——无需在本地并排放置同级仓库：

```bash
go test ./...
```

`build.sh` 会针对网关的目标平台交叉编译所有驱动。插件必须与网关二进制的 GOOS/GOARCH（以及 GOARM）一致——架构不匹配的插件在运行期会被静默跳过——因此每个平台使用独立的输出目录：

```bash
./build.sh                      # 所有平台（默认）
./build.sh windows              # 仅 windows/amd64
./build.sh linux                # 仅 linux/amd64
./build.sh arm64                # 仅 linux/arm64
./build.sh arm32                # 仅 linux/arm（GOARM=7）
./build.sh native               # 当前平台 -> plugins/
./build.sh linux /tmp/plugins   # 单一平台 + 指定目录
```

在 Windows 上，`build.bat` 提供相同的目标与选项：

```bat
build.bat
build.bat windows
build.bat native
set VERSION=v0.2.0 & build.bat
```

| 目标 | 输出目录 |
|---|---|
| `linux`（amd64） | `plugins-linux/` |
| `arm64` | `plugins-linux-arm64/` |
| `arm32`（GOARM=7） | `plugins-linux-arm/` |
| `windows`（amd64） | `plugins-windows/` |
| `native` | `plugins/` |

`Meta.Version` 在构建时由最近的 git tag 注入；设置 `VERSION=v0.2.0` 可覆盖。`arm32` 固定为 GOARM=7；ARMv6/ARMv5 开发板（树莓派 1 / Zero 及更早型号）请使用 `GOARM=5 ./build.sh arm32`。

如需针对尚未发布的 SDK checkout 开发，可将 workspace 指向它：

```bash
go work use .
go work edit -replace github.com/Ellisata/iot-gateway-sdk=../iot-gateway-sdk
go work edit -replace github.com/Ellisata/iot-gateway-sdk/testkit=../iot-gateway-sdk/testkit
```

将对应平台目录的内容复制到网关的 `plugins/` 目录（与网关可执行文件同目录）。单个驱动也可手工构建——所需的 `-ldflags` 见「版本」一节。

## 新增驱动

1. 创建 `<name>/main.go`，实现 `driver.Driver` 并调用 `driversdk.Main(...)`。
2. 目录名必须与插件后缀一致（`iot-driver-<name>`），不要嵌套。
3. 若协议无需硬件即可测试，请向 SDK testkit 补充对应的假设备（`github.com/Ellisata/iot-gateway-sdk/testkit/fake`）。
4. 网关与插件通过 `driversdk.APIVersion` 进行版本配对；用不兼容的 SDK 构建的插件会在握手阶段被拒绝。

## 版本

本仓库独立打 tag（如 `v0.1.0`）。网关的每次发布都会固定一个驱动 tag，并打包与之匹配的插件二进制。生产环境不要混用随意搭配的网关版本与驱动版本。

插件上报的 `Meta.Version` 在构建时由 tag 注入。`build.sh` 会自动完成；手工构建单个驱动时：

```bash
go build -ldflags "-X main.version=$(git describe --tags --abbrev=0)" -o plugins/iot-driver-modbus ./modbus
```
