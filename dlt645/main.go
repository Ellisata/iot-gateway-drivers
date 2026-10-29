// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

// Command iot-driver-dlt645 是 DL/T 645 电能表协议（串口 / TCP 两种传输）的进程外插件实现。
//
// 本插件移植自 iot-gateway 的内置驱动 backend/driver/dlt645（协议实现逐文件搬迁，
// 只有「日志出口」与「类型注册方式」两处因插件形态而变，见 ./dlt645drv）。
// 协议名与 scope 沿用同一组原值（DLT645.Serial / DLT645.TCP, scope = dlt645），
// 因而从 iot-gateway 侧迁过来的设备配置、点位地址与类型名无需任何转换。
//
// 本插件自带全部协议实现（见 ./dlt645drv）：数据标识字典、地址解析、帧编解码、
// 数值解码、串口/TCP 客户端与驱动都在插件进程内。本文件只做四件事：
//
//  1. 声明两个协议的名字、说明、串行独占性、配置表单与**点位扫描能力**；
//  2. 把 dlt645drv 的类型表与类型映射原样上报（Meta.Types / Meta.TypeMap）；
//  3. 用 dlt645drv.NewSerial / NewTCP 作工厂，用 dlt645drv.ScanPoints 作 finder；
//  4. Serve 之前跑一次字典自检，把表内笔误变成一行日志。
//
// 两个协议共享同一 DL/T 645 应用层（帧编解码、数据标识解析、数值解码），
// 仅底层 I/O 不同（串口 vs 串口服务器/DTU 透传的 TCP 字节流），
// 因此共用一个 scope 与一套类型表。
//
// 构建：
//
//	cd backend && go build -o ./plugins/iot-driver-dlt645 ./driversrc/dlt645
package main

import (
	"encoding/json"

	dlt645drv "github.com/Ellisata/iot-gateway-drivers/dlt645/dlt645drv"
	"github.com/Ellisata/iot-gateway-sdk/driver"
	"github.com/Ellisata/iot-gateway-sdk/driversdk"
)

const (
	// 协议名沿用 iot-gateway 内置时代的原值 —— 它们同时是 iot_protocol.name 与
	// device.protocol_id 的解析目标，改一个字就让既有设备静默采不到值。
	// 钉住它们的是 main_test.go 的 TestProtocolIdentityPinned。
	protocolSerial = "DLT645.Serial"
	protocolTCP    = "DLT645.TCP"

	// pluginScope 类型注册 scope，同样沿用原值（"dlt645"）。
	// 内置基线已随内置驱动清空，所以这里不再撞名（RegisterScope 拒绝与基线撞名）。
	pluginScope = "dlt645"
)

