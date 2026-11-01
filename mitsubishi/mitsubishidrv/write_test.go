// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package mitsubishi

import (
	"errors"
	"net"
	"testing"

	"github.com/Ellisata/iot-gateway-sdk/driver"
)

type fakeMCWrite struct {
	writes  []fakeMCWriteCall
	readTxt []byte
	readErr error
	wrErr   error
}

type fakeMCWriteCall struct {
	device  mcDevice
	head    uint32
	points  uint16
	bitMode bool
	data    []byte
}

func (f *fakeMCWrite) Write(device mcDevice, head uint32, points uint16, bitMode bool, data []byte) error {
	if f.wrErr != nil {
		return f.wrErr
	}
	f.writes = append(f.writes, fakeMCWriteCall{
		device: device, head: head, points: points, bitMode: bitMode,
		data: append([]byte(nil), data...),
	})
	return nil
}

func (f *fakeMCWrite) Read(device mcDevice, head uint32, points uint16, bitMode bool) ([]byte, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	return append([]byte(nil), f.readTxt...), nil
}

func mcWriteReq(id, name, dataType, value string) driver.WriteRequest {
	return driver.WriteRequest{DeviceAddressID: id, Name: name, DataType: dataType, Value: value}
}

func TestWriteMCBitDevice(t *testing.T) {
	f := &fakeMCWrite{}
	res, err := writeMCBatch(f, []driver.WriteRequest{
		mcWriteReq("p1", "M100", "bool", "true"),
	})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	if len(f.writes) != 1 {
		t.Fatalf("writes = %d, want 1", len(f.writes))
	}
	got := f.writes[0]
	if got.head != 100 || !got.bitMode || got.points != 1 {
		t.Errorf("target = %+v", got)
	}
	if len(got.data) != 1 || got.data[0] != 0x10 {
		t.Errorf("data = % X, want 10", got.data)
	}
	if !res[0].OK() || res[0].Value != "1" {
		t.Errorf("result = %+v", res[0])
	}
}

func TestWriteMCWordDevice(t *testing.T) {
	f := &fakeMCWrite{}
	_, err := writeMCBatch(f, []driver.WriteRequest{
		mcWriteReq("p1", "D100", "int16", "1234"),
	})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	got := f.writes[0]
	if got.head != 100 || got.bitMode || got.points != 1 {
		t.Errorf("target = %+v", got)
	}
	if len(got.data) != 2 || got.data[0] != 0xD2 || got.data[1] != 0x04 {
		t.Errorf("data = % X, want D2 04", got.data)
	}
}

func TestWriteMCFloat32(t *testing.T) {
	f := &fakeMCWrite{}
	_, err := writeMCBatch(f, []driver.WriteRequest{
		mcWriteReq("p1", "D200", "float32", "1.5"),
	})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	got := f.writes[0]
	if got.points != 2 {
		t.Errorf("points = %d, want 2", got.points)
	}
	want := []byte{0x00, 0x00, 0xC0, 0x3F}
	if len(got.data) != 4 || got.data[0] != want[0] || got.data[1] != want[1] ||
		got.data[2] != want[2] || got.data[3] != want[3] {
		t.Errorf("data = % X, want % X", got.data, want)
	}
}

func TestWriteMCWordDeviceBoolRMW(t *testing.T) {
	f := &fakeMCWrite{readTxt: []byte{0x00, 0x00}}
	_, err := writeMCBatch(f, []driver.WriteRequest{
		mcWriteReq("p1", "D100.5", "bool", "1"),
	})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	got := f.writes[0]
	if got.head != 100 || got.bitMode || got.points != 1 {
		t.Errorf("target = %+v", got)
	}
	if len(got.data) != 2 || got.data[0] != 0x20 || got.data[1] != 0x00 {
		t.Errorf("data = % X, want 20 00", got.data)
	}
}

func TestWriteMCUint8PadsWord(t *testing.T) {
	f := &fakeMCWrite{}
	_, err := writeMCBatch(f, []driver.WriteRequest{
		mcWriteReq("p1", "D100", "uint8", "7"),
	})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	if got := f.writes[0].data; len(got) != 2 || got[0] != 0x07 || got[1] != 0x00 {
		t.Errorf("data = % X, want 07 00", got)
	}
}

func TestWriteMCBitDeviceWordAccess(t *testing.T) {
	f := &fakeMCWrite{}
	_, err := writeMCBatch(f, []driver.WriteRequest{
		mcWriteReq("p1", "M100", "int16", "1"),
	})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	if got := f.writes[0]; got.head != 96 || got.bitMode {
		t.Errorf("target = %+v, want head 96 word mode", got)
	}
}

func TestWriteMCRejectsString(t *testing.T) {
	f := &fakeMCWrite{}
	res, _ := writeMCBatch(f, []driver.WriteRequest{
		mcWriteReq("p1", "D100", "string", "hello"),
	})
	if res[0].Code != driver.WriteCodeBadValue {
		t.Errorf("code = %d, want BadValue", res[0].Code)
	}
}

func TestWriteMCEndCodeBadAddress(t *testing.T) {
	f := &fakeMCWrite{wrErr: newEndCodeError(endCodeAddrRange)}
	res, err := writeMCBatch(f, []driver.WriteRequest{
		mcWriteReq("p1", "D999999", "int16", "1"),
	})
	if err != nil {
		t.Fatalf("end-code rejection must not be a transport error: %v", err)
	}
	if res[0].Code != driver.WriteCodeBadAddress {
		t.Errorf("code = %d, want BadAddress", res[0].Code)
	}
}

func TestWriteMCTransportError(t *testing.T) {
	f := &fakeMCWrite{wrErr: net.ErrClosed}
	res, err := writeMCBatch(f, []driver.WriteRequest{
		mcWriteReq("p1", "D100", "int16", "1"),
	})
	if err == nil {
		t.Fatal("expected transport error")
	}
	if !errors.Is(err, net.ErrClosed) {
		t.Errorf("error = %v", err)
	}
	if res[0].Code != driver.WriteCodeTransport {
		t.Errorf("code = %d, want Transport", res[0].Code)
	}
}
