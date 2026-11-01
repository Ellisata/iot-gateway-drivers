// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package s7

import (
	"encoding/binary"
	"fmt"

	"github.com/Ellisata/iot-gateway-drivers/internal/writeval"
	"github.com/Ellisata/iot-gateway-sdk/driver"
)

// 写路径（控制下发）。
//
// 与读路径刻意分开：读按区间规划、批量取数；写是逐点的控制动作，且必须把
// 每个点的结果单独回传。复用同一份地址解析、数据类型表与编码函数，保证
// "读回来的值"与"写下去的值"使用同一套编码（S7 全程大端）。
//
// 可写区域：DB / M / Q。I 是输入映像，只读；即使设备不报错也不应下发。
// 位地址（DB1.DBX0.0、M0.3、Q0.0）采用读-改-写一个字节，因为 gos7 的写
// 原语按字节寻址，没有单独的位写入命令。

// s7WriteTransport 写操作的最小传输接口，S7Client 与测试假设备各实现一份。
type s7WriteTransport interface {
	readRange(r S7Range, buf []byte) error
	writeRange(area s7Area, db, start int, data []byte) error
	MarkDisconnected()
}

var (
	_ driver.Writer    = (*s7Driver)(nil)
	_ s7WriteTransport = (*S7Client)(nil)
)

// Write 实现 driver.Writer。
func (d *s7Driver) Write(reqs []driver.WriteRequest) ([]driver.WriteResult, error) {
	d.mu.RLock()
	client := d.client
	d.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		return nil, fmt.Errorf("s7: not connected")
	}
	return writeS7Batch(client, reqs)
}

// writeS7Batch 逐点写入并返回与 reqs 等长的结果。
//
// 返回 error 只表示链路级失败（连接断开/超时）。已处理的点仍带结果返回；
// 调用方必须把整批视为"状态未知"，通过回读确认，而不是假定未处理的点没有生效。
func writeS7Batch(w s7WriteTransport, reqs []driver.WriteRequest) ([]driver.WriteResult, error) {
	results := make([]driver.WriteResult, len(reqs))
	for i := range reqs {
		res, fatal := writeS7One(w, reqs[i])
		results[i] = res
		if fatal != nil {
			return results, fatal
		}
	}
	return results, nil
}

// writeS7One 写入一个点位。协议层否定应答进 WriteResult，链路错误单独返回。
func writeS7One(w s7WriteTransport, req driver.WriteRequest) (driver.WriteResult, error) {
	res := driver.WriteResult{DeviceAddressID: req.DeviceAddressID}

	addr, ok := ParseS7Address(req.Name)
	if !ok {
		res.Code = driver.WriteCodeBadAddress
		res.Message = fmt.Sprintf("invalid address %q", req.Name)
		return res, nil
	}
	if addr.Area == S7AreaI {
		res.Code = driver.WriteCodeNotWritable
		res.Message = fmt.Sprintf("address %q is in the read-only input area (I)", req.Name)
		return res, nil
	}

	dt, ok := s7Lookup(req.DataType)
	if !ok {
		res.Code = driver.WriteCodeBadValue
		res.Message = fmt.Sprintf("unsupported data type %q", req.DataType)
		return res, nil
	}

	// 位地址：只接受 bool，读一个字节后改位再写回。
	if addr.IsBit() {
		if dt.Kind != driver.KindBool {
			res.Code = driver.WriteCodeBadValue
			res.Message = fmt.Sprintf("bit address %q requires bool data type, got %q", req.Name, req.DataType)
			return res, nil
		}
		v, err := writeval.Parse(dt, req.Value)
		if err != nil {
			res.Code, res.Message = driver.WriteCodeBadValue, err.Error()
			return res, nil
		}
		b := v.(bool)
		buf := make([]byte, 1)
		if err := w.readRange(S7Range{
			Area:        addr.Area,
			DB:          addr.DB,
			StartOffset: addr.ByteOffset,
			EndOffset:   addr.ByteOffset + 1,
		}, buf); err != nil {
			return s7WriteFailure(w, req, err)
		}
		mask := byte(1 << uint(addr.BitOffset))
		if b {
			buf[0] |= mask
		} else {
			buf[0] &^= mask
		}
		if err := w.writeRange(addr.Area, addr.DB, addr.ByteOffset, buf); err != nil {
			return s7WriteFailure(w, req, err)
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
	if addr.Width != 0 && addr.Width != dt.Size {
		res.Code = driver.WriteCodeBadValue
		res.Message = fmt.Sprintf("address %q is %d byte(s) wide, data type %q is %d",
			req.Name, addr.Width, req.DataType, dt.Size)
		return res, nil
	}
	if addr.Width == 0 {
		res.Code = driver.WriteCodeBadAddress
		res.Message = fmt.Sprintf("address %q has no writable width", req.Name)
		return res, nil
	}

	val, err := writeval.Parse(dt, req.Value)
	if err != nil {
		res.Code, res.Message = driver.WriteCodeBadValue, err.Error()
		return res, nil
	}
	raw, err := dt.Encode(val, binary.BigEndian)
	if err != nil {
		res.Code = driver.WriteCodeBadValue
		res.Message = fmt.Sprintf("encode %s value %q: %v", req.DataType, req.Value, err)
		return res, nil
	}
	if len(raw) != dt.Size {
		res.Code = driver.WriteCodeBadValue
		res.Message = fmt.Sprintf("encoded %s value has length %d, want %d", req.DataType, len(raw), dt.Size)
		return res, nil
	}

	if err := w.writeRange(addr.Area, addr.DB, addr.ByteOffset, raw); err != nil {
		return s7WriteFailure(w, req, err)
	}
	res.Code = driver.WriteCodeOK
	res.Value = writeval.Format(dt, val)
	return res, nil
}

// s7WriteFailure 把传输层错误分成"设备拒绝"与"链路失败"两类。
func s7WriteFailure(w s7WriteTransport, req driver.WriteRequest, err error) (driver.WriteResult, error) {
	res := driver.WriteResult{DeviceAddressID: req.DeviceAddressID}
	if isConnErr(err) {
		w.MarkDisconnected()
		res.Code = driver.WriteCodeTransport
		res.Message = err.Error()
		return res, fmt.Errorf("s7: write %q failed: %w", req.Name, err)
	}
	if isAddressNotFoundErr(err) {
		res.Code = driver.WriteCodeBadAddress
		res.Message = err.Error()
		return res, nil
	}
	res.Code = driver.WriteCodeRejected
	res.Message = err.Error()
	return res, nil
}
