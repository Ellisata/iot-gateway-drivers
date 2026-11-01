// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package cip

import (
	"errors"
	"net"
	"testing"

	gi "github.com/iceisfun/goindustrial/protocol/ethernetip/cip"

	"github.com/Ellisata/iot-gateway-sdk/driver"
)

type fakeRockwellWrite struct {
	tags   []string
	values []any
	err    error
}

func (f *fakeRockwellWrite) WriteTag(tag string, value any) error {
	if f.err != nil {
		return f.err
	}
	f.tags = append(f.tags, tag)
	f.values = append(f.values, value)
	return nil
}

func rockwellWriteReq(id, name, dataType, value string) driver.WriteRequest {
	return driver.WriteRequest{DeviceAddressID: id, Name: name, DataType: dataType, Value: value}
}

func TestWriteRockwellTypes(t *testing.T) {
	f := &fakeRockwellWrite{}
	res, err := writeRockwellBatch(f, []driver.WriteRequest{
		rockwellWriteReq("p1", "Speed", "int32", "123"),
		rockwellWriteReq("p2", "Ratio", "float32", "1.5"),
		rockwellWriteReq("p3", "Run", "bool", "1"),
		rockwellWriteReq("p4", "Delta", "int8", "-5"),
		rockwellWriteReq("p5", "Counter", "int64", "9000000000"),
		rockwellWriteReq("p6", "Gain", "float64", "2.5"),
	})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	want := []any{int32(123), float32(1.5), true, int8(-5), int64(9000000000), float64(2.5)}
	for i := range want {
		if f.values[i] != want[i] {
			t.Errorf("value[%d] = %T %v, want %T %v", i, f.values[i], f.values[i], want[i], want[i])
		}
		if !res[i].OK() {
			t.Errorf("result[%d] = %+v", i, res[i])
		}
	}
	if f.tags[0] != "Speed" || f.tags[5] != "Gain" {
		t.Errorf("tags = %v", f.tags)
	}
}

func TestWriteRockwellArrayElementPath(t *testing.T) {
	f := &fakeRockwellWrite{}
	_, err := writeRockwellBatch(f, []driver.WriteRequest{
		rockwellWriteReq("p1", "Array[3]", "int16", "7"),
	})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	if f.tags[0] != "Array[3]" {
		t.Errorf("tag = %q, want Array[3]", f.tags[0])
	}
	if f.values[0] != int16(7) {
		t.Errorf("value = %T %v, want int16 7", f.values[0], f.values[0])
	}
}

func TestWriteRockwellRejectsString(t *testing.T) {
	f := &fakeRockwellWrite{}
	res, _ := writeRockwellBatch(f, []driver.WriteRequest{
		rockwellWriteReq("p1", "Name", "string", "hello"),
	})
	if res[0].Code != driver.WriteCodeBadValue {
		t.Errorf("code = %d, want BadValue", res[0].Code)
	}
}

func TestWriteRockwellCIPStatus(t *testing.T) {
	cases := []struct {
		status gi.USINT
		want   int
	}{
		{gi.StatusPathSegmentError, driver.WriteCodeBadAddress},
		{gi.StatusResourceUnavailable, driver.WriteCodeRejected},
	}
	for _, tc := range cases {
		f := &fakeRockwellWrite{err: gi.Error{Status: tc.status}}
		res, err := writeRockwellBatch(f, []driver.WriteRequest{
			rockwellWriteReq("p1", "Missing", "int32", "1"),
		})
		if err != nil {
			t.Fatalf("status 0x%02X must not be a transport error: %v", tc.status, err)
		}
		if res[0].Code != tc.want {
			t.Errorf("status 0x%02X: code = %d, want %d", tc.status, res[0].Code, tc.want)
		}
	}
}

func TestWriteRockwellTransportError(t *testing.T) {
	f := &fakeRockwellWrite{err: net.ErrClosed}
	res, err := writeRockwellBatch(f, []driver.WriteRequest{
		rockwellWriteReq("p1", "Speed", "int32", "1"),
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
