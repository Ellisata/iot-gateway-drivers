// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package fanuc

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Ellisata/iot-gateway-sdk/driver"

	cipcore "github.com/Ellisata/iot-gateway-drivers/fanuc/cipcore"
)

// 本文件测点位范围扫描。假设备是 fake_test.go 的 fakeFANUC —— 它按规范独立
// 实现了块读与单点读，与 cipcore 的请求构造互不依赖，因此这里的用例同时
// 覆盖了编解码与扫描逻辑。

// testSession 把驱动实例包成 SessionAccess。
type testSession struct{ drv driver.Driver }

func (s testSession) Do(fn func(driver.Driver) error) error { return fn(s.drv) }

// connectScanDriver 连上一台假设备，并返回它的驱动实例。
//
// 限流调到上限 50 次/秒，让用例不必为每次请求等 100ms。
func connectScanDriver(t *testing.T, f *fakeFANUC) driver.Driver {
	t.Helper()
	d := NewDriver()
	t.Cleanup(func() { _ = d.Close() })
	cfg := fmt.Sprintf(`{"host":"127.0.0.1","port":%d,"timeoutMs":2000,"maxRequestsPerSecond":50}`, f.port())
	if err := d.Connect(cfg); err != nil {
		t.Fatalf("连接假设备: %v", err)
	}
	return d
}

func scanDeadline() time.Time { return time.Now().Add(5 * time.Second) }

// scan 跑一次扫描并在失败时终止用例。
func scan(t *testing.T, drv driver.Driver, options string) *driver.PointScanResult {
	t.Helper()
	res, err := ScanPoints(testSession{drv}, json.RawMessage(options), scanDeadline())
	if err != nil {
		t.Fatalf("ScanPoints 不应报错: %v", err)
	}
	return res
}

func pointNames(pts []driver.DiscoveredPoint) []string {
	out := make([]string, len(pts))
	for i, p := range pts {
		out[i] = p.Name
	}
	return out
}

func hasPoint(pts []driver.DiscoveredPoint, name, value string) bool {
	for _, p := range pts {
		if p.Name == name && p.Value == value {
			return true
		}
	}
	return false
}

// ==================== 选项解析 ====================

func TestParsePointScanOptions(t *testing.T) {
	cases := []struct {
		name    string
		options string
		class   uint16
		dtype   string
		start   uint16
		count   int
		wantErr bool
	}{
		{"默认类型与起始号", `{"count":10}`, objectIntegerRegister, typeInt32, 1, 10, false},
		{"显式 Long", `{"dataType":"Long","startAddress":5,"count":3}`, objectIntegerRegister, typeInt32, 5, 3, false},
		{"DWord", `{"dataType":"DWord","count":3}`, objectIntegerRegister, typeUint32, 1, 3, false},
		{"Float", `{"dataType":"Float","count":3}`, objectRealRegister, typeFloat32, 1, 3, false},
		// 内部裸名也接受：前端历史配置与手写 JSON 都会出现。
		{"内部类型名", `{"dataType":"float32","count":3}`, objectRealRegister, typeFloat32, 1, 3, false},
		// 数字以字符串传入（form-create 的某些控件）不应让整次扫描失败。
		{"数字是字符串", `{"startAddress":"7","count":"2"}`, objectIntegerRegister, typeInt32, 7, 2, false},
		// 越过属性号上限的部分截断，而不是报错。
		{"末尾截断", `{"startAddress":65530,"count":100}`, objectIntegerRegister, typeInt32, 65530, 6, false},
		{"选项为空", ``, 0, "", 0, 0, true},
		{"类型不支持", `{"dataType":"Word","count":3}`, 0, "", 0, 0, true},
		{"起始号为0", `{"startAddress":0,"count":3}`, 0, "", 0, 0, true},
		{"数量缺失", `{"startAddress":1}`, 0, "", 0, 0, true},
		{"数量为0", `{"count":0}`, 0, "", 0, 0, true},
		{"选项不是JSON", `{`, 0, "", 0, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var raw json.RawMessage
			if c.options != "" {
				raw = json.RawMessage(c.options)
			}
			opt, err := parsePointScanOptions(raw)
			if c.wantErr {
				if err == nil {
					t.Fatal("期望报错，实际没有")
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePointScanOptions = %v", err)
			}
			if opt.class != c.class || opt.dataType != c.dtype ||
				opt.start != c.start || opt.count != c.count {
				t.Fatalf("解析结果 = {class:0x%02X type:%s start:%d count:%d}，"+
					"期望 {class:0x%02X type:%s start:%d count:%d}",
					opt.class, opt.dataType, opt.start, opt.count,
					c.class, c.dtype, c.start, c.count)
			}
		})
	}
}

