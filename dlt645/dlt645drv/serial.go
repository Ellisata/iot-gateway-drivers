// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package dlt645

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/goburrow/serial"

	"github.com/Ellisata/iot-gateway-sdk/driver"
	"github.com/Ellisata/iot-gateway-sdk/driversdk/serialbus"
)

// serialReadTimeout 串口单次读的超时。
//
// 刻意取小值：帧的截断由 codec.go 的截止时间循环统一裁决，串口这里只需
// 保证「有字节就尽快返回、无字节就尽快回来复核截止时间」。
// 取大值会让每次请求前的 Drain（清理迟到应答）白等一个超时周期，
// 在 2400bps 下会显著拖慢轮询。
const serialReadTimeout = 20 * time.Millisecond

// serialDrainRounds Drain 最多读取的轮数，避免链路上持续有噪声时死循环。
const serialDrainRounds = 8

// serialPorts 进程级端口注册表：同一物理端口的所有会话共享一个句柄与一把
// 总线锁（见 SDK 的 driversdk/serialbus 包注释，那里写了各开一个句柄会同时
// 踩到的三个坑：应答字节被瓜分、select 竞争拿到 EAGAIN、一方 Close 恢复
// termios 打断另一方）。
var serialPorts = serialbus.New[io.ReadWriteCloser]()

// openSerialPort 打开端口。变量形式只为测试注入假端口：句柄共享的生命周期
// （谁打开、谁关闭、引用计数）必须能被单测钉住，而无真实硬件时无法真的去开
// /dev/ttyUSB0。
var openSerialPort = serial.Open

// serialClient RS-485 / RS-232 串口传输。
//
// 一个实例可能被**多个会话**（同一端口上的多台设备）共用：它们持有同一个
// serialbus.Handle，它的 Lock 就是串行总线锁 —— 一次「清缓冲 → 发送 → 读应答」
// 全程持有它（见 transport.go 与 codec.go 的 exchange）。
type serialClient struct {
	bus *serialbus.Handle[io.ReadWriteCloser]
	// dirty 上一轮交互没有干净收尾（写失败 / 读超时 / 读到噪声帧）。
	// 串口拿不到裸 fd、查不了接收缓冲，只能靠这个标记决定 Drain 要不要真去读。
	// 仅在持有总线锁的交互期间读写（exchange 全程持锁）。
	dirty bool
}

// serialParams 套好默认值的串口参数。
//
// 键与打开必须用同一份结果：否则"省略 baudRate"（默认 2400）与"显式 2400"
// 会被算成两个端口，仍然各开一个句柄。
type serialParams struct {
	baud     int
	dataBits int
	stopBits int
	parity   string
}

func serialParamsOf(cfg *DLT645Config) serialParams {
	p := serialParams{
		baud:     cfg.BaudRate,
		dataBits: cfg.DataBits,
		stopBits: cfg.StopBits,
		parity:   cfg.Parity,
	}
	if p.baud <= 0 {
		p.baud = defaultBaudRate
	}
	if p.dataBits == 0 {
		p.dataBits = defaultDataBits
	}
	if p.stopBits == 0 {
		p.stopBits = defaultStopBits
	}
	if p.parity == "" {
		p.parity = defaultParity
	}
	return p
}