// 表单来源说明：两份表单抠自 iot-gateway 的播种脚本
// database/sqlite/migrations/014_add_protocol_20260920.sql，已去掉 form-create 设计器
// 产生的 effect/display/hidden 等冗余字段，字段名与顺序与原值一致。
//
// 两处刻意的偏离，都是为了让表单说真话：
//
//  1. **每项补了 value 默认值**。内置版靠「字段缺席 → 用驱动默认值」兜底，
//     表单上是一片空白，用户看不出不填会发生什么；而本驱动的默认值里有几个是
//     反直觉的（parity 默认 E 而非仓库通用的 N、baudRate 默认 2400、
//     串口 preambleBytes 默认 4 而 TCP 默认 0，收发间延时同样是串口 30ms
//     而 TCP 0ms）。把驱动实际会用的值填进表单，提交与不提交的行为才一致。
//  2. **去掉了 maxFollowUpFrames**。该字段在当前实现里不参与读取逻辑
//     （不支持拼接后续帧，见 dlt645drv/types.go 的 MaxFollowUpFrames），
//     内置版留着它只是为了兼容已下发的表单。本插件没有历史配置要兼容，
//     一个填了毫无作用的旋钮只会误导现场 —— 字段本身仍被容错解析。
const (
	formSerial = `{
  "rule": [
    {"type":"select","field":"protocolVersion","title":"{{$t.protocolVersion_title}}","$required":true,"value":"2007",
     "info":"{{$t.protocolVersion_info}}",
     "options":[{"label":"DL/T 645-2007","value":"2007"},{"label":"DL/T 645-1997","value":"1997"}]},
    {"type":"input","field":"meterAddress","title":"{{$t.meterAddress_title}}","$required":true,"value":"000000000001",
     "info":"{{$t.meterAddress_info}}"},
    {"type":"input","field":"comPort","title":"{{$t.comPort_title}}","$required":true,"value":"COM1",
     "info":"{{$t.comPort_info}}"},
    {"type":"select","field":"baudRate","title":"{{$t.baudRate_title}}","$required":false,"value":"2400",
     "info":"{{$t.baudRate_info}}",
     "options":[{"label":"600","value":"600"},{"label":"1200","value":"1200"},{"label":"2400","value":"2400"},{"label":"4800","value":"4800"},{"label":"9600","value":"9600"},{"label":"19200","value":"19200"},{"label":"38400","value":"38400"},{"label":"57600","value":"57600"},{"label":"115200","value":"115200"}]},
    {"type":"select","field":"dataBits","title":"{{$t.dataBits_title}}","$required":false,"value":"8",
     "options":[{"label":"8","value":"8"},{"label":"7","value":"7"},{"label":"6","value":"6"},{"label":"5","value":"5"}]},
    {"type":"select","field":"stopBits","title":"{{$t.stopBits_title}}","$required":false,"value":"1",
     "options":[{"label":"1","value":"1"},{"label":"2","value":"2"}]},
    {"type":"select","field":"parity","title":"{{$t.parity_title}}","$required":false,"value":"E",
     "info":"{{$t.parity_info}}",
     "options":[{"label":"{{$t.parity_option_0}}","value":"E"},{"label":"{{$t.parity_option_1}}","value":"N"},{"label":"{{$t.parity_option_2}}","value":"O"}]},
    {"type":"inputNumber","field":"timeoutMs","title":"{{$t.timeoutMs_title}}","$required":false,"value":3000,
     "info":"{{$t.timeoutMs_info}}"},
    {"type":"inputNumber","field":"interFrameDelayMs","title":"{{$t.interFrameDelayMs_title}}","$required":false,"value":30,
     "info":"{{$t.interFrameDelayMs_info}}"},
    {"type":"inputNumber","field":"preambleBytes","title":"{{$t.preambleBytes_title}}","$required":false,"value":4,
     "info":"{{$t.preambleBytes_info}}"},
    {"type":"inputNumber","field":"maxDIsPerRead","title":"{{$t.maxDIsPerRead_title}}","$required":false,"value":12,
     "info":"{{$t.maxDIsPerRead_info}}"},
    {"type":"select","field":"checkChecksum","title":"{{$t.checkChecksum_title}}","$required":false,"value":"true",
     "info":"{{$t.checkChecksum_info}}",
     "options":[{"label":"{{$t.checkChecksum_option_0}}","value":"true"},{"label":"{{$t.checkChecksum_option_1}}","value":"false"}]}
  ],
  "options": {
    "form":{"inline":false,"hideRequiredAsterisk":false,"labelPosition":"right","size":"default","labelWidth":"125px"},
    "resetBtn":{"show":false,"innerText":"重置"},
    "submitBtn":{"show":false,"innerText":"提交"},
    "language":{"zh-cn":{"protocolVersion_title":"协议版本","protocolVersion_info":"2007 版数据标识为 4 字节，1997 版为 2 字节；选错表现为设备无应答（超时）","meterAddress_title":"表号","meterAddress_info":"1~12 位十进制表号，不足左补零，如 000000000001","comPort_title":"设备端口","comPort_info":"RS-485 / RS-232 串口，如 COM3、COM12（Windows）或 /dev/ttyUSB0、/dev/ttyS0（Linux）；须与网关设备上实际枚举出的端口名完全一致","baudRate_title":"波特率","baudRate_info":"645 电表出厂多为 2400","dataBits_title":"数据位","stopBits_title":"停止位","parity_title":"校验位","parity_info":"645 电表几乎全为偶校验（与仓库内其它驱动默认的 N 不同）","parity_option_0":"偶校验","parity_option_1":"无校验","parity_option_2":"奇校验","timeoutMs_title":"单帧超时(ms)","timeoutMs_info":"默认 3000；波特率低时过小会截断应答帧，驱动按波特率设有下限","interFrameDelayMs_title":"收发间延时(ms)","interFrameDelayMs_info":"串口默认 30；RS-485 收发器换向与电表处理时间，0 表示不延时。此项会乘进帧数，是轮询周期的主要变量","preambleBytes_title":"前导唤醒字节数","preambleBytes_info":"0~4，串口默认 4（FE FE FE FE）","maxDIsPerRead_title":"单请求数据标识数","maxDIsPerRead_info":"1~12，默认 12（协议上限）。帧数 = ⌈点数/本值⌉；设 1 时一帧一个点，大点位设备采不过来。遇异常应答会自动降级为逐个读取","checkChecksum_title":"校验码校验","checkChecksum_info":"默认校验；少数表的校验和计算不规范时可关闭，改为仅校验帧结构","checkChecksum_option_0":"校验","checkChecksum_option_1":"不校验"},"en":{"protocolVersion_title":"Protocol version","protocolVersion_info":"2007 identifiers are 4 bytes, 1997 identifiers are 2 bytes. A wrong choice shows up as no response (timeout)","meterAddress_title":"Meter address","meterAddress_info":"1-12 decimal digits, left-padded with zeros, for example 000000000001","comPort_title":"Device port","comPort_info":"RS-485 / RS-232 serial port, for example COM3 or COM12 (Windows) or /dev/ttyUSB0 or /dev/ttyS0 (Linux). It must exactly match the port name enumerated on the gateway host","baudRate_title":"Baud rate","baudRate_info":"Most 645 meters ship with 2400","dataBits_title":"Data bits","stopBits_title":"Stop bits","parity_title":"Parity","parity_info":"Nearly all 645 meters use even parity (unlike the N default of other drivers in this repo)","parity_option_0":"Even","parity_option_1":"None","parity_option_2":"Odd","timeoutMs_title":"Frame timeout (ms)","timeoutMs_info":"Defaults to 3000. At low baud rates a value that is too small truncates the response frame. The driver enforces a lower bound based on baud rate","interFrameDelayMs_title":"Inter-frame delay (ms)","interFrameDelayMs_info":"Serial default is 30. RS-485 transceiver turnaround plus meter processing time. 0 means no delay. This value multiplies across frames and is the main variable in the polling cycle","preambleBytes_title":"Leading wake-up bytes","preambleBytes_info":"0-4, serial default is 4 (FE FE FE FE)","maxDIsPerRead_title":"Data identifiers per request","maxDIsPerRead_info":"1-12, default is 12 (protocol maximum). Frames = ceil(points / this value). At 1 there is one point per frame and large point sets cannot keep up. An exception response automatically falls back to reading point by point","checkChecksum_title":"Checksum verification","checkChecksum_info":"Verify checksums by default. A few meters compute checksums incorrectly, in which case this can be disabled so only the frame structure is checked","checkChecksum_option_0":"Verify","checkChecksum_option_1":"Do not verify"}}
  }
}`

	formTCP = `{
  "rule": [
    {"type":"select","field":"protocolVersion","title":"{{$t.protocolVersion_title}}","$required":true,"value":"2007",
     "info":"{{$t.protocolVersion_info}}",
     "options":[{"label":"DL/T 645-2007","value":"2007"},{"label":"DL/T 645-1997","value":"1997"}]},
    {"type":"input","field":"meterAddress","title":"{{$t.meterAddress_title}}","$required":true,"value":"000000000001",
     "info":"{{$t.meterAddress_info}}"},
    {"type":"input","field":"host","title":"{{$t.host_title}}","$required":true,
     "info":"{{$t.host_info}}"},
    {"type":"inputNumber","field":"port","title":"{{$t.port_title}}","$required":true,"value":8899,
     "info":"{{$t.port_info}}"},
    {"type":"inputNumber","field":"timeoutMs","title":"{{$t.timeoutMs_title}}","$required":false,"value":3000,
     "info":"{{$t.timeoutMs_info}}"},
    {"type":"inputNumber","field":"interFrameDelayMs","title":"{{$t.interFrameDelayMs_title}}","$required":false,"value":0,
     "info":"{{$t.interFrameDelayMs_info}}"},
    {"type":"inputNumber","field":"preambleBytes","title":"{{$t.preambleBytes_title}}","$required":false,"value":0,
     "info":"{{$t.preambleBytes_info}}"},
    {"type":"inputNumber","field":"maxDIsPerRead","title":"{{$t.maxDIsPerRead_title}}","$required":false,"value":12,
     "info":"{{$t.maxDIsPerRead_info}}"},
    {"type":"select","field":"checkChecksum","title":"{{$t.checkChecksum_title}}","$required":false,"value":"true",
     "info":"{{$t.checkChecksum_info}}",
     "options":[{"label":"{{$t.checkChecksum_option_0}}","value":"true"},{"label":"{{$t.checkChecksum_option_1}}","value":"false"}]}
  ],
  "options": {
    "form":{"inline":false,"hideRequiredAsterisk":false,"labelPosition":"right","size":"default","labelWidth":"125px"},
    "resetBtn":{"show":false,"innerText":"重置"},
    "submitBtn":{"show":false,"innerText":"提交"},
    "language":{"zh-cn":{"protocolVersion_title":"协议版本","protocolVersion_info":"2007 版数据标识为 4 字节，1997 版为 2 字节；选错表现为设备无应答（超时）","meterAddress_title":"表号","meterAddress_info":"1~12 位十进制表号，不足左补零，如 000000000001","host_title":"设备IP","host_info":"串口服务器 / DTU 的地址","port_title":"设备端口","port_info":"串口服务器 / DTU 透传端口","timeoutMs_title":"单帧超时(ms)","timeoutMs_info":"默认 3000；过小会截断应答帧","interFrameDelayMs_title":"收发间延时(ms)","interFrameDelayMs_info":"TCP 透传默认 0（换向由串口服务器 / DTU 处理，网关这侧的等待是纯空转）；个别 DTU 应答慢时再调大","preambleBytes_title":"前导唤醒字节数","preambleBytes_info":"0~4，TCP 透传默认 0；个别 DTU 需要唤醒时再调大","maxDIsPerRead_title":"单请求数据标识数","maxDIsPerRead_info":"1~12，默认 12（协议上限）。帧数 = ⌈点数/本值⌉；设 1 时一帧一个点，大点位设备采不过来。遇异常应答会自动降级为逐个读取","checkChecksum_title":"校验码校验","checkChecksum_info":"默认校验；少数表的校验和计算不规范时可关闭，改为仅校验帧结构","checkChecksum_option_0":"校验","checkChecksum_option_1":"不校验"},"en":{"protocolVersion_title":"Protocol version","protocolVersion_info":"2007 identifiers are 4 bytes, 1997 identifiers are 2 bytes. A wrong choice shows up as no response (timeout)","meterAddress_title":"Meter address","meterAddress_info":"1-12 decimal digits, left-padded with zeros, for example 000000000001","host_title":"Device IP","host_info":"Address of the serial server / DTU","port_title":"Device port","port_info":"Transparent port of the serial server / DTU","timeoutMs_title":"Frame timeout (ms)","timeoutMs_info":"Defaults to 3000. A value that is too small truncates the response frame","interFrameDelayMs_title":"Inter-frame delay (ms)","interFrameDelayMs_info":"TCP transparent default is 0 (turnaround is handled by the serial server / DTU, so waiting on the gateway side is pure idle time). Increase it for a DTU that responds slowly","preambleBytes_title":"Leading wake-up bytes","preambleBytes_info":"0-4, TCP transparent default is 0. Increase it if a DTU needs a wake-up","maxDIsPerRead_title":"Data identifiers per request","maxDIsPerRead_info":"1-12, default is 12 (protocol maximum). Frames = ceil(points / this value). At 1 there is one point per frame and large point sets cannot keep up. An exception response automatically falls back to reading point by point","checkChecksum_title":"Checksum verification","checkChecksum_info":"Verify checksums by default. A few meters compute checksums incorrectly, in which case this can be disabled so only the frame structure is checked","checkChecksum_option_0":"Verify","checkChecksum_option_1":"Do not verify"}}
  }
}`
)

