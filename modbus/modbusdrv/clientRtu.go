// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package modbusdrv

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	goburrowModbus "github.com/goburrow/modbus"
	"github.com/goburrow/serial"

	"github.com/Ellisata/iot-gateway-sdk/driver"
	"github.com/Ellisata/iot-gateway-sdk/driversdk/serialbus"
	"github.com/Ellisata/iot-gateway-sdk/driversdk/serialproxy"
)

// rtuSerialBuses 进程级串口注册表：同一物理端口上的多个会话共享一个
// RTUClientHandler 与一把总线锁（一条 RS-485 上挂多台从站是 Modbus 的常态）。
//
// 共享的是 handler 而不是裸串口：goburrow/modbus 没有把内部串口暴露出来，
// 但它把 Packager/Transporter 做成了可替换接口；handler 自己就是两者，
// 每笔事务在总线锁内改 handler.SlaveId 再发送即可。
var rtuSerialBuses = serialbus.New[*goburrowModbus.RTUClientHandler]()

// rtuBackend 一个会话的底层收发：本地 handler 或网关代理字节流。
//
// 两者的总线锁语义相同（本进程共享 + 代理模式的跨进程锁），驱动其余代码
// 不需要知道当前是哪一种。
type rtuBackend interface {
	Lock()
	Unlock()
	// Client 返回绑定了本事务从站号的 Modbus 客户端。调用方必须持有总线锁。
	Client(slave byte) goburrowModbus.Client
	Alive() bool
	MarkBroken()
	Refs() int
	Close() error
}

// rtuLocalBackend 直接打开本机串口（同进程内多会话共享 handler）。
type rtuLocalBackend struct {
	h *serialbus.Handle[*goburrowModbus.RTUClientHandler]
}

func (b *rtuLocalBackend) Lock()   { b.h.Lock() }
func (b *rtuLocalBackend) Unlock() { b.h.Unlock() }

func (b *rtuLocalBackend) Client(slave byte) goburrowModbus.Client {
	handler := b.h.Resource()
	// SlaveId 在 rtuPackager 里，握手前写定即可；必须在总线锁内写。
	handler.SlaveId = slave
	return goburrowModbus.NewClient(handler)
}

func (b *rtuLocalBackend) Alive() bool  { return b.h.Alive() }
func (b *rtuLocalBackend) MarkBroken()  { b.h.MarkBroken() }
func (b *rtuLocalBackend) Refs() int    { return b.h.Refs() }
func (b *rtuLocalBackend) Close() error { return b.h.Release() }

// ModbusRTUClient Modbus RTU 串口客户端封装
//
// 通过 RS-232 / RS-485 串口与 Modbus RTU 设备通信。
// 每个会话一个实例，同一端口上的多个实例共享底层句柄（本地 handler 或代理流）。
type ModbusRTUClient struct {
	// mu 只保护本会话的可变状态（slaveID 与会话→backend 的绑定），
	// 总线互斥由 backend.Lock 负责。锁序固定为 总线锁 → mu，不要反向。
	mu       sync.RWMutex
	comPort  string
	baudRate int
	dataBits int
	stopBits int
	parity   string
	slaveID  byte
	timeout  time.Duration
	backend  rtuBackend
	// opener 由网关在启用串口代理时注入；Reconnect 沿用它，保证重连后仍是
	// 代理模式（否则会突然自己开一个 fd）。
	opener driver.SerialTransportOpener
}

// NewModbusRTUClient 创建并建立 Modbus RTU 串口连接
//
// 参数说明：
//   - comPort: 串口名称，Windows 下为 "COM1"、"COM2" 等，
//     Linux 下为 "/dev/ttyUSB0"、"/dev/ttyS0" 等
//   - baudRate: 波特率，常见值 9600、19200、38400、57600、115200
//   - dataBits: 数据位，通常为 8
//   - stopBits: 停止位，1 或 2
//   - parity: 校验位，"N"=无、"E"=偶、"O"=奇
//   - slaveID: 从站地址（1-247）
//   - timeout: 通信超时
func NewModbusRTUClient(comPort string, baudRate, dataBits, stopBits int,
	parity string, slaveID byte, timeout time.Duration) (*ModbusRTUClient, error) {
	return newModbusRTUClient(comPort, baudRate, dataBits, stopBits, parity, slaveID, timeout, nil)
}

