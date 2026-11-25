// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package fins

import (
	"bytes"
	"math"
	"strconv"
	"testing"

	"github.com/Ellisata/iot-gateway-sdk/driver"
)

var nonBitAddr = FINSAddress{Area: AreaDM, Word: 100, Bit: -1}

// TestParseFINSValueLayout32 32 位四种布局，以 float32 22.5
// （规范大端 41 B4 00 00）为例。点位级布局整体替代设备级两轴。
func TestParseFINSValueLayout32(t *testing.T) {
	cases := []struct {
		layout string
		raw    []byte
	}{
		{"ABCD", []byte{0x41, 0xB4, 0x00, 0x00}},
		{"CDAB", []byte{0x00, 0x00, 0x41, 0xB4}},
		{"BADC", []byte{0xB4, 0x41, 0x00, 0x00}},
		{"DCBA", []byte{0x00, 0x00, 0xB4, 0x41}},
	}
	for _, c := range cases {
		t.Run(c.layout, func(t *testing.T) {
			v, err := ParseFINSValueWithLayout(c.raw, nonBitAddr, "float32", c.layout)
			if err != nil {
				t.Fatalf("ParseFINSValueWithLayout: %v", err)
			}
			if got, ok := v.(float32); !ok || got != 22.5 {
				t.Fatalf("%s = %v (%T), want float32(22.5)", c.layout, v, v)
			}
		})
	}
}

// TestParseFINSValueLayout64 64 位六种布局，以 float64 位模式
// 0x0123456789ABCDEF（规范大端 01 23 45 67 89 AB CD EF）为例。
func TestParseFINSValueLayout64(t *testing.T) {
	want := math.Float64frombits(0x0123456789ABCDEF)
	cases := []struct {
		layout string
		raw    []byte
	}{
		{"ABCDEFGH", []byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xAB, 0xCD, 0xEF}},
		{"CDABGHEF", []byte{0x45, 0x67, 0x01, 0x23, 0xCD, 0xEF, 0x89, 0xAB}},
		{"BADCFEHG", []byte{0x23, 0x01, 0x67, 0x45, 0xAB, 0x89, 0xEF, 0xCD}},
		{"HGFEDCBA", []byte{0xEF, 0xCD, 0xAB, 0x89, 0x67, 0x45, 0x23, 0x01}},
		{"GHEFCDAB", []byte{0xCD, 0xEF, 0x89, 0xAB, 0x45, 0x67, 0x01, 0x23}},
		{"DCBAHGFE", []byte{0x67, 0x45, 0x23, 0x01, 0xEF, 0xCD, 0xAB, 0x89}},
	}
	for _, c := range cases {
		t.Run(c.layout, func(t *testing.T) {
			v, err := ParseFINSValueWithLayout(c.raw, nonBitAddr, "float64", c.layout)
			if err != nil {
				t.Fatalf("ParseFINSValueWithLayout: %v", err)
			}
			if got, ok := v.(float64); !ok || got != want {
				t.Fatalf("%s = %v (%T), want %v", c.layout, v, v, want)
			}
		})
	}
}

// TestParseFINSValueLayout16 16 位只有 ABCD / BADC。
func TestParseFINSValueLayout16(t *testing.T) {
	v, err := ParseFINSValueWithLayout([]byte{0x34, 0x12}, nonBitAddr, "int16", "BADC")
	if err != nil {
		t.Fatalf("ParseFINSValueWithLayout: %v", err)
	}
	if got, ok := v.(int16); !ok || got != 0x1234 {
		t.Fatalf("BADC int16 = %v (%T), want 0x1234", v, v)
	}
}

// TestParseFINSValueLayoutBCD BCD/LBCD/Date 同样按字节排列处理。
func TestParseFINSValueLayoutBCD(t *testing.T) {
	if v, err := ParseFINSValueWithLayout([]byte{0x34, 0x12}, nonBitAddr, "bcd", "BADC"); err != nil || v != int(1234) {
		t.Fatalf("bcd BADC = %v, err=%v, want 1234", v, err)
	}
	if v, err := ParseFINSValueWithLayout([]byte{0x78, 0x56, 0x34, 0x12}, nonBitAddr, "lbcd", "DCBA"); err != nil || v != int(12345678) {
		t.Fatalf("lbcd DCBA = %v, err=%v, want 12345678", v, err)
	}
	if v, err := ParseFINSValueWithLayout([]byte{0x00, 0x00, 0x00, 0x64}, nonBitAddr, "date", "ABCD"); err != nil || v != int64(100) {
		t.Fatalf("date ABCD = %v, err=%v, want 100", v, err)
	}
}