// meta 插件的协议清单。等价于内置驱动在 init() 里做的事：
// 驱动注册（由工厂承担）、类型注册、类型映射、协议配置表单。
//
// **两个协议都不声明 Discovery（局域网设备扫描），这是有意的**：
//
//   - 串口按契约不得声明 —— 扫描的目标模型是 host/port，表达不了 comPort，
//     而"一条 RS-485 总线上有哪些表"与 IP 网段无关（driversdk.Meta.Validate 会拒）。
//   - TCP 虽然可以，但它连的是**串口服务器 / DTU**，与 Omron.FINS.HostLinkTCP 同形，
//     而 FINS 刻意不声明同一条（见 driversrc/fins/main.go 的那一项注释）。
//     DL/T 645-2007 确有广播「读通信地址」(控制码 13H → 应答 93H，应答帧地址域
//     回填真实表号) 能枚举一条总线上的表，但那是"**总线上有哪些表**"，不是
//     "网段里有哪些主机"——是另一个功能，且 1997 版的广播语义无依据、无真机可验证。
//     按仓库"不做而不是猜着做"的定例：这里留 nil，并由 main_test.go 的
//     TestNoDeviceScan 钉住，免得被后人顺手加上。
//
// 点位扫描（PointDiscovery）则两个协议**都声明**：它的目标是一台已存在的设备，
// 串口设备同样有"它有哪些点位"这个问题，且串口恰是会话复用最重要的场景。
func meta() driversdk.Meta {
	return driversdk.Meta{
		APIVersion: driversdk.APIVersion,
		Protocols: []driversdk.Protocol{
			{
				Name: protocolSerial,
				// 串口独占 RS-485 总线：采集引擎据此整轮持设备锁（重连 + 全部读取 +
				// 转换 + 推送），保证总线上帧不被交错。TCP 为 false。
				SerialExclusive: true,
				Description:     "DL/T 645 电能表 串口（RS-485 / RS-232）",
				FormJSON:        json.RawMessage(formSerial),
				PointDiscovery:  dlt645drv.PointDiscovery(),
			},
			{
				Name: protocolTCP,
				// 名字里是 TCP，但网关视角是一条长连接，客户端内部已对单次
				// 请求+响应持互斥锁（见 dlt645drv/tcp.go 的 Lock/Unlock），
				// 与其它以太网协议同级 —— 不独占。判据与内置实现一致：
				// transport == TransportSerial 才独占。
				SerialExclusive: false,
				Description:     "DL/T 645 电能表 以太网（串口服务器 / DTU 透传）",
				FormJSON:        json.RawMessage(formTCP),
				PointDiscovery:  dlt645drv.PointDiscovery(),
			},
		},
		Scope:       pluginScope,
		Description: "DL/T 645 电能表",
		Version:     version,
		Types:       dlt645drv.Types(),
		TypeMap:     dlt645drv.CommonTypeMap(),
	}
}

