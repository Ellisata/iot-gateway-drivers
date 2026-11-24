// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package modbusdrv

import (
	"math"
	"testing"

	"github.com/Ellisata/iot-gateway-sdk/driver"
)

// TestParseReadResultLayout32 32 位四种经典布局。
// 以 float32 22.5（位模式 0x41B40000，规范大端 41 B4 00 00）为例：
//
//	ABCD = 41 B4 00 00  原样
//	CDAB = 00 00 41 B4  交换两个寄存器
//	BADC = B4 41 00 00  寄存器内交换字节
//	DCBA = 00 00 B4 41  整体反转
func TestParseReadResultLayout32(t *testing.T) {
	cases := []struct {
		layout string
		raw    []byte
	}{
		{LayoutABCD, []byte{0x41, 0xB4, 0x00, 0x00}},
		{LayoutCDAB, []byte{0x00, 0x00, 0x41, 0xB4}},
		{LayoutBADC, []byte{0xB4, 0x41, 0x00, 0x00}},
		{LayoutDCBA, []byte{0x00, 0x00, 0xB4, 0x41}},
	}
	for _, c := range cases {
		t.Run(c.layout, func(t *testing.T) {
			v, err := ParseReadResultWithLayout(c.raw, "float32", c.layout)
			if err != nil {
				t.Fatalf("ParseReadResultWithLayout: %v", err)
			}
			if got, ok := v.(float32); !ok || got != 22.5 {
				t.Fatalf("%s 解码 = %v (%T), want float32(22.5)", c.layout, v, v)
			}
		})
	}
}

// TestParseReadResultLayout64 64 位布局：五个布局名各自对应一种真实现场约定，
// 以 float64 位模式 0x0123456789ABCDEF（规范大端 01 23 45 67 89 AB CD EF）为例。
func TestParseReadResultLayout64(t *testing.T) {
	want := math.Float64frombits(0x0123456789ABCDEF)
	cases := []struct {
		layout string
		raw    []byte
	}{
		{LayoutABCDEFGH, []byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xAB, 0xCD, 0xEF}},
		{LayoutCDABGHEF, []byte{0x45, 0x67, 0x01, 0x23, 0xCD, 0xEF, 0x89, 0xAB}},
		{LayoutBADCFEHG, []byte{0x23, 0x01, 0x67, 0x45, 0xAB, 0x89, 0xEF, 0xCD}},
		{LayoutHGFEDCBA, []byte{0xEF, 0xCD, 0xAB, 0x89, 0x67, 0x45, 0x23, 0x01}},
		{LayoutGHEFCDAB, []byte{0xCD, 0xEF, 0x89, 0xAB, 0x45, 0x67, 0x01, 0x23}},
	}
	for _, c := range cases {
		t.Run(c.layout, func(t *testing.T) {
			v, err := ParseReadResultWithLayout(c.raw, "float64", c.layout)
			if err != nil {
				t.Fatalf("ParseReadResultWithLayout: %v", err)
			}
			got, ok := v.(float64)
			if !ok || got != want {
				t.Fatalf("%s 解码 = %v (%T), want %v", c.layout, v, v, want)
			}
		})
	}
}

// TestParseReadResultLayout16 16 位只有两种排列：字节原样 / 寄存器内交换。
func TestParseReadResultLayout16(t *testing.T) {
	v, err := ParseReadResultWithLayout([]byte{0x34, 0x12}, "int16", LayoutBADC)
	if err != nil {
		t.Fatalf("ParseReadResultWithLayout: %v", err)
	}
	if got, ok := v.(int16); !ok || got != 0x1234 {
		t.Fatalf("BADC 16 位解码 = %v (%T), want 0x1234", v, v)
	}
}

// TestParseReadResultLayoutRejectsWrongWidth 宽度与布局名不匹配必须报错，
// 而不是猜一个排列 —— 这也是保存时按类型宽度校验的驱动侧兜底。
func TestParseReadResultLayoutRejectsWrongWidth(t *testing.T) {
	cases := []struct {
		name, dataType, layout string
		raw                    []byte
	}{
		{"32 位布局用于 16 位", "int16", LayoutCDAB, []byte{0x12, 0x34}},
		{"64 位布局用于 32 位", "float32", LayoutGHEFCDAB, []byte{0x41, 0xB4, 0x00, 0x00}},
		{"32 位布局用于 64 位", "float64", LayoutCDAB, make([]byte, 8)},
		{"未知布局", "float32", "XYZ", []byte{0x41, 0xB4, 0x00, 0x00}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ParseReadResultWithLayout(c.raw, c.dataType, c.layout); err == nil {
				t.Fatal("布局与宽度不匹配应报错")
			}
		})
	}
}

