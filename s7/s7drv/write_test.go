// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package s7

import (
	"errors"
	"net"
	"testing"

	"github.com/Ellisata/iot-gateway-sdk/driver"
)

type fakeS7Write struct {
	readData     []byte
	readErr      error
	writeErr     error
	writes       []fakeS7WriteCall
	disconnected bool
}

type fakeS7WriteCall struct {
	area  s7Area
	db    int
	start int
	data  []byte
}

func (f *fakeS7Write) readRange(r S7Range, buf []byte) error {
	if f.readErr != nil {
		return f.readErr
	}
	copy(buf, f.readData)
	return nil
}

func (f *fakeS7Write) writeRange(area s7Area, db, start int, data []byte) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.writes = append(f.writes, fakeS7WriteCall{area: area, db: db, start: start, data: append([]byte(nil), data...)})
	return nil
}

func (f *fakeS7Write) MarkDisconnected() { f.disconnected = true }

func writeReq(id, name, dataType, value string) driver.WriteRequest {
	return driver.WriteRequest{DeviceAddressID: id, Name: name, DataType: dataType, Value: value}
}

func TestWriteS7Word(t *testing.T) {
	f := &fakeS7Write{}
	res, err := writeS7Batch(f, []driver.WriteRequest{
		writeReq("p1", "DB1.DBW0", "int", "1234"),
	})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	if len(f.writes) != 1 {
		t.Fatalf("writes = %d, want 1", len(f.writes))
	}
	got := f.writes[0]
	if got.area != S7AreaDB || got.db != 1 || got.start != 0 {
		t.Errorf("write target = %+v", got)
	}
	if len(got.data) != 2 || got.data[0] != 0x04 || got.data[1] != 0xD2 {
		t.Errorf("data = % X, want 04 D2", got.data)
	}
	if !res[0].OK() || res[0].Value != "1234" {
		t.Errorf("result = %+v", res[0])
	}
}

func TestWriteS7Bit(t *testing.T) {
	f := &fakeS7Write{readData: []byte{0x00}}
	res, err := writeS7Batch(f, []driver.WriteRequest{
		writeReq("p1", "M0.3", "bool", "true"),
	})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	if len(f.writes) != 1 || len(f.writes[0].data) != 1 || f.writes[0].data[0] != 0x08 {
		t.Fatalf("writes = %+v, want one byte 0x08", f.writes)
	}
	if !res[0].OK() || res[0].Value != "1" {
		t.Errorf("result = %+v", res[0])
	}
}

func TestWriteS7BitClear(t *testing.T) {
	f := &fakeS7Write{readData: []byte{0xFF}}
	_, err := writeS7Batch(f, []driver.WriteRequest{
		writeReq("p1", "Q0.3", "bool", "0"),
	})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	if got := f.writes[0].data[0]; got != 0xF7 {
		t.Errorf("data = 0x%02X, want 0xF7", got)
	}
}

func TestWriteS7ByteBool(t *testing.T) {
	f := &fakeS7Write{}
	_, err := writeS7Batch(f, []driver.WriteRequest{
		writeReq("p1", "MB10", "bool", "on"),
	})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	if got := f.writes[0].data; len(got) != 1 || got[0] != 0x01 {
		t.Errorf("data = % X, want 01", got)
	}
}

func TestWriteS7ReadOnlyInput(t *testing.T) {
	f := &fakeS7Write{}
	res, err := writeS7Batch(f, []driver.WriteRequest{
		writeReq("p1", "I0.0", "bool", "1"),
	})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	if res[0].Code != driver.WriteCodeNotWritable {
		t.Errorf("code = %d, want NotWritable", res[0].Code)
	}
	if len(f.writes) != 0 {
		t.Errorf("read-only input should not write, got %+v", f.writes)
	}
}

func TestWriteS7TypeWidthMismatch(t *testing.T) {
	f := &fakeS7Write{}
	res, _ := writeS7Batch(f, []driver.WriteRequest{
		writeReq("p1", "DB1.DBW0", "real", "1.5"),
	})
	if res[0].Code != driver.WriteCodeBadValue {
		t.Errorf("code = %d, want BadValue", res[0].Code)
	}
	if len(f.writes) != 0 {
		t.Errorf("mismatched type should not write")
	}
}

func TestWriteS7UnsupportedString(t *testing.T) {
	f := &fakeS7Write{}
	res, _ := writeS7Batch(f, []driver.WriteRequest{
		writeReq("p1", "DB1.STRING0.10", "string", "hello"),
	})
	if res[0].Code != driver.WriteCodeBadValue {
		t.Errorf("code = %d, want BadValue", res[0].Code)
	}
}

func TestWriteS7BadAddress(t *testing.T) {
	f := &fakeS7Write{}
	res, _ := writeS7Batch(f, []driver.WriteRequest{
		writeReq("p1", "not-an-address", "int", "1"),
	})
	if res[0].Code != driver.WriteCodeBadAddress {
		t.Errorf("code = %d, want BadAddress", res[0].Code)
	}
}

func TestWriteS7TransportError(t *testing.T) {
	f := &fakeS7Write{writeErr: net.ErrClosed}
	res, err := writeS7Batch(f, []driver.WriteRequest{
		writeReq("p1", "DB1.DBW0", "int", "1"),
	})
	if err == nil {
		t.Fatal("expected transport error")
	}
	if !errors.Is(err, net.ErrClosed) {
		t.Errorf("error = %v, want wrapped net.ErrClosed", err)
	}
	if res[0].Code != driver.WriteCodeTransport {
		t.Errorf("code = %d, want Transport", res[0].Code)
	}
	if !f.disconnected {
		t.Error("transport failure should mark the connection disconnected")
	}
}