// ==================== 扫描 ====================

func TestPointScanContiguousRegisters(t *testing.T) {
	f := newFakeFANUC(t, cipcore.VendorFANUC, false)
	f.setInt(1, 10)
	f.setInt(2, 0)
	f.setInt(3, -5)
	f.setInt(4, 7)
	drv := connectScanDriver(t, f)

	res := scan(t, drv, `{"dataType":"Long","startAddress":1,"count":4}`)

	if res.Scanned != 4 || res.Readable != 4 || res.Unreadable != 0 {
		t.Fatalf("计数 = scanned:%d readable:%d unreadable:%d，期望 4/4/0",
			res.Scanned, res.Readable, res.Unreadable)
	}
	if res.AssumedCommonDataType != driver.TypeLong {
		t.Errorf("AssumedCommonDataType = %q，期望 %q", res.AssumedCommonDataType, driver.TypeLong)
	}
	if got := pointNames(res.Points); fmt.Sprint(got) != "[R[1] R[2] R[3] R[4]]" {
		t.Errorf("点位 = %v", got)
	}
	if !hasPoint(res.Points, "R[1]", "10") || !hasPoint(res.Points, "R[3]", "-5") ||
		!hasPoint(res.Points, "R[4]", "7") {
		t.Errorf("点位值不对: %+v", res.Points)
	}
	// 值为 0 的点位仍然上报（不做零值过滤），只是不带 Value。
	for _, p := range res.Points {
		if p.Name == "R[2]" {
			if p.Value != "" || p.Note != pointScanZeroNote {
				t.Errorf("R[2] = {value:%q note:%q}，期望只带零值说明", p.Value, p.Note)
			}
		}
	}
}

// TestPointScanHoleOfOtherType 整型范围里夹着实型寄存器：整块被拒（0x14），
// 二分降级后把夹层标成"读不到"，两侧照常产出。
func TestPointScanHoleOfOtherType(t *testing.T) {
	f := newFakeFANUC(t, cipcore.VendorFANUC, false)
	f.setInt(1, 1)
	f.setInt(2, 2)
	f.setReal(3, 1.5)
	f.setInt(4, 4)
	f.setInt(5, 5)
	drv := connectScanDriver(t, f)

	res := scan(t, drv, `{"dataType":"Long","startAddress":1,"count":5}`)

	if res.Scanned != 5 || res.Readable != 4 || res.Unreadable != 1 {
		t.Fatalf("计数 = %d/%d/%d，期望 5/4/1", res.Scanned, res.Readable, res.Unreadable)
	}
	if got := pointNames(res.Points); fmt.Sprint(got) != "[R[1] R[2] R[4] R[5]]" {
		t.Errorf("点位 = %v，R[3] 应被标成读不到", got)
	}
}