// newModbusRTUClient 创建客户端；opener 非 nil 时走网关代理。
func newModbusRTUClient(comPort string, baudRate, dataBits, stopBits int,
	parity string, slaveID byte, timeout time.Duration,
	opener driver.SerialTransportOpener) (*ModbusRTUClient, error) {

	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	backend, err := buildRTUBackend(comPort, baudRate, dataBits, stopBits, parity, timeout, opener)
	if err != nil {
		return nil, fmt.Errorf("modbus rtu: %w", err)
	}

	mc := &ModbusRTUClient{
		comPort:  comPort,
		baudRate: baudRate,
		dataBits: dataBits,
		stopBits: stopBits,
		parity:   parity,
		slaveID:  slaveID,
		timeout:  timeout,
		backend:  backend,
		opener:   opener,
	}

	mode := "local"
	if opener != nil {
		mode = "proxy"
	}
	if refs := backend.Refs(); refs > 1 {
		logf("rtu client reusing %s (%s, %s, %d sessions shared, slave=%d)",
			comPort, mode, serialKeyParams(baudRate, dataBits, stopBits, parity), refs, slaveID)
	} else {
		logf("rtu client connected to %s (%s, baud=%d, data=%d, stop=%d, parity=%s, slave=%d)",
			comPort, mode, baudRate, dataBits, stopBits, parity, slaveID)
	}
	return mc, nil
}

// buildRTUBackend 按模式建立本地 handler 或代理字节流。
func buildRTUBackend(comPort string, baudRate, dataBits, stopBits int,
	parity string, timeout time.Duration,
	opener driver.SerialTransportOpener) (rtuBackend, error) {

	if opener != nil {
		return newRTUProxyBackend(comPort, baudRate, dataBits, stopBits, parity, timeout, opener)
	}
	h, err := acquireRTUHandler(comPort, baudRate, dataBits, stopBits, parity, timeout)
	if err != nil {
		return nil, err
	}
	return &rtuLocalBackend{h: h}, nil
}

func serialKeyParams(baud, dataBits, stopBits int, parity string) string {
	return fmt.Sprintf("baud=%d,data=%d,stop=%d,parity=%s", baud, dataBits, stopBits, parity)
}

// acquireRTUHandler 取得该串口对应的共享 handler：能复用就复用，否则打开并登记。
func acquireRTUHandler(comPort string, baudRate, dataBits, stopBits int,
	parity string, timeout time.Duration) (*serialbus.Handle[*goburrowModbus.RTUClientHandler], error) {

	key := serialbus.Key{
		Port:     comPort,
		Baud:     baudRate,
		DataBits: dataBits,
		StopBits: stopBits,
		Parity:   parity,
	}
	return rtuSerialBuses.Acquire(key, func() (*goburrowModbus.RTUClientHandler, error) {
		handler := goburrowModbus.NewRTUClientHandler(comPort)
		handler.BaudRate = baudRate
		handler.DataBits = dataBits
		handler.StopBits = stopBits
		handler.Parity = parity
		handler.Timeout = timeout
		// IdleTimeout 会起一个定时器，在空闲时关闭底层串口。共享句柄时这会在
		// 其它会话毫不知情的情况下把端口关掉（Close 会恢复 termios，打断
		// 仍在使用的读端）—— 端口生命周期只由最后一个 Release 决定，故禁用。
		handler.IdleTimeout = 0

		if err := handler.Connect(); err != nil {
			return nil, fmt.Errorf("connect to %s failed: %w%s",
				comPort, err, driver.SerialOpenFailureHint)
		}
		return handler, nil
	})
}

// SetUnitID 设置从站 ID（RS-485 总线挂多个从站时切换）
func (c *ModbusRTUClient) SetUnitID(unitID byte) {
	c.mu.Lock()
	c.slaveID = unitID
	c.mu.Unlock()
}

// locked 取得总线锁与本会话视角的 Modbus client。
//
// 返回的 client 绑定共享底层；SlaveId 已在总线锁内写成**本会话**的从站号，
// 因此同一端口上的两个从站不会互相串帧。调用方用完必须 unlock。
func (c *ModbusRTUClient) locked() (goburrowModbus.Client, func(), error) {
	c.mu.RLock()
	b := c.backend
	slave := c.slaveID
	c.mu.RUnlock()

	if b == nil {
		return nil, nil, fmt.Errorf("modbus rtu: serial connection is closed")
	}
	b.Lock()
	if !b.Alive() {
		b.Unlock()
		return nil, nil, fmt.Errorf("modbus rtu: serial connection is closed")
	}
	return b.Client(slave), b.Unlock, nil
}

