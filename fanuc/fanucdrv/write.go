// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package fanuc

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Ellisata/iot-gateway-sdk/driver"

	cipcore "github.com/Ellisata/iot-gateway-drivers/fanuc/cipcore"
)

// 编译期断言：声明 Writable 的协议必须实现 Writer，否则 driversdk.Serve 启动失败。
var _ driver.Writer = (*fanucDriver)(nil)

// Write 控制下发（Set_Attribute_Single）。
//
// 只开放数值寄存器 R[]：
//   - 整型写走 0x6B，实型写走 0x6C；
//   - 写入 0x6C 会改变该寄存器在控制器里的类型（FANUC 手册明确说明），
//     这是设备语义，不是驱动的副作用；
//   - 单点失败进 WriteResult.Code；只有链路级失败才让 Write 返回 error，
//     调用方据此把整批视为"状态未知"并回读确认。
func (d *fanucDriver) Write(reqs []driver.WriteRequest) ([]driver.WriteResult, error) {
	d.mu.RLock()
	c := d.client
	lim := d.limit
	d.mu.RUnlock()

	if c == nil || !c.isConnected() {
		return nil, fmt.Errorf("fanuc: not connected")
	}

	results := make([]driver.WriteResult, len(reqs))
	for i := range reqs {
		req := &reqs[i]
		results[i] = driver.WriteResult{DeviceAddressID: req.DeviceAddressID}

		parsed, err := parseRegisterAddress(req.Name, req.DataType)
		if err != nil {
			results[i].Code = driver.WriteCodeBadAddress
			results[i].Message = err.Error()
			continue
		}

		data, formatted, err := encodeWriteValue(parsed.DataType, req.Value)
		if err != nil {
			results[i].Code = driver.WriteCodeBadValue
			results[i].Message = err.Error()
			continue
		}

		if lim != nil {
			lim.wait()
		}
		if err := c.writeAttr(parsed.Class, parsed.Index, data); err != nil {
			if cipcore.IsGeneralStatusError(err) {
				results[i].Code = writeCodeOfCIPErr(err)
				results[i].Message = err.Error()
				continue
			}
			// 链路级失败：这次写入是否到达设备、是否生效都未知。
			return nil, fmt.Errorf("fanuc: write R[%d] failed: %w", parsed.Index, err)
		}
		results[i].Code = driver.WriteCodeOK
		results[i].Value = formatted
	}
	return results, nil
}

// encodeWriteValue 把字符串形式的值按内部类型编码成 4 字节小端数据。
func encodeWriteValue(dataType, value string) (data []byte, formatted string, err error) {
	dt, ok := fanucLookup(dataType)
	if !ok || dt.Encode == nil {
		return nil, "", fmt.Errorf("unsupported data type %q", dataType)
	}

	s := strings.TrimSpace(value)
	switch dt.Kind {
	case driver.KindInt:
		v, perr := strconv.ParseInt(s, 10, 64)
		if perr != nil {
			return nil, "", fmt.Errorf("invalid integer %q: %w", value, perr)
		}
		data, err = dt.Encode(v, littleEndian)
		if err != nil {
			return nil, "", err
		}
		return data, dt.Format(int32(v)), nil
	case driver.KindUInt:
		v, perr := strconv.ParseUint(s, 10, 64)
		if perr != nil {
			return nil, "", fmt.Errorf("invalid unsigned integer %q: %w", value, perr)
		}
		data, err = dt.Encode(v, littleEndian)
		if err != nil {
			return nil, "", err
		}
		return data, dt.Format(uint32(v)), nil
	case driver.KindFloat:
		v, perr := strconv.ParseFloat(s, 64)
		if perr != nil {
			return nil, "", fmt.Errorf("invalid float %q: %w", value, perr)
		}
		data, err = dt.Encode(v, littleEndian)
		if err != nil {
			return nil, "", err
		}
		return data, dt.Format(float32(v)), nil
	default:
		return nil, "", fmt.Errorf("data type %q is not writable", dataType)
	}
}

// writeCodeOfCIPErr 把 CIP 通用状态错误映射为 WriteResult.Code。
func writeCodeOfCIPErr(err error) int {
	var ge *cipcore.GeneralStatusError
	if !errors.As(err, &ge) {
		return driver.WriteCodeRejected
	}
	switch ge.Status() {
	case cipcore.GeneralStatusServiceNotSupported:
		return driver.WriteCodeUnsupported
	case cipcore.GeneralStatusAttributeNotSettable:
		return driver.WriteCodeNotWritable
	case cipcore.GeneralStatusInvalidParameter,
		cipcore.GeneralStatusTooMuchData,
		cipcore.GeneralStatusDataTooLarge:
		return driver.WriteCodeBadValue
	case cipcore.GeneralStatusPathSegmentError,
		cipcore.GeneralStatusPathDestinationUnknown,
		cipcore.GeneralStatusAttributeNotSupported,
		cipcore.GeneralStatusObjectDoesNotExist,
		cipcore.GeneralStatusInvalidAttribute:
		return driver.WriteCodeBadAddress
	default:
		return driver.WriteCodeRejected
	}
}
