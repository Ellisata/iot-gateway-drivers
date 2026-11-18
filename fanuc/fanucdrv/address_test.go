// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package fanuc

import "testing"

func TestParseRegisterAddress(t *testing.T) {
	tests := []struct {
		name     string
		addr     string
		dataType string
		want     registerAddress
		wantErr  bool
	}{
		{
			name: "int32 register",
			addr: "R[5]", dataType: "int32",
			want: registerAddress{Name: "R[5]", Index: 5, Class: objectIntegerRegister, DataType: "int32", Kind: "int"},
		},
		{
			name: "uint32 uses integer object",
			addr: "R[7]", dataType: "uint32",
			want: registerAddress{Name: "R[7]", Index: 7, Class: objectIntegerRegister, DataType: "uint32", Kind: "uint"},
		},
		{
			name: "float32 uses real object",
			addr: "R[2]", dataType: "float32",
			want: registerAddress{Name: "R[2]", Index: 2, Class: objectRealRegister, DataType: "float32", Kind: "float"},
		},
		{
			name: "case and spaces",
			addr: " r[ 12 ] ", dataType: "Float32",
			want: registerAddress{Name: "r[ 12 ]", Index: 12, Class: objectRealRegister, DataType: "float32", Kind: "float"},
		},
		{name: "register 0 is class attribute", addr: "R[0]", dataType: "int32", wantErr: true},
		{name: "out of 16-bit range", addr: "R[65536]", dataType: "int32", wantErr: true},
		{name: "float register number", addr: "R[1.5]", dataType: "int32", wantErr: true},
		{name: "wrong prefix", addr: "X[1]", dataType: "int32", wantErr: true},
		{name: "position register not supported yet", addr: "PR[1]", dataType: "float32", wantErr: true},
		{name: "component suffix not supported", addr: "R[1].X", dataType: "float32", wantErr: true},
		{name: "unsupported type", addr: "R[1]", dataType: "bool", wantErr: true},
		{name: "empty", addr: "", dataType: "int32", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRegisterAddress(tt.addr, tt.dataType)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseRegisterAddress(%q, %q) = %+v, want error", tt.addr, tt.dataType, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRegisterAddress(%q, %q) = %v", tt.addr, tt.dataType, err)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
