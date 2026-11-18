// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package fanuc

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Ellisata/iot-gateway-sdk/driver"

	cipcore "github.com/Ellisata/iot-gateway-drivers/fanuc/cipcore"
)

func configJSON(f *fakeFANUC) string {
	return fmt.Sprintf(`{"host":"127.0.0.1","port":%d,"timeoutMs":1000,"maxRequestsPerSecond":50}`, f.port())
}

func startDriver(t testing.TB, f *fakeFANUC) *fanucDriver {
	t.Helper()
	return startDriverWithConfig(t, configJSON(f))
}

func startDriverWithConfig(t testing.TB, cfg string) *fanucDriver {
	t.Helper()
	d := newFANUCDriver()
	if err := d.Connect(cfg); err != nil {
		t.Fatalf("Connect() = %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func configJSONWithMax(f *fakeFANUC, maxRegisters int) string {
	return fmt.Sprintf(
		`{"host":"127.0.0.1","port":%d,"timeoutMs":1000,"maxRequestsPerSecond":50,"maxRegistersPerRead":%d}`,
		f.port(), maxRegisters)
}

func TestReadIntegerAndReal(t *testing.T) {
	f := newFakeFANUC(t, 591, false)
	f.setInt(1, 42)
	f.setReal(2, 3.5)
	d := startDriver(t, f)

	addrs := []driver.DeviceAddress{
		{ID: "a", Name: "R[1]", DataType: "int32"},
		{ID: "b", Name: "R[2]", DataType: "float32"},
		{ID: "c", Name: "R[2]", DataType: "int32"},  // 实型寄存器用整型对象读 → 0x14
		{ID: "d", Name: "R[99]", DataType: "int32"}, // 不存在 → 0x05
		{ID: "e", Name: "X[1]", DataType: "int32"},  // 本地解析失败
	}
	got, err := d.Read(addrs)
	if err != nil {
		t.Fatalf("Read() = %v", err)
	}
	if len(got) != len(addrs) {
		t.Fatalf("len = %d, want %d", len(got), len(addrs))
	}
	for i := range addrs {
		if got[i].DeviceAddressID != addrs[i].ID {
			t.Fatalf("result[%d].ID = %q, want %q", i, got[i].DeviceAddressID, addrs[i].ID)
		}
	}

	if got[0].Value != "42" || got[0].Kind != driver.KindInt || got[0].Quality != driver.QualityGood {
		t.Fatalf("R[1] = %+v", got[0])
	}
	if got[1].Value != "3.5" || got[1].Kind != driver.KindFloat || got[1].Quality != driver.QualityGood {
		t.Fatalf("R[2] = %+v", got[1])
	}
	for i := 2; i < len(got); i++ {
		if got[i].Quality != driver.QualityBadAddress {
			t.Fatalf("result[%d] quality = %d, want QualityBadAddress", i, got[i].Quality)
		}
		if got[i].Value != "" {
			t.Fatalf("result[%d] has value %q on bad address", i, got[i].Value)
		}
	}
}

func TestReadDegradesPerPointWhenRealClassUnsupported(t *testing.T) {
	// R-30iA 早期软件画像：0x6C 不存在（0x08），0x6B 正常。
	f := newFakeFANUC(t, 591, true)
	f.setInt(1, 7)
	f.setReal(2, 3.5)
	d := startDriver(t, f)

	got, err := d.Read([]driver.DeviceAddress{
		{ID: "int", Name: "R[1]", DataType: "int32"},
		{ID: "real", Name: "R[2]", DataType: "float32"},
	})
	if err != nil {
		t.Fatalf("Read() = %v", err)
	}
	if got[0].Quality != driver.QualityGood || got[0].Value != "7" {
		t.Fatalf("int point = %+v", got[0])
	}
	if got[1].Quality != driver.QualityBadAddress {
		t.Fatalf("real point quality = %d, want QualityBadAddress", got[1].Quality)
	}
}

func TestReadDeduplicatesSameRegister(t *testing.T) {
	f := newFakeFANUC(t, 591, false)
	f.setInt(1, -1) // 同一段 4 字节：int32 读 -1，uint32 读 4294967295
	d := startDriver(t, f)

	got, err := d.Read([]driver.DeviceAddress{
		{ID: "signed", Name: "R[1]", DataType: "int32"},
		{ID: "unsigned", Name: "R[1]", DataType: "uint32"},
	})
	if err != nil {
		t.Fatalf("Read() = %v", err)
	}
	if got[0].Value != "-1" {
		t.Fatalf("int32 value = %q, want -1", got[0].Value)
	}
	if got[1].Value != "4294967295" {
		t.Fatalf("uint32 value = %q, want 4294967295", got[1].Value)
	}
	if n := f.readCount(0x6B, 1); n != 1 {
		t.Fatalf("device saw %d reads of R[1], want 1 (deduplicated)", n)
	}
}

// ==================== 块读 ====================

func TestReadBlockContiguous(t *testing.T) {
	f := newFakeFANUC(t, 591, false)
	for i := 1; i <= 60; i++ {
		f.setInt(uint16(i), int32(i*10))
	}
	d := startDriverWithConfig(t, configJSONWithMax(f, 100))

	addrs := benchAddrs(60)
	got, err := d.Read(addrs)
	if err != nil {
		t.Fatalf("Read() = %v", err)
	}
	for i := range got {
		want := fmt.Sprintf("%d", (i+1)*10)
		if got[i].Quality != driver.QualityGood || got[i].Value != want {
			t.Fatalf("R[%d] = %+v, want value %s", i+1, got[i], want)
		}
	}
	// 60 个连续寄存器 = 1 次块读 + 首次校验的 2 次单点读（首、末）。
	if n := f.blockCount(); n != 1 {
		t.Fatalf("block requests = %d, want 1", n)
	}
	if n := f.singleCount(); n != 2 {
		t.Fatalf("single reads = %d, want 2 (first/last verification)", n)
	}

	// 第二轮不再校验，只有块读。
	if _, err := d.Read(addrs); err != nil {
		t.Fatalf("second Read() = %v", err)
	}
	if n := f.blockCount(); n != 2 {
		t.Fatalf("block requests after second read = %d, want 2", n)
	}
	if n := f.singleCount(); n != 2 {
		t.Fatalf("single reads after second read = %d, want still 2", n)
	}
}

func TestReadBlockMergesSmallGaps(t *testing.T) {
	f := newFakeFANUC(t, 591, false)
	for i := 1; i <= 6; i++ {
		f.setInt(uint16(i), int32(i*3))
	}
	d := startDriverWithConfig(t, configJSONWithMax(f, 100))

	// R1,R2,R5,R6：间隙 2 ≤ blockMergeGap，应合成一块 R[1..6]。
	var addrs []driver.DeviceAddress
	for _, idx := range []int{1, 2, 5, 6} {
		addrs = append(addrs, driver.DeviceAddress{
			ID:       fmt.Sprintf("p-%d", idx),
			Name:     fmt.Sprintf("R[%d]", idx),
			DataType: "int32",
		})
	}
	got, err := d.Read(addrs)
	if err != nil {
		t.Fatalf("Read() = %v", err)
	}
	for i, idx := range []int{1, 2, 5, 6} {
		want := fmt.Sprintf("%d", idx*3)
		if got[i].Quality != driver.QualityGood || got[i].Value != want {
			t.Fatalf("R[%d] = %+v, want %s", idx, got[i], want)
		}
	}
	if n := f.blockCount(); n != 1 {
		t.Fatalf("block requests = %d, want 1 (gap should be merged)", n)
	}
}

func TestReadBlockRealRegisters(t *testing.T) {
	f := newFakeFANUC(t, 591, false)
	for i := 1; i <= 20; i++ {
		f.setReal(uint16(i), float32(i)/4)
	}
	d := startDriverWithConfig(t, configJSONWithMax(f, 100))

	addrs := make([]driver.DeviceAddress, 20)
	for i := range addrs {
		addrs[i] = driver.DeviceAddress{
			ID:       fmt.Sprintf("r-%d", i+1),
			Name:     fmt.Sprintf("R[%d]", i+1),
			DataType: "float32",
		}
	}
	got, err := d.Read(addrs)
	if err != nil {
		t.Fatalf("Read() = %v", err)
	}
	for i := range got {
		want := fmt.Sprintf("%v", float32(i+1)/4)
		// 格式化走 float32 最短往返表示，用同一条路径生成期望值。
		if got[i].Quality != driver.QualityGood || got[i].Value != want {
			t.Fatalf("R[%d] = %+v, want %s", i+1, got[i], want)
		}
	}
	if n := f.blockCount(); n != 1 {
		t.Fatalf("block requests = %d, want 1 (class 0x6C)", n)
	}
}

func TestReadBlockDisabledByConfig(t *testing.T) {
	f := newFakeFANUC(t, 591, false)
	for i := 1; i <= 10; i++ {
		f.setInt(uint16(i), int32(i))
	}
	d := startDriverWithConfig(t, configJSONWithMax(f, 1))

	got, err := d.Read(benchAddrs(10))
	if err != nil {
		t.Fatalf("Read() = %v", err)
	}
	for i := range got {
		if got[i].Quality != driver.QualityGood {
			t.Fatalf("result[%d] = %+v", i, got[i])
		}
	}
	if n := f.blockCount(); n != 0 {
		t.Fatalf("block requests = %d, want 0 when maxRegistersPerRead=1", n)
	}
	if n := f.singleCount(); n != 10 {
		t.Fatalf("single reads = %d, want 10", n)
	}
}

func TestReadBlockFallsBackWhenUnsupported(t *testing.T) {
	f := newFakeFANUC(t, 591, false)
	f.setBlockUnsupported(true)
	for i := 1; i <= 30; i++ {
		f.setInt(uint16(i), int32(i*7))
	}
	d := startDriverWithConfig(t, configJSONWithMax(f, 100))

	check := func() {
		t.Helper()
		got, err := d.Read(benchAddrs(30))
		if err != nil {
			t.Fatalf("Read() = %v", err)
		}
		for i := range got {
			if got[i].Quality != driver.QualityGood || got[i].Value != fmt.Sprintf("%d", (i+1)*7) {
				t.Fatalf("R[%d] = %+v", i+1, got[i])
			}
		}
	}

	check()
	if n := f.blockCount(); n != 1 {
		t.Fatalf("block requests = %d, want 1 (attempted once)", n)
	}
	if n := f.singleCount(); n != 30 {
		t.Fatalf("single reads = %d, want 30 (fallback)", n)
	}

	// 静默期内不再尝试块读，全部走单读。
	check()
	if n := f.blockCount(); n != 1 {
		t.Fatalf("block requests = %d, want still 1 (block paused)", n)
	}
	if n := f.singleCount(); n != 60 {
		t.Fatalf("single reads = %d, want 60", n)
	}
}

func TestReadBlockDetectsShiftedResponse(t *testing.T) {
	f := newFakeFANUC(t, 591, false)
	f.setBlockShift(true) // 设备忽略起始号，总从 R[1] 返回
	for i := 1; i <= 40; i++ {
		f.setInt(uint16(i), int32(i*100))
	}
	d := startDriverWithConfig(t, configJSONWithMax(f, 100))

	// 从 R[10] 开始，块读若被当真会拿到 R[1..20] 的值（错位 900）。
	addrs := make([]driver.DeviceAddress, 20)
	for i := range addrs {
		idx := i + 10
		addrs[i] = driver.DeviceAddress{
			ID:       fmt.Sprintf("p-%d", idx),
			Name:     fmt.Sprintf("R[%d]", idx),
			DataType: "int32",
		}
	}

	check := func() {
		t.Helper()
		got, err := d.Read(addrs)
		if err != nil {
			t.Fatalf("Read() = %v", err)
		}
		for i := range got {
			want := fmt.Sprintf("%d", (i+10)*100)
			if got[i].Quality != driver.QualityGood || got[i].Value != want {
				t.Fatalf("R[%d] = %+v, want %s", i+10, got[i], want)
			}
		}
	}

	check()
	if n := f.blockCount(); n != 1 {
		t.Fatalf("block requests = %d, want 1", n)
	}
	// 首次校验读 R[10] 即发现错位（1 次），随后回退 20 次单读。
	if n := f.singleCount(); n != 21 {
		t.Fatalf("single reads = %d, want 21 (1 verify + 20 fallback)", n)
	}

	check()
	if n := f.blockCount(); n != 1 {
		t.Fatalf("block requests = %d, want still 1 (block paused after mismatch)", n)
	}
	if n := f.singleCount(); n != 41 {
		t.Fatalf("single reads = %d, want 41", n)
	}
}

func TestQualityOfCIPErr(t *testing.T) {
	if q := qualityOfCIPErr(cipcore.NewGeneralStatusError(cipcore.GeneralStatusAttributeNotSupported)); q != driver.QualityBadAddress {
		t.Fatalf("0x14 quality = %d, want QualityBadAddress", q)
	}
	if q := qualityOfCIPErr(cipcore.NewGeneralStatusError(cipcore.GeneralStatusResourceUnavailable)); q != driver.QualityBadComms {
		t.Fatalf("0x02 quality = %d, want QualityBadComms", q)
	}
}

func TestReadAllInvalidDoesNotTouchDevice(t *testing.T) {
	f := newFakeFANUC(t, 591, false)
	d := startDriver(t, f)

	got, err := d.Read([]driver.DeviceAddress{
		{ID: "a", Name: "PR[1]", DataType: "float32"},
		{ID: "b", Name: "R[0]", DataType: "int32"},
	})
	if err != nil {
		t.Fatalf("Read() = %v", err)
	}
	for i := range got {
		if got[i].Quality != driver.QualityBadAddress {
			t.Fatalf("result[%d] = %+v", i, got[i])
		}
	}
	if n := f.readCount(0x6B, 1); n != 0 {
		t.Fatalf("device saw %d requests, want 0", n)
	}
}

func TestWriteIntegerAndReal(t *testing.T) {
	f := newFakeFANUC(t, 591, false)
	f.setInt(1, 0)
	f.setReal(2, 0)
	d := startDriver(t, f)

	got, err := d.Write([]driver.WriteRequest{
		{DeviceAddressID: "a", Name: "R[1]", DataType: "int32", Value: "123"},
		{DeviceAddressID: "b", Name: "R[2]", DataType: "float32", Value: "1.25"},
		{DeviceAddressID: "c", Name: "R[1]", DataType: "int32", Value: "abc"},
		{DeviceAddressID: "d", Name: "R[2]", DataType: "int32", Value: "5"},
	})
	if err != nil {
		t.Fatalf("Write() = %v", err)
	}
	if got[0].Code != driver.WriteCodeOK || got[0].Value != "123" {
		t.Fatalf("write int = %+v", got[0])
	}
	if got[1].Code != driver.WriteCodeOK || got[1].Value != "1.25" {
		t.Fatalf("write real = %+v", got[1])
	}
	if got[2].Code != driver.WriteCodeBadValue {
		t.Fatalf("write bad value code = %d, want %d", got[2].Code, driver.WriteCodeBadValue)
	}
	if got[3].Code != driver.WriteCodeBadAddress {
		t.Fatalf("write wrong type code = %d, want %d", got[3].Code, driver.WriteCodeBadAddress)
	}

	if v, ok := f.intValue(1); !ok || v != 123 {
		t.Fatalf("device R[1] = %d (ok=%v), want 123", v, ok)
	}
	if v, ok := f.realValue(2); !ok || v != 1.25 {
		t.Fatalf("device R[2] = %v (ok=%v), want 1.25", v, ok)
	}
}

func TestPingReusesAndTemporaryConnections(t *testing.T) {
	f := newFakeFANUC(t, 591, false)
	f.setInt(1, 1)

	d := startDriver(t, f)
	if err := d.Ping(configJSON(f)); err != nil {
		t.Fatalf("Ping(reused) = %v", err)
	}

	// 未连接的新实例走临时连接；结束后不得留下连接状态。
	fresh := newFANUCDriver()
	if err := fresh.Ping(configJSON(f)); err != nil {
		t.Fatalf("Ping(temp) = %v", err)
	}
	if fresh.IsConnected() {
		t.Fatal("Ping 不应把临时实例标记为已连接")
	}
}

func TestPingAcceptsGeneralStatusAsReachable(t *testing.T) {
	// 控制器里没有任何寄存器：读 R[1] 回 0x05，但设备显然是通的。
	f := newFakeFANUC(t, 591, false)
	d := newFANUCDriver()
	if err := d.Ping(configJSON(f)); err != nil {
		t.Fatalf("Ping() = %v，通用状态错误应视为可达", err)
	}
}

func TestConnectRejectsNonFANUCVendor(t *testing.T) {
	f := newFakeFANUC(t, 1, false) // 厂商号 1 = Rockwell
	d := newFANUCDriver()
	err := d.Connect(configJSON(f))
	if err == nil {
		t.Fatal("expected vendor rejection")
	}
	if !strings.Contains(err.Error(), "不是 FANUC") {
		t.Fatalf("error = %v, want a clear vendor message", err)
	}
}

func TestReadReturnsErrorOnTransportFailure(t *testing.T) {
	f := newFakeFANUC(t, 591, false)
	f.setInt(1, 1)
	d := startDriver(t, f)

	f.close()
	if _, err := d.Read([]driver.DeviceAddress{{ID: "a", Name: "R[1]", DataType: "int32"}}); err == nil {
		t.Fatal("expected transport error after device close")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	f := newFakeFANUC(t, 591, false)
	d := startDriver(t, f)
	if err := d.Close(); err != nil {
		t.Fatalf("first Close() = %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("second Close() = %v", err)
	}
	if d.IsConnected() {
		t.Fatal("IsConnected = true after Close")
	}
}
