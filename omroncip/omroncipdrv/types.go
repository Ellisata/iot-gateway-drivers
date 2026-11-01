// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package cip

import "strings"

// protocolName CIP 协议类型注册作用域名（TypeRegistry 前缀）。
const protocolName = "cip"

// 协议注册名（driver.Register）。CIP 仅 TCP（EtherNet/IP 显式报文）一种传输，单协议名。
const ProtocolCIP = "Omron.CIP"

// 协议默认值
const (
	defaultPort      = 44818 // EtherNet/IP 显式报文标准端口
	defaultTimeoutMS = 5000
	defaultStringLen = 80 // STRING 标签读取最大字节数
)

// CIP 服务码
const (
	cipServiceDataTableRead = 0x4C // Omron 专有：数据表读取（Data Table Read）
	cipServiceVariableWrite = 0x4D // Omron：变量写入（Write Service for Variables）
	cipServiceMultipleRead  = 0x0A // 多服务请求：一个报文内拼接多个 0x4C 读服务
)

// Omron 变量服务的数据类型码。
//
// 这些码与 CIP Common 类型码一致（W506 附录 A / Z396 §8-5 的 "Data Type Code"
// 表）：BOOL=C1、SINT=C2、INT=C3、DINT=C4、USINT=C6、UINT=C7、UDINT=C8、
// REAL=CA、LREAL=CB、STRING=D0、BYTE=D1、WORD=D2、DWORD=D3。
//
// 早期版本这里有一组错位的取值（如 SINT=D2、UINT=C8、WORD=CA），0x4C 读
// 请求并不携带类型码，所以读路径只在**批量读响应按类型码折算长度**时受影响；
// 写服务（0x4D）必须把类型码显式发给设备，错位的值会让设备按错误类型解析
// 数据长度。这里按手册统一修正，读写共用同一张表。
const (
	cipTypeBool   = 0x00C1 // BOOL
	cipTypeSINT   = 0x00C2 // SINT：int8
	cipTypeINT    = 0x00C3 // INT：int16
	cipTypeDINT   = 0x00C4 // DINT：int32
	cipTypeUSINT  = 0x00C6 // USINT：uint8
	cipTypeUINT   = 0x00C7 // UINT：uint16
	cipTypeUDINT  = 0x00C8 // UDINT：uint32
	cipTypeREAL   = 0x00CA // REAL：float32
	cipTypeLREAL  = 0x00CB // LREAL：float64
	cipTypeSTRING = 0x00D0 // STRING
	cipTypeWORD   = 0x00D2 // WORD：16 位
	cipTypeDWORD  = 0x00D3 // DWORD：32 位
)

// cipTypeCodeByTypeName 内部类型名 → Omron 数据表类型码（构建 0x4C 请求用）。
// bcd/lbcd 无原生类型码，以 WORD/DWORD 读回后本地 BCD 解码（与 FINS 语义一致）。
var cipTypeCodeByTypeName = map[string]uint16{
	"bool":    cipTypeBool,
	"int8":    cipTypeSINT,
	"int16":   cipTypeINT,
	"int32":   cipTypeDINT,
	"uint8":   cipTypeUSINT,
	"uint16":  cipTypeUINT,
	"word":    cipTypeWORD,
	"uint32":  cipTypeUDINT,
	"float32": cipTypeREAL,
	"float64": cipTypeLREAL,
	"string":  cipTypeSTRING,
	"bcd":     cipTypeWORD,
	"lbcd":    cipTypeDWORD,
}

// cipTypeCode 查询内部类型名对应的 Omron 数据表类型码。
func cipTypeCode(dataType string) (uint16, bool) {
	if code, ok := cipTypeCodeByTypeName[dataType]; ok {
		return code, true
	}
	// 与 cipLookup 的大小写回退保持一致：历史配置里可能存着 "INT16"。
	code, ok := cipTypeCodeByTypeName[strings.ToLower(dataType)]
	return code, ok
}

// cipTypeSizeByCode Omron 数据表类型码 → 固定字节数（0x0A 多服务响应按此切分各标签数据）。
// 与 cipTypeCodeByTypeName 互逆；string 等动态长度类型不在其中（批量读不收纳）。
var cipTypeSizeByCode = map[uint16]int{
	cipTypeBool:  1,
	cipTypeSINT:  1,
	cipTypeINT:   2,
	cipTypeDINT:  4,
	cipTypeUSINT: 1,
	cipTypeUINT:  2,
	cipTypeUDINT: 4,
	cipTypeWORD:  2,
	cipTypeDWORD: 4,
	cipTypeREAL:  4,
	cipTypeLREAL: 8,
}

// cipTypeSize 查询类型码对应的固定数据字节数。
func cipTypeSize(code uint16) (int, bool) {
	size, ok := cipTypeSizeByCode[code]
	return size, ok
}

// cipFixedSizeType 判断类型是否为固定字节长度（非 string 等动态长度类型）。
// 0x0A 批量读响应按固定尺寸切分，动态长度类型只能单读。
func cipFixedSizeType(dataType string) bool {
	dt, ok := cipLookup(dataType)
	return ok && dt.Size > 0
}
