// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package modbusdrv

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"

	goburrowModbus "github.com/goburrow/modbus"

	"github.com/Ellisata/iot-gateway-sdk/driver"
)

// 写路径（控制下发）。
//
// 与读路径刻意分开：读按区间规划、批量取数；写是逐点的控制动作，且必须把
// 每个点的结果单独回传（批量写不是事务，一个点被设备拒绝不应影响其它点）。
// 复用同一份地址解析、数据类型表与 byte/word order 约定，保证"读回来的值"
// 与"写下去的值"使用同一套编码。
//
// 只允许写线圈（FC1/0x）与保持寄存器（FC3/4x）。离散输入（FC2）与输入寄存器
// （FC4）是只读区，即使设备不报错也不应下发。

// modbusWriteTransport 写操作的最小传输接口，TCP/RTU 客户端各实现一份。
type modbusWriteTransport interface {
	WriteSingleCoil(address uint16, value bool) error
	WriteSingleRegister(address uint16, value uint16) error
	WriteMultipleRegisters(address uint16, values []byte) error
}

var (
	_ driver.Writer = (*modbusTCPDriver)(nil)
	_ driver.Writer = (*modbusRTUDriver)(nil)
)

// Write 实现 driver.Writer（TCP）。
func (d *modbusTCPDriver) Write(reqs []driver.WriteRequest) ([]driver.WriteResult, error) {
	d.mu.RLock()
	client := d.client
	cfg := d.config
	d.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		return nil, fmt.Errorf("modbus: not connected")
	}
	client.SetUnitID(cfg.UnitID)
	return writeBatch("modbus", client, reqs, cfg)
}

// Write 实现 driver.Writer（RTU）。
//
// RTU 的独占性由网关设备锁与插件会话锁保证；客户端自身也对每次收发持锁，
// 因此这里不需要额外加锁即可与 Read 串行。
func (d *modbusRTUDriver) Write(reqs []driver.WriteRequest) ([]driver.WriteResult, error) {
	d.mu.RLock()
	client := d.client
	cfg := d.config
	d.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		return nil, fmt.Errorf("modbus rtu: not connected")
	}
	sharedCfg := sharedTCPConfig(cfg)
	client.SetUnitID(cfg.UnitID)
	return writeBatch("modbus rtu", client, reqs, sharedCfg)
}

// writeBatch 逐点写入并返回与 reqs 等长的结果。
//
// 返回 error 只表示**链路级**失败（连接断开、超时、帧损坏）。与此前已处理的
// 点对应的 WriteResult 仍会返回；失败点自身标 WriteCodeTransport。调用方必须
// 把整批视为"状态未知"，通过回读确认，而不是假定未处理的点没有生效。
func writeBatch(prefix string, w modbusWriteTransport, reqs []driver.WriteRequest, cfg *ModbusTcpConfig) ([]driver.WriteResult, error) {
	results := make([]driver.WriteResult, len(reqs))
	for i := range reqs {
		res, transportErr := writeOne(prefix, w, reqs[i], cfg)
		results[i] = res
		if transportErr != nil {
			return results, transportErr
		}
	}
	return results, nil
}

// writeOne 写入一个点位。协议层否定应答进 WriteResult，链路错误单独返回。
func writeOne(prefix string, w modbusWriteTransport, req driver.WriteRequest, cfg *ModbusTcpConfig) (driver.WriteResult, error) {
	res := driver.WriteResult{DeviceAddressID: req.DeviceAddressID}

	coil, addr, code, msg := resolveWriteTarget(req.Name, cfg)
	if code != driver.WriteCodeOK {
		res.Code, res.Message = code, msg
		return res, nil
	}

	dt, ok := lookupType(req.DataType)
	if !ok {
		res.Code = driver.WriteCodeBadValue
		res.Message = fmt.Sprintf("unsupported data type %q", req.DataType)
		return res, nil
	}

	if coil {
		if dt.Kind != driver.KindBool {
			res.Code = driver.WriteCodeBadValue
			res.Message = fmt.Sprintf("coil %q requires bool data type, got %q", req.Name, req.DataType)
			return res, nil
		}
		v, err := parseBoolValue(req.Value)
		if err != nil {
			res.Code, res.Message = driver.WriteCodeBadValue, err.Error()
			return res, nil
		}
		if err := w.WriteSingleCoil(addr, v); err != nil {
			return writeFailure(prefix, req, err)
		}
		res.Code = driver.WriteCodeOK
		res.Value = formatBool(v)
		return res, nil
	}

	if dt.Encode == nil {
		res.Code = driver.WriteCodeBadValue
		res.Message = fmt.Sprintf("data type %q does not support writing", req.DataType)
		return res, nil
	}
	val, err := parseWriteValue(dt, req.Value)
	if err != nil {
		res.Code = driver.WriteCodeBadValue
		res.Message = fmt.Sprintf("invalid %s value %q: %v", req.DataType, req.Value, err)
		return res, nil
	}
	raw, err := dt.Encode(val, parseByteOrder(cfg.ByteOrder))
	if err != nil {
		res.Code = driver.WriteCodeBadValue
		res.Message = fmt.Sprintf("encode %s value %q: %v", req.DataType, req.Value, err)
		return res, nil
	}
	raw = applyWriteWordOrder(raw, dt.Size, cfg.WordOrder)
	raw = padRegisterBytes(raw, cfg.ByteOrder)
	if len(raw) < 2 || len(raw)%2 != 0 {
		res.Code = driver.WriteCodeBadValue
		res.Message = fmt.Sprintf("encoded %s value has invalid register length %d", req.DataType, len(raw))
		return res, nil
	}

	if len(raw) == 2 {
		err = w.WriteSingleRegister(addr, binary.BigEndian.Uint16(raw))
	} else {
		err = w.WriteMultipleRegisters(addr, raw)
	}
	if err != nil {
		return writeFailure(prefix, req, err)
	}
	res.Code = driver.WriteCodeOK
	if dt.Format != nil {
		res.Value = dt.Format(val)
	} else {
		res.Value = fmt.Sprint(val)
	}
	return res, nil
}