// TestParseStringLayout 字符串只支持 ABCD / BADC：
// 两个寄存器 "AB" "CD" 以寄存器内交换存储为 BA DC，解码后应还原成 ABCD。
func TestParseStringLayout(t *testing.T) {
	v, err := ParseReadResultWithLayout([]byte{'B', 'A', 'D', 'C'}, "string", LayoutBADC)
	if err != nil {
		t.Fatalf("ParseReadResultWithLayout: %v", err)
	}
	if got, ok := v.(string); !ok || got != "ABCD" {
		t.Fatalf("BADC 字符串解码 = %v (%T), want ABCD", v, v)
	}
	if _, err := ParseReadResultWithLayout([]byte("ABCD"), "string", LayoutCDAB); err == nil {
		t.Fatal("字符串点位不支持跨寄存器布局，应报错")
	}
}

// TestParseRegisterResultsPerPointLayout 一台设备、两个 float32 点位、两种布局：
// 设备级是 ABCD，第二个点位用点位级 CDAB。两个都必须解出同一个值。
func TestParseRegisterResultsPerPointLayout(t *testing.T) {
	cfg := DefaultModbusTcpConfig() // 设备级 ABCD（BE/BE）
	addrs := []driver.DeviceAddress{
		{ID: "a", Name: "%MW0", DataType: "float32"},
		{ID: "b", Name: "%MW2", DataType: "float32", ByteOrderLayout: LayoutCDAB},
	}
	// 点位 a 用 ABCD，点位 b 用 CDAB
	results := append([]byte{0x41, 0xB4, 0x00, 0x00}, 0x00, 0x00, 0x41, 0xB4)
	ar := &AddressRange{
		StartAddress: 0,
		Quantity:     4,
		AddressMap:   map[string]uint16{"a": 0, "b": 2},
	}

	records := parseRegisterResults(results, addrs, cfg, ar)
	if len(records) != 2 {
		t.Fatalf("记录数 = %d, want 2", len(records))
	}
	for _, r := range records {
		if r.Quality != driver.QualityGood {
			t.Errorf("点位 %s 质量码 = %d, want %d", r.DeviceAddressID, r.Quality, driver.QualityGood)
		}
		if r.Value != "22.5" {
			t.Errorf("点位 %s 值 = %q, want 22.5", r.DeviceAddressID, r.Value)
		}
	}
}

// TestWriteRespectsPerPointLayout 写路径必须用与读路径同一份布局：
// 用 BADC 写 float32，落盘字节应和读回来的 BADC 序列一致。
func TestWriteRespectsPerPointLayout(t *testing.T) {
	f := newFakeWriteTransport()
	cfg := DefaultModbusTcpConfig() // 设备级 ABCD

	results, err := writeBatch("test", f, []driver.WriteRequest{{
		DeviceAddressID: "b",
		Name:            "%MW2",
		DataType:        "float32",
		Value:           "22.5",
		ByteOrderLayout: LayoutBADC,
	}}, cfg)
	if err != nil {
		t.Fatalf("writeBatch: %v", err)
	}
	if len(results) != 1 || !results[0].OK() {
		t.Fatalf("结果 = %+v，期望成功", results)
	}
	want := []byte{0xB4, 0x41, 0x00, 0x00} // BADC
	got := f.multi[2]
	if len(got) != len(want) {
		t.Fatalf("写入字节 = % X, want % X", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("写入字节 = % X, want % X", got, want)
		}
	}
}

// TestWriteRejectsWrongWidthLayout 写路径同样拒绝宽度不匹配的布局。
func TestWriteRejectsWrongWidthLayout(t *testing.T) {
	f := newFakeWriteTransport()
	cfg := DefaultModbusTcpConfig()

	results, err := writeBatch("test", f, []driver.WriteRequest{{
		DeviceAddressID: "s",
		Name:            "%MW0",
		DataType:        "int16",
		Value:           "1",
		ByteOrderLayout: LayoutGHEFCDAB,
	}}, cfg)
	if err != nil {
		t.Fatalf("writeBatch: %v", err)
	}
	if len(results) != 1 || results[0].Code != driver.WriteCodeBadValue {
		t.Fatalf("结果 = %+v，期望 WriteCodeBadValue", results)
	}
}
