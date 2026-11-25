// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package modbusdrv

import (
	"encoding/binary"

	"github.com/Ellisata/iot-gateway-sdk/driver"

	layoutpkg "github.com/Ellisata/iot-gateway-drivers/internal/layout"
)

// 点位级字节/字排列布局（ByteOrderLayout）。
//
// 排列表与编解码已下沉到 internal/layout（欧姆龙 FINS 共用同一份），这里只留
// 别名与本包原有的未导出函数名，调用点与测试无需改动。布局语义、宽度分组与
// DCBAHGFE 的说明见 internal/layout/layout.go。
//
// 空串表示**跟随设备配置**，由 ParseReadResult 走旧的 byteOrder/wordOrder
// 两轴语义 —— 设备级配置有存量设备，语义不能改。
const (
	LayoutABCD = layoutpkg.ABCD
	LayoutCDAB = layoutpkg.CDAB
	LayoutBADC = layoutpkg.BADC
	LayoutDCBA = layoutpkg.DCBA

	LayoutABCDEFGH = layoutpkg.ABCDEFGH
	LayoutCDABGHEF = layoutpkg.CDABGHEF
	LayoutBADCFEHG = layoutpkg.BADCFEHG
	LayoutHGFEDCBA = layoutpkg.HGFEDCBA
	LayoutGHEFCDAB = layoutpkg.GHEFCDAB
	LayoutDCBAHGFE = layoutpkg.DCBAHGFE
)

// normalizeLayout 统一大小写与空白。布局名对外不区分大小写。
func normalizeLayout(s string) string {
	return layoutpkg.Normalize(s)
}

// layoutPermutation 查 (布局, 值宽度) 的排列。未登记返回 false。
func layoutPermutation(layout string, size int) ([]int, bool) {
	return layoutpkg.Permutation(layout, size)
}

// stringLayoutOrder 字符串点位只支持「字内是否交换字节」这一维：
// ABCD = 原样，BADC = 每个 16 位字内交换。跨字重排对字符串没有公认语义，
// 因此不开放 32/64 位布局名。
func stringLayoutOrder(layout string) (binary.ByteOrder, bool) {
	return layoutpkg.StringOrder(layout)
}

// encodeWithLayout 按点位级布局把一个值编码成线上字节（写路径）。
// 与读路径共用排列表：先按大端得到规范字节，再按排列表重排。
func encodeWithLayout(val any, dt *driver.DataType, layout string) ([]byte, error) {
	return layoutpkg.EncodeWithLayout(val, dt, layout)
}
