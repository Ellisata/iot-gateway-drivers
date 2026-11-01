// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package modbusdrv

import (
	"errors"
	"math"
	"testing"

	goburrowModbus "github.com/goburrow/modbus"

	"github.com/Ellisata/iot-gateway-sdk/driver"
	"github.com/Ellisata/iot-gateway-sdk/testkit/fake"
)

// fakeWriteTransport 记录写调用，不碰任何真实连接。
type fakeWriteTransport struct {
	coils map[uint16]bool
	regs  map[uint16]uint16
	multi map[uint16][]byte

	coilErr  error
	regErr   error
	multiErr error
}

func newFakeWriteTransport() *fakeWriteTransport {
	return &fakeWriteTransport{
		coils: make(map[uint16]bool),
		regs:  make(map[uint16]uint16),
		multi: make(map[uint16][]byte),
	}
}

func (f *fakeWriteTransport) WriteSingleCoil(address uint16, value bool) error {
	if f.coilErr != nil {
		return f.coilErr
	}
	f.coils[address] = value
	return nil
}

func (f *fakeWriteTransport) WriteSingleRegister(address, value uint16) error {
	if f.regErr != nil {
		return f.regErr
	}
	f.regs[address] = value
	return nil
}

func (f *fakeWriteTransport) WriteMultipleRegisters(address uint16, values []byte) error {
	if f.multiErr != nil {
		return f.multiErr
	}
	cp := append([]byte(nil), values...)
	f.multi[address] = cp
	return nil
}

func TestWriteHoldingRegister(t *testing.T) {
	f := newFakeWriteTransport()
	cfg := DefaultModbusTcpConfig()

	results, err := writeBatch("test", f, []driver.WriteRequest{
		{DeviceAddressID: "p1", Name: "40001", DataType: "int16", Value: "42"},
	}, cfg)
	if err != nil {
		t.Fatalf("writeBatch: %v", err)
	}
	if len(results) != 1 || !results[0].OK() {
		t.Fatalf("结果 = %+v，期望成功", results)
	}
	if got := f.regs[0]; got != 42 {
		t.Errorf("寄存器[0] = %d，期望 42", got)
	}
	if results[0].Value != "42" {
		t.Errorf("结果 Value = %q，期望 42", results[0].Value)
	}
}

func TestWriteCoil(t *testing.T) {
	f := newFakeWriteTransport()
	cfg := DefaultModbusTcpConfig()

	results, err := writeBatch("test", f, []driver.WriteRequest{
		{DeviceAddressID: "p1", Name: "00001", DataType: "bool", Value: "true"},
		{DeviceAddressID: "p2", Name: "00002", DataType: "bool", Value: "0"},
	}, cfg)
	if err != nil {
		t.Fatalf("writeBatch: %v", err)
	}
	if !results[0].OK() || !results[1].OK() {
		t.Fatalf("结果 = %+v，期望两条都成功", results)
	}
	if !f.coils[0] {
		t.Error("线圈[0] = false，期望 true")
	}
	if f.coils[1] {
		t.Error("线圈[1] = true，期望 false")
	}
}

func TestWriteReadOnlyAreaRejected(t *testing.T) {
	f := newFakeWriteTransport()
	cfg := DefaultModbusTcpConfig()

	results, err := writeBatch("test", f, []driver.WriteRequest{
		{DeviceAddressID: "in", Name: "30001", DataType: "int16", Value: "1"},
		{DeviceAddressID: "di", Name: "10001", DataType: "bool", Value: "1"},
	}, cfg)
	if err != nil {
		t.Fatalf("writeBatch: %v", err)
	}
	for _, r := range results {
		if r.Code != driver.WriteCodeNotWritable {
			t.Errorf("%s code = %d，期望 NotWritable", r.DeviceAddressID, r.Code)
		}
	}
	if len(f.regs) != 0 || len(f.coils) != 0 {
		t.Error("只读区不应触碰传输层")
	}
}

func TestWriteValueValidation(t *testing.T) {
	f := newFakeWriteTransport()
	cfg := DefaultModbusTcpConfig()

	cases := []driver.WriteRequest{
		{DeviceAddressID: "bad-int", Name: "40001", DataType: "int16", Value: "abc"},
		{DeviceAddressID: "coil-int", Name: "00001", DataType: "int16", Value: "1"},
		{DeviceAddressID: "string", Name: "40002", DataType: "string", Value: "hello"},
		{DeviceAddressID: "bad-addr", Name: "not-an-address", DataType: "int16", Value: "1"},
	}
	results, err := writeBatch("test", f, cases, cfg)
	if err != nil {
		t.Fatalf("writeBatch: %v", err)
	}
	if len(results) != len(cases) {
		t.Fatalf("结果数 = %d，期望 %d", len(results), len(cases))
	}
	for i, r := range results {
		if r.OK() {
			t.Errorf("cases[%d] 应失败，实际成功", i)
		}
	}
	if results[0].Code != driver.WriteCodeBadValue {
		t.Errorf("非法值 code = %d，期望 BadValue", results[0].Code)
	}
	if results[2].Code != driver.WriteCodeBadValue {
		t.Errorf("string 无编码器 code = %d，期望 BadValue", results[2].Code)
	}
	if results[3].Code != driver.WriteCodeBadAddress {
		t.Errorf("非法地址 code = %d，期望 BadAddress", results[3].Code)
	}
}