// factories 协议名 → 工厂。键必须与 meta().Protocols[].Name 逐一对应，
// 否则 Serve 会在启动时报错（main_test.go 也钉住了这条不变式）。
func factories() driversdk.Factories {
	return driversdk.Factories{
		protocolSerial: dlt645drv.NewSerial,
		protocolTCP:    dlt645drv.NewTCP,
	}
}

// finders 协议名 → 点位扫描实现。键必须与 meta() 里**声明了 PointDiscovery** 的协议
// 逐一对应，否则 Serve 会在启动时报错（main_test.go 的 TestPointFindersMatchPointDiscovery
// 也钉住了这条不变式）。
//
// **两个协议共用同一个函数**：点位扫描走的地址解析、数据标识字典与解码与传输层无关，
// 两边的表单、预算、上限因此逐字相同（PointDiscovery 也是同一份声明）。
// 串口与 TCP 的差别只在会话复用路径上，而那由 driversdk 的回退表处理。
func finders() driversdk.PointFinders {
	scan := func(req driversdk.FindPointsRequest) (*driver.PointScanResult, error) {
		return dlt645drv.ScanPoints(req.Session, req.Options, req.Deadline)
	}
	return driversdk.PointFinders{
		protocolSerial: scan,
		protocolTCP:    scan,
	}
}

// version is overridden at build time with -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	// 字典自检：遍历两张静态表（约 70 条），微秒级完成，不会拖延 Serve。
	// 放在这里而不是 init()，是为了让它排在 driversdk.Serve 之前但不隐式发生 ——
	// Serve 之前不做**耗时**自检（父进程消失时插件靠 stdin EOF 退出，卡在自检里
	// 会留下孤儿进程），这条只读表的检查不在此列。
	dlt645drv.VerifyDictionary()

	driversdk.Main(factories(), meta(), driversdk.Capabilities{
		PointFinders: finders(),
	})
}
