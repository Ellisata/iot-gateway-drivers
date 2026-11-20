// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package modbusdrv

import (
	"fmt"
	"io"
	"testing"
	"time"

	goburrowModbus "github.com/goburrow/modbus"

	"github.com/Ellisata/iot-gateway-sdk/driver"
	"github.com/Ellisata/iot-gateway-sdk/driversdk/serialbus"
	"github.com/Ellisata/iot-gateway-sdk/driversdk/serialproxy"
)

// newTestRTUClient 构造一个不经真实串口的客户端，供复用判据等单测使用。
// 每个实例用独立的 Bus，避免用例之间互相影响。
func newTestRTUClient(comPort string, baud int, slave byte, alive bool) *ModbusRTUClient {
	bus := serialbus.New[*goburrowModbus.RTUClientHandler]()
	h, err := bus.Acquire(
		serialbus.Key{Port: comPort, Baud: baud, DataBits: 8, StopBits: 1, Parity: "N"},
		func() (*goburrowModbus.RTUClientHandler, error) {
			return goburrowModbus.NewRTUClientHandler(comPort), nil
		},
	)
	if err != nil {
		panic("构造测试 RTU 客户端失败: " + err.Error())
	}
	if !alive {
		h.MarkBroken()
	}
	return &ModbusRTUClient{
		comPort: comPort, baudRate: baud, dataBits: 8, stopBits: 1,
		parity: "N", slaveID: slave, backend: &rtuLocalBackend{h: h},
	}
}

// TestRTUSharedHandlerSlaveSwitching 同一端口上的两个会话共享 handler，
// 但每一笔事务必须携带**本会话**的从站号 —— 这是共用一个 handler 的全部
// 风险所在：串帧会把 2 号站的应答当 1 号站的。
func TestRTUSharedHandlerSlaveSwitching(t *testing.T) {
	bus := serialbus.New[*goburrowModbus.RTUClientHandler]()
	h, err := bus.Acquire(
		serialbus.Key{Port: "COM42", Baud: 9600, DataBits: 8, StopBits: 1, Parity: "N"},
		func() (*goburrowModbus.RTUClientHandler, error) {
			return goburrowModbus.NewRTUClientHandler("COM42"), nil
		},
	)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	defer h.Release()

	a := &ModbusRTUClient{backend: &rtuLocalBackend{h: h}, slaveID: 1}
	b := &ModbusRTUClient{backend: &rtuLocalBackend{h: h}, slaveID: 2}

	_, unlockA, err := a.locked()
	if err != nil {
		t.Fatalf("会话 A 取锁失败: %v", err)
	}
	if got := h.Resource().SlaveId; got != 1 {
		t.Errorf("会话 A 的事务里 SlaveId = %d, want 1", got)
	}
	unlockA()

	_, unlockB, err := b.locked()
	if err != nil {
		t.Fatalf("会话 B 取锁失败: %v", err)
	}
	if got := h.Resource().SlaveId; got != 2 {
		t.Errorf("会话 B 的事务里 SlaveId = %d, want 2（上一会话的从站号串过来了）", got)
	}
	unlockB()
}

// TestRTUDeadHandleNotConnected 判死的共享句柄必须让所有会话都报未连接，
// 驱动才会走重连（Acquire 换新句柄）。
func TestRTUDeadHandleNotConnected(t *testing.T) {
	c := newTestRTUClient("COM43", 9600, 1, true)
	if !c.IsConnected() {
		t.Fatal("新建客户端应当已连接")
	}
	c.backend.MarkBroken()
	if c.IsConnected() {
		t.Fatal("判死后仍报告已连接 —— 会对着一个死句柄收发")
	}
	if _, _, err := c.locked(); err == nil {
		t.Fatal("判死的句柄仍允许发起事务")
	}
}

// rtuFakeConn 代理字节流替身：Write 记录请求，Read 交出预置应答。
type rtuFakeConn struct {
	req      []byte
	resp     []byte
	locked   bool
	unlocked bool
	closed   bool
}

func (c *rtuFakeConn) Read(b []byte) (int, error) {
	if len(c.resp) == 0 {
		return 0, serialproxy.ErrTimeout
	}
	n := copy(b, c.resp)
	c.resp = c.resp[n:]
	return n, nil
}

func (c *rtuFakeConn) Write(b []byte) (int, error) {
	c.req = append([]byte(nil), b...)
	return len(b), nil
}

func (c *rtuFakeConn) Close() error { c.closed = true; return nil }
func (c *rtuFakeConn) Lock()        { c.locked = true }
func (c *rtuFakeConn) Unlock()      { c.unlocked = true }

// modbusCRC 计算 Modbus RTU 的 CRC16（低字节在前）。
func modbusCRC(data []byte) (lo, hi byte) {
	var crc uint16 = 0xFFFF
	for _, b := range data {
		crc ^= uint16(b)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return byte(crc), byte(crc >> 8)
}

// 代理模式下，Modbus RTU 必须用自定义 Transporter 走宿主字节流：
// 请求按本会话的从站号发出、应答按 CRC 校验解出、总线锁转发到代理。
func TestRTUProxyReadHoldingRegisters(t *testing.T) {
	conn := &rtuFakeConn{}
	resp := []byte{0x01, 0x03, 0x02, 0x12, 0x34}
	lo, hi := modbusCRC(resp)
	conn.resp = append(resp, lo, hi)

	opens := 0
	opener := func(driver.SerialOpenOptions) (io.ReadWriteCloser, error) {
		opens++
		return conn, nil
	}
	port := fmt.Sprintf("COM%d", 700+int(time.Now().UnixNano()%100))
	c, err := newModbusRTUClient(port, 9600, 8, 1, "N", 1, 200*time.Millisecond, opener)
	if err != nil {
		t.Fatalf("代理模式建连失败: %v", err)
	}
	defer c.Close()

	got, err := c.ReadHoldingRegisters(0, 1)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(got) != 2 || got[0] != 0x12 || got[1] != 0x34 {
		t.Fatalf("解出的寄存器 = % X", got)
	}
	if len(conn.req) < 2 || conn.req[0] != 1 || conn.req[1] != 3 {
		t.Fatalf("发出的请求 = % X（从站号或功能码不对）", conn.req)
	}
	if !conn.locked || !conn.unlocked {
		t.Errorf("总线锁没有转发到代理资源（locked=%v unlocked=%v）", conn.locked, conn.unlocked)
	}

	// 同端口第二个会话共享同一个代理字节流，不重复 serial.open。
	c2, err := newModbusRTUClient(port, 9600, 8, 1, "N", 2, 200*time.Millisecond, opener)
	if err != nil {
		t.Fatalf("第二个会话建连失败: %v", err)
	}
	defer c2.Close()
	if opens != 1 {
		t.Errorf("同端口被 opener 打开 %d 次，want 1", opens)
	}
}