// TestParseFINSValueLayoutRejectsWrongWidth 宽度与布局名不匹配必须报错，
// 而不是猜一个排列（保存侧也会按宽度拒绝）。
func TestParseFINSValueLayoutRejectsWrongWidth(t *testing.T) {
	cases := []struct {
		name, dataType, layout string
		raw                    []byte
	}{
		{"32 位布局用于 16 位", "int16", "CDAB", []byte{0x12, 0x34}},
		{"64 位布局用于 32 位", "float32", "GHEFCDAB", []byte{0x41, 0xB4, 0x00, 0x00}},
		{"32 位布局用于 64 位", "float64", "CDAB", make([]byte, 8)},
		{"未知布局", "float32", "XYZ", []byte{0x41, 0xB4, 0x00, 0x00}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ParseFINSValueWithLayout(c.raw, nonBitAddr, c.dataType, c.layout); err == nil {
				t.Fatal("布局与宽度不匹配应报错")
			}
		})
	}
}

// TestParseFINSValueLayoutIgnoredForNonNumeric 位/单字节/字符串没有点位级布局：
// 即使传入非空 layout，也与设备级默认解码结果一致。
func TestParseFINSValueLayoutIgnoredForNonNumeric(t *testing.T) {
	cfg := DefaultFINSConfig()

	// 字符串：不做字内交换
	strRaw := []byte{'A', 'B', 0x00, 0x00}
	if v, err := ParseFINSValueWithLayout(strRaw, nonBitAddr, "string", "BADC"); err != nil || v != "AB" {
		t.Fatalf("string layout = %v, err=%v, want AB", v, err)
	}

	// int8：取低字节，布局不参与
	i8Raw := []byte{0x00, 0x12}
	wantI8, err := ParseFINSValue(i8Raw, nonBitAddr, "int8", cfg)
	if err != nil {
		t.Fatalf("ParseFINSValue(int8): %v", err)
	}
	if v, err := ParseFINSValueWithLayout(i8Raw, nonBitAddr, "int8", "BADC"); err != nil || v != wantI8 {
		t.Fatalf("int8 layout = %v, err=%v, want %v", v, err, wantI8)
	}

	// 位地址：从字里取位，布局不参与
	bitAddr := FINSAddress{Area: AreaDM, Word: 100, Bit: 5}
	if v, err := ParseFINSValueWithLayout([]byte{0x00, 0x20}, bitAddr, "bool", "BADC"); err != nil || v != true {
		t.Fatalf("bool bit layout = %v, err=%v, want true", v, err)
	}
}

// TestParseFINSValueLayoutOverridesDeviceOrder 点位级布局优先于设备级两轴：
// 设备配成 LITTLE/LITTLE，点位仍按 ABCD 解出大端值。
func TestParseFINSValueLayoutOverridesDeviceOrder(t *testing.T) {
	cfg := DefaultFINSConfig()
	cfg.ByteOrder = ByteOrderLittleEndian
	cfg.WordOrder = WordOrderLittleEndian

	v, err := ParseFINSValueWithLayout([]byte{0x41, 0xB4, 0x00, 0x00}, nonBitAddr, "float32", "ABCD")
	if err != nil {
		t.Fatalf("ParseFINSValueWithLayout: %v", err)
	}
	if got, ok := v.(float32); !ok || got != 22.5 {
		t.Fatalf("point layout should override device order, got %v (%T)", v, v)
	}
}

func finsWriteReqLayout(id, name, dataType, value, layout string) driver.WriteRequest {
	req := finsWriteReq(id, name, dataType, value)
	req.ByteOrderLayout = layout
	return req
}

// TestWriteFINSRespectsPerPointLayout 写路径与读路径共用同一份布局：
// float32 22.5 用 BADC 写出 B4 41 00 00。
func TestWriteFINSRespectsPerPointLayout(t *testing.T) {
	f := &fakeFINSWrite{}
	_, err := writeFINSBatch(f, []driver.WriteRequest{
		finsWriteReqLayout("p1", "D100", "float32", "22.5", "BADC"),
	}, &FINSConfig{})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	if got := f.writes[0].data; !bytes.Equal(got, []byte{0xB4, 0x41, 0x00, 0x00}) {
		t.Errorf("data = % X, want B4 41 00 00", got)
	}
}

// TestWriteFINSLayout64DCBAHGFE 64 位 DCBAHGFE 的写回字节。
func TestWriteFINSLayout64DCBAHGFE(t *testing.T) {
	value := math.Float64frombits(0x0123456789ABCDEF)
	f := &fakeFINSWrite{}
	_, err := writeFINSBatch(f, []driver.WriteRequest{
		finsWriteReqLayout("p1", "D100", "float64", strconv.FormatFloat(value, 'g', -1, 64), "DCBAHGFE"),
	}, &FINSConfig{})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	want := []byte{0x67, 0x45, 0x23, 0x01, 0xEF, 0xCD, 0xAB, 0x89}
	if got := f.writes[0].data; !bytes.Equal(got, want) {
		t.Errorf("data = % X, want % X", got, want)
	}
}

