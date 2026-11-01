// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package mitsubishi

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/Ellisata/iot-gateway-drivers/internal/writeval"
	"github.com/Ellisata/iot-gateway-sdk/driver"
)

// 写路径（控制下发）。
//
// 与读路径刻意分开：读按区间规划、批量取数；写是逐点的控制动作，且必须把
// 每个点的结果单独回传。复用同一份地址解析、数据类型表与编码函数，保证
// "读回来的值"与"写下去的值"使用同一套编码（MC 全程小端）。
//
// 可写软元件：
//   - 位设备（M/Y/X/L/B/…）的 bool 点位：位单位批量写（子命令 0001），
//     nibble 打包（前一点在高半字节）；
//   - 字设备（D/W/R/ZR/…）的常规类型：字单位批量写；
//   - 字设备的 bool 点位（D100 或 D100.5）：读-改-写所在字；
//   - 位设备的非 bool 类型：按 16 位对齐后做字访问（与读路径一致）。

// mcReadWriter 同时具备读、写能力的传输层。字设备 bool 点位需要读-改-写，
// 只有写能力的桩不足以完成这类点位。
type mcReadWriter interface {
	mcWriter
	Read(device mcDevice, head uint32, points uint16, bitMode bool) ([]byte, error)
}

var (
	_ driver.Writer = (*mcDriver)(nil)
	_ mcWriter      = (*mcTCPClient)(nil)
	_ mcReadWriter  = (*mcTCPClient)(nil)
	_ mcWriter      = (*mcSerialClient)(nil)
	_ mcReadWriter  = (*mcSerialClient)(nil)
)

// Write 实现 driver.Writer。
func (d *mcDriver) Write(reqs []driver.WriteRequest) ([]driver.WriteResult, error) {
	d.mu.RLock()
	client := d.client
	d.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		return nil, fmt.Errorf("mc: not connected")
	}
	w, ok := client.(mcWriter)
	if !ok {
		return nil, fmt.Errorf("mc: transport %T does not support writing", client)
	}
	return writeMCBatch(w, reqs)
}

// writeMCBatch 逐点写入并返回与 reqs 等长的结果。
//
// 返回 error 只表示链路级失败（连接断开/超时）。已处理的点仍带结果返回；
// 调用方必须把整批视为"状态未知"，通过回读确认。
func writeMCBatch(w mcWriter, reqs []driver.WriteRequest) ([]driver.WriteResult, error) {
	results := make([]driver.WriteResult, len(reqs))
	for i := range reqs {
		res, fatal := writeMCOne(w, reqs[i])
		results[i] = res
		if fatal != nil {
			return results, fatal
		}
	}
	return results, nil
}

