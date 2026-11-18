// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

// Command iot-driver-fanuc 是 FANUC 机器人 EtherNet/IP（CIP 显式消息）协议的
// 进程外插件实现。
//
// 支持的控制器：R-30iA / R-30iA Mate / R-30iB / R-30iB Plus。
//
// 三代共有的能力是数值寄存器 R[]：
//
//	R[n] + int32 / uint32  → 厂商对象 0x6B（32 位整型）
//	R[n] + float32         → 厂商对象 0x6C（32 位实型，控制器软件需 V7.40+）
//
// 连续寄存器默认按 100 个一块（maxRegistersPerRead，上限 124）用
// Get_Attribute_All 一次读回；块读编码首次会用单点读交叉校验，设备拒绝或
// 数据错位时自动退回单读并暂停块读 5 分钟。
//
// **P0 刻意不开放位置寄存器（PR/CURPOS）**：它们只在 R-30iB（V8.10+）与
// Plus 上存在，且载荷是 40/44 字节的复合结构，轴序与状态字的语义需要真机
// 核对。现在把 PR[n] 报成"地址无效"，比在 R-30iA 上读出一段错位数据安全。
// 同理不做 DI/DO/GI/GO（Assembly 实例由机器人侧连接配置决定）与 SR
// （字符串载荷格式未核实）。
//
// 协议名与 scope 一旦发布即冻结：它们同时是 iot_protocol.name 与
// device.protocol_id 的解析目标（main_test.go 的 TestProtocolIdentityPinned）。
//
// 构建：
//
//	go build -o ./plugins/iot-driver-fanuc ./fanuc
//
// 注意：Serve 之前不做任何耗时自检。父进程消失时插件靠 stdin EOF 退出，
// 若在调用 Serve 前就卡在自检里，Unix 侧没有兜底机制，会留下孤儿进程。
package main

import (
	"encoding/json"

	"github.com/Ellisata/iot-gateway-sdk/driversdk"

	fanucdrv "github.com/Ellisata/iot-gateway-drivers/fanuc/fanucdrv"
)

const (
	// protocolName 协议名，同时是 iot_protocol.name 与 device.protocol_id
	// 的解析目标。新协议，一旦发布即冻结。
	protocolName = "FANUC.CIP"

	// pluginScope 类型注册 scope，与协议名一起冻结。
	pluginScope = "fanuc"
)

// formFANUC FANUC CIP 的协议配置表单（form-create schema）。
//
// 字段名与 fanucdrv.ConfigFieldNames() 一一对应 —— 表单里的 field 就是
// Connect 收到的 protocol_json 的键，改一边必须改另一边（main_test 钉住了）。
//
// 只暴露现场需要调的四个参数。内部安全边界（单轮预算、重连退避、显式连接数）
// 不在这里出现：暴露出去只会让用户在不理解后果的情况下绕过限流，而机器人
// 控制器还要服务示教器与产线程序。
const formFANUC = `{
  "rule": [
    {"type":"input","field":"host","title":"{{$t.host_title}}","$required":true,"value":""},
    {"type":"inputNumber","field":"port","title":"{{$t.port_title}}","$required":false,"value":44818},
    {"type":"inputNumber","field":"timeoutMs","title":"{{$t.timeoutMs_title}}","$required":false,"value":5000},
    {"type":"inputNumber","field":"maxRequestsPerSecond","title":"{{$t.maxRequestsPerSecond_title}}","$required":false,"value":10},
    {"type":"inputNumber","field":"maxRegistersPerRead","title":"{{$t.maxRegistersPerRead_title}}","$required":false,"value":100,"info":"{{$t.maxRegistersPerRead_info}}"}
  ],
  "options": {
    "form":{"inline":false,"hideRequiredAsterisk":false,"labelPosition":"right","size":"default","labelWidth":"150px"},
    "resetBtn":{"show":false,"innerText":"重置"},
    "submitBtn":{"show":false,"innerText":"提交"},
    "language":{"zh-cn":{"host_title":"机器人IP","port_title":"端口","timeoutMs_title":"请求超时(ms)","maxRequestsPerSecond_title":"每秒请求上限","maxRegistersPerRead_title":"块读寄存器数","maxRegistersPerRead_info":"单次 Get_Attribute_All 连续读取的寄存器数，上限 124。设为 1 关闭块读、逐点单读。首次块读会用单点读交叉校验，设备拒绝或不匹配时自动回退单读并暂停块读 5 分钟"},"en":{"host_title":"Robot IP","port_title":"Port","timeoutMs_title":"Request timeout (ms)","maxRequestsPerSecond_title":"Requests per second limit","maxRegistersPerRead_title":"Registers per block read","maxRegistersPerRead_info":"Registers per Get_Attribute_All request, max 124. Set 1 to disable block reads and read point by point. The first block read is cross-checked with a single read. On rejection or mismatch the driver falls back to single reads and pauses block reads for 5 minutes"}}
  }
}`

// meta 插件的协议清单。
//
// 单协议、非串行独占、可写：
//   - CIP 显式消息是 TCP 请求/响应，不存在串行总线上的帧交错；
//   - 写能力只到 R[]（见 fanucdrv/write.go），网关侧仍需 control.enabled
//     与点位 rw_permission 才会真正下发。
func meta() driversdk.Meta {
	return driversdk.Meta{
		APIVersion: driversdk.APIVersion,
		Protocols: []driversdk.Protocol{
			{
				Name:            protocolName,
				SerialExclusive: false,
				Writable:        true,
				Description:     "FANUC 机器人 EtherNet/IP（CIP 显式消息）",
				FormJSON:        json.RawMessage(formFANUC),
				// 扫描与点位扫描都未声明：ListIdentity 只能发现设备，但
				// "这是一台 FANUC 机器人"与"它有哪些寄存器"是两件事；
				// 在真机核对完寄存器范围之前，不放出会返回空清单的按钮。
			},
		},
		Scope:       pluginScope,
		Description: "FANUC CIP",
		Version:     version,
		Types:       fanucdrv.Types(),
		TypeMap:     fanucdrv.CommonTypeMap(),
	}
}

// factories 协议名 → 工厂。键必须与 meta().Protocols[].Name 逐一对应，
// 否则 Serve 会在启动时报错（main_test 也钉住了这条不变式）。
func factories() driversdk.Factories {
	return driversdk.Factories{
		protocolName: fanucdrv.NewDriver,
	}
}

// version is overridden at build time with -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	driversdk.Main(factories(), meta(), driversdk.Capabilities{})
}
