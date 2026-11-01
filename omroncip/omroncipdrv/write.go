// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package cip

import (
	"encoding/binary"
	"fmt"

	"github.com/Ellisata/iot-gateway-drivers/internal/writeval"
	cipcore "github.com/Ellisata/iot-gateway-drivers/omroncip/cipcore"
	"github.com/Ellisata/iot-gateway-sdk/driver"
)

// 写路径（控制下发），使用 Omron 的 0x4D "Write Service for Variables"。
//
// 与读路径共用标签名解析、类型表与类型码：写请求必须显式携带数据类型码，
// 设备按它解释后面数据区的长度与编码（小端）。字符串等动态长度类型没有
// 编码函数，写路径明确拒绝，而不是猜一个长度发下去。

var (
	_ driver.Writer = (*cipDriver)(nil)
	_ cipTagWriter  = (*cipClient)(nil)
)

// Write 实现 driver.Writer。
func (d *cipDriver) Write(reqs []driver.WriteRequest) ([]driver.WriteResult, error) {
	d.mu.RLock()
	client := d.client
	d.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		return nil, fmt.Errorf("cip: not connected")
	}
	w, ok := client.(cipTagWriter)
	if !ok {
		return nil, fmt.Errorf("cip: transport %T does not support writing", client)
	}
	return writeCIPBatch(w, reqs)
}

// writeCIPBatch 逐点写入并返回与 reqs 等长的结果。
//
// 返回 error 只表示链路级失败（连接断开/超时）。已处理的点仍带结果返回；
// 调用方必须把整批视为"状态未知"，通过回读确认。
func writeCIPBatch(w cipTagWriter, reqs []driver.WriteRequest) ([]driver.WriteResult, error) {
	results := make([]driver.WriteResult, len(reqs))
	for i := range reqs {
		res, fatal := writeCIPOne(w, reqs[i])
		results[i] = res
		if fatal != nil {
			return results, fatal
		}
	}
	return results, nil
}

// writeCIPOne 写入一个标签。通用状态错误进 WriteResult，链路错误单独返回。
func writeCIPOne(w cipTagWriter, req driver.WriteRequest) (driver.WriteResult, error) {
	res := driver.WriteResult{DeviceAddressID: req.DeviceAddressID}

	tag, ok := ParseCIPAddress(req.Name)
	if !ok {
		res.Code = driver.WriteCodeBadAddress
		res.Message = fmt.Sprintf("invalid tag name %q", req.Name)
		return res, nil
	}
	dt, ok := cipLookup(req.DataType)
	if !ok {
		res.Code = driver.WriteCodeBadValue
		res.Message = fmt.Sprintf("unsupported data type %q", req.DataType)
		return res, nil
	}
	code, ok := cipTypeCode(req.DataType)
	if !ok {
		res.Code = driver.WriteCodeBadValue
		res.Message = fmt.Sprintf("no Omron type code for data type %q", req.DataType)
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
	raw, err := dt.Encode(val, binary.LittleEndian)
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

	if err := w.WriteTag(tag, code, raw); err != nil {
		if cipcore.IsGeneralStatusError(err) {
			res.Code = driver.WriteCodeRejected
			if cipcore.IsAddressStatusError(err) {
				res.Code = driver.WriteCodeBadAddress
			}
			res.Message = err.Error()
			return res, nil
		}
		res.Code = driver.WriteCodeTransport
		res.Message = err.Error()
		return res, fmt.Errorf("cip: write %q failed: %w", req.Name, err)
	}
	res.Code = driver.WriteCodeOK
	res.Value = writeval.Format(dt, val)
	return res, nil
}
