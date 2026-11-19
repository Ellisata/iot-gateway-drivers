// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package fanuc

import (
	"encoding/binary"
	"encoding/json"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/Ellisata/iot-gateway-sdk/driver"
	"github.com/Ellisata/iot-gateway-sdk/testkit/fake"

	cipcore "github.com/Ellisata/iot-gateway-drivers/fanuc/cipcore"
)

// 本文件测的是设备扫描（probe.go）。假应答由 SDK 的 testkit/fake 构造 ——
// 那份实现是照规范另写一遍的，与 cipcore 的解析器互不依赖，因此这里的用例
// 是一次真正的交叉验证（两边同时把 Socket Address 的字节序写反时，只有
// 独立实现才能让测试红）。

// probeBudget 用例里的探测预算。远大于 loopback 上的往返耗时，
// 因此它只对"必须等满超时"的那几条路径起作用。
const probeBudget = 5 * time.Second

// targetFor 把一个 "host:port" 地址转成 ScanTarget。
func targetFor(t *testing.T, addr string, broadcast bool) driver.ScanTarget {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("拆分地址 %q: %v", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("端口 %q 不是数字: %v", portStr, err)
	}
	return driver.ScanTarget{Host: host, Port: port, Broadcast: broadcast}
}

func hasAttr(dev driver.DiscoveredDevice, name, value string) bool {
	for _, a := range dev.Extra {
		if a.Name == name && a.Value == value {
			return true
		}
	}
	return false
}

// fanucR30iB 一个典型的 FANUC 机器人控制器身份（厂商号 591）。
func fanucR30iB(ip string, port int) fake.CIPIdentity {
	return fake.CIPIdentity{
		VendorID:     cipcore.VendorFANUC,
		DeviceType:   0x000D,
		ProductCode:  0x0001,
		Major:        9,
		Minor:        30,
		SerialNumber: 0x12345678,
		ProductName:  "R-30iB Plus",
		IP:           net.ParseIP(ip),
		Port:         port,
	}
}

// fakeFanucServer 起一个只应答 ListIdentity 的 UDP 假设备。
func fakeFanucServer(t *testing.T, ids ...fake.CIPIdentity) *fake.UDPServer {
	t.Helper()
	resp := fake.BuildListIdentityResponse(ids...)
	srv, err := fake.NewUDPServer(func(req []byte) []byte {
		if len(req) < 2 || binary.LittleEndian.Uint16(req[0:2]) != cipcore.CmdListIdentity {
			return nil
		}
		return resp
	})
	if err != nil {
		t.Fatalf("启动假设备: %v", err)
	}
	t.Cleanup(srv.Close)
	return srv
}

// ==================== 选项解析 ====================

func TestParseScanOptions(t *testing.T) {
	cases := []struct {
		name    string
		options string
		timeout time.Duration
		window  time.Duration
		wantErr bool
	}{
		{"默认值", ``, 1500 * time.Millisecond, 500 * time.Millisecond, false},
		{"空对象", `{}`, 1500 * time.Millisecond, 500 * time.Millisecond, false},
		{"超时是数字", `{"timeoutMs":900}`, 900 * time.Millisecond, 300 * time.Millisecond, false},
		{"超时是字符串", `{"timeoutMs":"900"}`, 900 * time.Millisecond, 300 * time.Millisecond, false},
		// 安静窗口跟着超时缩小（想扫快一点时它必须一起小），但**不跟着放大**：
		// 应答是同时到达的，把超时调大没有理由让每次扫描多等 1 秒。
		{"超时调大窗口封顶", `{"timeoutMs":6000}`, 6000 * time.Millisecond, 500 * time.Millisecond, false},
		{"超时是0回默认", `{"timeoutMs":0}`, 1500 * time.Millisecond, 500 * time.Millisecond, false},
		// 认不出的超时静默回默认值：这个值错了的表现只是"扫得慢"。
		{"超时认不出", `{"timeoutMs":"很快"}`, 1500 * time.Millisecond, 500 * time.Millisecond, false},
		{"选项不是JSON", `{`, 0, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var raw json.RawMessage
			if c.options != "" {
				raw = json.RawMessage(c.options)
			}
			opt, err := parseScanOptions(raw)
			if c.wantErr {
				if err == nil {
					t.Fatal("期望报错，实际没有")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseScanOptions = %v", err)
			}
			if opt.timeout != c.timeout {
				t.Errorf("timeout = %v, 期望 %v", opt.timeout, c.timeout)
			}
			if opt.replyWindow != c.window {
				t.Errorf("replyWindow = %v, 期望 %v", opt.replyWindow, c.window)
			}
		})
	}
}

