// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

// Command iot-driver-modbus 是 Modbus 协议的进程外插件实现。
//
// **本插件是该协议在网关里的唯一实现**：内置的 driver/modbus 已随其余协议
// 一并移除，主二进制里不再有任何协议代码。协议名与 scope 沿用内置时代的
// 原值（ModBus.TCP / ModBus.RTU / modbus），因此既有 iot_protocol 行、
// device.protocol_id 与前端表单都不需要迁移 —— 换的只是「谁持有实现」。
//
// 本插件自带全部协议实现（见 ./modbusdrv）：类型表、配置解析、PLC 地址解析、
// 区间规划、TCP/RTU 客户端与驱动都在插件进程内。本文件只做三件事：
//
//  1. 声明两个协议的名字、说明、串行独占性与配置表单；
//  2. 把 modbusdrv 的类型表与类型映射原样上报（Meta.Types / Meta.TypeMap）；
//  3. 用 modbusdrv.NewTCP / NewRTU 作工厂。
//
// 构建：
//
//	cd backend && go build -o ./plugins/iot-driver-modbus ./driversrc/modbus
//
// 注意：Serve 之前不做任何耗时自检。父进程消失时插件靠 stdin EOF 退出，
// 若在调用 Serve 前就卡在自检里，Unix 侧没有兜底机制，会留下孤儿进程。
package main

import (
	"encoding/json"

	"github.com/Ellisata/iot-gateway-drivers/modbus/modbusdrv"
	"github.com/Ellisata/iot-gateway-sdk/driver"
	"github.com/Ellisata/iot-gateway-sdk/driversdk"
)

const (
	// 协议名沿用内置时代的原值 —— 它们同时是 iot_protocol.name 与
	// device.protocol_id 的解析目标，改一个字就让既有设备静默采不到值。
	// 钉住它们的是 main_test.go 的 TestProtocolIdentityPinned
	// 与 pluginhost/testdata/parity/typemap/modbus.json 里的机器生成清单。
	protocolTCP = "ModBus.TCP"
	protocolRTU = "ModBus.RTU"

	// pluginScope 类型注册 scope，同样沿用原值。内置基线已随内置驱动清空，
	// 所以这里不再撞名（RegisterScope 拒绝与基线撞名，见 driver/dataTypeMap.go）。
	pluginScope = "modbus"
)

// formTCP Modbus TCP 的协议配置表单。
// 字段与内置 ModBus.TCP 的表单一致（见 migrations/008_add_protocol_20260821.sql），
// 已去掉 form-create 设计器产生的 _fc_id/name/_fc_drag_tag 等冗余字段。
const formTCP = `{
  "rule": [
    {"type":"input","field":"host","title":"{{$t.host_title}}","$required":true},
    {"type":"inputNumber","field":"port","title":"{{$t.port_title}}","$required":true},
    {"type":"inputNumber","field":"unitId","title":"{{$t.unitId_title}}","$required":true},
    {"type":"select","field":"wordOrder","title":"{{$t.wordOrder_title}}","$required":false,
     "options":[{"label":"{{$t.wordOrder_option_0}}","value":""},{"label":"{{$t.wordOrder_option_1}}","value":"BIG_ENDIAN"},{"label":"{{$t.wordOrder_option_2}}","value":"LITTLE_ENDIAN"}]},
    {"type":"select","field":"byteOrder","title":"{{$t.byteOrder_title}}","$required":false,
     "options":[{"label":"{{$t.wordOrder_option_0}}","value":""},{"label":"{{$t.wordOrder_option_1}}","value":"BIG_ENDIAN"},{"label":"{{$t.wordOrder_option_2}}","value":"LITTLE_ENDIAN"}]}
  ],
  "options": {
    "form":{"inline":false,"hideRequiredAsterisk":false,"labelPosition":"right","size":"default","labelWidth":"125px"},
    "resetBtn":{"show":false,"innerText":"重置"},
    "submitBtn":{"show":false,"innerText":"提交"},
    "language":{"zh-cn":{"host_title":"设备ip","port_title":"设备端口","unitId_title":"站号","wordOrder_title":"字序","wordOrder_option_0":"默认","wordOrder_option_1":"高字在前","wordOrder_option_2":"低字在前","byteOrder_title":"字节序"},"en":{"host_title":"Device IP","port_title":"Device port","unitId_title":"Unit ID","wordOrder_title":"Word order","wordOrder_option_0":"Default","wordOrder_option_1":"High word first","wordOrder_option_2":"Low word first","byteOrder_title":"Byte order"}}
  }
}`

