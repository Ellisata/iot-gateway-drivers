// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package s7

import (
	"testing"

	"github.com/Ellisata/iot-gateway-sdk/driver"
)

// 本文件的主题：**类型字节数 > 地址后缀宽度**时，设备确认越界之后，要把
// "类型不匹配"与"地址不存在"分开。前者的正确修法是改数据类型，显示成
// 「地址无效」会把人引向改地址（现场形态：DB1.DBW0 配 Int 正常，配 Float
// 就报地址无效 —— Float 是 4 字节，驱动去读 [0,4)，而 DB1 只有 2 字节）。

// missingFrom 造 [lo, hi) 全部不存在的偏移集合（模拟 DB 只到 lo 为止）。
func missingFrom(lo, hi int) []int {
	out := make([]int, 0, hi-lo)
	for i := lo; i < hi; i++ {
		out = append(out, i)
	}
	return out
}

// typePoint 造一个单点批次。
func typePoint(name, dataType string) driver.DeviceAddress {
	return driver.DeviceAddress{ID: "p", Name: name, DataType: dataType}
}

// TestDegrade_TypeWiderThanAddressBecomesParseError 声明 2 字节、类型 4 字节：
// 整段被拒，但按声明宽度探读（2 字节）是通的 —— 地址存在，是类型过宽。
func TestDegrade_TypeWiderThanAddressBecomesParseError(t *testing.T) {
	cfg := DefaultS7Config()
	cfg.MaxGap = 16
	f := newFakeS7(missingFrom(2, 1024)...) // DB1 只有 2 字节
	addrs := []driver.DeviceAddress{typePoint("DB1.DBW0", "real")}
	d := &s7Driver{config: cfg}
	sig := driver.RangeSig(addrs)

	res := runRound(t, d, f, sig, addrs, cfg)
	if res[0].Quality != driver.QualityBadParse {
		t.Fatalf("quality = %d, want %d（类型宽度越界应显示解析失败）",
			res[0].Quality, driver.QualityBadParse)
	}
	if res[0].Value != "" {
		t.Fatalf("value = %q, want empty", res[0].Value)
	}
	if got := d.badFor(sig)["p"]; got != driver.QualityBadParse {
		t.Fatalf("计划缓存 bad[p] = %d, want %d", got, driver.QualityBadParse)
	}

	// 第二轮：点位已被摘出计划，质量码必须仍是解析失败（不能退化成未归类），
	// 且不该再为它发任何请求。
	calls := len(f.callsSince(0))
	res = runRound(t, d, f, sig, addrs, cfg)
	if res[0].Quality != driver.QualityBadParse {
		t.Fatalf("第二轮 quality = %d, want %d", res[0].Quality, driver.QualityBadParse)
	}
	if extra := len(f.callsSince(calls)); extra != 0 {
		t.Fatalf("第二轮不该再发请求, got %d", extra)
	}
}

// TestDegrade_TypeWiderButAddressMissingStaysBadAddress 地址本身不存在时
// （连声明宽度都读不到）必须维持地址级判定 —— 探读不能把"没建的 DB"洗成
// "类型不匹配"，那会把用户引向改类型，同样解决不了问题。
func TestDegrade_TypeWiderButAddressMissingStaysBadAddress(t *testing.T) {
	cfg := DefaultS7Config()
	cfg.MaxGap = 16
	f := newFakeS7(missingFrom(0, 1024)...) // DB2 不存在
	addrs := []driver.DeviceAddress{typePoint("DB2.DBW0", "real")}
	d := &s7Driver{config: cfg}

	res := runRound(t, d, f, driver.RangeSig(addrs), addrs, cfg)
	if res[0].Quality != driver.QualityBadAddress {
		t.Fatalf("quality = %d, want %d", res[0].Quality, driver.QualityBadAddress)
	}
}

// TestDegrade_NoProbeWhenSpanEqualsWidth 跨度 = 声明宽度（byte 点位）时不做
// 探读：这不是类型宽度问题，多一次请求没有任何信息量。
func TestDegrade_NoProbeWhenSpanEqualsWidth(t *testing.T) {
	cfg := DefaultS7Config()
	cfg.MaxGap = 16
	f := newFakeS7(5)
	addrs := []driver.DeviceAddress{typePoint("DB1.DBB5", "byte")}
	d := &s7Driver{config: cfg}

	res := runRound(t, d, f, driver.RangeSig(addrs), addrs, cfg)
	if res[0].Quality != driver.QualityBadAddress {
		t.Fatalf("quality = %d, want %d", res[0].Quality, driver.QualityBadAddress)
	}
	if n := len(f.callsSince(0)); n != 1 {
		t.Fatalf("calls = %d, want 1（跨度=声明宽度，不该探读）", n)
	}
}
