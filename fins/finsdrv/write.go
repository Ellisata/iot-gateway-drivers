// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package fins

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Ellisata/iot-gateway-drivers/internal/writeval"
	"github.com/Ellisata/iot-gateway-sdk/driver"
)

// 写路径（控制下发）。
//
// 与读路径刻意分开：读按区间规划、批量取数；写是逐点的控制动作，且必须把
// 每个点的结果单独回传。复用同一份地址解析、数据类型表与字节序/字序约定，
// 保证"读回来的值"与"写下去的值"使用同一套编码。
//
// 可写内存区：CIO / WR / HR / DM（与读路径相同的四个字区）。
// bool 点位带 .bit 后缀时用 FINS 的位写入（命令体携带位号）；不带后缀时
// 写入所在字的低字节（与读路径从字中取低字节的约定一致）。

var (
	_ driver.Writer  = (*finsDriver)(nil)
	_ finsAreaWriter = (*finsTCPClient)(nil)
	_ finsAreaWriter = (*finsUDPClient)(nil)
	_ finsAreaWriter = (*finsSerialClient)(nil)
	_ finsAreaWriter = (*finsHostLinkTCPClient)(nil)
)

// Write 实现 driver.Writer。
func (d *finsDriver) Write(reqs []driver.WriteRequest) ([]driver.WriteResult, error) {
	d.mu.RLock()
	client := d.client
	cfg := d.config
	d.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		return nil, fmt.Errorf("fins: not connected")
	}
	w, ok := client.(finsAreaWriter)
	if !ok {
		return nil, fmt.Errorf("fins: transport %T does not support writing", client)
	}
	return writeFINSBatch(w, reqs, cfg)
}

// writeFINSBatch 逐点写入并返回与 reqs 等长的结果。
//
// 返回 error 只表示链路级失败（连接断开/超时）。已处理的点仍带结果返回；
// 调用方必须把整批视为"状态未知"，通过回读确认。
func writeFINSBatch(w finsAreaWriter, reqs []driver.WriteRequest, cfg *FINSConfig) ([]driver.WriteResult, error) {
	results := make([]driver.WriteResult, len(reqs))
	for i := range reqs {
		res, fatal := writeFINSOne(w, reqs[i], cfg)
		results[i] = res
		if fatal != nil {
			return results, fatal
		}
	}
	return results, nil
}

// writeFINSOne 写入一个点位。结束码错误进 WriteResult，链路错误单独返回。
func writeFINSOne(w finsAreaWriter, req driver.WriteRequest, cfg *FINSConfig) (driver.WriteResult, error) {
	res := driver.WriteResult{DeviceAddressID: req.DeviceAddressID}

	addr, ok := ParseFINSAddress(req.Name)
	if !ok {
		res.Code = driver.WriteCodeBadAddress
		res.Message = fmt.Sprintf("invalid address %q", req.Name)
		return res, nil
	}
	dt, ok := finsLookup(req.DataType)
	if !ok {
		res.Code = driver.WriteCodeBadValue
		res.Message = fmt.Sprintf("unsupported data type %q", req.DataType)
		return res, nil
	}

	if dt.Kind == driver.KindBool {
		v, err := writeval.Parse(dt, req.Value)
		if err != nil {
			res.Code, res.Message = driver.WriteCodeBadValue, err.Error()
			return res, nil
		}
		b := v.(bool)
		if addr.Bit >= 0 {
			// 位写入：1 字节/位，0x00/0x01。
			data := []byte{0x00}
			if b {
				data[0] = 0x01
			}
			if err := w.Write(addr.Area, addr.Word, addr.Bit, data); err != nil {
				return finsWriteFailure(req, err)
			}
		} else {
			// 无位后缀：写入所在字的低字节，与读路径取 raw[len-1] 一致。
			data := []byte{0x00, 0x00}
			if b {
				data[1] = 0x01
			}
			if err := w.Write(addr.Area, addr.Word, -1, data); err != nil {
				return finsWriteFailure(req, err)
			}
		}
		res.Code = driver.WriteCodeOK
		res.Value = writeval.Format(dt, b)
		return res, nil
	}

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
	raw, err := dt.Encode(val, parseByteOrder(cfg.ByteOrder))
	if err != nil {
		res.Code = driver.WriteCodeBadValue
		res.Message = fmt.Sprintf("encode %s value %q: %v", req.DataType, req.Value, err)
		return res, nil
	}
	if strings.EqualFold(cfg.WordOrder, WordOrderLittleEndian) {
		switch dt.Size {
		case 4:
			raw = swapWords32(raw, true)
		case 8:
			raw = swapWords64(raw, true)
		}
	}
	raw = padFINSWord(raw)
	if len(raw) < 2 || len(raw)%2 != 0 {
		res.Code = driver.WriteCodeBadValue
		res.Message = fmt.Sprintf("encoded %s value has invalid word length %d", req.DataType, len(raw))
		return res, nil
	}

	if err := w.Write(addr.Area, addr.Word, -1, raw); err != nil {
		return finsWriteFailure(req, err)
	}
	res.Code = driver.WriteCodeOK
	res.Value = writeval.Format(dt, val)
	return res, nil
}

// padFINSWord 把 1 字节类型放进 16 位字的低字节（FINS 字内大端）。
func padFINSWord(raw []byte) []byte {
	if len(raw) == 0 || len(raw)%2 == 0 {
		return raw
	}
	return append([]byte{0x00}, raw...)
}

// finsWriteFailure 把传输层错误分成"设备拒绝"与"链路失败"两类。
func finsWriteFailure(req driver.WriteRequest, err error) (driver.WriteResult, error) {
	res := driver.WriteResult{DeviceAddressID: req.DeviceAddressID}
	if IsEndCodeError(err) {
		res.Code = classifyFINSEndCode(err)
		res.Message = err.Error()
		return res, nil
	}
	res.Code = driver.WriteCodeTransport
	res.Message = err.Error()
	return res, fmt.Errorf("fins: write %q failed: %w", req.Name, err)
}

// classifyFINSEndCode 把结束码映射到写结果码。
//
// 0x1101~0x1104 是 W342 的「内存区/地址/数据量」类错误，说明点位配置或
// 写入长度有问题；其余（含 0x0401 命令不支持）按设备拒绝处理。
func classifyFINSEndCode(err error) int {
	var e *finsEndCodeError
	if !errors.As(err, &e) {
		return driver.WriteCodeRejected
	}
	switch e.code {
	case 0x1101, 0x1102:
		return driver.WriteCodeBadAddress
	case 0x1103, 0x1104:
		return driver.WriteCodeBadValue
	default:
		return driver.WriteCodeRejected
	}
}
