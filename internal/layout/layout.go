// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

// Package layout 提供点位级字节/字排列布局（ByteOrderLayout）的共享实现。
//
// 布局名直接写出**规范大端字节序列**的排列：字母 A、B、C… 依次代表值在内存中
// 的第 0、1、2… 个字节（大端序）。例如 32 位的 CDAB 表示线上先出现第 3、4 个
// 字节，再出现第 1、2 个字节。名称按值宽度区分：
//
//	16 位：ABCD / BADC
//	32 位：ABCD / CDAB / BADC / DCBA
//	64 位：ABCDEFGH / CDABGHEF / BADCFEHG / HGFEDCBA / GHEFCDAB / DCBAHGFE
//
// 空串表示**跟随设备配置**，由各协议按自己的 byteOrder/wordOrder 两轴语义解释
// —— 设备级配置有存量设备，语义不能改。
//
// 本包**与协议无关**，只依赖 SDK 的 driver.DataType：Modbus 与欧姆龙 FINS 共用
// 同一份排列表，避免两个插件各持一份而漂移。协议特有的字符串/位预处理留在
// 各自驱动包内（如 Modbus 的 stringLayoutOrder 只接受 ABCD/BADC）。
package layout

import (
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/Ellisata/iot-gateway-sdk/driver"
)

const (
	ABCD = "ABCD"
	CDAB = "CDAB"
	BADC = "BADC"
	DCBA = "DCBA"

	ABCDEFGH = "ABCDEFGH"
	CDABGHEF = "CDABGHEF"
	BADCFEHG = "BADCFEHG"
	HGFEDCBA = "HGFEDCBA"
	GHEFCDAB = "GHEFCDAB"
	// DCBAHGFE：每个 16 位字内交换字节，再交换相邻字对。
	DCBAHGFE = "DCBAHGFE"
)

// permutations 值宽度 → 布局名 → 索引表。
//
// 表里第 i 项是「规范大端字节的第 i 个字节」在**线上数据**中的位置。这里用到的
// 排列都是自反的（交换类），所以解码与编码可以共用同一张表：
//
//	解码：canonical[i] = wire[perm[i]]
//	编码：wire[perm[i]] = canonical[i]
//
// 新增布局时三个宽度表都要有意识地过一遍：漏一个宽度的表现是该布局在那一类
// 点位上「能选但保存/解析时报错」。
var permutations = map[int]map[string][]int{
	2: {
		ABCD: {0, 1},
		BADC: {1, 0},
	},
	4: {
		ABCD: {0, 1, 2, 3},
		CDAB: {2, 3, 0, 1}, // 交换两个 16 位字
		BADC: {1, 0, 3, 2}, // 每个字内交换字节
		DCBA: {3, 2, 1, 0}, // 整体反转
	},
	8: {
		ABCDEFGH: {0, 1, 2, 3, 4, 5, 6, 7}, // 大端，原样
		CDABGHEF: {2, 3, 0, 1, 6, 7, 4, 5}, // 相邻字两两交换
		BADCFEHG: {1, 0, 3, 2, 5, 4, 7, 6}, // 每个字内交换字节
		HGFEDCBA: {7, 6, 5, 4, 3, 2, 1, 0}, // 8 字节整体反转
		GHEFCDAB: {6, 7, 4, 5, 2, 3, 0, 1}, // 4 个 16 位字整体逆序
		// 每个字内交换字节，再交换相邻字对：
		//   A B C D E F G H → B A D C F E H G → D C B A H G F E
		DCBAHGFE: {3, 2, 1, 0, 7, 6, 5, 4},
	},
}

// Normalize 统一大小写与空白。布局名对外不区分大小写。
func Normalize(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

// Permutation 查 (布局, 值宽度) 的排列。未登记返回 false。
func Permutation(layout string, size int) ([]int, bool) {
	table, ok := permutations[size]
	if !ok {
		return nil, false
	}
	perm, ok := table[Normalize(layout)]
	return perm, ok
}

// StringOrder 字符串点位只支持「字内是否交换字节」这一维：
// ABCD = 原样，BADC = 每个 16 位字内交换。跨字的重排队对字符串没有
// 公认语义（每个字的字符顺序与字之间的顺序是两个独立约定），
// 因此不开放 32/64 位布局名。
func StringOrder(layout string) (binary.ByteOrder, bool) {
	switch Normalize(layout) {
	case ABCD:
		return binary.BigEndian, true
	case BADC:
		return binary.LittleEndian, true
	default:
		return nil, false
	}
}

// EncodeWithLayout 按点位级布局把一个值编码成线上字节（写路径）。
//
// 与读路径共用 permutations：先按大端得到规范字节，再按排列表重排。
func EncodeWithLayout(val any, dt *driver.DataType, layout string) ([]byte, error) {
	perm, ok := Permutation(layout, dt.Size)
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
