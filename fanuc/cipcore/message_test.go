// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package cip

import (
	"bytes"
	"testing"
)

// TestBuildGetAttributeSingle 逐字节钉住路径编码。
//
// 路径长度按**字**计、8 位逻辑段是 2 字节且不补 0 —— 这两点错一个，设备上
// 的表现都是 0x04 路径段错误，看起来像"这个寄存器不存在"。
func TestBuildGetAttributeSingle(t *testing.T) {
	got := BuildGetAttributeSingle(0x6B, 1, 5)
	want := []byte{
		0x0E,       // Get_Attribute_Single
		0x03,       // 路径 3 个字 = 6 字节
		0x20, 0x6B, // class 0x6B (8-bit)
		0x24, 0x01, // instance 1 (8-bit)
		0x30, 0x05, // attribute 5 (8-bit)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("BuildGetAttributeSingle = % X, want % X", got, want)
	}
}

func TestBuildGetAttributeSingle16BitAttribute(t *testing.T) {
	got := BuildGetAttributeSingle(0x6B, 1, 300)
	want := []byte{
		0x0E, 0x04,
		0x20, 0x6B,
		0x24, 0x01,
		0x31, 0x2C, 0x01, 0x00, // attribute 300, 16-bit + pad
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("BuildGetAttributeSingle(attr=300) = % X, want % X", got, want)
	}
}

func TestBuildGetAttributeBlock(t *testing.T) {
	// 手册示例：读 R[6]–R[15]（10 个），Class 0x6B。
	got := BuildGetAttributeBlock(0x6B, 6, 10)
	want := []byte{
		0x01,       // Get_Attribute_All
		0x04,       // 路径 4 个字 = 8 字节
		0x20, 0x6B, // class 0x6B
		0x25, 0x01, 0x0A, 0x00, // instance 0x0A01：count=10 在高字节，实例 1 在低字节
		0x30, 0x06, // attribute 6 = 起始寄存器号
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("BuildGetAttributeBlock = % X, want % X", got, want)
	}
}

func TestBuildGetAttributeBlockClampsCount(t *testing.T) {
	got := BuildGetAttributeBlock(0x6C, 1, 999)
	// [0]=service [1]=path words [2:4]=class [4]=0x25 [5:7]=instance(LE) [7]=pad
	if got[6] != 124 { // instance 高字节 = count
		t.Fatalf("count byte = %d, want clamped 124", got[6])
	}
}

func TestBuildSetAttributeSingle(t *testing.T) {
	got := BuildSetAttributeSingle(0x6C, 1, 2, []byte{0x00, 0x00, 0xA0, 0x3F})
	want := []byte{
		0x10, 0x03,
		0x20, 0x6C,
		0x24, 0x01,
		0x30, 0x02,
		0x00, 0x00, 0xA0, 0x3F, // 1.25f little-endian
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("BuildSetAttributeSingle = % X, want % X", got, want)
	}
}

func TestParseExplicitResponseOK(t *testing.T) {
	data, err := ParseExplicitResponse([]byte{0x8E, 0x00, 0x00, 0x00, 0x2A, 0x00, 0x00, 0x00}, ServiceGetAttributeSingle)
	if err != nil {
		t.Fatalf("ParseExplicitResponse() = %v", err)
	}
	if !bytes.Equal(data, []byte{0x2A, 0x00, 0x00, 0x00}) {
		t.Fatalf("data = % X", data)
	}
}

func TestParseExplicitResponseStatus(t *testing.T) {
	_, err := ParseExplicitResponse([]byte{0x8E, 0x00, 0x14, 0x00}, ServiceGetAttributeSingle)
	if err == nil {
		t.Fatal("expected GeneralStatusError")
	}
	var ge *GeneralStatusError
	if !asGeneral(err, &ge) {
		t.Fatalf("err = %T %v, want *GeneralStatusError", err, err)
	}
	if ge.Status() != GeneralStatusAttributeNotSupported {
		t.Fatalf("status = 0x%02X, want 0x14", ge.Status())
	}
	if !IsAddressStatusError(err) {
		t.Fatal("0x14 应归为地址级错误")
	}
}

func TestParseExplicitResponseSkipsAdditionalStatus(t *testing.T) {
	// 附加状态字数 = 1，后面跟 2 字节附加状态，再跟数据。
	resp := []byte{0x8E, 0x00, 0x00, 0x01, 0x34, 0x12, 0x2A, 0x00, 0x00, 0x00}
	data, err := ParseExplicitResponse(resp, ServiceGetAttributeSingle)
	if err != nil {
		t.Fatalf("ParseExplicitResponse() = %v", err)
	}
	if !bytes.Equal(data, []byte{0x2A, 0x00, 0x00, 0x00}) {
		t.Fatalf("data = % X", data)
	}
}

func TestParseExplicitResponseTruncatedAdditionalStatus(t *testing.T) {
	if _, err := ParseExplicitResponse([]byte{0x8E, 0x00, 0x00, 0x02, 0x00}, ServiceGetAttributeSingle); err == nil {
		t.Fatal("expected truncation error")
	}
}

func TestParseExplicitResponseShort(t *testing.T) {
	if _, err := ParseExplicitResponse([]byte{0x8E}, ServiceGetAttributeSingle); err == nil {
		t.Fatal("expected short response error")
	}
}

// asGeneral 是 errors.As 的测试内封装，避免测试文件直接依赖私有字段。
func asGeneral(err error, target **GeneralStatusError) bool {
	for err != nil {
		if ge, ok := err.(*GeneralStatusError); ok {
			*target = ge
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
