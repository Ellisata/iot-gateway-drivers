// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package cip

import (
	"encoding/binary"
	"fmt"
	"net"
)

// ==================== EtherNet/IP ListIdentity ====================
//
// ListIdentity 是 EtherNet/IP 的标准身份查询（封装层命令 0x0063）：发一条空载荷
// 请求，设备回一条自述身份（厂商、设备类型、产品名、版本、序列号）。
//
// 本插件只用它做一件事：**连接前确认对面是 FANUC 机器人**。把驱动指到一台
// Allen-Bradley PLC 上时，后续的 0x6B/0x6C 读写可能"恰好"成功并写坏对方的
// 数据，这种事故不值得用"用户填错了 IP"来兜底。
//
// 字节序是这里最容易错的地方：封装头与 Identity 项的字段是小端，而 Socket
// Address 里的端口与 IP 是网络序（大端）—— 这条不对称来自规范本身。

// ItemIdentity CIP Identity Item 的项类型码（EtherNet/IP spec vol.2, Table 2-6.1）。
const ItemIdentity = 0x000C

// VendorFANUC FANUC 在 ODVA 的厂商编号（591 / 0x024F）。
const VendorFANUC = 591

// BuildListIdentity 构建 ListIdentity 请求（空载荷、会话句柄 0）。
func BuildListIdentity() []byte {
	return BuildEncapHeader(CmdListIdentity, 0, 0)
}

// Identity 一台 EtherNet/IP 设备自述的身份。
type Identity struct {
	EncapVersion  uint16
	VendorID      uint16
	DeviceType    uint16
	ProductCode   uint16
	RevisionMajor byte
	RevisionMinor byte
	Status        uint16
	SerialNumber  uint32
	ProductName   string
	State         byte

	// IP/Port 设备自述的通信地址。部分设备会填 0.0.0.0，调用方应回落。
	IP   string
	Port int
}

// ParseListIdentity 解析一条 ListIdentity 应答，返回其中全部 Identity 项。
//
// 单条坏项跳过而不是让整条应答失败 —— 一条坏项不该让同一帧里其它可用的
// 身份一起消失。
func ParseListIdentity(resp []byte) ([]Identity, error) {
	cmd, _, status, data, err := ParseEncapResponse(resp)
	if err != nil {
		return nil, err
	}
	if status != 0 {
		return nil, fmt.Errorf("cip: list identity status 0x%08X", status)
	}
	if cmd != CmdListIdentity {
		return nil, fmt.Errorf("cip: unexpected list identity response command 0x%04X", cmd)
	}
	return parseIdentityItems(data)
}

func parseIdentityItems(data []byte) ([]Identity, error) {
	if len(data) < 2 {
		return nil, fmt.Errorf("cip: short list identity payload (%d bytes)", len(data))
	}
	count := int(binary.LittleEndian.Uint16(data[0:2]))

	var out []Identity
	offset := 2
	for i := 0; i < count; i++ {
		if offset+4 > len(data) {
			return nil, fmt.Errorf("cip: list identity item %d header truncated", i)
		}
		itemType := binary.LittleEndian.Uint16(data[offset : offset+2])
		itemLen := int(binary.LittleEndian.Uint16(data[offset+2 : offset+4]))
		offset += 4
		if offset+itemLen > len(data) {
			return nil, fmt.Errorf("cip: list identity item %d length %d truncated", i, itemLen)
		}
		body := data[offset : offset+itemLen]
		offset += itemLen

		if itemType != ItemIdentity {
			continue
		}
		if id, ok := parseIdentityItem(body); ok {
			out = append(out, id)
		}
	}
	return out, nil
}

const identityFixedLen = 32

// parseIdentityItem 解析一个 Identity 项的项体。
//
//	0  : 封装协议版本(2 LE)
//	2  : Socket Address(16)
//	18 : 厂商 ID(2 LE)
//	20 : 设备类型(2 LE)
//	22 : 产品代码(2 LE)
//	24 : 版本 major(1) + minor(1)
//	26 : 状态字(2 LE)
//	28 : 序列号(4 LE)
//	32 : 产品名（1 字节长度 + 字符，总长补齐到偶数）
//	32+产品名 : 状态字节(1)
func parseIdentityItem(body []byte) (Identity, bool) {
	if len(body) < identityFixedLen+1 {
		return Identity{}, false
	}

	var id Identity
	id.EncapVersion = binary.LittleEndian.Uint16(body[0:2])
	id.IP, id.Port = parseSocketAddress(body[2:18])
	id.VendorID = binary.LittleEndian.Uint16(body[18:20])
	id.DeviceType = binary.LittleEndian.Uint16(body[20:22])
	id.ProductCode = binary.LittleEndian.Uint16(body[22:24])
	id.RevisionMajor = body[24]
	id.RevisionMinor = body[25]
	id.Status = binary.LittleEndian.Uint16(body[26:28])
	id.SerialNumber = binary.LittleEndian.Uint32(body[28:32])

	nameLen := int(body[identityFixedLen])
	nameEnd := identityFixedLen + 1 + nameLen
	if nameEnd > len(body) {
		// 长度字段比项体还长：整项作废而不是截断取用 —— 型号错了比空着更糟。
		return Identity{}, false
	}
	id.ProductName = string(body[identityFixedLen+1 : nameEnd])
	if nameEnd < len(body) {
		id.State = body[nameEnd]
	}
	return id, true
}

// parseSocketAddress 解析 16 字节 Socket Address：端口与 IP 是网络序。
func parseSocketAddress(b []byte) (string, int) {
	if len(b) < 16 {
		return "", 0
	}
	port := int(binary.BigEndian.Uint16(b[2:4]))
	ip := net.IPv4(b[4], b[5], b[6], b[7])
	if ip.Equal(net.IPv4zero) {
		return "", port
	}
	return ip.String(), port
}
