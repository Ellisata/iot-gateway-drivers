// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package fanuc

import (
	"fmt"
	"testing"
	"time"

	"github.com/Ellisata/iot-gateway-sdk/driver"
)

// ==================== 性能基准 ====================
//
// 这一组基准测的是**驱动 + CIP 显式消息**在 loopback 假设备上的开销，用于回答：
//
//   - 一次 R[] 读取（一个请求/响应往返）在网关侧花多少时间；
//   - 单轮 N 个点位的成本如何随 N 增长（默认块读；配置 maxRegistersPerRead=1
//     时退化为 N 次请求，可与块读直接对比）；
//   - 限流之外，本地解析/去重/解码的 CPU 成本；
//   - 写入与连接建立的固定成本。
//
// **不代表真机容量**：真实 FANUC 控制器的显式消息处理时延通常是毫秒级，
// 且与运动控制共用 CPU；这里的 loopback 数字是驱动侧开销的**下界**。
// 端到端容量（采集引擎 + 插件 RPC + 假设备 + RTT）要用网关的
// iot-gateway-premium/backend/cmd/loadtest 跑（该工具目前尚未接入 fanuc）。
//
// 运行：
//
//	go test -run '^$' -bench . -benchmem ./fanuc/fanucdrv
//	go test -run '^$' -bench 'ReadBatch' -benchtime 2s -count 3 ./fanuc/fanucdrv

// startBenchDriver 连接假设备，并**去掉限流**。
//
// 限流是生产安全边界（默认 10 次/秒），它让 ns/op 变成"睡眠时间"，会把
// 协议与编解码的真实成本完全淹没。这里测的是链路能力；有效吞吐还要再按
// maxRequestsPerSecond 取小值：
//
//	每设备帧率 = min(maxRequestsPerSecond, 1/单帧耗时)
//	单轮可采点数 ≈ 每设备帧率 × 采集周期 × 块大小
func startBenchDriver(b *testing.B, f *fakeFANUC) *fanucDriver {
	b.Helper()
	LogEnabled = false
	d := startDriver(b, f)
	disableBenchLimiter(d)
	return d
}

func disableBenchLimiter(d *fanucDriver) {
	d.mu.Lock()
	d.limit = nil
	d.mu.Unlock()
}

func benchAddrs(n int) []driver.DeviceAddress {
	addrs := make([]driver.DeviceAddress, n)
	for i := range addrs {
		addrs[i] = driver.DeviceAddress{
			ID:       fmt.Sprintf("point-%d", i),
			Name:     fmt.Sprintf("R[%d]", i+1),
			DataType: "int32",
		}
	}
	return addrs
}

func reportThroughput(b *testing.B, points int, elapsed time.Duration) {
	b.Helper()
	b.StopTimer()
	if elapsed > 0 {
		b.ReportMetric(float64(b.N*points)/elapsed.Seconds(), "points/s")
	}
}

