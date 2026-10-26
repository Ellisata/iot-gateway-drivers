# 为官方驱动贡献代码

[English](CONTRIBUTING.en.md) | 简体中文

本仓库包含 iot-gateway 的官方协议驱动。它们采用 **AGPL-3.0-only** 授权，并提供商业/OEM 双授权选项。提交贡献即表示你同意 [CLA](CLA.md)。

## 规则

1. **驱动采用 AGPL-3.0-only。** 不要将许可证不兼容的代码复制进本仓库。
2. **遵循 SDK 契约。** 导入 `github.com/Ellisata/iot-gateway-sdk/driver` 和 `github.com/Ellisata/iot-gateway-sdk/driversdk`；不要导入网关核心包。
3. **一个目录对应一个插件。** 目录名决定二进制名（`iot-driver-<name>`），必须唯一且扁平。
4. **线协议兼容性即契约。** 未经与 SDK、网关发布协调，不要修改 `driversdk.APIVersion` 或帧/负载的布局。
5. **测试必须无需硬件即可运行。** 请向 SDK testkit 添加假服务器（`github.com/Ellisata/iot-gateway-sdk/testkit/fake`），并覆盖重连、超时和错误地址等场景。

## 开发

```bash
gofmt -l .
go vet ./...
go test ./...
```

SDK 与 testkit 是普通的 Go module 依赖。如需针对尚未发布的 SDK checkout 开发，可添加 workspace 替换：

```bash
go work use .
go work edit -replace github.com/Ellisata/iot-gateway-sdk=../iot-gateway-sdk
go work edit -replace github.com/Ellisata/iot-gateway-sdk/testkit=../iot-gateway-sdk/testkit
```

## 发布

每次发布独立打 tag。网关发布时会固定一个驱动 tag，并为每个受支持的平台打包对应的插件二进制。
