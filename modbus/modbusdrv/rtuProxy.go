// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package modbusdrv

// 网关代理模式下的 Modbus RTU 收发。
//
// goburrow/modbus 的 RTUClientHandler 自己开串口，无法塞进一个外部字节流；
// 但它把 Packager / Transporter 做成了可替换接口。代理模式因此这样做：
//   - handler **只当 Packager 用**（Encode/Decode/Verify，SlaveId 照常设），
//     从不调用 Connect；
//   - Transporter 自己实现，用 serialproxy 的字节流收发；
//   - 总线锁由 serialbus.Handle 提供（资源上的 Lock/Unlock 会转发到网关的
//     跨进程锁）。
//
// Send 的应答长度计算与时序照抄 goburrow/modbus（BSD-3）的 rtuSerialTransporter，
// 因为它们属于 Modbus RTU 的线协议语义而不是库的内部实现；CRC 校验仍由
// handler 的 Decode 完成。

import (
	"encoding/binary"
	"io"
	"time"

	goburrowModbus "github.com/goburrow/modbus"

	"github.com/Ellisata/iot-gateway-sdk/driver"
	"github.com/Ellisata/iot-gateway-sdk/driversdk/serialbus"
)

const (
	rtuProxyMinSize = 4
	rtuProxyMaxSize = 256
)

// rtuProxyBuses 代理模式下的进程内注册表：同一插件的多个会话仍按端口共享
// 一个 serialproxy.Conn（避免一个进程里重复 serial.open），跨进程互斥由网关
// 侧的总线锁兜底。
var rtuProxyBuses = serialbus.New[io.ReadWriteCloser]()

// rtuProxyBackend 经网关代理的 Modbus RTU 后端。
type rtuProxyBackend struct {
	bus      *serialbus.Handle[io.ReadWriteCloser]
	packager *goburrowModbus.RTUClientHandler
	client   goburrowModbus.Client
}

func newRTUProxyBackend(comPort string, baudRate, dataBits, stopBits int,
	parity string, timeout time.Duration,
	opener driver.SerialTransportOpener) (*rtuProxyBackend, error) {

	key := serialbus.Key{
		Port:     comPort,
		Baud:     baudRate,
		DataBits: dataBits,
		StopBits: stopBits,
		Parity:   parity,
	}
	opts := driver.SerialOpenOptions{
		Port:        comPort,
		BaudRate:    baudRate,
		DataBits:    dataBits,
		StopBits:    stopBits,
		Parity:      parity,
		ReadTimeout: timeout,
	}
	h, err := rtuProxyBuses.Acquire(key, func() (io.ReadWriteCloser, error) {
		return opener(opts)
	})
	if err != nil {
		return nil, err
	}

	// 只作 Packager：不 Connect，绝不打开本机端口。
	packager := goburrowModbus.NewRTUClientHandler(comPort)
	packager.Timeout = timeout
	transporter := &rtuProxyTransporter{bus: h, baud: baudRate}

	return &rtuProxyBackend{
		bus:      h,
		packager: packager,
		client:   goburrowModbus.NewClient2(packager, transporter),
	}, nil
}

func (b *rtuProxyBackend) Lock()   { b.bus.Lock() }
func (b *rtuProxyBackend) Unlock() { b.bus.Unlock() }

func (b *rtuProxyBackend) Client(slave byte) goburrowModbus.Client {
	b.packager.SlaveId = slave // 总线锁内写，见 rtuBackend.Client 的契约
	return b.client
}

func (b *rtuProxyBackend) Alive() bool   { return b.bus.Alive() }
func (b *rtuProxyBackend) MarkBroken()   { b.bus.MarkBroken() }
func (b *rtuProxyBackend) Refs() int     { return b.bus.Refs() }
func (b *rtuProxyBackend) Close() error  { return b.bus.Release() }

// rtuProxyTransporter 用代理字节流实现 modbus.Transporter。
//
// 调用方持有总线锁，因此 Send 内部对 conn 的读写是独占的。
type rtuProxyTransporter struct {
	bus  *serialbus.Handle[io.ReadWriteCloser]
	baud int
}

func (t *rtuProxyTransporter) Send(aduRequest []byte) ([]byte, error) {
	conn := t.bus.Resource()

	if _, err := conn.Write(aduRequest); err != nil {
		return nil, err
	}

	function := aduRequest[1]
	functionFail := aduRequest[1] & 0x80
	bytesToRead := rtuProxyResponseLength(aduRequest)
	time.Sleep(rtuProxyDelay(t.baud, len(aduRequest)+bytesToRead))

	var n int
	data := make([]byte, rtuProxyMaxSize)
	n, err := io.ReadAtLeast(conn, data[:], rtuProxyMinSize)
	if err != nil {
		return nil, err
	}

	if data[1] == function {
		if n < bytesToRead {
			if bytesToRead > rtuProxyMinSize && bytesToRead <= rtuProxyMaxSize {
				var n1 int
				n1, err = io.ReadFull(conn, data[n:bytesToRead])
				n += n1
			}
		}
	} else if data[1] == functionFail {
		if n < 5 {
			var n1 int
			n1, err = io.ReadFull(conn, data[n:5])
			n += n1
		}
	}
	if err != nil {
		return nil, err
	}
	return data[:n], nil
}

// rtuProxyResponseLength 预期的应答长度（含从站号、功能码与 CRC）。
// 照抄 goburrow/modbus 的 calculateResponseLength。
func rtuProxyResponseLength(adu []byte) int {
	length := rtuProxyMinSize
	switch adu[1] {
	case goburrowModbus.FuncCodeReadDiscreteInputs,
		goburrowModbus.FuncCodeReadCoils:
		count := int(binary.BigEndian.Uint16(adu[4:]))
		length += 1 + count/8
		if count%8 != 0 {
			length++
		}
	case goburrowModbus.FuncCodeReadInputRegisters,
		goburrowModbus.FuncCodeReadHoldingRegisters,
		goburrowModbus.FuncCodeReadWriteMultipleRegisters:
		count := int(binary.BigEndian.Uint16(adu[4:]))
		length += 1 + count*2
	case goburrowModbus.FuncCodeWriteSingleCoil,
		goburrowModbus.FuncCodeWriteMultipleCoils,
		goburrowModbus.FuncCodeWriteSingleRegister,
		goburrowModbus.FuncCodeWriteMultipleRegisters:
		length += 4
	case goburrowModbus.FuncCodeMaskWriteRegister:
		length += 6
	case goburrowModbus.FuncCodeReadFIFOQueue:
		// undetermined
	default:
	}
	return length
}

// rtuProxyDelay 帧间等待：与 goburrow/modbus 的 calculateDelay 一致
// （MODBUS over Serial Line 规范第 13 页）。
func rtuProxyDelay(baud int, chars int) time.Duration {
	var characterDelay, frameDelay int
	if baud <= 0 || baud > 19200 {
		characterDelay = 750
		frameDelay = 1750
	} else {
		characterDelay = 15000000 / baud
		frameDelay = 35000000 / baud
	}
	return time.Duration(characterDelay*chars+frameDelay) * time.Microsecond
}
