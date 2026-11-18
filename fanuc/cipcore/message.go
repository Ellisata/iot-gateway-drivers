// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package cip

import (
	"fmt"
)

// CIP 服务码。
//
// FANUC 机器人通过 Get_Attribute_Single / Set_Attribute_Single 访问数值寄存器
// （类 0x6B 整型 / 0x6C 实型）与位置对象（类 0x7B–0x7E），不提供 class 级服务，
// 因此本包只实现 instance + attribute 的单点读写。
const (
	ServiceGetAttributeSingle byte = 0x0E
	ServiceSetAttributeSingle byte = 0x10
	ServiceGetAttributeAll    byte = 0x01 // 预留给后续的块读（FANUC 最多前 124 个）
	ServiceSetAttributeAll    byte = 0x02 // 预留给后续的块写（FANUC 最多前 115 个）
)

// 逻辑段类型（CIP spec vol.1, 3-4.4 Logical Segment）。
//
// 首字节同时编码「段类型」与「数值宽度」：bit7-5=001(逻辑段)，bit4-2=段类型
// （类/实例/属性），bit1-0=格式（0=8 位，1=16 位，2=32 位）。
// 于是类/实例/属性的 8 位段分别是 0x20/0x24/0x30，16 位段是 0x21/0x25/0x31。
const (
	segmentClass     byte = 0x20 // 类段
	segmentInstance  byte = 0x24 // 实例段
	segmentAttribute byte = 0x30 // 属性段
)

// MaxBlockRegisters 单次 Get_Attribute_All 块读最多携带的寄存器数。
//
// FANUC 手册给出的上限是 124（Set_Attribute_All 的上限是 115）。超过上限时
// 设备回 0x15（too much data），所以这里在构造侧就钳住，避免发出注定失败的请求。
const MaxBlockRegisters = 124

// BuildGetAttributeSingle 构建 Get_Attribute_Single 的 CIP 请求（不含封装层）。
func BuildGetAttributeSingle(class uint16, instance uint32, attr uint16) []byte {
	return buildAttributeRequest(ServiceGetAttributeSingle, class, instance, attr, nil)
}

// BuildGetAttributeBlock 构建 FANUC 的「块读」请求：
// 从 start 号寄存器起连续读 count 个（Get_Attribute_All，服务 0x01）。
//
// 编码来自 FANUC EtherNet/IP 手册的显式消息示例（读 R[6]–R[15]）：
//
//	Service   = 0x01 (Get_Attribute_All)
//	Instance  = (count << 8) | 1   → 10 个寄存器为 0x0A01
//	Attribute = 起始寄存器号       → R[6] 为 0x06
//	Class     = 0x6B（整型）/ 0x6C（实型）
//
// 也就是说：**低字节固定 1（对象唯一实例），高字节是数量，起始号放在属性段**。
// 这与标准 CIP 的 Get_Attribute_All 语义不同（标准是从属性 0 读全部），
// 是 FANUC 的厂商扩展；写错时设备通常回 0x14/0x15，驱动会退回到单点读，
// 不会静默拿到错误数据（首次块读还会用单点读交叉校验，见 driver.Read）。
func BuildGetAttributeBlock(class uint16, start uint16, count int) []byte {
	if count < 1 {
		count = 1
	}
	if count > MaxBlockRegisters {
		count = MaxBlockRegisters
	}
	instance := uint32(count)<<8 | uint32(registerInstanceLowByte)
	return buildAttributeRequest(ServiceGetAttributeAll, class, instance, start, nil)
}

// registerInstanceLowByte FANUC 数值寄存器对象的唯一实例号（1），
// 块读时占用 instance 的低 8 位。
const registerInstanceLowByte = 1

// BuildSetAttributeSingle 构建 Set_Attribute_Single 的 CIP 请求。
func BuildSetAttributeSingle(class uint16, instance uint32, attr uint16, data []byte) []byte {
	return buildAttributeRequest(ServiceSetAttributeSingle, class, instance, attr, data)
}

// buildAttributeRequest 拼装「服务 | 路径长度 | 类/实例/属性路径 | 服务数据」。
//
// 路径长度字段的单位是**字（2 字节）**，不是字节 —— 这是 CIP 报文最常见的
// 一个错位来源：少乘/多乘 2 的症状是设备回 0x04 路径段错误，看起来像对象不存在。
func buildAttributeRequest(service byte, class uint16, instance uint32, attr uint16, data []byte) []byte {
	path := make([]byte, 0, 12)
	path = appendLogicalSegment(path, segmentClass, uint32(class))
	path = appendLogicalSegment(path, segmentInstance, instance)
	path = appendLogicalSegment(path, segmentAttribute, uint32(attr))

	out := make([]byte, 0, 2+len(path)+len(data))
	out = append(out, service)
	out = append(out, byte(len(path)/2)) // 路径长度按字计
	out = append(out, path...)
	out = append(out, data...)
	return out
}

// appendLogicalSegment 按数值宽度选择 8/16/32 位逻辑段。
//
//	8 位 : [type, value]                        （2 字节）
//	16 位: [type+1, lo, hi, pad]                （4 字节）
//	32 位: [type+2, b0, b1, b2, b3, pad]        （6 字节）
//
// 路径必须按字对齐，所以 16/32 位段末尾各补一个 0。8 位段天然 2 字节，
// **不需要也不允许**再补 —— 多补一个 0 会让后续段整体错位一位，设备回 0x04。
func appendLogicalSegment(path []byte, segType byte, value uint32) []byte {
	switch {
	case value <= 0xFF:
		return append(path, segType, byte(value))
	case value <= 0xFFFF:
		return append(path, segType+1, byte(value), byte(value>>8), 0x00)
	default:
		return append(path, segType+2,
			byte(value), byte(value>>8), byte(value>>16), byte(value>>24), 0x00)
	}
}

// ParseExplicitResponse 解析 CIP 显式报文响应，返回服务数据区。
//
// 响应布局：
//
//	服务(1) | 保留(1) | 通用状态(1) | 附加状态字数(1) | 附加状态字(n×2) | 数据
//
// 通用状态非 0 时返回 *GeneralStatusError（设备已响应，连接仍可用）。
// 响应服务码按规范是请求服务码 | 0x80，但个别实现会原样回填，因此这里只比较低 7 位。
func ParseExplicitResponse(resp []byte, wantService byte) ([]byte, error) {
	if len(resp) < 4 {
		return nil, fmt.Errorf("cip: short explicit response (%d bytes)", len(resp))
	}
	if resp[0]&0x7F != wantService&0x7F {
		return nil, fmt.Errorf("cip: unexpected response service 0x%02X (want 0x%02X)", resp[0], wantService|0x80)
	}
	status := resp[2]
	addlSize := int(resp[3])
	headerLen := 4 + 2*addlSize
	if len(resp) < headerLen {
		return nil, fmt.Errorf("cip: explicit response additional status truncated (%d bytes)", len(resp))
	}
	if status != 0 {
		return nil, NewGeneralStatusError(status)
	}
	return resp[headerLen:], nil
}
