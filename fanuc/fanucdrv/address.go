// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package fanuc

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// registerAddress 一个解析后的 FANUC 数值寄存器点位。
//
// 地址只描述"哪个寄存器"，类型由点位的 data_type 决定（int32/uint32 → 0x6B，
// float32 → 0x6C）。把类型写进地址会让同一台设备上"改类型"变成"改地址"，
// 而网关的类型漂移逻辑正是按 data_type 判定的。
type registerAddress struct {
	Name     string // 原始地址（用于日志）
	Index    uint16 // 寄存器号，即 CIP 属性号
	Class    uint16 // 0x6B 或 0x6C
	DataType string // 内部类型裸名（小写）
	Kind     string // 数据类型类别
}

// registerPattern 接受 R[1]、r[ 12 ] 这类写法；拒绝 R[0]、R[]、R[1].X。
var registerPattern = regexp.MustCompile(`(?i)^\s*r\s*\[\s*([0-9]+)\s*\]\s*$`)

// parseRegisterAddress 解析 FANUC 数值寄存器地址，并按 data_type 选择对象类。
//
// P0 只支持 R[]：位置寄存器（PR/CURPOS）在 R-30iB/Plus 上是另一组对象，
// 且载荷是 40/44 字节的复合结构，需要真机确认后再开（见 README 的路线图）。
// 现在明确报"不支持该地址"，比读出一段错位的数据安全。
func parseRegisterAddress(name, dataType string) (registerAddress, error) {
	raw := strings.TrimSpace(name)
	m := registerPattern.FindStringSubmatch(raw)
	if m == nil {
		return registerAddress{}, fmt.Errorf("fanuc: 地址 %q 无效，期望 R[n]（如 R[5]）", name)
	}

	idx64, err := strconv.ParseUint(m[1], 10, 16)
	if err != nil {
		return registerAddress{}, fmt.Errorf("fanuc: 地址 %q 的寄存器号超出范围（1..%d）", name, maxRegisterIndex)
	}
	idx := uint16(idx64)
	if idx == 0 {
		// 属性 0 在 CIP 里是类级属性，不是第 0 号寄存器；FANUC 寄存器从 R[1] 起。
		return registerAddress{}, fmt.Errorf("fanuc: 地址 %q 无效，寄存器号从 1 开始", name)
	}

	dt := strings.ToLower(strings.TrimSpace(dataType))
	var class uint16
	switch dt {
	case typeInt32, typeUint32:
		class = objectIntegerRegister
	case typeFloat32:
		class = objectRealRegister
	default:
		return registerAddress{}, fmt.Errorf(
			"fanuc: 地址 %q 的类型 %q 不支持（可用 int32 / uint32 / float32）", name, dataType)
	}

	return registerAddress{
		Name:     raw,
		Index:    idx,
		Class:    class,
		DataType: dt,
		Kind:     typeKind(dt),
	}, nil
}
