// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package mitsubishi

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/goburrow/serial"

	"github.com/Ellisata/iot-gateway-sdk/driver"
	"github.com/Ellisata/iot-gateway-sdk/driversdk/serialbus"
	"github.com/Ellisata/iot-gateway-sdk/driversdk/serialproxy"
)

// mcSerialBuses 进程级串口注册表：同一物理端口上的多个会话共享一个句柄
// 与一把总线锁（4C 帧靠站号寻址，一条 RS-485 上挂多台 PLC 是常态）。
var mcSerialBuses = serialbus.New[io.ReadWriteCloser]()

// openSerialPort 打开端口。变量形式只为测试注入假端口（句柄共享的生命周期
// 必须能被单测钉住，而无真实硬件时无法真的去开 /dev/ttyUSB0）。
var openSerialPort = serial.Open

// mcSerialClient 4C 帧 Format5（二进制）串口传输层客户端。
//
// 一个实例对应一个会话；同一端口上的多个实例共享 serialbus.Handle。
// 单次请求+响应全程持有总线锁，避免并发请求在串口上交错、破坏帧结构。
type mcSerialClient struct {
	bus     *serialbus.Handle[io.ReadWriteCloser]
	timeout time.Duration
}

// newMCSerialClient 创建并打开 4C Format5 串口连接。
//
// opener 非 nil 时字节流来自网关代理（跨插件共用物理口），否则直接打开本机串口。
func newMCSerialClient(cfg *MCConfig, opener driver.SerialTransportOpener) (mcTransport, error) {
	if cfg.ComPort == "" {
		return nil, fmt.Errorf("mc serial: comPort is required")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	baud := cfg.BaudRate
	if baud <= 0 {
		baud = 9600
	}
	dataBits := cfg.DataBits
	if dataBits == 0 {
		dataBits = 8
	}
	stopBits := cfg.StopBits
	if stopBits == 0 {
		stopBits = 1
	}
	parity := cfg.Parity
	if parity == "" {
		parity = "N"
	}

	key := serialbus.Key{
		Port:     cfg.ComPort,
		Baud:     baud,
		DataBits: dataBits,
		StopBits: stopBits,
		Parity:   parity,
	}
	opts := driver.SerialOpenOptions{
		Port:        cfg.ComPort,
		BaudRate:    baud,
		DataBits:    dataBits,
		StopBits:    stopBits,
		Parity:      parity,
		ReadTimeout: timeout,
	}
	h, err := mcSerialBuses.Acquire(key, func() (io.ReadWriteCloser, error) {
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
			return nil, fmt.Errorf("open %s failed: %w%s",
				cfg.ComPort, err, driver.SerialOpenFailureHint)
		}
		return port, nil
	})
	if err != nil {
		return nil, fmt.Errorf("mc serial: %w", err)
	}

	if refs := h.Refs(); refs > 1 {
		logf("mc serial client reusing %s (%s, %d sessions shared)",
			h.Key().Port, h.Key(), refs)
	} else {
		logf("mc serial client connected to %s (baud=%d, data=%d, stop=%d, parity=%s)",
			cfg.ComPort, baud, dataBits, stopBits, parity)
	}
	return &mcSerialClient{
		bus:     h,
		timeout: timeout,
	}, nil
}

// Read 通过 4C Format5 帧读取设备数据。
func (c *mcSerialClient) Read(device mcDevice, head uint32, points uint16, bitMode bool) ([]byte, error) {
	c.bus.Lock()
	defer c.bus.Unlock()

	if !c.bus.Alive() {
		return nil, fmt.Errorf("mc serial: not connected")
	}

	frame := buildSerialFrame(device, head, points, bitMode)
	logDebug("mc serial send raw=% X", frame)
	if _, err := c.bus.Resource().Write(frame); err != nil {
		c.markBroken(err)
		return nil, fmt.Errorf("mc serial: write failed: %w", err)
	}

	resp, err := c.readFrame()
	if err != nil {
		c.markBroken(err)
		return nil, fmt.Errorf("mc serial: read failed: %w", err)
	}

	data, err := parseSerialResponse(resp)
	if err != nil {
		return nil, err // 结束码/和校验错误：连接是通的，不标记断连
	}
	return data, nil
}

