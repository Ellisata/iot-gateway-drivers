// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package modbusdrv

import (
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/Ellisata/iot-gateway-sdk/driver"
)

// 点位级字节/字排列布局（ByteOrderLayout）。
//
// 布局名直接写出**规范大端字节序列**的排列：字母 A、B、C… 依次代表值在内存中
// 的第 0、1、2… 个字节（大端序）。例如 32 位的 CDAB 表示线上先出现第 3、4 个
// 字节，再出现第 1、2 个字节。名称按值宽度区分：
//
//	16 位：ABCD / BADC
//	32 位：ABCD / CDAB / BADC / DCBA
//	64 位：ABCDEFGH / CDABGHEF / BADCFEHG / HGFEDCBA / GHEFCDAB
//
// 这样 64 位不再借用 32 位的四个名字（把 32 位的 BADC 套到 8 字节会得到
// FEHGBADC 这种没有任何厂商使用的排列，且常见的 GHEFCDAB / BADCFEHG 反而
// 无法表达）。空串表示**跟随设备配置**，由 ParseReadResult 走旧的
// byteOrder/wordOrder 两轴语义 —— 设备级配置有存量设备，语义不能改。
const (
	LayoutABCD = "ABCD"
	LayoutCDAB = "CDAB"
	LayoutBADC = "BADC"
	LayoutDCBA = "DCBA"

	LayoutABCDEFGH = "ABCDEFGH"
	LayoutCDABGHEF = "CDABGHEF"
	LayoutBADCFEHG = "BADCFEHG"
	LayoutHGFEDCBA = "HGFEDCBA"
	LayoutGHEFCDAB = "GHEFCDAB"
)

// layoutPermutations 值宽度 → 布局名 → 索引表。
//
// 表里第 i 项是「规范大端字节的第 i 个字节」在**线上数据**中的位置。这里用到的
// 排列都是自反的（交换类），所以解码与编码可以共用同一张表：
//
//	解码：canonical[i] = wire[perm[i]]
//	编码：wire[perm[i]] = canonical[i]
//
// 新增布局时三个宽度表都要有意识地过一遍：漏一个宽度的表现是该布局在那一类
// 点位上「能选但保存/解析时报错」。
var layoutPermutations = map[int]map[string][]int{
	2: {
		LayoutABCD: {0, 1},
		LayoutBADC: {1, 0},
	},
	4: {
		LayoutABCD: {0, 1, 2, 3},
		LayoutCDAB: {2, 3, 0, 1}, // 交换两个 16 位寄存器
		LayoutBADC: {1, 0, 3, 2}, // 每个寄存器内交换字节
		LayoutDCBA: {3, 2, 1, 0}, // 整体反转
	},
	8: {
		LayoutABCDEFGH: {0, 1, 2, 3, 4, 5, 6, 7}, // 大端，原样
		LayoutCDABGHEF: {2, 3, 0, 1, 6, 7, 4, 5}, // 相邻寄存器两两交换
		LayoutBADCFEHG: {1, 0, 3, 2, 5, 4, 7, 6}, // 每个寄存器内交换字节
		LayoutHGFEDCBA: {7, 6, 5, 4, 3, 2, 1, 0}, // 8 字节整体反转
		LayoutGHEFCDAB: {6, 7, 4, 5, 2, 3, 0, 1}, // 4 个寄存器整体逆序
	},
}

// normalizeLayout 统一大小写与空白。布局名对外不区分大小写。
func normalizeLayout(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

// layoutPermutation 查 (布局, 值宽度) 的排列。未登记返回 false。
func layoutPermutation(layout string, size int) ([]int, bool) {
	table, ok := layoutPermutations[size]
	if !ok {
		return nil, false
	}
	perm, ok := table[normalizeLayout(layout)]
	return perm, ok
}

// stringLayoutOrder 字符串点位只支持「寄存器内是否交换字节」这一维：
// ABCD = 原样，BADC = 每个 16 位寄存器内交换。跨寄存器的重排队字符串没有
// 公认语义（每个寄存器的字符顺序与寄存器之间的顺序是两个独立约定），
// 因此不开放 32/64 位布局名。
func stringLayoutOrder(layout string) (binary.ByteOrder, bool) {
	switch normalizeLayout(layout) {
	case LayoutABCD:
		return binary.BigEndian, true
	case LayoutBADC:
		return binary.LittleEndian, true
	default:
		return nil, false
	}
}

// encodeWithLayout 按点位级布局把一个值编码成线上字节（写路径）。
//
// 与读路径共用 layoutPermutations：先按大端得到规范字节，再按排列表重排。
func encodeWithLayout(val any, dt *driver.DataType, layout string) ([]byte, error) {
	perm, ok := layoutPermutation(layout, dt.Size)
	if !ok {
		return nil, fmt.Errorf("布局 %q 不适用于 %s（%d 字节）", layout, dt.Name, dt.Size)
	}
	canonical, err := dt.Encode(val, binary.BigEndian)
	if err != nil {
		return nil, err
	}
	if len(canonical) != dt.Size {
		return nil, fmt.Errorf("%s 编码长度 %d，期望 %d", dt.Name, len(canonical), dt.Size)
	}
	wire := make([]byte, dt.Size)
	for i, p := range perm {
		wire[p] = canonical[i]
	}
	return wire, nil
}