// writeMCOne 写入一个点位。结束码错误进 WriteResult，链路错误单独返回。
func writeMCOne(w mcWriter, req driver.WriteRequest) (driver.WriteResult, error) {
	res := driver.WriteResult{DeviceAddressID: req.DeviceAddressID}

	addr, ok := ParseMCAddress(req.Name)
	if !ok {
		res.Code = driver.WriteCodeBadAddress
		res.Message = fmt.Sprintf("invalid address %q", req.Name)
		return res, nil
	}
	dt, ok := mcLookup(req.DataType)
	if !ok {
		res.Code = driver.WriteCodeBadValue
		res.Message = fmt.Sprintf("unsupported data type %q", req.DataType)
		return res, nil
	}

	isBitDevice := addr.Device.isBit
	isBool := dt.Kind == driver.KindBool

	switch {
	case isBitDevice && isBool:
		v, err := writeval.Parse(dt, req.Value)
		if err != nil {
			res.Code, res.Message = driver.WriteCodeBadValue, err.Error()
			return res, nil
		}
		b := v.(bool)
		// 位单位写：前一个点在高半字节，单点写在 0x10/0x00。
		data := []byte{0x00}
		if b {
			data[0] = 0x10
		}
		if err := w.Write(addr.Device, addr.Number, 1, true, data); err != nil {
			return mcWriteFailure(req, err)
		}
		res.Code = driver.WriteCodeOK
		res.Value = writeval.Format(dt, b)
		return res, nil

	case !isBitDevice && isBool:
		v, err := writeval.Parse(dt, req.Value)
		if err != nil {
			res.Code, res.Message = driver.WriteCodeBadValue, err.Error()
			return res, nil
		}
		b := v.(bool)
		rw, ok := w.(mcReadWriter)
		if !ok {
			res.Code = driver.WriteCodeUnsupported
			res.Message = "transport cannot read the containing word for a bool write"
			return res, nil
		}
		raw, err := rw.Read(addr.Device, addr.Number, 1, false)
		if err != nil {
			return mcWriteFailure(req, err)
		}
		if len(raw) < 2 {
			res.Code = driver.WriteCodeTransport
			res.Message = fmt.Sprintf("short word read (%d bytes)", len(raw))
			return res, fmt.Errorf("mc: write %q: short word read (%d bytes)", req.Name, len(raw))
		}
		bit := addr.Bit
		if bit < 0 {
			// 与读路径一致：无 .bit 后缀的 bool 默认取 bit 0。
			bit = 0
		}
		word := binary.LittleEndian.Uint16(raw[:2])
		mask := uint16(1) << uint(bit)
		if b {
			word |= mask
		} else {
			word &^= mask
		}
		data := make([]byte, 2)
		binary.LittleEndian.PutUint16(data, word)
		if err := w.Write(addr.Device, addr.Number, 1, false, data); err != nil {
			return mcWriteFailure(req, err)
		}
		res.Code = driver.WriteCodeOK
		res.Value = writeval.Format(dt, b)
		return res, nil
	}

	// 常规字单位写。
	if dt.Encode == nil || dt.Size <= 0 {
		res.Code = driver.WriteCodeBadValue
		res.Message = fmt.Sprintf("data type %q does not support writing", req.DataType)
		return res, nil
	}
	val, err := writeval.Parse(dt, req.Value)
	if err != nil {
		res.Code, res.Message = driver.WriteCodeBadValue, err.Error()
		return res, nil
	}
	raw, err := dt.Encode(val, binary.LittleEndian)
	if err != nil {
		res.Code = driver.WriteCodeBadValue
		res.Message = fmt.Sprintf("encode %s value %q: %v", req.DataType, req.Value, err)
		return res, nil
	}
	raw = padMCWord(raw)
	if len(raw) < 2 || len(raw)%2 != 0 {
		res.Code = driver.WriteCodeBadValue
		res.Message = fmt.Sprintf("encoded %s value has invalid word length %d", req.DataType, len(raw))
		return res, nil
	}
	points := len(raw) / 2
	if points > maxReadWordsLimit {
		res.Code = driver.WriteCodeBadValue
		res.Message = fmt.Sprintf("%s value spans %d words, exceeds limit %d", req.DataType, points, maxReadWordsLimit)
		return res, nil
	}

	head := addr.Number
	if isBitDevice {
		// 位设备的字访问：与读路径一样按 16 位对齐。
		head = (addr.Number / 16) * 16
	}
	if err := w.Write(addr.Device, head, uint16(points), false, raw); err != nil {
		return mcWriteFailure(req, err)
	}
	res.Code = driver.WriteCodeOK
	res.Value = writeval.Format(dt, val)
	return res, nil
}

// padMCWord 把 1 字节类型补齐为一个 16 位字：MC 字节数据存放在字的低字节
// （低字节在前）。2/4/8 字节类型原样返回。
func padMCWord(raw []byte) []byte {
	if len(raw) == 0 || len(raw)%2 == 0 {
		return raw
	}
	return append(raw, 0)
}

// mcWriteFailure 把传输层错误分成"设备拒绝"与"链路失败"两类。
func mcWriteFailure(req driver.WriteRequest, err error) (driver.WriteResult, error) {
	res := driver.WriteResult{DeviceAddressID: req.DeviceAddressID}
	if IsEndCodeError(err) {
		res.Code = classifyMCEndCode(err)
		res.Message = err.Error()
		return res, nil
	}
	res.Code = driver.WriteCodeTransport
	res.Message = err.Error()
	return res, fmt.Errorf("mc: write %q failed: %w", req.Name, err)
}

// classifyMCEndCode 把结束码映射到写结果码。
func classifyMCEndCode(err error) int {
	var e *mcEndCodeError
	if !errors.As(err, &e) {
		return driver.WriteCodeRejected
	}
	switch e.code {
	case endCodeAddrRange, endCodeDeviceIllegal, endCodeDeviceRange:
		return driver.WriteCodeBadAddress
	case endCodePointsError:
		return driver.WriteCodeBadValue
	default:
		return driver.WriteCodeRejected
	}
}
