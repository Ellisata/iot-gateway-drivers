// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package cip

import (
	"bytes"
	"errors"
	"net"
	"testing"

	cipcore "github.com/Ellisata/iot-gateway-drivers/omroncip/cipcore"
	"github.com/Ellisata/iot-gateway-sdk/driver"
)

type fakeCIPWrite struct {
	writes [][]byte
	codes  []uint16
	tags   []string
	err    error
}

func (f *fakeCIPWrite) WriteTag(tag string, code uint16, data []byte) error {
	if f.err != nil {
		return f.err
	}
	f.tags = append(f.tags, tag)
	f.codes = append(f.codes, code)
	f.writes = append(f.writes, append([]byte(nil), data...))
	return nil
}

func cipWriteReq(id, name, dataType, value string) driver.WriteRequest {
	return driver.WriteRequest{DeviceAddressID: id, Name: name, DataType: dataType, Value: value}
}

func TestWriteCIPInt16(t *testing.T) {
	f := &fakeCIPWrite{}
	res, err := writeCIPBatch(f, []driver.WriteRequest{
		cipWriteReq("p1", "MotorSpeed", "int16", "1234"),
	})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	if len(f.writes) != 1 || !bytes.Equal(f.writes[0], []byte{0xD2, 0x04}) {
		t.Errorf("data = % X, want D2 04", f.writes)
	}
	if f.codes[0] != cipTypeINT {
		t.Errorf("code = 0x%04X, want INT 0x%04X", f.codes[0], cipTypeINT)
	}
	if !res[0].OK() || res[0].Value != "1234" {
		t.Errorf("result = %+v", res[0])
	}
}

func TestWriteCIPBool(t *testing.T) {
	f := &fakeCIPWrite{}
	_, err := writeCIPBatch(f, []driver.WriteRequest{
		cipWriteReq("p1", "Run", "bool", "1"),
	})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	if f.codes[0] != cipTypeBool {
		t.Errorf("code = 0x%04X, want BOOL 0x%04X", f.codes[0], cipTypeBool)
	}
	if !bytes.Equal(f.writes[0], []byte{0x01}) {
		t.Errorf("data = % X, want 01", f.writes[0])
	}
}

func TestWriteCIPTypeCodes(t *testing.T) {
	f := &fakeCIPWrite{}
	_, err := writeCIPBatch(f, []driver.WriteRequest{
		cipWriteReq("p1", "U", "uint16", "1"),
		cipWriteReq("p2", "W", "word", "1"),
		cipWriteReq("p3", "R", "float32", "1.5"),
	})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	want := []uint16{cipTypeUINT, cipTypeWORD, cipTypeREAL}
	for i := range want {
		if f.codes[i] != want[i] {
			t.Errorf("code[%d] = 0x%04X, want 0x%04X", i, f.codes[i], want[i])
		}
	}
}

func TestWriteCIPRejectsString(t *testing.T) {
	f := &fakeCIPWrite{}
	res, _ := writeCIPBatch(f, []driver.WriteRequest{
		cipWriteReq("p1", "Name", "string", "hello"),
	})
	if res[0].Code != driver.WriteCodeBadValue {
		t.Errorf("code = %d, want BadValue", res[0].Code)
	}
}

func TestWriteCIPGeneralStatus(t *testing.T) {
	cases := []struct {
		status byte
		want   int
	}{
		{cipcore.GeneralStatusPathSegmentError, driver.WriteCodeBadAddress},
		{0x02, driver.WriteCodeRejected},
	}
	for _, tc := range cases {
		f := &fakeCIPWrite{err: cipcore.NewGeneralStatusError(tc.status)}
		res, err := writeCIPBatch(f, []driver.WriteRequest{
			cipWriteReq("p1", "Missing", "int16", "1"),
		})
		if err != nil {
			t.Fatalf("status 0x%02X must not be a transport error: %v", tc.status, err)
		}
		if res[0].Code != tc.want {
			t.Errorf("status 0x%02X: code = %d, want %d", tc.status, res[0].Code, tc.want)
		}
	}
}

func TestWriteCIPTransportError(t *testing.T) {
	f := &fakeCIPWrite{err: net.ErrClosed}
	res, err := writeCIPBatch(f, []driver.WriteRequest{
		cipWriteReq("p1", "MotorSpeed", "int16", "1"),
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

func TestBuildDataTableWrite(t *testing.T) {
	req := buildDataTableWrite("Motor", cipTypeINT, []byte{0xD2, 0x04})
	want := []byte{
		0x4D, 0x04, // service + path words
		0x91, 0x05, 'M', 'o', 't', 'o', 'r', 0x00, // ANSI symbol + odd padding
		0xC3, 0x00, // data type + AddInfoLength
		0x01, 0x00, // elements
		0xD2, 0x04,
	}
	if !bytes.Equal(req, want) {
		t.Errorf("req = % X\nwant  % X", req, want)
	}
}

func TestParseDataTableWrite(t *testing.T) {
	if err := parseDataTableWrite([]byte{0xCD, 0x00, 0x00, 0x00}); err != nil {
		t.Fatalf("success response: %v", err)
	}
	err := parseDataTableWrite([]byte{0xCD, 0x00, 0x04, 0x00})
	if err == nil || !cipcore.IsGeneralStatusError(err) {
		t.Fatalf("expected general status error, got %v", err)
	}
	if err := parseDataTableWrite([]byte{0xCC, 0x00, 0x00, 0x00}); err == nil {
		t.Error("expected service echo mismatch")
	}
}

// TestOmronTypeCodesMatchCIPCommon 钉住 W506 的类型码表，避免再次出现
// 整组错位（读路径按这些码折算批量响应长度，写路径必须原样发给设备）。
func TestOmronTypeCodesMatchCIPCommon(t *testing.T) {
	want := map[uint16]string{
		cipTypeBool:   "BOOL",
		cipTypeSINT:   "SINT",
		cipTypeINT:    "INT",
		cipTypeDINT:   "DINT",
		cipTypeUSINT:  "USINT",
		cipTypeUINT:   "UINT",
		cipTypeUDINT:  "UDINT",
		cipTypeREAL:   "REAL",
		cipTypeLREAL:  "LREAL",
		cipTypeSTRING: "STRING",
		cipTypeWORD:   "WORD",
		cipTypeDWORD:  "DWORD",
	}
	wantCodes := map[string]uint16{
		"BOOL": 0x00C1, "SINT": 0x00C2, "INT": 0x00C3, "DINT": 0x00C4,
		"USINT": 0x00C6, "UINT": 0x00C7, "UDINT": 0x00C8, "REAL": 0x00CA,
		"LREAL": 0x00CB, "STRING": 0x00D0, "WORD": 0x00D2, "DWORD": 0x00D3,
	}
	for code, name := range want {
		if code != wantCodes[name] {
			t.Errorf("%s code = 0x%04X, want 0x%04X", name, code, wantCodes[name])
		}
	}
}
