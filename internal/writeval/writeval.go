// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

// Package writeval 解析/格式化控制下发值。
//
// 各协议插件的写路径都需要把网关下发的字符串按点位 DataType 转成编码函数
// 能接受的值，并在成功后按同一类型格式化回传。两个方向的规则必须完全一致，
// 否则"写下去的值"与"回读到的值"会以不同的精度/写法出现在审计里。
//
// 这里刻意不依赖任何协议包：只认 driver.DataType 的 Kind，因此所有插件
// 共享同一份解析规则（与 modbus 内置时代的 parseWriteValue 语义一致）。
package writeval

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Ellisata/iot-gateway-sdk/driver"
)

// Parse 把字符串按 dt.Kind 解析为编码函数可接受的值。
//
// 不使用 json.Unmarshal：JSON 数字一律变 float64，int64/uint64 的高位会丢精度。
// 整数接受 Go 字面量前缀（如 0x10、0o17），与 modbus 写路径保持一致。
func Parse(dt *driver.DataType, raw string) (any, error) {
	if dt == nil {
		return nil, fmt.Errorf("unknown data type")
	}
	s := strings.TrimSpace(raw)
	if s == "" && dt.Kind != driver.KindString {
		return nil, fmt.Errorf("empty value")
	}
	switch dt.Kind {
	case driver.KindBool:
		return parseBool(s)
	case driver.KindInt:
		return strconv.ParseInt(s, 0, 64)
	case driver.KindUInt:
		return strconv.ParseUint(s, 0, 64)
	case driver.KindFloat:
		return strconv.ParseFloat(s, 64)
	case driver.KindString:
		// 字符串保留原始值（含首尾空白）：控制字符串可能就是带空格的配方名。
		return raw, nil
	case driver.KindTime:
		return strconv.ParseInt(s, 10, 64)
	default:
		return nil, fmt.Errorf("unsupported kind %q", dt.Kind)
	}
}

// Format 按类型自带的 Format 输出实际写入值；没有 Format 时退回 %v。
func Format(dt *driver.DataType, v any) string {
	if dt != nil && dt.Format != nil {
		return dt.Format(v)
	}
	return fmt.Sprintf("%v", v)
}

// parseBool 接受现场常见的布尔写法。
func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "on", "yes":
		return true, nil
	case "0", "false", "off", "no":
		return false, nil
	default:
		return false, fmt.Errorf("invalid bool value %q", s)
	}
}
