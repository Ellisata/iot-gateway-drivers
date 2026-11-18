// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package cip

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestBuildRegisterSession(t *testing.T) {
	frame := BuildRegisterSession(0, EncapProtocolVersion)
	if len(frame) != EncapHeaderSize+4 {
		t.Fatalf("len = %d, want %d", len(frame), EncapHeaderSize+4)
	}
	if cmd := binary.LittleEndian.Uint16(frame[0:2]); cmd != CmdRegisterSession {
		t.Fatalf("cmd = 0x%04X", cmd)
	}
	if l := binary.LittleEndian.Uint16(frame[2:4]); l != 4 {
		t.Fatalf("length = %d", l)
	}
	if !bytes.Equal(frame[24:], []byte{0x01, 0x00, 0x00, 0x00}) {
		t.Fatalf("payload = % X", frame[24:])
	}
}

func TestBuildSendRRDataUCMMAddressItemIsNull(t *testing.T) {
	cipData := []byte{0x0E, 0x03, 0x20, 0x6B, 0x24, 0x01, 0x30, 0x05}
	frame := BuildSendRRData(0x12345678, cipData)

	if cmd := binary.LittleEndian.Uint16(frame[0:2]); cmd != CmdSendRRData {
		t.Fatalf("cmd = 0x%04X", cmd)
	}
	if sess := binary.LittleEndian.Uint32(frame[4:8]); sess != 0x12345678 {
		t.Fatalf("session = 0x%08X", sess)
	}
	payload := frame[EncapHeaderSize:]
	// 项数 = 2；第一个地址项必须是 Null（0x0000, len 0），不能用 0x00A1。
	if count := binary.LittleEndian.Uint16(payload[6:8]); count != 2 {
		t.Fatalf("item count = %d", count)
	}
	if itemType := binary.LittleEndian.Uint16(payload[8:10]); itemType != 0x0000 {
		t.Fatalf("address item type = 0x%04X, want Null 0x0000", itemType)
	}
	if itemLen := binary.LittleEndian.Uint16(payload[10:12]); itemLen != 0 {
		t.Fatalf("address item length = %d", itemLen)
	}
	if itemType := binary.LittleEndian.Uint16(payload[12:14]); itemType != ItemCIPData {
		t.Fatalf("data item type = 0x%04X", itemType)
	}
	if itemLen := binary.LittleEndian.Uint16(payload[14:16]); int(itemLen) != len(cipData) {
		t.Fatalf("data item length = %d, want %d", itemLen, len(cipData))
	}
	if !bytes.Equal(payload[16:], cipData) {
		t.Fatalf("cip data = % X", payload[16:])
	}
}

func TestParseSendRRData(t *testing.T) {
	payload := []byte{0, 0, 0, 0, 0, 0, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0xB2, 0x00, 0x02, 0x00, 0xAA, 0xBB}
	data, err := ParseSendRRData(payload)
	if err != nil {
		t.Fatalf("ParseSendRRData() = %v", err)
	}
	if !bytes.Equal(data, []byte{0xAA, 0xBB}) {
		t.Fatalf("data = % X", data)
	}
}

func TestParseEncapResponseRejectsBadLength(t *testing.T) {
	frame := BuildEncapHeader(CmdSendRRData, 0, 10) // 声明 10 字节但后面没有载荷
	if _, _, _, _, err := ParseEncapResponse(frame); err == nil {
		t.Fatal("expected length error")
	}
}
