// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package cip

import (
	"fmt"
	"strings"

	"github.com/Ellisata/iot-gateway-drivers/internal/writeval"
	"github.com/Ellisata/iot-gateway-sdk/driver"
)

// 写路径（控制下发），使用 Rockwell Logix 的 0x4D Write Tag 服务。
//
// goindustrial 的 WriteTag 按 Go 值类型推断 CIP 类型码，因此这里必须把
// 字符串解析成**与点位声明类型宽度一致**的 Go 原始类型（如 float32 而不能是
// float64），否则设备会把 LREAL 的数据写进 REAL 标签。数组元素、结构体成员与
// 位访问（Tag.5）由库的 ParseTagPath 解析，标签名原样透传。

var (
	_ driver.Writer     = (*rockwellDriver)(nil)
	_ rockwellTagWriter = (*goindustrialClient)(nil)
)

// Write 实现 driver.Writer。
func (d *rockwellDriver) Write(reqs []driver.WriteRequest) ([]driver.WriteResult, error) {
	d.mu.RLock()
	client := d.client
	d.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		return nil, fmt.Errorf("rockwell: not connected")
	}
	w, ok := client.(rockwellTagWriter)
	if !ok {
		return nil, fmt.Errorf("rockwell: transport %T does not support writing", client)
	}
	return writeRockwellBatch(w, reqs)
}

// writeRockwellBatch 逐点写入并返回与 reqs 等长的结果。
//
// 返回 error 只表示链路级失败（连接断开/超时）。已处理的点仍带结果返回；
// 调用方必须把整批视为"状态未知"，通过回读确认。
func writeRockwellBatch(w rockwellTagWriter, reqs []driver.WriteRequest) ([]driver.WriteResult, error) {
	results := make([]driver.WriteResult, len(reqs))
	for i := range reqs {
		res, fatal := writeRockwellOne(w, reqs[i])
		results[i] = res
		if fatal != nil {
			return results, fatal
		}
	}
	return results, nil
}

// writeRockwellOne 写入一个标签。CIP 状态错误进 WriteResult，链路错误单独返回。
func writeRockwellOne(w rockwellTagWriter, req driver.WriteRequest) (driver.WriteResult, error) {
	res := driver.WriteResult{DeviceAddressID: req.DeviceAddressID}

	tag, ok := ParseRockwellAddress(req.Name)
	if !ok {
		res.Code = driver.WriteCodeBadAddress
		res.Message = fmt.Sprintf("invalid tag name %q", req.Name)
		return res, nil
	}
	dt, ok := rockwellLookup(req.DataType)
	if !ok {
		res.Code = driver.WriteCodeBadValue
		res.Message = fmt.Sprintf("unsupported data type %q", req.DataType)
		return res, nil
	}
	val, err := rockwellWriteValue(req.DataType, dt, req.Value)
	if err != nil {
		res.Code, res.Message = driver.WriteCodeBadValue, err.Error()
		return res, nil
	}

	if err := w.WriteTag(tag, val); err != nil {
		if isCIPStatusError(err) {
			res.Code = driver.WriteCodeRejected
			if isAddressStatusError(err) {
				res.Code = driver.WriteCodeBadAddress
			}
			res.Message = describeCIPError(err)
			return res, nil
		}
		res.Code = driver.WriteCodeTransport
		res.Message = describeCIPError(err)
		return res, fmt.Errorf("rockwell: write %q failed: %w", req.Name, err)
	}
	res.Code = driver.WriteCodeOK
	res.Value = writeval.Format(dt, val)
	return res, nil
}

// rockwellWriteValue 把字符串解析成 goindustrial 能映射到正确 CIP 类型码的
// Go 原始类型。Logix 数值类型原生的有符号/浮点宽度由点位 DataType 决定。
func rockwellWriteValue(dataType string, dt *driver.DataType, raw string) (any, error) {
	name := strings.ToLower(dataType)
	if name == "string" {
		return nil, fmt.Errorf("string write is not supported by the Logix Write Tag path")
	}
	v, err := writeval.Parse(dt, raw)
	if err != nil {
		return nil, err
	}
	switch name {
	case "bool":
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("bool: unexpected parsed type %T", v)
		}
		return b, nil
	case "int8":
		n, ok := v.(int64)
		if !ok {
			return nil, fmt.Errorf("int8: unexpected parsed type %T", v)
		}
		return int8(n), nil
	case "int16":
		n, ok := v.(int64)
		if !ok {
			return nil, fmt.Errorf("int16: unexpected parsed type %T", v)
		}
		return int16(n), nil
	case "int32":
		n, ok := v.(int64)
		if !ok {
			return nil, fmt.Errorf("int32: unexpected parsed type %T", v)
		}
		return int32(n), nil
	case "int64":
		n, ok := v.(int64)
		if !ok {
			return nil, fmt.Errorf("int64: unexpected parsed type %T", v)
		}
		return n, nil
	case "float32":
		f, ok := v.(float64)
		if !ok {
			return nil, fmt.Errorf("float32: unexpected parsed type %T", v)
		}
		return float32(f), nil
	case "float64":
		f, ok := v.(float64)
		if !ok {
			return nil, fmt.Errorf("float64: unexpected parsed type %T", v)
		}
		return f, nil
	default:
		return nil, fmt.Errorf("data type %q has no Logix write type", dataType)
	}
}
