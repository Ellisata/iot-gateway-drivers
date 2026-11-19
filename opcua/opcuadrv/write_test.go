// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package opcua

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/gopcua/opcua/ua"

	"github.com/Ellisata/iot-gateway-sdk/driver"
)

// writeReq 构造一个写请求。地址统一用 ns=2;i=100（直接 NodeID，无需浏览）。
func writeReq(id, dataType, value string) driver.WriteRequest {
	return driver.WriteRequest{
		DeviceAddressID: id,
		Name:            "ns=2;i=100",
		DataType:        dataType,
		Value:           value,
	}
}

// okWriter 返回一个总是回 StatusOK 的假客户端，并把收到的 Variant 原值
// 依次收集起来，供断言本驱动编码出的是正确的 OPC UA 类型。
func okWriter(got *[]any) *fakeClient {
	return &fakeClient{
		writeFunc: func(ctx context.Context, req *ua.WriteRequest) (*ua.WriteResponse, error) {
			if len(req.NodesToWrite) != 1 {
				return nil, fmt.Errorf("want 1 node per write, got %d", len(req.NodesToWrite))
			}
			// 掩码没置 Value 位时，服务器会回 OK 但线上根本没有值。
			if !req.NodesToWrite[0].Value.Has(ua.DataValueValue) {
				return nil, fmt.Errorf("DataValue.EncodingMask 缺少 Value 位")
			}
			if got != nil {
				*got = append(*got, req.NodesToWrite[0].Value.Value.Value())
			}
			return &ua.WriteResponse{Results: []ua.StatusCode{ua.StatusOK}}, nil
		},
	}
}

// TestWriteParsesValues 验证每个内部类型名 → 正确的 OPC UA Variant 类型，
// 且 WriteResult.Value 与 Read 的格式化表示一致。
func TestWriteParsesValues(t *testing.T) {
	var got []any
	d := newTestDriver(okWriter(&got))
	d.connect(t)

	reqs := []driver.WriteRequest{
		writeReq("bool", "bool", "on"),
		writeReq("byte", "byte", "255"),
		writeReq("char", "char", "-5"),
		writeReq("int16", "int16", "0x10"),
		writeReq("uint16", "uint16", "65535"),
		writeReq("int32", "int32", "-2147483648"),
		writeReq("uint32", "uint32", "4294967295"),
		writeReq("int64", "int64", "-9223372036854775808"),
		writeReq("uint64", "uint64", "18446744073709551615"),
		writeReq("float32", "float32", "1.5"),
		writeReq("float64", "float64", "-2.25"),
		writeReq("string", "string", " a b "),
		writeReq("bytestring", "byteString", "0x0A0b"),
		writeReq("datetime", "datetime", "2026-11-19 12:34:56"),
	}
	results, err := d.Write(reqs)
	if err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if len(results) != len(reqs) {
		t.Fatalf("results len = %d, want %d", len(results), len(reqs))
	}

	wantVals := []any{
		true,
		uint8(255),
		int8(-5),
		int16(16),
		uint16(65535),
		int32(-2147483648),
		uint32(4294967295),
		int64(-9223372036854775808),
		uint64(18446744073709551615),
		float32(1.5),
		float64(-2.25),
		" a b ",
		[]byte{0x0a, 0x0b},
		time.Date(2026, 11, 19, 12, 34, 56, 0, time.UTC),
	}
	wantStr := []string{
		"1", "255", "-5", "16", "65535", "-2147483648", "4294967295",
		"-9223372036854775808", "18446744073709551615", "1.5", "-2.25",
		" a b ", "0a0b", "2026-11-19 12:34:56",
	}

	if len(got) != len(wantVals) {
		t.Fatalf("write calls = %d, want %d", len(got), len(wantVals))
	}
	for i := range wantVals {
		if !reflect.DeepEqual(got[i], wantVals[i]) {
			t.Errorf("%s: variant value = %T %v, want %T %v",
				reqs[i].DataType, got[i], got[i], wantVals[i], wantVals[i])
		}
		if results[i].DeviceAddressID != reqs[i].DeviceAddressID {
			t.Errorf("result[%d].id = %q, want %q", i, results[i].DeviceAddressID, reqs[i].DeviceAddressID)
		}
		if results[i].Code != driver.WriteCodeOK {
			t.Errorf("%s: Code = %d, want OK (%s)", reqs[i].DataType, results[i].Code, results[i].Message)
		}
		if results[i].Value != wantStr[i] {
			t.Errorf("%s: Value = %q, want %q", reqs[i].DataType, results[i].Value, wantStr[i])
		}
	}
}

