// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package writeval

import (
	"testing"

	"github.com/Ellisata/iot-gateway-sdk/driver"
)

func TestParse(t *testing.T) {
	cases := []struct {
		kind string
		raw  string
		want any
	}{
		{driver.KindBool, "on", true},
		{driver.KindBool, "false", false},
		{driver.KindInt, "0x10", int64(16)},
		{driver.KindUInt, "42", uint64(42)},
		{driver.KindFloat, "1.5", 1.5},
		{driver.KindString, " a b ", " a b "},
		{driver.KindTime, "-5", int64(-5)},
	}
	for _, tc := range cases {
		got, err := Parse(&driver.DataType{Kind: tc.kind}, tc.raw)
		if err != nil {
			t.Errorf("Parse(%s, %q): %v", tc.kind, tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Parse(%s, %q) = %T %v, want %T %v", tc.kind, tc.raw, got, got, tc.want, tc.want)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for _, tc := range []struct {
		kind string
		raw  string
	}{
		{driver.KindInt, ""},
		{driver.KindBool, "maybe"},
		{driver.KindUInt, "-1"},
	} {
		if _, err := Parse(&driver.DataType{Kind: tc.kind}, tc.raw); err == nil {
			t.Errorf("Parse(%s, %q) should fail", tc.kind, tc.raw)
		}
	}
}

func TestFormat(t *testing.T) {
	dt := &driver.DataType{Format: func(v any) string { return "formatted" }}
	if got := Format(dt, 1); got != "formatted" {
		t.Errorf("Format = %q", got)
	}
	if got := Format(&driver.DataType{}, 7); got != "7" {
		t.Errorf("Format fallback = %q", got)
	}
}