// formRTU Modbus RTU 的协议配置表单。
//
// **与内置版的一处关键差异：这里必须列出全部串口链路参数。**
// 内置版的表单只有 comPort/unitId/byteOrder/wordOrder，靠"字段缺席 → 解析器
// 填默认值"兜底；但解析器的 modbusRTUConfigRaw 当时也**没有声明**这几个字段，
// 于是 baudRate/dataBits/stopBits/parity/timeoutMs 被钉死在 9600/8/1/N/5000，
// 表单与 protocol_json 两条路都改不了 —— 非 9600/8/1/N 的 RTU 设备永远连不上。
// 两者现已同步补全，并由 main_test.go 的 TestFormsMatchConfigStruct 与
// TestSerialParamsAreConfigurable 钉住，不会再漂移。
//
// 每项都带 value 默认值：表单上留空时"不填会发生什么"无从知晓，而把解析器
// 实际会用的值填进去，提交与不提交的行为才一致（照 dlt645/main.go 的做法）。
// comPort 是自由文本框而非下拉：现场端口号不可穷举（COM7、/dev/ttyUSB1 …），
// 而前端并未给 select 开 filterable/allow-create，写死选项等于配不了。
const formRTU = `{
  "rule": [
    {"type":"input","field":"comPort","title":"{{$t.comPort_title}}","$required":true,"value":"COM1",
     "info":"{{$t.comPort_info}}"},
    {"type":"select","field":"baudRate","title":"{{$t.baudRate_title}}","$required":false,"value":"9600",
     "info":"{{$t.baudRate_info}}",
     "options":[{"label":"1200","value":"1200"},{"label":"2400","value":"2400"},{"label":"4800","value":"4800"},{"label":"9600","value":"9600"},{"label":"19200","value":"19200"},{"label":"38400","value":"38400"},{"label":"57600","value":"57600"},{"label":"115200","value":"115200"}]},
    {"type":"select","field":"dataBits","title":"{{$t.dataBits_title}}","$required":false,"value":"8",
     "info":"{{$t.dataBits_info}}",
     "options":[{"label":"8","value":"8"},{"label":"7","value":"7"},{"label":"6","value":"6"},{"label":"5","value":"5"}]},
    {"type":"select","field":"stopBits","title":"{{$t.stopBits_title}}","$required":false,"value":"1",
     "options":[{"label":"1","value":"1"},{"label":"2","value":"2"}]},
    {"type":"select","field":"parity","title":"{{$t.parity_title}}","$required":false,"value":"N",
     "info":"{{$t.parity_info}}",
     "options":[{"label":"{{$t.parity_option_0}}","value":"N"},{"label":"{{$t.parity_option_1}}","value":"E"},{"label":"{{$t.parity_option_2}}","value":"O"}]},
    {"type":"inputNumber","field":"timeoutMs","title":"{{$t.timeoutMs_title}}","$required":false,"value":5000,
     "info":"{{$t.timeoutMs_info}}"},
    {"type":"inputNumber","field":"unitId","title":"{{$t.unitId_title}}","$required":true,"value":1},
    {"type":"select","field":"byteOrder","title":"{{$t.byteOrder_title}}","$required":false,"value":"",
     "options":[{"label":"{{$t.byteOrder_option_0}}","value":""},{"label":"{{$t.byteOrder_option_1}}","value":"BIG_ENDIAN"},{"label":"{{$t.byteOrder_option_2}}","value":"LITTLE_ENDIAN"}]},
    {"type":"select","field":"wordOrder","title":"{{$t.wordOrder_title}}","$required":false,"value":"",
     "options":[{"label":"{{$t.byteOrder_option_0}}","value":""},{"label":"{{$t.byteOrder_option_1}}","value":"BIG_ENDIAN"},{"label":"{{$t.byteOrder_option_2}}","value":"LITTLE_ENDIAN"}]}
  ],
  "options": {
    "form":{"inline":false,"hideRequiredAsterisk":false,"labelPosition":"right","size":"default","labelWidth":"125px"},
    "resetBtn":{"show":false,"innerText":"重置"},
    "submitBtn":{"show":false,"innerText":"提交"},
    "language":{"zh-cn":{"comPort_title":"设备端口","comPort_info":"如 COM3、COM12（Windows）或 /dev/ttyUSB0、/dev/ttyS0（Linux）；须与网关设备上实际枚举出的端口名完全一致","baudRate_title":"波特率","baudRate_info":"默认 9600；必须与从站设备完全一致，不一致表现为无应答或 CRC 校验错","dataBits_title":"数据位","dataBits_info":"默认 8；Modbus RTU 绝大多数设备为 8 位","stopBits_title":"停止位","parity_title":"校验位","parity_info":"默认无校验；不少设备用偶校验，必须与从站一致","parity_option_0":"无校验","parity_option_1":"偶校验","parity_option_2":"奇校验","timeoutMs_title":"单帧超时(ms)","timeoutMs_info":"默认 5000；从站响应慢或波特率很低时可调大。过小会把应答帧截断，表现为偶发 CRC 校验错而非明确超时","unitId_title":"站号","byteOrder_title":"字节序","byteOrder_option_0":"默认","byteOrder_option_1":"高字在前","byteOrder_option_2":"低字在前","wordOrder_title":"字序"},"en":{"comPort_title":"Device port","comPort_info":"For example COM3 or COM12 (Windows) or /dev/ttyUSB0 or /dev/ttyS0 (Linux). It must exactly match the port name enumerated on the gateway host","baudRate_title":"Baud rate","baudRate_info":"Defaults to 9600. It must match the slave device exactly. A mismatch shows up as no response or a CRC error","dataBits_title":"Data bits","dataBits_info":"Defaults to 8. Most Modbus RTU devices use 8 data bits","stopBits_title":"Stop bits","parity_title":"Parity","parity_info":"Defaults to no parity. Many devices use even parity, which must match the slave","parity_option_0":"None","parity_option_1":"Even","parity_option_2":"Odd","timeoutMs_title":"Frame timeout (ms)","timeoutMs_info":"Defaults to 5000. Increase it when the slave responds slowly or the baud rate is very low. Too small truncates the response frame, which shows up as intermittent CRC errors rather than a clear timeout","unitId_title":"Unit ID","byteOrder_title":"Byte order","byteOrder_option_0":"Default","byteOrder_option_1":"High word first","byteOrder_option_2":"Low word first","wordOrder_title":"Word order"}}
  }
}`