// ==================== 探测 ====================

func TestProbeFindsFanucDevice(t *testing.T) {
	srv := fakeFanucServer(t, fanucR30iB("127.0.0.1", 44818))

	devices, err := Probe(targetFor(t, srv.Addr(), false), nil, probeBudget)
	if err != nil {
		t.Fatalf("Probe 不应报错: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("扫到 %d 台设备，期望 1 台: %+v", len(devices), devices)
	}

	dev := devices[0]
	if dev.Host != "127.0.0.1" || dev.Port != 44818 {
		t.Errorf("设备地址 = %s:%d，期望 127.0.0.1:44818", dev.Host, dev.Port)
	}
	if dev.Name != "R-30iB Plus" || dev.Model != "R-30iB Plus" {
		t.Errorf("名称/型号 = %q/%q，期望都取自设备自述的 R-30iB Plus", dev.Name, dev.Model)
	}
	if dev.Vendor != "FANUC" {
		t.Errorf("Vendor = %q，期望 FANUC", dev.Vendor)
	}
	if dev.Version != "9.30" {
		t.Errorf("Version = %q，期望 9.30", dev.Version)
	}
	if !hasAttr(dev, "厂商号", "591") {
		t.Errorf("缺少厂商号属性或值不对: %+v", dev.Extra)
	}
	if !hasAttr(dev, "序列号", "0x12345678") {
		t.Errorf("缺少序列号属性或值不对: %+v", dev.Extra)
	}

	// ProtocolJSON 必须能被本插件自己的配置解析器吃下 —— 键名写错时
	// 设备建出来连不上，而两边都不报错。
	cfg, err := ParseConfig(dev.ProtocolJSON)
	if err != nil {
		t.Fatalf("ProtocolJSON 解析失败: %v", err)
	}
	if cfg.Host != "127.0.0.1" || cfg.Port != 44818 {
		t.Errorf("ProtocolJSON 回读 = %s:%d，与扫描结果不一致", cfg.Host, cfg.Port)
	}
}

// TestProbeFiltersOtherVendors 非 FANUC 的 EtherNet/IP 设备**刻意不上报**。
//
// ListIdentity 是厂商无关的标准命令，同一个端口上 Allen-Bradley、Omron 等
// 设备都会应答。上报它们只会让用户建出本插件连不上的设备。
func TestProbeFiltersOtherVendors(t *testing.T) {
	srv := fakeFanucServer(t, fake.CIPIdentity{
		VendorID:    1, // Allen-Bradley
		DeviceType:  0x000E,
		ProductCode: 0x006F,
		Major:       32,
		Minor:       11,
		ProductName: "1756-L83E",
		IP:          net.ParseIP("127.0.0.1"),
		Port:        44818,
	})

	devices, err := Probe(targetFor(t, srv.Addr(), false), nil, probeBudget)
	if err != nil {
		t.Fatalf("Probe 不应报错: %v", err)
	}
	if len(devices) != 0 {
		t.Fatalf("非 FANUC 设备不应上报，实际扫到 %+v", devices)
	}
}

// TestProbeNoDevice 目标上没有设备 = 正常结果，不是错误。
func TestProbeNoDevice(t *testing.T) {
	srv, err := fake.NewUDPServer(func([]byte) []byte { return nil })
	if err != nil {
		t.Fatalf("启动假设备: %v", err)
	}
	t.Cleanup(srv.Close)

	devices, err := Probe(targetFor(t, srv.Addr(), false),
		json.RawMessage(`{"timeoutMs":100}`), probeBudget)
	if err != nil {
		t.Fatalf("扫不到设备不应报错: %v", err)
	}
	if len(devices) != 0 {
		t.Fatalf("期望 0 台设备，实际 %+v", devices)
	}
}

// TestProbeIgnoresForeignTraffic 端口上跑着别的服务（应答不是 ListIdentity）
// 时同样按"没有设备"处理。
func TestProbeIgnoresForeignTraffic(t *testing.T) {
	srv, err := fake.NewUDPServer(func([]byte) []byte {
		return []byte("not an ethernet/ip frame")
	})
	if err != nil {
		t.Fatalf("启动假设备: %v", err)
	}
	t.Cleanup(srv.Close)

	devices, err := Probe(targetFor(t, srv.Addr(), false),
		json.RawMessage(`{"timeoutMs":100}`), probeBudget)
	if err != nil {
		t.Fatalf("无效应答不应报错: %v", err)
	}
	if len(devices) != 0 {
		t.Fatalf("期望 0 台设备，实际 %+v", devices)
	}
}

// TestProbeMultipleDevicesFromOneBroadcast 一条广播应答可以带多台设备。
//
// 广播目标下收应答按"安静窗口"判定结束；单播目标收到第一条就停。
func TestProbeMultipleDevicesFromOneBroadcast(t *testing.T) {
	srv := fakeFanucServer(t,
		fanucR30iB("127.0.0.1", 44818),
		fanucR30iB("127.0.0.2", 44818),
	)

	devices, err := Probe(targetFor(t, srv.Addr(), true), nil, probeBudget)
	if err != nil {
		t.Fatalf("Probe 不应报错: %v", err)
	}
	if len(devices) != 2 {
		t.Fatalf("扫到 %d 台设备，期望 2 台: %+v", len(devices), devices)
	}
}

// TestProbeInvalidTarget 空地址/非 IPv4 字面量直接跳过，不发起探测也不报错。
func TestProbeInvalidTarget(t *testing.T) {
	for _, host := range []string{"", "robot.example.com"} {
		devices, err := Probe(driver.ScanTarget{Host: host}, nil, probeBudget)
		if err != nil {
			t.Errorf("目标 %q: 不应报错: %v", host, err)
		}
		if len(devices) != 0 {
			t.Errorf("目标 %q: 期望 0 台设备，实际 %+v", host, devices)
		}
	}
}

// ==================== 身份 → 设备映射 ====================

// TestBuildDeviceFallsBackToSourceAddr 设备自述 0.0.0.0 时回落到数据报源地址。
func TestBuildDeviceFallsBackToSourceAddr(t *testing.T) {
	id := cipcore.Identity{
		VendorID:    cipcore.VendorFANUC,
		ProductName: "R-30iB",
		// IP 留空 = 设备自述 0.0.0.0
	}
	dev, ok := buildDevice(id, &net.UDPAddr{IP: net.ParseIP("192.168.1.10"), Port: 44818})
	if !ok {
		t.Fatal("有源地址时应能建出设备")
	}
	if dev.Host != "192.168.1.10" || dev.Port != 44818 {
		t.Errorf("地址 = %s:%d，期望回落为 192.168.1.10:44818", dev.Host, dev.Port)
	}
}

// TestBuildDeviceNoAddress 自述地址与源地址都不可用时不能建出设备。
func TestBuildDeviceNoAddress(t *testing.T) {
	if _, ok := buildDevice(cipcore.Identity{VendorID: cipcore.VendorFANUC}, nil); ok {
		t.Fatal("没有可用地址时不应建出设备")
	}
}