// TestWriteRejectsBadInput 地址/类型/值的错误必须在本地拦下，且不进网络。
func TestWriteRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		req  driver.WriteRequest
	}{
		{"invalid address", driver.WriteRequest{
			DeviceAddressID: "a", Name: "ns=x;i=5", DataType: "int16", Value: "1",
		}},
		{"unknown type", writeReq("b", "int99", "1")},
		{"int16 range", writeReq("c", "int16", "40000")},
		{"uint8 range", writeReq("d", "byte", "256")},
		{"bool spelling", writeReq("e", "bool", "maybe")},
		{"datetime format", writeReq("f", "datetime", "2026/11/19")},
		{"bytestring hex", writeReq("g", "byteString", "zz")},
		{"empty numeric", writeReq("h", "int32", "")},
	}
	wantCodes := []int{
		driver.WriteCodeBadAddress,
		driver.WriteCodeBadValue,
		driver.WriteCodeBadValue,
		driver.WriteCodeBadValue,
		driver.WriteCodeBadValue,
		driver.WriteCodeBadValue,
		driver.WriteCodeBadValue,
		driver.WriteCodeBadValue,
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			c := okWriter(nil)
			c.writeFunc = func(ctx context.Context, req *ua.WriteRequest) (*ua.WriteResponse, error) {
				called = true
				return &ua.WriteResponse{Results: []ua.StatusCode{ua.StatusOK}}, nil
			}
			d := newTestDriver(c)
			d.connect(t)

			results, err := d.Write([]driver.WriteRequest{tc.req})
			if err != nil {
				t.Fatalf("Write() error: %v", err)
			}
			if len(results) != 1 {
				t.Fatalf("results len = %d, want 1", len(results))
			}
			if results[0].Code != wantCodes[i] {
				t.Errorf("Code = %d, want %d (%s)", results[0].Code, wantCodes[i], results[0].Message)
			}
			if results[0].Message == "" {
				t.Error("Message 为空：现场只能看到一个没有原因的失败码")
			}
			if called {
				t.Error("本地就能判定的错误不应下发到设备")
			}
		})
	}
}

// TestWriteStatusCodeMapping 逐点状态码 → WriteCodeXxx 的映射边界。
func TestWriteStatusCodeMapping(t *testing.T) {
	cases := []struct {
		status ua.StatusCode
		want   int
	}{
		{ua.StatusOK, driver.WriteCodeOK},
		{ua.StatusBadNotWritable, driver.WriteCodeNotWritable},
		{ua.StatusBadUserAccessDenied, driver.WriteCodeNotWritable},
		{ua.StatusBadWriteNotSupported, driver.WriteCodeNotWritable},
		{ua.StatusBadSecurityModeInsufficient, driver.WriteCodeNotWritable},
		{ua.StatusBadTypeMismatch, driver.WriteCodeBadValue},
		{ua.StatusBadOutOfRange, driver.WriteCodeBadValue},
		{ua.StatusBadNodeIDUnknown, driver.WriteCodeBadAddress},
		{ua.StatusBadAttributeIDInvalid, driver.WriteCodeBadAddress},
		{ua.StatusBadServiceUnsupported, driver.WriteCodeUnsupported},
		{ua.StatusBad, driver.WriteCodeRejected},
		{ua.StatusUncertain, driver.WriteCodeRejected}, // Uncertain 不算写入成功
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("0x%08X", uint32(tc.status)), func(t *testing.T) {
			c := &fakeClient{
				writeFunc: func(ctx context.Context, req *ua.WriteRequest) (*ua.WriteResponse, error) {
					return &ua.WriteResponse{Results: []ua.StatusCode{tc.status}}, nil
				},
			}
			d := newTestDriver(c)
			d.connect(t)

			results, err := d.Write([]driver.WriteRequest{writeReq("a", "int16", "1")})
			if err != nil {
				t.Fatalf("Write() error: %v", err)
			}
			if results[0].Code != tc.want {
				t.Errorf("Code = %d, want %d (%s)", results[0].Code, tc.want, results[0].Message)
			}
			if tc.want != driver.WriteCodeOK && results[0].Message == "" {
				t.Error("失败结果缺少 Message")
			}
		})
	}
}