// meta 插件的协议清单。等价于内置驱动在 init() 里做的事：
// 驱动注册（由工厂承担）、类型注册、类型映射、协议配置表单。
func meta() driversdk.Meta {
	return driversdk.Meta{
		APIVersion: driversdk.APIVersion,
		Protocols: []driversdk.Protocol{
			{
				Name:        protocolTCP,
				Description: "Modbus TCP",
				FormJSON:    json.RawMessage(formTCP),
				// Modbus TCP 支持局域网扫描发现。
				// **RTU 刻意不声明**：串口协议的目标模型是 comPort 而不是 host/port，
				// 扫描（网段内找 IP）这套根本套不上去；串口上"总线上有哪些站号"
				// 是另一个功能。Meta.Validate 也会拒绝 SerialExclusive + Discovery
				// 的组合，所以这里不声明不是疏忽而是唯一正确的写法。
				Discovery: modbusdrv.Discovery(),
				// 点位扫描与局域网扫描相反，**两个协议都声明**（见下）。
				PointDiscovery: modbusdrv.PointDiscovery(),
			},
			{
				Name: protocolRTU,
				// RTU 独占串行总线：采集引擎据此整轮持设备锁，
				// 保证总线上帧不被交错。TCP 为 false。
				SerialExclusive: true,
				Description:     "Modbus RTU 串口",
				FormJSON:        json.RawMessage(formRTU),
				// **与 Discovery 的那条禁令正相反**：串口协议可以、且应当声明点位
				// 扫描。这条不对称是刻意的 —— Discovery 的目标是"网段里的一个 IP"，
				// 与串口无关；而点位扫描的目标是**一台已存在的设备**，串口设备同样有
				// "它有哪些点位"这个问题，而且串口恰是会话复用最重要的场景
				//（回落临时实例会抢占串口句柄，把采集打断）。
				// driversdk 的两个校验函数也是分开写的，就是为了让这条差异活下来。
				PointDiscovery: modbusdrv.PointDiscovery(),
			},
		},
		Scope:       pluginScope,
		Description: "Modbus",
		// 1.2.1：本地就解析不出来的地址（如 "%MW1x"）不再让整批 Read 失败 —— 它在
		// 规划前被拆出去，只让那一个点位标 QualityBadAddress（报警「地址无效」）。
		// 此前这种地址会让 Read 报错，采集侧据此把**在线**的设备判为断联并推通知。
		//
		// 1.2.0：采集路径的区间失败改为有界对半降级 —— 区间里混进一个设备上不存在的
		// 寄存器时，只有那个点位保持 quality=0，同区间其余点位照常取值。此前是整段
		// 一起标 0，而旧的「含空洞才降级」那条路既够不到连续块（坏地址落在块里是
		// 最常见的形态），又会在任一子段失败时把已经读到的兄弟子段一并丢掉。
		// 降级结果进规划缓存（带 5 分钟 TTL，设备侧补上寄存器后自愈）。
		//
		// 1.1.0：RTU 表单补齐串口链路参数（此前 baudRate/dataBits/stopBits/
		// parity/timeoutMs 既不在表单里、解析层也读不到）。
		// 仅便于现场辨认，不参与握手判据（那由 APIVersion 把关）。
		Version: version,
		Types:   modbusdrv.Types(),
		TypeMap: modbusdrv.CommonTypeMap(),
	}
}