func TestPointScanRealClass(t *testing.T) {
	f := newFakeFANUC(t, cipcore.VendorFANUC, false)
	f.setReal(1, 1.25)
	f.setReal(2, -2.5)
	drv := connectScanDriver(t, f)

	res := scan(t, drv, `{"dataType":"Float","startAddress":1,"count":2}`)

	if res.AssumedCommonDataType != driver.TypeFloat {
		t.Errorf("AssumedCommonDataType = %q，期望 %q", res.AssumedCommonDataType, driver.TypeFloat)
	}
	if !hasPoint(res.Points, "R[1]", "1.25") || !hasPoint(res.Points, "R[2]", "-2.5") {
		t.Errorf("实型值不对: %+v", res.Points)
	}
}

// TestPointScanBlockUnsupported 设备不认块读（0x08）时改用单读，结果不变。
func TestPointScanBlockUnsupported(t *testing.T) {
	f := newFakeFANUC(t, cipcore.VendorFANUC, false)
	for i := uint16(1); i <= 5; i++ {
		f.setInt(i, int32(i*10))
	}
	f.setBlockUnsupported(true)
	drv := connectScanDriver(t, f)

	res := scan(t, drv, `{"dataType":"Long","startAddress":1,"count":5}`)

	if res.Scanned != 5 || res.Readable != 5 || res.Unreadable != 0 {
		t.Fatalf("计数 = %d/%d/%d，期望 5/5/0", res.Scanned, res.Readable, res.Unreadable)
	}
	if !hasPoint(res.Points, "R[1]", "10") || !hasPoint(res.Points, "R[5]", "50") {
		t.Errorf("点位值不对: %+v", res.Points)
	}
}

// TestPointScanBlockShiftDetected 设备忽略起始号、总从 R[1] 返回：交叉校验
// 必须发现错位并改走单读，绝不能把错位的数据当成 R[3..] 的值。
func TestPointScanBlockShiftDetected(t *testing.T) {
	f := newFakeFANUC(t, cipcore.VendorFANUC, false)
	for i := uint16(1); i <= 10; i++ {
		f.setInt(i, int32(i))
	}
	f.setBlockShift(true)
	drv := connectScanDriver(t, f)

	res := scan(t, drv, `{"dataType":"Long","startAddress":3,"count":5}`)

	if res.Readable != 5 || res.Unreadable != 0 {
		t.Fatalf("计数 = %d/%d，期望 5/0", res.Readable, res.Unreadable)
	}
	for i := uint16(3); i <= 7; i++ {
		want := fmt.Sprintf("%d", i)
		if !hasPoint(res.Points, formatRegisterName(i), want) {
			t.Errorf("R[%d] 期望值 %s，实际点位 %+v", i, want, res.Points)
		}
	}
}

// TestPointScanMissingClass 控制器没有实型对象（R-30iA 早期软件）时，
// 整段按"读不到"记账，不逐点浪费请求。
func TestPointScanMissingClass(t *testing.T) {
	f := newFakeFANUC(t, cipcore.VendorFANUC, true) // noRealClass
	drv := connectScanDriver(t, f)

	res := scan(t, drv, `{"dataType":"Float","startAddress":1,"count":3}`)

	if res.Scanned != 3 || res.Readable != 0 || res.Unreadable != 3 {
		t.Fatalf("计数 = %d/%d/%d，期望 3/0/3", res.Scanned, res.Readable, res.Unreadable)
	}
	if len(res.Points) != 0 {
		t.Fatalf("不应有点位，实际 %+v", res.Points)
	}
	// 块读 + 一次单点探测即可判定，不应退化成逐点。
	if got := f.blockCount(); got > 2 {
		t.Errorf("块读请求 %d 次，期望不超过 2 次", got)
	}
}

// TestPointScanDeadlineAlreadyPassed 预算已耗尽时直接返回，不读设备。
func TestPointScanDeadlineAlreadyPassed(t *testing.T) {
	f := newFakeFANUC(t, cipcore.VendorFANUC, false)
	f.setInt(1, 1)
	drv := connectScanDriver(t, f)

	res, err := ScanPoints(testSession{drv}, json.RawMessage(`{"count":10}`),
		time.Now().Add(-time.Second))
	if err != nil {
		t.Fatalf("预算耗尽不应报错: %v", err)
	}
	if !res.Incomplete {
		t.Error("预算耗尽时 Incomplete 应为 true")
	}
	if res.Scanned != 0 {
		t.Errorf("Scanned = %d，期望 0", res.Scanned)
	}
}