// BenchmarkReadSinglePoint 单点读取：一次 Read 调用 = 一次 CIP 往返。
// ns/op 就是**单帧往返 + 驱动开销**的最低值。
func BenchmarkReadSinglePoint(b *testing.B) {
	f := newFakeFANUC(b, 591, false)
	f.setInt(1, 42)
	d := startBenchDriver(b, f)
	addrs := benchAddrs(1)

	if _, err := d.Read(addrs); err != nil { // 预热
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	start := time.Now()
	for i := 0; i < b.N; i++ {
		if _, err := d.Read(addrs); err != nil {
			b.Fatal(err)
		}
	}
	reportThroughput(b, 1, time.Since(start))
}

// BenchmarkReadBatch 一轮读取 n 个**不同**寄存器。
//
// 默认 maxRegistersPerRead=100，n≤100 的连续寄存器会合并成**一次块读**
// （Get_Attribute_All），所以 10/50/100 三档的帧数都是 1，points/s 的差异
// 来自逐点解析、解码与结果重组。与关闭块读（maxRegistersPerRead=1）对比
// 见 BenchmarkReadBatchNoBlock。
func BenchmarkReadBatch(b *testing.B) {
	for _, n := range []int{10, 50, 100} {
		b.Run(fmt.Sprintf("points=%d", n), func(b *testing.B) {
			f := newFakeFANUC(b, 591, false)
			for i := 1; i <= n; i++ {
				f.setInt(uint16(i), int32(i))
			}
			d := startBenchDriver(b, f)
			addrs := benchAddrs(n)

			if _, err := d.Read(addrs); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			start := time.Now()
			for i := 0; i < b.N; i++ {
				res, err := d.Read(addrs)
				if err != nil {
					b.Fatal(err)
				}
				if len(res) != n {
					b.Fatalf("len = %d, want %d", len(res), n)
				}
			}
			reportThroughput(b, n, time.Since(start))
		})
	}
}

// BenchmarkReadBatchNoBlock 关闭块读后的 100 点/轮：100 次单点往返。
// 与 BenchmarkReadBatch/points=100 的比值就是块读带来的帧数收益。
func BenchmarkReadBatchNoBlock(b *testing.B) {
	LogEnabled = false
	const n = 100
	f := newFakeFANUC(b, 591, false)
	for i := 1; i <= n; i++ {
		f.setInt(uint16(i), int32(i))
	}
	d := startDriverWithConfig(b, configJSONWithMax(f, 1))
	disableBenchLimiter(d) // 与 BenchmarkReadBatch 同口径：不限流
	addrs := benchAddrs(n)

	if _, err := d.Read(addrs); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	start := time.Now()
	for i := 0; i < b.N; i++ {
		if _, err := d.Read(addrs); err != nil {
			b.Fatal(err)
		}
	}
	reportThroughput(b, n, time.Since(start))
}

// BenchmarkReadBatchRateLimited 刻意保留限流（maxRequestsPerSecond=50）并关闭
// 块读：100 点/轮 = 100 × 20ms ≈ 2s，即单设备 50 点/秒。这是 P0 单读路径的
// 实际上限，也是块读要解决的问题。运行时用 -benchtime 1x 即可。
func BenchmarkReadBatchRateLimited(b *testing.B) {
	LogEnabled = false
	const n = 100
	f := newFakeFANUC(b, 591, false)
	for i := 1; i <= n; i++ {
		f.setInt(uint16(i), int32(i))
	}
	// 注意：这里**不**调用 disableBenchLimiter。
	d := startDriverWithConfig(b, configJSONWithMax(f, 1))
	addrs := benchAddrs(n)

	if _, err := d.Read(addrs); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	start := time.Now()
	for i := 0; i < b.N; i++ {
		if _, err := d.Read(addrs); err != nil {
			b.Fatal(err)
		}
	}
	reportThroughput(b, n, time.Since(start))
}

// BenchmarkReadDuplicatePoints 大量点位落在少量寄存器上（如多个业务点位
// 引用同一个 R[n]）。驱动按 (class,index) 去重，块读开启时只发 1 次请求；
// 这个基准主要量 1000 个点位的本地解析、解码与结果重组成本。
func BenchmarkReadDuplicatePoints(b *testing.B) {
	const (
		points = 1000
		unique = 10
	)
	f := newFakeFANUC(b, 591, false)
	for i := 1; i <= unique; i++ {
		f.setInt(uint16(i), int32(i))
	}
	d := startBenchDriver(b, f)

	addrs := make([]driver.DeviceAddress, points)
	for i := range addrs {
		idx := i%unique + 1
		addrs[i] = driver.DeviceAddress{
			ID:       fmt.Sprintf("point-%d", i),
			Name:     fmt.Sprintf("R[%d]", idx),
			DataType: "int32",
		}
	}
	if _, err := d.Read(addrs); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	start := time.Now()
	for i := 0; i < b.N; i++ {
		if _, err := d.Read(addrs); err != nil {
			b.Fatal(err)
		}
	}
	reportThroughput(b, points, time.Since(start))
}

// BenchmarkWriteBatch 一轮写入 n 个寄存器：每次 Set_Attribute_Single 一个往返。
func BenchmarkWriteBatch(b *testing.B) {
	for _, n := range []int{10, 50} {
		b.Run(fmt.Sprintf("points=%d", n), func(b *testing.B) {
			f := newFakeFANUC(b, 591, false)
			for i := 1; i <= n; i++ {
				f.setInt(uint16(i), 0)
			}
			d := startBenchDriver(b, f)

			reqs := make([]driver.WriteRequest, n)
			for i := range reqs {
				reqs[i] = driver.WriteRequest{
					DeviceAddressID: fmt.Sprintf("point-%d", i),
					Name:            fmt.Sprintf("R[%d]", i+1),
					DataType:        "int32",
					Value:           "1",
				}
			}
			if _, err := d.Write(reqs); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			start := time.Now()
			for i := 0; i < b.N; i++ {
				if _, err := d.Write(reqs); err != nil {
					b.Fatal(err)
				}
			}
			reportThroughput(b, n, time.Since(start))
		})
	}
}

// BenchmarkConnect 一次完整建连：UDP ListIdentity + TCP 拨号 + RegisterSession。
func BenchmarkConnect(b *testing.B) {
	LogEnabled = false
	f := newFakeFANUC(b, 591, false)
	cfg := configJSON(f)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d := newFANUCDriver()
		if err := d.Connect(cfg); err != nil {
			b.Fatal(err)
		}
		d.Close()
	}
}

// BenchmarkParseRegisterAddress 纯本地地址解析（每轮每点位一次）。
func BenchmarkParseRegisterAddress(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := parseRegisterAddress("R[123]", "float32"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDecodeValue 纯本地解码 + 格式化（每个成功点位一次）。
func BenchmarkDecodeValue(b *testing.B) {
	raw := []byte{0x00, 0x00, 0xA0, 0x3F} // 1.25f
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, _, err := decodeValue(raw, "float32"); err != nil {
			b.Fatal(err)
		}
	}
}
