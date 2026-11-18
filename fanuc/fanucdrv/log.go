// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package fanuc

import "github.com/Ellisata/iot-gateway-sdk/driversdk"

// Debug 控制 Debug 级日志是否输出，默认关闭。
//
// 插件进程没有网关的日志级别配置通道，所以它是源码级开关：现场要抓
// 帧明细就改成 true 重新编译。默认关闭是必要的 —— 插件 stderr 是同步管道，
// 采集热路径上逐帧输出会反压插件、拖慢采集。
var Debug bool

// LogEnabled 控制普通日志是否输出，默认开启。
//
// 基准测试会把它关掉：go test 会把子进程/测试进程的 stderr 逐行转印到
// 基准输出里，每个 benchmark 的建连日志会把 ns/op 那几行淹没。生产路径
// 永远是 true，只有测试代码会改它。
var LogEnabled = true

// logf 输出一行日志。
//
// 一律走 driversdk.Logf（stderr）：stdout 是插件与网关之间的帧通道，
// 往里写任何非帧数据都会被判定失步并杀掉进程。
func logf(format string, args ...any) {
	if !LogEnabled {
		return
	}
	driversdk.Logf("fanuc.cip: "+format, args...)
}

func logDebug(format string, args ...any) {
	if Debug {
		logf(format, args...)
	}
}
