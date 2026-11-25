// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package layout

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/Ellisata/iot-gateway-sdk/driver"
)

// TestPermutationsSelfInverse 所有布局都是交换类排列（自反）：编码与解码
// 因此可以安全地共用同一张表。新增布局时这条会先失败。
func TestPermutationsSelfInverse(t *testing.T) {
	for size, table := range permutations {
		for name, perm := range table {
			if len(perm) != size {
				t.Errorf("%s(%d): 排列长度 %d", name, size, len(perm))
				continue
			}
			for i, p := range perm {
				if p < 0 || p >= size || perm[p] != i {
					t.Errorf("%s(%d): perm=%v 不是自反排列（perm[%d]=%d）", name, size, perm, i, p)
					break
				}
			}
		}
	}
}

// TestDCBAHGFE64 DCBAHGFE = 每个 16 位字内交换字节 + 交换相邻字对。
// 规范大端 01 23 45 67 89 AB CD EF 在线上应为 67 45 23 01 EF CD AB 89。
func TestDCBAHGFE64(t *testing.T) {
	perm, ok := Permutation("dcbahgfe", 8) // 大小写不敏感
	if !ok {
		t.Fatal("DCBAHGFE 未登记")
	}
	wantPerm := []int{3, 2, 1, 0, 7, 6, 5, 4}
	if !bytes.Equal(intsToBytes(perm), intsToBytes(wantPerm)) {
		t.Fatalf("perm = %v, want %v", perm, wantPerm)
	}

	canonical := []byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xAB, 0xCD, 0xEF}
	wire := make([]byte, 8)
	for i, p := range perm {
		wire[p] = canonical[i]
	}
	wantWire := []byte{0x67, 0x45, 0x23, 0x01, 0xEF, 0xCD, 0xAB, 0x89}
	if !bytes.Equal(wire, wantWire) {
		t.Fatalf("wire = % X, want % X", wire, wantWire)
	}

	// 解码方向：canonical[i] = wire[perm[i]]
	back := make([]byte, 8)
	for i, p := range perm {
		back[i] = wire[p]
	}
	if !bytes.Equal(back, canonical) {
		t.Fatalf("decode = % X, want % X", back, canonical)
	}
}

// TestEncodeWithLayoutDCBAHGFE 写路径与大端规范字节互逆。
func TestEncodeWithLayoutDCBAHGFE(t *testing.T) {
	dt := &driver.DataType{
		Name: "uint64",
		Size: 8,
		Encode: func(val any, bo binary.ByteOrder) ([]byte, error) {
			buf := make([]byte, 8)
			bo.PutUint64(buf, val.(uint64))
			return buf, nil
		},
	}
	got, err := EncodeWithLayout(uint64(0x0123456789ABCDEF), dt, DCBAHGFE)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	want := []byte{0x67, 0x45, 0x23, 0x01, 0xEF, 0xCD, 0xAB, 0x89}
	if !bytes.Equal(got, want) {
		t.Fatalf("encode = % X, want % X", got, want)
	}
}

func intsToBytes(v []int) []byte {
	out := make([]byte, len(v))
	for i, n := range v {
		out[i] = byte(n)
	}
	return out
}