// markBroken 在链路上出现真正的 I/O 错误时判死共享句柄。
//
// 读超时不算：它只说明这一次没有应答（从站离线、站号写错）。共享句柄一旦判死，
// 同一条总线上的**所有**从站都要重连 —— 把"一个从站掉线"放大成"整条总线反复
// 重连"正是本次要修的问题。协议异常（从站应答但拒绝）同理不算。
func (c *ModbusRTUClient) markBroken(err error) {
	if isProtocolErr(err) || errors.Is(err, serial.ErrTimeout) || errors.Is(err, serialproxy.ErrTimeout) {
		return
	}
	c.mu.RLock()
	b := c.backend
	c.mu.RUnlock()
	if b != nil {
		b.MarkBroken()
	}
}

// ReadHoldingRegisters 读取保持寄存器（功能码 03）
func (c *ModbusRTUClient) ReadHoldingRegisters(address, quantity uint16) ([]byte, error) {
	client, unlock, err := c.locked()
	if err != nil {
		return nil, err
	}
	defer unlock()

	results, err := client.ReadHoldingRegisters(address, quantity)
	if err != nil {
		c.markBroken(err)
		return nil, fmt.Errorf("modbus rtu: read holding registers at %d len %d failed: %w",
			address, quantity, err)
	}
	return results, nil
}

// ReadInputRegisters 读取输入寄存器（功能码 04）
func (c *ModbusRTUClient) ReadInputRegisters(address, quantity uint16) ([]byte, error) {
	client, unlock, err := c.locked()
	if err != nil {
		return nil, err
	}
	defer unlock()

	results, err := client.ReadInputRegisters(address, quantity)
	if err != nil {
		c.markBroken(err)
		return nil, fmt.Errorf("modbus rtu: read input registers at %d len %d failed: %w",
			address, quantity, err)
	}
	return results, nil
}

// ReadCoils 读取线圈（功能码 01）
func (c *ModbusRTUClient) ReadCoils(address, quantity uint16) ([]byte, error) {
	client, unlock, err := c.locked()
	if err != nil {
		return nil, err
	}
	defer unlock()

	results, err := client.ReadCoils(address, quantity)
	if err != nil {
		c.markBroken(err)
		return nil, fmt.Errorf("modbus rtu: read coils at %d len %d failed: %w", address, quantity, err)
	}
	return results, nil
}

// ReadDiscreteInputs 读取离散输入（功能码 02）
func (c *ModbusRTUClient) ReadDiscreteInputs(address, quantity uint16) ([]byte, error) {
	client, unlock, err := c.locked()
	if err != nil {
		return nil, err
	}
	defer unlock()

	results, err := client.ReadDiscreteInputs(address, quantity)
	if err != nil {
		c.markBroken(err)
		return nil, fmt.Errorf("modbus rtu: read discrete inputs at %d len %d failed: %w", address, quantity, err)
	}
	return results, nil
}

// WriteSingleCoil 写单个线圈（功能码 05）。
// 串口是独占的，收发全程持写锁，避免与采集轮询在总线上交错。
func (c *ModbusRTUClient) WriteSingleCoil(address uint16, value bool) error {
	client, unlock, err := c.locked()
	if err != nil {
		return err
	}
	defer unlock()

	var raw uint16
	if value {
		raw = 0xFF00
	}
	if _, err := client.WriteSingleCoil(address, raw); err != nil {
		c.markBroken(err)
		return fmt.Errorf("modbus rtu: write single coil at %d failed: %w", address, err)
	}
	return nil
}

// WriteSingleRegister 写单个保持寄存器（功能码 06）。
func (c *ModbusRTUClient) WriteSingleRegister(address, value uint16) error {
	client, unlock, err := c.locked()
	if err != nil {
		return err
	}
	defer unlock()

	if _, err := client.WriteSingleRegister(address, value); err != nil {
		c.markBroken(err)
		return fmt.Errorf("modbus rtu: write single register at %d failed: %w", address, err)
	}
	return nil
}