// TestWriteServiceLevelError 整个 Write 服务被拒（ua.StatusCode 作为 error）：
// 不标记断开、不返回 error，逐点归一成结果码。
func TestWriteServiceLevelError(t *testing.T) {
	c := &fakeClient{
		writeFunc: func(ctx context.Context, req *ua.WriteRequest) (*ua.WriteResponse, error) {
			return nil, ua.StatusBadServiceUnsupported
		},
	}
	d := newTestDriver(c)
	d.connect(t)

	results, err := d.Write([]driver.WriteRequest{writeReq("a", "int16", "1")})
	if err != nil {
		t.Fatalf("服务级拒绝不应让整批变成链路失败: %v", err)
	}
	if results[0].Code != driver.WriteCodeUnsupported {
		t.Errorf("Code = %d, want Unsupported", results[0].Code)
	}
	if !d.IsConnected() {
		t.Error("服务级拒绝不应标记断开")
	}
}

// TestWriteSessionDead 会话类状态码意味着整条链路写不了：标记断开并返回 error。
func TestWriteSessionDead(t *testing.T) {
	c := &fakeClient{
		writeFunc: func(ctx context.Context, req *ua.WriteRequest) (*ua.WriteResponse, error) {
			return nil, ua.StatusBadSessionClosed
		},
	}
	d := newTestDriver(c)
	d.connect(t)

	results, err := d.Write([]driver.WriteRequest{writeReq("a", "int16", "1")})
	if err == nil {
		t.Fatal("会话已关闭时应返回 error")
	}
	if len(results) != 1 || results[0].Code != driver.WriteCodeTransport {
		t.Errorf("results = %+v, want 1 条 Transport", results)
	}
	if d.IsConnected() {
		t.Error("会话已关闭时必须标记断开，否则采集引擎不会重连")
	}
}

// TestWriteTransportError 网络层错误：当前点 Transport、返回 error、标记断开。
func TestWriteTransportError(t *testing.T) {
	c := &fakeClient{
		writeFunc: func(ctx context.Context, req *ua.WriteRequest) (*ua.WriteResponse, error) {
			return nil, io.EOF
		},
	}
	d := newTestDriver(c)
	d.connect(t)

	results, err := d.Write([]driver.WriteRequest{writeReq("a", "int16", "1")})
	if err == nil {
		t.Fatal("链路错误应返回 error")
	}
	if len(results) != 1 || results[0].Code != driver.WriteCodeTransport {
		t.Errorf("results = %+v, want 1 条 Transport", results)
	}
	if d.IsConnected() {
		t.Error("链路错误后 IsConnected 应为 false")
	}
}