// writeFailure 把传输层错误分成"设备拒绝"与"链路失败"两类。
func writeFailure(prefix string, req driver.WriteRequest, err error) (driver.WriteResult, error) {
	res := driver.WriteResult{DeviceAddressID: req.DeviceAddressID}
	if isProtocolErr(err) {
		res.Code = classifyWriteProtocolError(err)
		res.Message = err.Error()
		return res, nil
	}
	// 链路级失败：状态未知，调用方必须回读确认。
	res.Code = driver.WriteCodeTransport
	res.Message = err.Error()
	return res, fmt.Errorf("%s: write %q failed: %w", prefix, req.Name, err)
}

// classifyWriteProtocolError 把 Modbus 异常码映射到写结果码。
func classifyWriteProtocolError(err error) int {
	var me *goburrowModbus.ModbusError
	if !errors.As(err, &me) {
		return driver.WriteCodeRejected
	}
	switch me.ExceptionCode {
	case goburrowModbus.ExceptionCodeIllegalDataAddress:
		return driver.WriteCodeBadAddress
	case goburrowModbus.ExceptionCodeIllegalDataValue:
		return driver.WriteCodeBadValue
	default:
		return driver.WriteCodeRejected
	}
}

// resolveWriteTarget 解析点位地址并判定它属于线圈还是保持寄存器。
//
// 规则与读路径的 groupAddrsByFC 一致：
//   - 配置模式（startAddress > 0）：全区都归 cfg.FunctionCode，name 只用于定位；
//   - 自动模式：按 name 解析出的功能码分组，解析不出（FC=0）时回退 cfg.FunctionCode。
//
// 只有 FC1（线圈）与 FC3（保持寄存器）可写；FC2/FC4 明确报 NotWritable。
func resolveWriteTarget(name string, cfg *ModbusTcpConfig) (coil bool, addr uint16, code int, msg string) {
	parsedFC, addr, ok := ParsePLCAddress(name)
	if !ok {
		return false, 0, driver.WriteCodeBadAddress, fmt.Sprintf("invalid address %q", name)
	}

	fc := parsedFC
	if cfg.StartAddress > 0 {
		if addr < cfg.StartAddress {
			return false, 0, driver.WriteCodeBadAddress,
				fmt.Sprintf("address %q reg=%d < startAddress=%d", name, addr, cfg.StartAddress)
		}
		fc = cfg.FunctionCode
	} else if fc == 0 {
		fc = cfg.FunctionCode
	}

	switch fc {
	case FuncCodeReadCoils:
		return true, addr, driver.WriteCodeOK, ""
	case FuncCodeReadHoldingRegisters:
		return false, addr, driver.WriteCodeOK, ""
	case FuncCodeReadDiscreteInputs, FuncCodeReadInputRegisters:
		return false, 0, driver.WriteCodeNotWritable,
			fmt.Sprintf("address %q is in a read-only area (function code %d)", name, fc)
	default:
		return false, 0, driver.WriteCodeBadAddress,
			fmt.Sprintf("address %q has unsupported function code %d", name, fc)
	}
}

// parseWriteValue 把网关下发的字符串按数据类型类别解析成编码函数能接受的值。
//
// 不使用 json.Unmarshal：JSON 数字一律变 float64，int64/uint64 的高位会丢精度；
// 控制值必须按目标类型精确解析。
func parseWriteValue(dt *driver.DataType, s string) (any, error) {
	s = strings.TrimSpace(s)
	if s == "" && dt.Kind != driver.KindString {
		return nil, fmt.Errorf("empty value")
	}
	switch dt.Kind {
	case driver.KindBool:
		return parseBoolValue(s)
	case driver.KindInt:
		return strconv.ParseInt(s, 0, 64)
	case driver.KindUInt:
		return strconv.ParseUint(s, 0, 64)
	case driver.KindFloat:
		return strconv.ParseFloat(s, 64)
	case driver.KindString:
		return s, nil
	case driver.KindTime:
		return strconv.ParseInt(s, 10, 64)
	default:
		return nil, fmt.Errorf("unsupported kind %q", dt.Kind)
	}
}

// parseBoolValue 接受现场常见的布尔写法。
func parseBoolValue(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "on", "yes":
		return true, nil
	case "0", "false", "off", "no":
		return false, nil
	default:
		return false, fmt.Errorf("invalid bool value %q", s)
	}
}

// applyWriteWordOrder 是读路径 swapWords32/64 的逆操作：
// 读时先换字再交给 Decode，写时先 Encode 再换字，保证同一组配置读写的字节序一致。
func applyWriteWordOrder(raw []byte, size int, wordOrder string) []byte {
	if !strings.EqualFold(wordOrder, WordOrderLittleEndian) {
		return raw
	}
	switch size {
	case 4:
		return swapWords32(raw, true)
	case 8:
		return swapWords64(raw, true)
	default:
		return raw
	}
}

// padRegisterBytes 把 1 字节类型（uint8/int8）补齐到一个 16 位寄存器。
// 值放在低字节；按配置的字节序决定它是第一个还是第二个字节。
func padRegisterBytes(raw []byte, byteOrder string) []byte {
	if len(raw)%2 == 0 {
		return raw
	}
	if strings.EqualFold(byteOrder, ByteOrderLittleEndian) {
		return append(raw, 0)
	}
	return append([]byte{0}, raw...)
}