// TestPointScanTruncatesAtMaxPoints 超过 MaxPoints 时精确截断并置 Truncated。
func TestPointScanTruncatesAtMaxPoints(t *testing.T) {
	f := newFakeFANUC(t, cipcore.VendorFANUC, false)
	for i := uint16(1); i <= pointScanMaxPoints+10; i++ {
		f.setInt(i, int32(i))
	}
	drv := connectScanDriver(t, f)

	res := scan(t, drv, fmt.Sprintf(`{"startAddress":1,"count":%d}`, pointScanMaxPoints+10))

	if !res.Truncated {
		t.Error("达到 MaxPoints 时 Truncated 应为 true")
	}
	if len(res.Points) != pointScanMaxPoints || res.Readable != pointScanMaxPoints {
		t.Fatalf("点位 %d、可读 %d，期望都是 %d",
			len(res.Points), res.Readable, pointScanMaxPoints)
	}
	if res.Scanned != pointScanMaxPoints {
		t.Errorf("Scanned = %d，期望与已产出条数一致（%d）", res.Scanned, pointScanMaxPoints)
	}
}

// TestPointScanNamesRoundTrip 产出的地址必须能被本插件自己的地址解析器还原 ——
// 解析不出来的地址写进 device_address 后永远采不到值。
func TestPointScanNamesRoundTrip(t *testing.T) {
	f := newFakeFANUC(t, cipcore.VendorFANUC, false)
	f.setInt(7, 1)
	f.setReal(8, 2.5)
	drv := connectScanDriver(t, f)

	for _, c := range []struct {
		options  string
		dataType string
		index    uint16
		class    uint16
	}{
		{`{"dataType":"Long","startAddress":7,"count":1}`, typeInt32, 7, objectIntegerRegister},
		{`{"dataType":"Float","startAddress":8,"count":1}`, typeFloat32, 8, objectRealRegister},
	} {
		res := scan(t, drv, c.options)
		if len(res.Points) != 1 {
			t.Fatalf("%s: 扫到 %d 个点位", c.options, len(res.Points))
		}
		addr, err := parseRegisterAddress(res.Points[0].Name, c.dataType)
		if err != nil {
			t.Fatalf("%s: 地址 %q 无法还原: %v", c.options, res.Points[0].Name, err)
		}
		if addr.Index != c.index || addr.Class != c.class {
			t.Errorf("%s: 还原为 %+v，期望 index=%d class=0x%02X",
				c.options, addr, c.index, c.class)
		}
	}
}

// ==================== 能力声明 ====================

func TestPointDiscoverySpec(t *testing.T) {
	pd := PointDiscovery()
	if pd.Title == "" {
		t.Error("点位扫描声明缺少 Title")
	}
	if len(pd.FormJSON) == 0 || !json.Valid(pd.FormJSON) {
		t.Error("点位扫描声明缺少合法的 FormJSON")
	}
	if pd.MaxBudgetMs != pointScanBudgetMs {
		t.Errorf("MaxBudgetMs = %d，期望 %d", pd.MaxBudgetMs, pointScanBudgetMs)
	}
	if pd.MaxPoints != pointScanMaxPoints {
		t.Errorf("MaxPoints = %d，期望 %d", pd.MaxPoints, pointScanMaxPoints)
	}
	if _, ok := CommonTypeMap()[pd.DefaultCommonDataType]; !ok {
		t.Errorf("DefaultCommonDataType %q 不是 TypeMap 的键", pd.DefaultCommonDataType)
	}
}
