// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package opcua

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"

	"github.com/Ellisata/iot-gateway-sdk/driver"
)

// freePort 获取一个空闲的本地端口（探测后立即释放，存在极小竞态窗口，测试可接受）。
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// TestOpcUaDriverIntegration 使用 gopcua 进程内服务器验证真实连接：Ping、Connect、
// Read 直接 NodeID（值/质量/Kind）、Bad 节点（不存在）质量 0。
//
// 注：进程内服务器的 TranslateBrowsePathsToNodeIds 返回 serviceUnsupported，
// 浏览路径解析不在此集成测试覆盖（由 TestReadBrowsePathCached 单测覆盖）。
func TestOpcUaDriverIntegration(t *testing.T) {
	port := freePort(t)
	srv := server.New(server.EndPoint("127.0.0.1", port))
	defer srv.Close()

	ns := server.NewNodeNameSpace(srv, "urn:test:opcua")
	nsID := ns.ID()
	ns.AddNode(server.NewVariableNode(ua.NewNumericNodeID(nsID, 1001), "Temperature", float32(21.5)))
	ns.AddNode(server.NewVariableNode(ua.NewNumericNodeID(nsID, 1002), "Counter", int32(42)))
	ns.AddNode(server.NewVariableNode(ua.NewNumericNodeID(nsID, 1003), "Switch", true))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("start test server: %v", err)
	}

	cfgJSON := fmt.Sprintf(`{"host":"127.0.0.1","port":%d}`, port)
	d := newOpcUaDriver()

	// Ping：临时连接握手后关闭
	if err := d.Ping(cfgJSON); err != nil {
		t.Fatalf("Ping() error: %v", err)
	}

	// Connect + Read
	if err := d.Connect(cfgJSON); err != nil {
		t.Fatalf("Connect() error: %v", err)
	}
	defer d.Close()
	if !d.IsConnected() {
		t.Fatal("IsConnected() = false after Connect")
	}

	addrs := []driver.DeviceAddress{
		{ID: "temp", Name: fmt.Sprintf("ns=%d;i=1001", nsID), DataType: "float32"},
		{ID: "cnt", Name: fmt.Sprintf("ns=%d;i=1002", nsID), DataType: "int32"},
		{ID: "sw", Name: fmt.Sprintf("ns=%d;i=1003", nsID), DataType: "bool"},
		{ID: "missing", Name: fmt.Sprintf("ns=%d;i=9999", nsID), DataType: "float32"},
	}
	results, err := d.Read(addrs)
	if err != nil {
		t.Fatalf("Read() error: %v", err)
	}

	want := []struct {
		val  string
		q    int
		kind string
	}{
		{"21.5", driver.QualityGood, "float"},
		{"42", driver.QualityGood, "int"},
		{"1", driver.QualityGood, "bool"},
		{"", driver.QualityBadAddress, "float"}, // 不存在的节点：BadNodeIdUnknown → 地址级，值置空
	}
	for i, w := range want {
		if results[i].DeviceAddressID != addrs[i].ID {
			t.Errorf("result[%d].id = %q, want %q", i, results[i].DeviceAddressID, addrs[i].ID)
		}
		if results[i].Value != w.val {
			t.Errorf("result[%d].Value = %q, want %q", i, results[i].Value, w.val)
		}
		if results[i].Quality != w.q {
			t.Errorf("result[%d].Quality = %d, want %d", i, results[i].Quality, w.q)
		}
		if results[i].Kind != w.kind {
			t.Errorf("result[%d].Kind = %q, want %q", i, results[i].Kind, w.kind)
		}
	}
}

// TestOpcUaDriverWriteIntegration 用 gopcua 进程内服务器验证真实写路径：
// 多个类型经 Write 服务下发后，Read 能读回新值；不存在的节点返回地址级错误。
func TestOpcUaDriverWriteIntegration(t *testing.T) {
	port := freePort(t)
	srv := server.New(server.EndPoint("127.0.0.1", port))
	defer srv.Close()

	ns := server.NewNodeNameSpace(srv, "urn:test:opcua:write")
	nsID := ns.ID()
	ns.AddNode(server.NewVariableNode(ua.NewNumericNodeID(nsID, 2001), "SetTemp", float32(20)))
	ns.AddNode(server.NewVariableNode(ua.NewNumericNodeID(nsID, 2002), "Enable", false))
	ns.AddNode(server.NewVariableNode(ua.NewNumericNodeID(nsID, 2003), "Count", int32(1)))
	ns.AddNode(server.NewVariableNode(ua.NewNumericNodeID(nsID, 2004), "Name", "old"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("start test server: %v", err)
	}

	d := newOpcUaDriver()
	if err := d.Connect(fmt.Sprintf(`{"host":"127.0.0.1","port":%d}`, port)); err != nil {
		t.Fatalf("Connect() error: %v", err)
	}
	defer d.Close()

	reqs := []driver.WriteRequest{
		{DeviceAddressID: "temp", Name: fmt.Sprintf("ns=%d;i=2001", nsID), DataType: "float32", Value: "30.5"},
		{DeviceAddressID: "en", Name: fmt.Sprintf("ns=%d;i=2002", nsID), DataType: "bool", Value: "1"},
		{DeviceAddressID: "cnt", Name: fmt.Sprintf("ns=%d;i=2003", nsID), DataType: "int32", Value: "99"},
		{DeviceAddressID: "name", Name: fmt.Sprintf("ns=%d;i=2004", nsID), DataType: "string", Value: "new"},
	}
	results, err := d.Write(reqs)
	if err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	for i, r := range results {
		if r.Code != driver.WriteCodeOK {
			t.Errorf("result[%d] (%s) Code = %d, want OK (%s)", i, reqs[i].DeviceAddressID, r.Code, r.Message)
		}
	}

	// 回读确认确实写进了服务器的地址空间（不是只拿到了 OK 状态码）。
	addrs := []driver.DeviceAddress{
		{ID: "temp", Name: fmt.Sprintf("ns=%d;i=2001", nsID), DataType: "float32"},
		{ID: "en", Name: fmt.Sprintf("ns=%d;i=2002", nsID), DataType: "bool"},
		{ID: "cnt", Name: fmt.Sprintf("ns=%d;i=2003", nsID), DataType: "int32"},
		{ID: "name", Name: fmt.Sprintf("ns=%d;i=2004", nsID), DataType: "string"},
	}
	read, err := d.Read(addrs)
	if err != nil {
		t.Fatalf("Read() error: %v", err)
	}
	want := []string{"30.5", "1", "99", "new"}
	for i, w := range want {
		if read[i].Value != w {
			t.Errorf("%s 回读值 = %q, want %q", addrs[i].ID, read[i].Value, w)
		}
		if read[i].Quality != driver.QualityGood {
			t.Errorf("%s 回读 Quality = %d, want Good", addrs[i].ID, read[i].Quality)
		}
	}

	// 不存在的节点：服务器回 BadNodeIDUnknown → 地址级错误，不是链路失败。
	results, err = d.Write([]driver.WriteRequest{{
		DeviceAddressID: "missing", Name: fmt.Sprintf("ns=%d;i=9999", nsID),
		DataType: "int32", Value: "1",
	}})
	if err != nil {
		t.Fatalf("Write(missing) error: %v", err)
	}
	if results[0].Code != driver.WriteCodeBadAddress {
		t.Errorf("missing node Code = %d, want BadAddress (%s)", results[0].Code, results[0].Message)
	}
	if !d.IsConnected() {
		t.Error("BadNodeIDUnknown 是地址级错误，不应标记断开")
	}
}