// TestWriteFINSLayoutOverridesDeviceOrder 设备级 LITTLE/LITTLE 不影响点位级 ABCD。
func TestWriteFINSLayoutOverridesDeviceOrder(t *testing.T) {
	f := &fakeFINSWrite{}
	cfg := &FINSConfig{
		ByteOrder: ByteOrderLittleEndian,
		WordOrder: WordOrderLittleEndian,
	}
	_, err := writeFINSBatch(f, []driver.WriteRequest{
		finsWriteReqLayout("p1", "D100", "float32", "22.5", "ABCD"),
	}, cfg)
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	if got := f.writes[0].data; !bytes.Equal(got, []byte{0x41, 0xB4, 0x00, 0x00}) {
		t.Errorf("data = % X, want 41 B4 00 00", got)
	}
}

// TestWriteFINSRejectsWrongWidthLayout 写路径同样拒绝宽度不匹配的布局。
func TestWriteFINSRejectsWrongWidthLayout(t *testing.T) {
	f := &fakeFINSWrite{}
	res, err := writeFINSBatch(f, []driver.WriteRequest{
		finsWriteReqLayout("p1", "D100", "int16", "1", "GHEFCDAB"),
	}, &FINSConfig{})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	if res[0].Code != driver.WriteCodeBadValue {
		t.Fatalf("code = %d, want WriteCodeBadValue", res[0].Code)
	}
	if len(f.writes) != 0 {
		t.Fatalf("宽度不匹配不应写设备，实际写了 %d 次", len(f.writes))
	}
}

// TestWriteFINSIgnoresLayoutForInt8 单字节类型没有排列：带 layout 也按设备级
// 写低字节（保存侧本就不允许给它们配布局）。
func TestWriteFINSIgnoresLayoutForInt8(t *testing.T) {
	f := &fakeFINSWrite{}
	_, err := writeFINSBatch(f, []driver.WriteRequest{
		finsWriteReqLayout("p1", "D100", "int8", "18", "BADC"),
	}, &FINSConfig{})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	if got := f.writes[0].data; !bytes.Equal(got, []byte{0x00, 0x12}) {
		t.Errorf("data = % X, want 00 12", got)
	}
}

// TestDriverReadPerPointLayout 一台设备、两个 float32 点位、两种点位级布局：
// 同一次区间读取里，第一个按 ABCD、第二个按 CDAB 解出同一个值。
func TestDriverReadPerPointLayout(t *testing.T) {
	cfg := DefaultFINSConfig()
	mt := &mockTransport{fn: func(area finsArea, word, count uint16) ([]byte, error) {
		if area != AreaDM || word != 100 || count != 4 {
			t.Errorf("unexpected read: area=0x%02X word=%d count=%d", area, word, count)
		}
		return []byte{0x41, 0xB4, 0x00, 0x00, 0x00, 0x00, 0x41, 0xB4}, nil
	}}
	d := &finsDriver{config: cfg, client: mt}

	addrs := []driver.DeviceAddress{
		{ID: "a", Name: "D100", DataType: "float32", ByteOrderLayout: "ABCD"},
		{ID: "b", Name: "D102", DataType: "float32", ByteOrderLayout: "CDAB"},
	}
	results, err := d.Read(addrs)
	if err != nil {
		t.Fatalf("Read = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	for _, r := range results {
		if r.Value != "22.5" || r.Quality != driver.QualityGood {
			t.Errorf("point %s = %+v, want value 22.5 quality %d", r.DeviceAddressID, r, driver.QualityGood)
		}
	}
}

// TestDriverReadLayoutWidthMismatch 宽度不匹配只坏这一个点位（QualityBadParse），
// 不让整批 Read 失败。
func TestDriverReadLayoutWidthMismatch(t *testing.T) {
	cfg := DefaultFINSConfig()
	mt := &mockTransport{fn: func(area finsArea, word, count uint16) ([]byte, error) {
		return []byte{0x12, 0x34}, nil
	}}
	d := &finsDriver{config: cfg, client: mt}

	results, err := d.Read([]driver.DeviceAddress{
		{ID: "a", Name: "D100", DataType: "int16", ByteOrderLayout: "CDAB"},
	})
	if err != nil {
		t.Fatalf("手滑的布局不应让整批 Read 失败: %v", err)
	}
	if len(results) != 1 || results[0].Quality != driver.QualityBadParse {
		t.Fatalf("results = %+v, want QualityBadParse", results)
	}
}
