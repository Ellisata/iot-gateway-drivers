// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package fins

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

// finsSerialBuses 进程级串口注册表：同一物理端口上的多个会话共享一个句柄
// 与一把总线锁（Host Link 靠单元号寻址，一条 RS-485 上挂多台 PLC 是常态）。
var finsSerialBuses = serialbus.New[io.ReadWriteCloser]()

// openSerialPort 打开端口。变量形式只为测试注入假端口（句柄共享的生命周期
// 必须能被单测钉住，而无真实硬件时无法真的去开 /dev/ttyUSB0）。
var openSerialPort = serial.Open

// finsSerialClient Host Link（FINS over serial）传输层客户端。
//
// 一个实例对应一个会话；同一端口上的多个实例共享 serialbus.Handle。
// 单次请求+响应全程持有总线锁，避免并发请求（采集轮询与连接测试同时触发）
// 在串口上交错、破坏帧结构。
type finsSerialClient struct {
	bus     *serialbus.Handle[io.ReadWriteCloser]
	timeout time.Duration
	unitNo  byte
	dstUnit byte
	srcUnit byte
	sid     byte
	fcsMode string
}

// newFINSSerialClient 创建并打开 Host Link 串口连接。
//
// opener 非 nil 时字节流来自网关代理（跨插件共用物理口），否则直接打开本机串口。
func newFINSSerialClient(cfg *FINSConfig, opener driver.SerialTransportOpener) (finsTransport, error) {
	if cfg.ComPort == "" {
		return nil, fmt.Errorf("fins serial: comPort is required")
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
	h, err := finsSerialBuses.Acquire(key, func() (io.ReadWriteCloser, error) {
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
		return nil, fmt.Errorf("fins serial: %w", err)
	}

	if refs := h.Refs(); refs > 1 {
		logf("serial client reusing %s (%s, %d sessions shared)",
			h.Key().Port, h.Key(), refs)
	} else {
		logf("serial client connected to %s (baud=%d, data=%d, stop=%d, parity=%s, unit=%d)",
			cfg.ComPort, baud, dataBits, stopBits, parity, cfg.UnitNo)
	}
	return &finsSerialClient{
		bus:     h,
		timeout: timeout,
		unitNo:  cfg.UnitNo,
		dstUnit: cfg.DstUnit,
		srcUnit: cfg.SrcUnit,
		fcsMode: cfg.FCSMode,
	}, nil
}

// Read 通过 Host Link 帧读取内存区。
func (c *finsSerialClient) Read(area finsArea, word, count uint16) ([]byte, error) {
	c.bus.Lock()
	defer c.bus.Unlock()

	if !c.bus.Alive() {
		return nil, fmt.Errorf("fins serial: not connected")
	}

	c.sid++
	sid := c.sid
	finsBody := []byte{
		cmdMemoryAreaReadHi, cmdMemoryAreaReadLo,
		byte(area),
		byte(word >> 8), byte(word),
		0x00, // 位地址：字访问
		byte(count >> 8), byte(count),
	}
	frame := buildSerialFrame(c.fcsMode, c.unitNo, c.dstUnit, c.srcUnit, sid, finsBody)

	logDebug("serial send raw=%q", string(frame))
	if _, err := c.bus.Resource().Write(frame); err != nil {
		c.markBroken(err)
		return nil, fmt.Errorf("fins serial: write failed: %w", err)
	}

	resp, err := c.readFrame()
	if err != nil {
		c.markBroken(err)
		return nil, fmt.Errorf("fins serial: read failed: %w", err)
	}

	data, err := parseSerialResponse(resp, sid, c.fcsMode)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Write 通过 Host Link 帧写入内存区。
func (c *finsSerialClient) Write(area finsArea, word uint16, bit int, data []byte) error {
	c.bus.Lock()
	defer c.bus.Unlock()

	if !c.bus.Alive() {
		return fmt.Errorf("fins serial: not connected")
	}

	c.sid++
	sid := c.sid
	finsBody := append(
		[]byte{byte(cmdMemoryAreaWrite >> 8), byte(cmdMemoryAreaWrite & 0xFF)},
		buildMemoryWriteBody(area, word, bit, data)...,
	)
	frame := buildSerialFrame(c.fcsMode, c.unitNo, c.dstUnit, c.srcUnit, sid, finsBody)

	logDebug("serial send write raw=%q", string(frame))
	if _, err := c.bus.Resource().Write(frame); err != nil {
		c.markBroken(err)
		return fmt.Errorf("fins serial: write failed: %w", err)
	}

	resp, err := c.readFrame()
	if err != nil {
		c.markBroken(err)
		return fmt.Errorf("fins serial: read write response failed: %w", err)
	}
	_, err = parseSerialResponse(resp, sid, c.fcsMode)
	return err
}

// markBroken 在链路上出现真正的 I/O 错误时判死共享句柄。
//
// 读超时不算：它只说明这一次没有应答（设备离线、单元号写错），不代表 fd 坏了。
// 共享句柄的判死会波及同一条总线上的所有会话，把"一台设备掉线"放大成"整条
// 总线反复重连"正是本次要修的问题。
func (c *finsSerialClient) markBroken(err error) {
	if !errors.Is(err, serial.ErrTimeout) && !errors.Is(err, serialproxy.ErrTimeout) {
		c.bus.MarkBroken()
	}
}

// readFrame 读取一个完整 Host Link 响应帧（读到 CR 终止符）。
// 串口读超时由 serial.Config.Timeout 控制（返回 serial.ErrTimeout）。
// 调用方必须持有总线锁。
func (c *finsSerialClient) readFrame() ([]byte, error) {
	buf := make([]byte, 0, 128)
	tmp := make([]byte, 1)
	for {
		n, err := c.bus.Resource().Read(tmp)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			continue
		}
		// 跳过上一帧 CRLF 结尾遗留的 \n/\r（Host Link 帧必以 @ 开头，前导换行非法）
		if len(buf) == 0 && (tmp[0] == '\n' || tmp[0] == '\r') {
			continue
		}
		buf = append(buf, tmp[0])
		if tmp[0] == '\r' {
			return buf, nil
		}
		if len(buf) > 512 {
			return nil, fmt.Errorf("fins serial: response too long (%d bytes)", len(buf))
		}
	}
}

// IsConnected 返回连接状态
func (c *finsSerialClient) IsConnected() bool {
	return c != nil && c.bus != nil && c.bus.Alive()
}

// Close 释放本会话对共享句柄的引用；最后一个会话才真正关闭端口。
func (c *finsSerialClient) Close() error {
	if c == nil || c.bus == nil {
		return nil
	}
	return c.bus.Release()
}
