// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package fins

import (
	"encoding/binary"
	"fmt"
	"strings"

	layoutpkg "github.com/Ellisata/iot-gateway-drivers/internal/layout"
)

// ParseFINSValue 将读取到的原始字节解码为指定类型的 Go 值。
//
// raw 为该点位所在的字数据（每字 2 字节，FINS 线上大端）。
// 位点位（bool，地址携带 .bit）先从所在字中提取该位为 1 字节再走 bool 解码。
//
// byteOrder 控制字节序（默认大端）；wordOrder 控制 32/64 位值的字序
// （BIG_ENDIAN=高字在前，LITTLE_ENDIAN=低字在前）。
func ParseFINSValue(raw []byte, addr FINSAddress, dataType string, cfg *FINSConfig) (any, error) {
	return parseFINSValue(raw, addr, dataType, parseByteOrder(cfg.ByteOrder),
		strings.EqualFold(cfg.WordOrder, WordOrderLittleEndian), "")
}

// ParseFINSValueWithLayout 按**点位级布局**解析（layout 非空时使用）。
//
// 布局是对整个值字节序列的排列（见 internal/layout），**整体替代**设备级的
// byteOrder/wordOrder 两轴，不与它们叠加。空串表示跟随设备配置，调用方应改走
// ParseFINSValue。
//
// 位点位、单字节类型（bool/uint8/int8）与字符串没有点位级布局：位与单字节
// 取的是固定位置，字符串的跨字重排没有公认语义 —— 这几类一律忽略 layout，
// 按各自的固定解码方式处理（它们本来就不受字节序影响；保存侧也按同样口径
// 拒绝非空布局）。
func ParseFINSValueWithLayout(raw []byte, addr FINSAddress, dataType, layout string) (any, error) {
	return parseFINSValue(raw, addr, dataType, binary.BigEndian, false, layout)
}

// parseFINSValue 读路径的唯一解码实现。layout 为空时走设备级两轴；非空时只对
// 2/4/8 字节类型生效，按排列表还原成规范大端后解码。
func parseFINSValue(raw []byte, addr FINSAddress, dataType string, order binary.ByteOrder, swapWords bool, layout string) (any, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("fins parser: empty raw data")
	}

	dt, ok := finsLookup(dataType)
	if !ok {
		return nil, fmt.Errorf("fins parser: unsupported data type: %q", dataType)
	}

	// 位点位：从 2 字节字中提取所在位。
	// FINS 位 0..7 位于低字节（raw[1]），位 8..15 位于高字节（raw[0]），
	// 字内位序 LSB-first。位地址没有点位级布局（见 ParseFINSValueWithLayout）。
	if addr.IsBit() {
		if len(raw) < 2 {
			return nil, fmt.Errorf("fins parser: bool needs a word (2 bytes), got %d", len(raw))
		}
		return dt.Decode([]byte{finsBitValue(raw, addr.Bit)}, order)
	}

	// 动态长度类型（STRING）：整体传给解码函数。字符串不做点位级布局。
	if dt.Size == 0 {
		return dt.Decode(raw, order)
	}

	// 常规类型：截取类型大小字节。
	// 单字节类型（uint8/int8，及无位后缀的 bool）取字的低字节——
	// FINS 字节数据通常存于 16 位字的低字节。单字节没有排列可言。
	if len(raw) < dt.Size {
		return nil, fmt.Errorf("fins parser: %s needs %d bytes, got %d", dataType, dt.Size, len(raw))
	}
	data := raw[:dt.Size]
	if dt.Size == 1 {
		return dt.Decode(raw[len(raw)-1:], order)
	}

	// 点位级布局：按排列表把线上字节还原成规范大端序列，再交给类型解码函数。
	// 必须**跳过**下面的字序交换与设备字节序，否则会应用两次。
	if layout != "" {
		perm, ok := layoutpkg.Permutation(layout, dt.Size)
		if !ok {
			return nil, fmt.Errorf("fins parser: layout %q not applicable to %s (%d bytes)", layout, dataType, dt.Size)
		}
		canonical := make([]byte, dt.Size)
		for i, p := range perm {
			canonical[i] = data[p]
		}
		return dt.Decode(canonical, binary.BigEndian)
	}

	// 32/64 位类型：按字序交换（相邻 16 位字对交换）
	switch dt.Size {
	case 4:
		data = swapWords32(data, swapWords)
	case 8:
		data = swapWords64(data, swapWords)
	}

	return dt.Decode(data, order)
}

// finsBitValue 从 2 字节字里取出第 bit 位（0..15）。
//
// 位序是 FINS 的约定：位 0..7 位于低字节（raw[1]），位 8..15 位于高字节（raw[0]），
// 字内 LSB-first。**抽成函数是因为它现在有两个调用方**（采集解码与点位扫描的
// 零值判定），而两份实现一旦有一位之差，表现是"扫出来的值和采集到的值不一样"——
// 用户会先怀疑 PLC，再怀疑网关，最后才怀疑到这里。
//
// 越界（字不够长、bit 不在 0..15）返回 0 而不是 panic：调用方各自已在读取前
// 校验过长度，这里再兜一次是因为它也会被扫描路径调用，而扫描的字节偏移来自
// 用户填的范围。
func finsBitValue(raw []byte, bit int) byte {
	if len(raw) < 2 || bit < 0 || bit > 15 {
		return 0
	}
	byteIdx := 0
	if bit < 8 {
		byteIdx = 1
	}
	return (raw[byteIdx] >> uint(bit%8)) & 0x01
}

// FormatFINSValue 将解码后的值格式化为字符串（用于存储和展示）。
func FormatFINSValue(v any, dataType string) string {
	dt, ok := finsLookup(dataType)
	if !ok || dt.Format == nil {
		return fmt.Sprintf("%v", v)
	}
	return dt.Format(v)
}

// swapWords32 对 4 字节数据进行字序交换（如果需要）。
// 输入 [A1 A2 B1 B2]，大端字序保持，小端字序交换为 [B1 B2 A1 A2]。
func swapWords32(data []byte, swap bool) []byte {
	if !swap || len(data) < 4 {
		return data
	}
	swapped := make([]byte, 4)
	copy(swapped[:2], data[2:4])
	copy(swapped[2:4], data[:2])
	return swapped
}

// swapWords64 对 8 字节数据进行字序交换（4 个相邻字两两交换）：
//
//	大端字序保持 [R1 R2 R3 R4]，小端字序交换为 [R2 R1 R4 R3]。
func swapWords64(data []byte, swap bool) []byte {
	if !swap || len(data) < 8 {
		return data
	}
	swapped := make([]byte, 8)
	copy(swapped[:2], data[2:4])
	copy(swapped[2:4], data[:2])
	copy(swapped[4:6], data[6:8])
	copy(swapped[6:8], data[4:6])
	return swapped
}

// parseByteOrder 解析字节序字符串
func parseByteOrder(s string) binary.ByteOrder {
	switch strings.ToUpper(s) {
	case ByteOrderLittleEndian:
		return binary.LittleEndian
	default:
		return binary.BigEndian
	}
}