// float32 写入后再用读路径解析，必须得到原值 —— byteOrder/wordOrder 两边必须互逆。
func TestWriteFloat32RoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name      string
		byteOrder string
		wordOrder string
	}{
		{"big/big", ByteOrderBigEndian, WordOrderBigEndian},
		{"little/big", ByteOrderLittleEndian, WordOrderBigEndian},
		{"big/little-word", ByteOrderBigEndian, WordOrderLittleEndian},
		{"little/little-word", ByteOrderLittleEndian, WordOrderLittleEndian},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeWriteTransport()
			cfg := DefaultModbusTcpConfig()
			cfg.ByteOrder = tc.byteOrder
			cfg.WordOrder = tc.wordOrder

			results, err := writeBatch("test", f, []driver.WriteRequest{
				{DeviceAddressID: "p1", Name: "40001", DataType: "float32", Value: "3.5"},
			}, cfg)
			if err != nil || !results[0].OK() {
				t.Fatalf("写入失败: results=%+v err=%v", results, err)
			}
			raw := f.multi[0]
			if len(raw) != 4 {
				t.Fatalf("写入字节长度 = %d，期望 4", len(raw))
			}
			got, err := ParseReadResult(raw, "float32", tc.byteOrder, tc.wordOrder)
			if err != nil {
				t.Fatalf("回读解析: %v", err)
			}
			var gotF float64
			switch v := got.(type) {
			case float32:
				gotF = float64(v)
			case float64:
				gotF = v
			}
			if math.Abs(gotF-3.5) > 1e-6 {
				t.Errorf("回读 = %v，期望 3.5", got)
			}
		})
	}
}

func TestWriteUint8PadsToRegister(t *testing.T) {
	cfg := DefaultModbusTcpConfig()
	cfg.ByteOrder = ByteOrderLittleEndian
	f := newFakeWriteTransport()

	results, err := writeBatch("test", f, []driver.WriteRequest{
		{DeviceAddressID: "p1", Name: "40001", DataType: "uint8", Value: "18"},
	}, cfg)
	if err != nil || !results[0].OK() {
		t.Fatalf("写入失败: results=%+v err=%v", results, err)
	}
	// 小端：raw = [0x12, 0x00]，WriteSingleRegister 发的是 raw 的大端表示 0x1200。
	if got := f.regs[0]; got != 0x1200 {
		t.Errorf("寄存器[0] = 0x%04x，期望 0x1200", got)
	}
}

func TestWriteTransportError(t *testing.T) {
	f := newFakeWriteTransport()
	f.regErr = errors.New("i/o timeout")
	cfg := DefaultModbusTcpConfig()

	results, err := writeBatch("test", f, []driver.WriteRequest{
		{DeviceAddressID: "p1", Name: "40001", DataType: "int16", Value: "1"},
	}, cfg)
	if err == nil {
		t.Fatal("期望返回链路错误")
	}
	if len(results) != 1 || results[0].Code != driver.WriteCodeTransport {
		t.Fatalf("结果 = %+v，期望 Transport", results)
	}
}

func TestWriteProtocolErrorClassified(t *testing.T) {
	f := newFakeWriteTransport()
	f.regErr = modbusExc(goburrowModbus.ExceptionCodeIllegalDataAddress)
	cfg := DefaultModbusTcpConfig()

	results, err := writeBatch("test", f, []driver.WriteRequest{
		{DeviceAddressID: "p1", Name: "40001", DataType: "int16", Value: "1"},
	}, cfg)
	if err != nil {
		t.Fatalf("协议异常不应升级为链路错误: %v", err)
	}
	if results[0].Code != driver.WriteCodeBadAddress {
		t.Errorf("code = %d，期望 BadAddress", results[0].Code)
	}
}

