// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package fins

import (
	"bytes"
	"errors"
	"net"
	"testing"

	"github.com/Ellisata/iot-gateway-sdk/driver"
)

type fakeFINSWrite struct {
	writes []fakeFINSWriteCall
	err    error
}

type fakeFINSWriteCall struct {
	area finsArea
	word uint16
	bit  int
	data []byte
}

func (f *fakeFINSWrite) Write(area finsArea, word uint16, bit int, data []byte) error {
	if f.err != nil {
		return f.err
	}
	f.writes = append(f.writes, fakeFINSWriteCall{
		area: area, word: word, bit: bit, data: append([]byte(nil), data...),
	})
	return nil
}

func finsWriteReq(id, name, dataType, value string) driver.WriteRequest {
	return driver.WriteRequest{DeviceAddressID: id, Name: name, DataType: dataType, Value: value}
}

func TestWriteFINSWord(t *testing.T) {
	f := &fakeFINSWrite{}
	res, err := writeFINSBatch(f, []driver.WriteRequest{
		finsWriteReq("p1", "D100", "int16", "1234"),
	}, &FINSConfig{})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	got := f.writes[0]
	if got.area != AreaDM || got.word != 100 || got.bit != -1 {
		t.Errorf("target = %+v", got)
	}
	if !bytes.Equal(got.data, []byte{0x04, 0xD2}) {
		t.Errorf("data = % X, want 04 D2", got.data)
	}
	if !res[0].OK() || res[0].Value != "1234" {
		t.Errorf("result = %+v", res[0])
	}
}

func TestWriteFINSBit(t *testing.T) {
	f := &fakeFINSWrite{}
	_, err := writeFINSBatch(f, []driver.WriteRequest{
		finsWriteReq("p1", "D100.5", "bool", "1"),
	}, &FINSConfig{})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	got := f.writes[0]
	if got.word != 100 || got.bit != 5 {
		t.Errorf("target = %+v", got)
	}
	if !bytes.Equal(got.data, []byte{0x01}) {
		t.Errorf("data = % X, want 01", got.data)
	}
}

func TestWriteFINSBoolWithoutBitWritesWord(t *testing.T) {
	f := &fakeFINSWrite{}
	_, err := writeFINSBatch(f, []driver.WriteRequest{
		finsWriteReq("p1", "D100", "bool", "true"),
	}, &FINSConfig{})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	got := f.writes[0]
	if got.bit != -1 || !bytes.Equal(got.data, []byte{0x00, 0x01}) {
		t.Errorf("write = %+v, want word write 00 01", got)
	}
}

func TestWriteFINSWordOrder(t *testing.T) {
	f := &fakeFINSWrite{}
	_, err := writeFINSBatch(f, []driver.WriteRequest{
		finsWriteReq("p1", "D200", "float32", "1.5"),
	}, &FINSConfig{WordOrder: WordOrderLittleEndian})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	// 1.5 = 0x3FC00000，大端字序 [3F C0][00 00]；低字在前时为 [00 00][3F C0]。
	if got := f.writes[0].data; !bytes.Equal(got, []byte{0x00, 0x00, 0x3F, 0xC0}) {
		t.Errorf("data = % X, want 00 00 3F C0", got)
	}
}

func TestWriteFINSUint8PadsWord(t *testing.T) {
	f := &fakeFINSWrite{}
	_, err := writeFINSBatch(f, []driver.WriteRequest{
		finsWriteReq("p1", "D100", "uint8", "7"),
	}, &FINSConfig{})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	if got := f.writes[0].data; !bytes.Equal(got, []byte{0x00, 0x07}) {
		t.Errorf("data = % X, want 00 07", got)
	}
}

func TestWriteFINSRejectsString(t *testing.T) {
	f := &fakeFINSWrite{}
	res, _ := writeFINSBatch(f, []driver.WriteRequest{
		finsWriteReq("p1", "D100", "string", "hello"),
	}, &FINSConfig{})
	if res[0].Code != driver.WriteCodeBadValue {
		t.Errorf("code = %d, want BadValue", res[0].Code)
	}
}

func TestWriteFINSEndCode(t *testing.T) {
	cases := []struct {
		code uint16
		want int
	}{
		{0x1102, driver.WriteCodeBadAddress},
		{0x1104, driver.WriteCodeBadValue},
		{0x0401, driver.WriteCodeRejected},
	}
	for _, tc := range cases {
		f := &fakeFINSWrite{err: newEndCodeError(tc.code)}
		res, err := writeFINSBatch(f, []driver.WriteRequest{
			finsWriteReq("p1", "D100", "int16", "1"),
		}, &FINSConfig{})
		if err != nil {
			t.Fatalf("end code 0x%04X must not be a transport error: %v", tc.code, err)
		}
		if res[0].Code != tc.want {
			t.Errorf("end code 0x%04X: code = %d, want %d", tc.code, res[0].Code, tc.want)
		}
	}
}

func TestWriteFINSTransportError(t *testing.T) {
	f := &fakeFINSWrite{err: net.ErrClosed}
	res, err := writeFINSBatch(f, []driver.WriteRequest{
		finsWriteReq("p1", "D100", "int16", "1"),
	}, &FINSConfig{})
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

func TestBuildWriteFrame(t *testing.T) {
	frame := buildWriteFrame(AreaDM, 100, 5, []byte{0x01}, 1, 2, 3, 4, 7)
	// FINS 头 10 + 命令码 2 + 区域 1 + 字 2 + 位 1 + 数量 2 + 数据 1
	if len(frame) != 19 {
		t.Fatalf("frame length = %d, want 19", len(frame))
	}
	want := []byte{
		0x80, 0x00, 0x02, 0x00, 0x01, 0x02, 0x00, 0x03, 0x04, 0x07,
		0x01, 0x02,
		0x82,
		0x00, 0x64,
		0x05,
		0x00, 0x01,
		0x01,
	}
	if !bytes.Equal(frame, want) {
		t.Errorf("frame = % X\nwant    % X", frame, want)
	}
}