// newSerialClient 打开串口；同一物理端口上参数一致的已有句柄会被复用。
// 串口参数不同的设备被判为配置错误（同一条总线上的设备必须参数一致）。
//
// opener 非 nil 时字节流来自网关代理（跨插件共用物理口），否则直接打开本机串口。
func newSerialClient(cfg *DLT645Config, opener driver.SerialTransportOpener) (dlt645Transport, error) {
	if strings.TrimSpace(cfg.ComPort) == "" {
		return nil, fmt.Errorf("dlt645 serial: comPort 未配置")
	}

	params := serialParamsOf(cfg)
	key := serialbus.Key{
		Port:     cfg.ComPort,
		Baud:     params.baud,
		DataBits: params.dataBits,
		StopBits: params.stopBits,
		Parity:   params.parity,
	}
	opts := driver.SerialOpenOptions{
		Port:        cfg.ComPort,
		BaudRate:    params.baud,
		DataBits:    params.dataBits,
		StopBits:    params.stopBits,
		Parity:      params.parity,
		ReadTimeout: serialReadTimeout,
	}

	h, err := serialPorts.Acquire(key, func() (io.ReadWriteCloser, error) {
		if opener != nil {
			port, err := opener(opts)
			if err != nil {
				return nil, err
			}
			return port, nil
		}
		port, err := openSerialPort(&serial.Config{
			Address:  opts.Port,
			BaudRate: opts.BaudRate,
			DataBits: opts.DataBits,
			StopBits: opts.StopBits,
			Parity:   opts.Parity,
			Timeout:  opts.ReadTimeout,
		})
		if err != nil {
			return nil, fmt.Errorf("打开 %s 失败: %w%s",
				cfg.ComPort, err, driver.SerialOpenFailureHint)
		}
		return port, nil
	})
	if err != nil {
		return nil, fmt.Errorf("dlt645 serial: %w", err)
	}

	if refs := h.Refs(); refs > 1 {
		logf("serial 复用已打开的 %s（%s，共 %d 个会话共用）",
			h.Key().Port, h.Key(), refs)
	} else {
		logf("serial 已连接 %s（%s）", cfg.ComPort, h.Key())
	}
	return &serialClient{bus: h}, nil
}

func (c *serialClient) Lock()   { c.bus.Lock() }
func (c *serialClient) Unlock() { c.bus.Unlock() }

func (c *serialClient) Write(p []byte) (int, error) {
	if !c.bus.Alive() {
		return 0, fmt.Errorf("dlt645 serial: 连接已关闭")
	}
	n, err := c.bus.Resource().Write(p)
	if err != nil {
		c.bus.MarkBroken()
		return n, fmt.Errorf("dlt645 serial: 写入失败: %w", err)
	}
	return n, nil
}

func (c *serialClient) Read(p []byte) (int, error) {
	if !c.bus.Alive() {
		return 0, fmt.Errorf("dlt645 serial: 连接已关闭")
	}
	n, err := c.bus.Resource().Read(p)
	if err != nil && !isTimeoutErr(err) {
		c.bus.MarkBroken()
	}
	return n, err
}

// SetReadDeadline 串口不支持可变的读截止时间（go burrow/serial 的超时在打开时固定），
// 由 serialReadTimeout 的短超时配合 codec 的截止时间循环共同实现等效语义。
func (c *serialClient) SetReadDeadline(time.Time) error { return nil }

// Drain 丢弃接收缓冲中的残留字节，最多读取 serialDrainRounds 轮。
//
// 串口读用的是 goburrow/serial 的 select + 打开时固定的超时，
// 缓冲为空时**每次读都要空等到 serialReadTimeout（20ms）**才返回 ErrTimeout。
// 而 serial.Port 只有 io.ReadWriteCloser、拿不到裸 fd，做不了 TCP 那样的
// 待读字节探测，因此改用 dirty 标记：上一轮干净收尾就整段跳过。
//
// 这不削弱「值慢一拍」的防护——迟到的应答只可能在某轮没按时收到应答时才在途，
// 而那种轮次恰恰会把 dirty 置上。参考：同仓库的 ModBus.RTU（goburrow/modbus）
// 从不 pre-drain，只按预期长度读 + 校验 CRC，本策略比它更保守。
func (c *serialClient) Drain() {
	if !c.bus.Alive() {
		return
	}
	if !c.dirty {
		return
	}
	c.dirty = false

	buf := make([]byte, 256)
	for i := 0; i < serialDrainRounds; i++ {
		n, err := c.bus.Resource().Read(buf)
		if err != nil || n == 0 {
			return
		}
		logDebug("serial: 丢弃残留字节 raw=% X", buf[:n])
	}
}

// MarkDirty 见 transport.go 的接口说明。
func (c *serialClient) MarkDirty() { c.dirty = true }

func (c *serialClient) IsConnected() bool {
	return c != nil && c.bus != nil && c.bus.Alive()
}

// Close 释放本会话对句柄的引用；最后一个引用才真正关闭端口。
func (c *serialClient) Close() error {
	if c == nil || c.bus == nil {
		return nil
	}
	return c.bus.Release()
}