// TestWritePartialOnTransport 已成功的点保留结果，失败点之后中止。
func TestWritePartialOnTransport(t *testing.T) {
	calls := 0
	c := &fakeClient{
		writeFunc: func(ctx context.Context, req *ua.WriteRequest) (*ua.WriteResponse, error) {
			calls++
			if calls == 1 {
				return &ua.WriteResponse{Results: []ua.StatusCode{ua.StatusOK}}, nil
			}
			return nil, io.EOF
		},
	}
	d := newTestDriver(c)
	d.connect(t)

	results, err := d.Write([]driver.WriteRequest{
		writeReq("a", "int16", "1"),
		writeReq("b", "int16", "2"),
		writeReq("c", "int16", "3"),
	})
	if err == nil {
		t.Fatal("第二个点链路失败后应返回 error")
	}
	if calls != 2 {
		t.Errorf("write calls = %d, want 2（第三个点应中止）", calls)
	}
	if results[0].Code != driver.WriteCodeOK || results[1].Code != driver.WriteCodeTransport {
		t.Errorf("results = %+v, want [OK, Transport]", results)
	}
}

// TestWriteBrowsePathCached 浏览路径与 Read 共用同一份解析缓存。
func TestWriteBrowsePathCached(t *testing.T) {
	target := ua.NewNumericNodeID(2, 500)
	sendCalls := 0
	c := &fakeClient{
		sendFunc: func(ctx context.Context, req ua.Request) (ua.Response, error) {
			sendCalls++
			return &ua.TranslateBrowsePathsToNodeIDsResponse{Results: []*ua.BrowsePathResult{
				{StatusCode: ua.StatusOK, Targets: []*ua.BrowsePathTarget{
					{TargetID: ua.NewExpandedNodeID(target, "", 0)},
				}},
			}}, nil
		},
		writeFunc: func(ctx context.Context, req *ua.WriteRequest) (*ua.WriteResponse, error) {
			if got := req.NodesToWrite[0].NodeID.String(); got != target.String() {
				return nil, fmt.Errorf("node = %s, want %s", got, target)
			}
			return &ua.WriteResponse{Results: []ua.StatusCode{ua.StatusOK}}, nil
		},
	}
	d := newTestDriver(c)
	d.connect(t)

	req := driver.WriteRequest{
		DeviceAddressID: "path", Name: "Root/Objects/Device/SetTemp",
		DataType: "float32", Value: "30.5",
	}
	for i := 0; i < 2; i++ {
		results, err := d.Write([]driver.WriteRequest{req})
		if err != nil {
			t.Fatalf("Write #%d error: %v", i+1, err)
		}
		if results[0].Code != driver.WriteCodeOK {
			t.Fatalf("Write #%d Code = %d (%s)", i+1, results[0].Code, results[0].Message)
		}
	}
	if sendCalls != 1 {
		t.Errorf("browse path resolve calls = %d, want 1（第二次应命中缓存）", sendCalls)
	}
}

// TestWriteNotConnected 未连接时 Write 报错。
func TestWriteNotConnected(t *testing.T) {
	d := newOpcUaDriver()
	if _, err := d.Write([]driver.WriteRequest{writeReq("a", "int16", "1")}); err == nil {
		t.Fatal("未连接时 Write 应报错")
	}
}

// TestWriteUnresolvablePath 浏览路径解析不出（BadNoMatch）：地址级，不连坐、不断开。
func TestWriteUnresolvablePath(t *testing.T) {
	c := &fakeClient{
		sendFunc: func(ctx context.Context, req ua.Request) (ua.Response, error) {
			return nil, ua.StatusBadNoMatch
		},
	}
	d := newTestDriver(c)
	d.connect(t)

	results, err := d.Write([]driver.WriteRequest{{
		DeviceAddressID: "path", Name: "Root/Objects/Nope",
		DataType: "int32", Value: "1",
	}})
	if err != nil {
		t.Fatalf("服务级路径解析失败不应返回 error: %v", err)
	}
	if results[0].Code != driver.WriteCodeBadAddress {
		t.Errorf("Code = %d, want BadAddress", results[0].Code)
	}
	if !d.IsConnected() {
		t.Error("BadNoMatch 不应标记断开")
	}
}