func TestWriteConfigMode(t *testing.T) {
	cfg := DefaultModbusTcpConfig()
	cfg.StartAddress = 100
	cfg.FunctionCode = FuncCodeReadHoldingRegisters
	f := newFakeWriteTransport()

	results, err := writeBatch("test", f, []driver.WriteRequest{
		{DeviceAddressID: "in-range", Name: "40101", DataType: "int16", Value: "1"},
		{DeviceAddressID: "below", Name: "40001", DataType: "int16", Value: "1"},
	}, cfg)
	if err != nil {
		t.Fatalf("writeBatch: %v", err)
	}
	if !results[0].OK() {
		t.Errorf("in-range 应成功: %+v", results[0])
	}
	if results[1].Code != driver.WriteCodeBadAddress {
		t.Errorf("below code = %d，期望 BadAddress", results[1].Code)
	}

	cfg.FunctionCode = FuncCodeReadInputRegisters
	results, err = writeBatch("test", f, []driver.WriteRequest{
		{DeviceAddressID: "ro", Name: "40101", DataType: "int16", Value: "1"},
	}, cfg)
	if err != nil {
		t.Fatalf("writeBatch: %v", err)
	}
	if results[0].Code != driver.WriteCodeNotWritable {
		t.Errorf("只读配置模式 code = %d，期望 NotWritable", results[0].Code)
	}
}

// 结果与请求按 DeviceAddressID 一一对应，顺序不得错位。
func TestWriteResultsPreserveOrder(t *testing.T) {
	f := newFakeWriteTransport()
	cfg := DefaultModbusTcpConfig()

	reqs := []driver.WriteRequest{
		{DeviceAddressID: "a", Name: "40001", DataType: "int16", Value: "1"},
		{DeviceAddressID: "b", Name: "40002", DataType: "int16", Value: "2"},
		{DeviceAddressID: "c", Name: "40003", DataType: "int16", Value: "3"},
	}
	results, err := writeBatch("test", f, reqs, cfg)
	if err != nil {
		t.Fatalf("writeBatch: %v", err)
	}
	for i := range reqs {
		if results[i].DeviceAddressID != reqs[i].DeviceAddressID {
			t.Errorf("results[%d].id = %q，期望 %q", i, results[i].DeviceAddressID, reqs[i].DeviceAddressID)
		}
	}
	if f.regs[0] != 1 || f.regs[1] != 2 || f.regs[2] != 3 {
		t.Errorf("寄存器写入值 = %v，期望 1,2,3", f.regs)
	}
}

// ==================== 真实 TCP + 假 PLC 的端到端 ====================

// TestWriteThenRead_TCP 用真实 Modbus TCP 连接写假 PLC，再用同一驱动读回。
// 这是编码、帧格式、假设备状态更新三者的联合验证；上面的单元用例只覆盖编码。
func TestWriteThenRead_TCP(t *testing.T) {
	regs := map[uint16]uint16{}
	bits := map[uint16]bool{}
	srv, err := fake.NewModbusTCPWithOption(
		fake.WithModbusRegisters(regs),
		fake.WithModbusBits(bits),
	)
	if err != nil {
		t.Fatalf("起假 PLC: %v", err)
	}
	defer srv.Close()

	drv := NewTCP()
	if err := drv.Connect(tcpJSON(t, srv)); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer drv.Close()
	w, ok := drv.(driver.Writer)
	if !ok {
		t.Fatal("Modbus TCP 驱动必须实现 driver.Writer")
	}

	reqs := []driver.WriteRequest{
		{DeviceAddressID: "reg", Name: "40001", DataType: "int16", Value: "42"},
		{DeviceAddressID: "coil", Name: "00001", DataType: "bool", Value: "true"},
		{DeviceAddressID: "float", Name: "40010", DataType: "float32", Value: "3.5"},
	}
	results, err := w.Write(reqs)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	for i, r := range results {
		if !r.OK() {
			t.Fatalf("results[%d] = %+v", i, r)
		}
	}
	if regs[0] != 42 || !bits[0] {
		t.Fatalf("假设备状态 = regs[0]=%d bits[0]=%v，期望 42/true", regs[0], bits[0])
	}

	addrs := []driver.DeviceAddress{
		{ID: "reg", Name: "40001", DataType: "int16"},
		{ID: "coil", Name: "00001", DataType: "bool"},
		{ID: "float", Name: "40010", DataType: "float32"},
	}
	read, err := drv.Read(addrs)
	if err != nil {
		t.Fatalf("Read back: %v", err)
	}
	want := map[string]string{"reg": "42", "coil": "1", "float": "3.5"}
	for _, r := range read {
		if r.Quality != driver.QualityGood {
			t.Errorf("%s quality=%d，期望 Good", r.DeviceAddressID, r.Quality)
		}
		if r.Value != want[r.DeviceAddressID] {
			t.Errorf("%s 回读 = %q，期望 %q", r.DeviceAddressID, r.Value, want[r.DeviceAddressID])
		}
	}
}