// factories 协议名 → 工厂。键必须与 meta().Protocols[].Name 逐一对应，
// 否则 Serve 会在启动时报错（main_test.go 也钉住了这条不变式）。
func factories() driversdk.Factories {
	return driversdk.Factories{
		protocolTCP: modbusdrv.NewTCP,
		protocolRTU: modbusdrv.NewRTU,
	}
}

// probers 协议名 → 扫描探测实现。键必须与 meta() 里声明了 Discovery 的协议
// 逐一对应，否则 Serve 会在启动时报错（main_test.go 的 TestProbersMatchDiscovery
// 也钉住了这条不变式）。
//
// 只有 TCP 有探测实现 —— RTU 是串口，见 meta() 里的说明。
func probers() driversdk.Probers {
	return driversdk.Probers{
		protocolTCP: func(req driversdk.ProbeRequest) ([]driver.DiscoveredDevice, error) {
			return modbusdrv.ProbeTCP(req.Target, req.Options, req.Budget)
		},
	}
}

// finders 协议名 → 点位扫描实现。键必须与 meta() 里声明了 PointDiscovery 的协议
// 逐一对应，否则 Serve 会在启动时报错（main_test.go 的
// TestPointFindersMatchPointDiscovery 也钉住了这条不变式）。
//
// **TCP 与 RTU 都有** —— 与 probers 只挂 TCP 正相反：点位扫描的目标是一台已存在
// 的设备，串口设备同样需要它，而且串口恰是会话复用最重要的场景。
func finders() driversdk.PointFinders {
	find := func(req driversdk.FindPointsRequest) (*driver.PointScanResult, error) {
		return modbusdrv.ScanPoints(req.Session, req.Options, req.Deadline)
	}
	return driversdk.PointFinders{
		protocolTCP: find,
		protocolRTU: find,
	}
}

// version is overridden at build time with -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	driversdk.Main(factories(), meta(), driversdk.Capabilities{
		Probers:      probers(),
		PointFinders: finders(),
	})
}
