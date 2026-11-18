// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package fanuc

import "strings"

// protocolScope 类型注册作用域前缀，同时也是 driversdk.Meta.Scope。
//
// 与协议名一起**发布即冻结**：它决定网关把点位 data_type 存成 "fanuc.int32"
// 还是别的名字，改一个字就让存量点位在 NormalizeAddressType 里被判为漂移。
const protocolScope = "fanuc"

// FANUC 厂商对象（EtherNet/IP 显式消息）。
//
// 这是 R-30iA / R-30iB / R-30iB Plus 共有的两个数值寄存器对象：
//
//	0x6B  整型寄存器（32 位有符号）
//	0x6C  实型寄存器（32 位 IEEE-754 float）
//
// 两者互斥：用 0x6B 读一个 REAL 寄存器会回 0x14 Undefined Attribute，反之亦然。
// 所以点位声明的 data_type 决定走哪个类，**不能猜**。
//
// 0x6C 需要控制器软件 V7.40 或更高（R-30iA 的早期版本没有），
// 读失败由点位级质量码体现，不影响整台设备。
const (
	objectIntegerRegister uint16 = 0x6B
	objectRealRegister    uint16 = 0x6C

	// registerInstance FANUC 数值寄存器对象只有实例 1，属性号即寄存器号。
	registerInstance uint32 = 1

	// maxRegisterIndex 属性号上限（CIP 属性 ID 为 16 位）。
	maxRegisterIndex = 65535
)

// 内部类型裸名。它们在 typemap.go 里映射到通用类型名，在 address 的
// data_type 列里逐字存下来（网关侧会拼上 scope 前缀存成 "fanuc.int32"）。
const (
	typeInt32   = "int32"
	typeUint32  = "uint32"
	typeFloat32 = "float32"
)

// typeKind 解析内部类型名对应的类别 Kind，供构建 ReadResult 时填充。
// 未注册类型返回 ""（推送通道回退为数值推断）。
func typeKind(name string) string {
	if dt, ok := fanucLookup(strings.ToLower(name)); ok {
		return dt.Kind
	}
	return ""
}