// Write 通过 4C Format5 帧批量写设备数据。
func (c *mcSerialClient) Write(device mcDevice, head uint32, points uint16, bitMode bool, data []byte) error {
	c.bus.Lock()
	defer c.bus.Unlock()

	if !c.bus.Alive() {
		return fmt.Errorf("mc serial: not connected")
	}

	frame := buildSerialWriteFrame(device, head, points, bitMode, data)
	logDebug("mc serial send write raw=% X", frame)
	if _, err := c.bus.Resource().Write(frame); err != nil {
		c.markBroken(err)
		return fmt.Errorf("mc serial: write failed: %w", err)
	}

	resp, err := c.readFrame()
	if err != nil {
		c.markBroken(err)
		return fmt.Errorf("mc serial: read write response failed: %w", err)
	}
	if _, err := parseSerialResponse(resp); err != nil {
		return err // 结束码/和校验错误：连接是通的，不标记断连
	}
	return nil
}

// markBroken 在链路上出现真正的 I/O 错误时判死共享句柄。
//
// 读超时不算：它只说明这一次没有应答（设备离线、站号写错），不代表 fd 坏了。
// 共享句柄的判死会波及同一条总线上的所有会话，把"一台设备掉线"放大成"整条
// 总线反复重连"正是本次要修的问题。
func (c *mcSerialClient) markBroken(err error) {
	if !errors.Is(err, serial.ErrTimeout) && !errors.Is(err, serialproxy.ErrTimeout) {
		c.bus.MarkBroken()
	}
}

// readFrame 读取一个完整 4C Format5 响应帧（解填充后的帧体，含和校验字节）。
//
// 以 DLE 状态机逐字节解析：帧头 DLE STX 起始，帧体 DLE 字节被双写（0x10 0x10 表示
// 字面 0x10），帧尾为 DLE ETX（0x10 0x03）。因字面 0x10 恒被双写，帧尾 DLE ETX
// 不会与数据混淆。串口读超时由 serial.Config.Timeout 控制（返回 serial.ErrTimeout）。
// 调用方必须持有总线锁。
func (c *mcSerialClient) readFrame() ([]byte, error) {
	buf := make([]byte, 0, 64)
	tmp := make([]byte, 1)
	var started, prevDLE bool

	for {
		n, err := c.bus.Resource().Read(tmp)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			continue
		}
		b := tmp[0]

		if !started {
			// 等待帧头 DLE STX
			if b == dleByte {
				prevDLE = true
			} else if prevDLE && b == stxByte {
				started = true
				prevDLE = false
			} else {
				prevDLE = false
			}
			continue
		}

		// 帧体内
		if prevDLE {
			if b == etxByte {
				return buf, nil // DLE ETX 帧尾
			}
			// 字面 0x10 对（含 0x10 0x10 双写），还原为单字节 0x10
			buf = append(buf, b)
			prevDLE = false
			continue
		}
		if b == dleByte {
			prevDLE = true
			continue
		}
		buf = append(buf, b)
		if len(buf) > maxSerialFrameLen {
			return nil, fmt.Errorf("mc serial: response too long (%d bytes)", len(buf))
		}
	}
}

// IsConnected 返回连接状态
func (c *mcSerialClient) IsConnected() bool {
	return c != nil && c.bus != nil && c.bus.Alive()
}

// Close 释放本会话对共享句柄的引用；最后一个会话才真正关闭端口。
func (c *mcSerialClient) Close() error {
	if c == nil || c.bus == nil {
		return nil
	}
	return c.bus.Release()
}