// WriteMultipleRegisters 写多个连续保持寄存器（功能码 16）。
func (c *ModbusRTUClient) WriteMultipleRegisters(address uint16, values []byte) error {
	if len(values) == 0 || len(values)%2 != 0 {
		return fmt.Errorf("modbus rtu: write multiple registers at %d: invalid byte length %d", address, len(values))
	}

	client, unlock, err := c.locked()
	if err != nil {
		return err
	}
	defer unlock()

	if _, err := client.WriteMultipleRegisters(address, uint16(len(values)/2), values); err != nil {
		c.markBroken(err)
		return fmt.Errorf("modbus rtu: write multiple registers at %d len %d failed: %w",
			address, len(values)/2, err)
	}
	return nil
}

// Ping 通过当前已建立的串口连接发送一次读取请求验证设备可达。
// 与常规读取不同，失败时不会将连接标记为断开，避免对采集引擎的既有状态产生副作用。
func (c *ModbusRTUClient) Ping() error {
	client, unlock, err := c.locked()
	if err != nil {
		return err
	}
	defer unlock()

	if _, err := client.ReadHoldingRegisters(0, 1); err != nil {
		return fmt.Errorf("modbus rtu: ping read failed: %w", err)
	}
	return nil
}

// IsConnected 返回当前连接状态
func (c *ModbusRTUClient) IsConnected() bool {
	c.mu.RLock()
	b := c.backend
	c.mu.RUnlock()
	return b != nil && b.Alive()
}

// matchConfig 报告本客户端的连接能否直接服务 cfg 描述的那条链路。
//
// 必须逐项比全，不能只比端口号：
//   - **从站号**：Ping 用的是 handler 里既有的 SlaveId，复用一条从站地址不同的
//     连接会把"测试 2 号站"的请求发到总线上的 1 号站 —— 1 号站正常应答，
//     测试连接报成功，而用户想验的 2 号站根本没被问到。代价是"同一条 485 总线
//     上测第二块从站"会被拒（退回临时实例，而临时实例在端口被占用时会失败），
//     这是正确的拒绝：不能拿一号站的应答冒充二号站。
//   - **波特率/数据位/停止位/校验位**：帧参数不同的连接不是同一条链路，
//     复用它得到的是与被测配置无关的结论。
//
// 端口名用 EqualFold：Windows 的串口名大小写不敏感，"com1" 与 "COM1" 是同一个
// 端口，用 == 比会把本可复用的情况误判成不可复用，退回临时实例后撞 Access is denied。
func (c *ModbusRTUClient) matchConfig(cfg *ModbusRTUConfig) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.backend != nil && c.backend.Alive() &&
		strings.EqualFold(c.comPort, cfg.ComPort) &&
		c.baudRate == cfg.BaudRate &&
		c.dataBits == cfg.DataBits &&
		c.stopBits == cfg.StopBits &&
		strings.EqualFold(c.parity, cfg.Parity) &&
		c.slaveID == cfg.UnitID
}

// Reconnect 关闭旧连接并重新建立串口连接
//
// 共享句柄下这是"把这条总线的连接换新"：先判死并释放本会话的引用（其它会话
// 会在下一次连接时跟着换到新句柄），再按本会话的参数重新建立。代理模式沿用
// 注入的 opener，重连后仍然是宿主字节流。
func (c *ModbusRTUClient) Reconnect() error {
	c.mu.RLock()
	old := c.backend
	comPort, baud, dataBits, stopBits := c.comPort, c.baudRate, c.dataBits, c.stopBits
	parity, timeout, opener := c.parity, c.timeout, c.opener
	c.mu.RUnlock()

	if old != nil {
		old.MarkBroken()
		_ = old.Close()
	}

	backend, err := buildRTUBackend(comPort, baud, dataBits, stopBits, parity, timeout, opener)
	if err != nil {
		c.mu.Lock()
		c.backend = nil
		c.mu.Unlock()
		return fmt.Errorf("modbus rtu: reconnect to %s failed: %w", comPort, err)
	}

	c.mu.Lock()
	c.backend = backend
	c.mu.Unlock()

	logf("rtu client reconnected to %s", comPort)
	return nil
}

// Close 关闭 Modbus RTU 串口连接
func (c *ModbusRTUClient) Close() error {
	c.mu.Lock()
	b := c.backend
	c.backend = nil
	c.mu.Unlock()

	if b != nil {
		return b.Close()
	}
	logf("rtu client closed: %s", c.comPort)
	return nil
}
