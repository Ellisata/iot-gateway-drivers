// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package cip

import (
	"errors"
	"fmt"
)

// GeneralStatusError CIP 通用状态错误：设备已响应但拒绝了该请求。
//
// 状态非 0 说明**连接是通的**，不应据此判定设备离线、也不应中断整台设备的
// 读取 —— 这正是驱动层把它与网络/超时错误分开处理的依据。
type GeneralStatusError struct {
	status byte
}

// CIP 通用状态码（CIP spec vol.1 表 3-6.1 的常用项）。
const (
	GeneralStatusConnectionFailure      byte = 0x01
	GeneralStatusResourceUnavailable    byte = 0x02
	GeneralStatusInvalidParameter       byte = 0x03
	GeneralStatusPathSegmentError       byte = 0x04
	GeneralStatusPathDestinationUnknown byte = 0x05
	GeneralStatusPartialTransfer        byte = 0x06
	GeneralStatusConnectionLost         byte = 0x07
	GeneralStatusServiceNotSupported    byte = 0x08
	GeneralStatusAttributeListError     byte = 0x0A
	GeneralStatusAttributeNotSettable   byte = 0x0E
	GeneralStatusTooMuchData            byte = 0x13
	GeneralStatusAttributeNotSupported  byte = 0x14
	GeneralStatusDataTooLarge           byte = 0x15
	GeneralStatusObjectDoesNotExist     byte = 0x16
	GeneralStatusInvalidAttribute       byte = 0x1C
	GeneralStatusEmbeddedError          byte = 0x1E
)

var generalStatusText = map[byte]string{
	GeneralStatusConnectionFailure:      "connection failure",
	GeneralStatusResourceUnavailable:    "resource unavailable",
	GeneralStatusInvalidParameter:       "invalid parameter value",
	GeneralStatusPathSegmentError:       "path segment error",
	GeneralStatusPathDestinationUnknown: "path destination unknown",
	GeneralStatusPartialTransfer:        "partial transfer",
	GeneralStatusConnectionLost:         "connection lost",
	GeneralStatusServiceNotSupported:    "service not supported",
	GeneralStatusAttributeListError:     "attribute list error",
	GeneralStatusAttributeNotSettable:   "attribute not settable",
	GeneralStatusTooMuchData:            "too much data",
	GeneralStatusAttributeNotSupported:  "attribute not supported",
	GeneralStatusDataTooLarge:           "data too large for response packet",
	GeneralStatusObjectDoesNotExist:     "object does not exist",
	GeneralStatusInvalidAttribute:       "invalid attribute",
	GeneralStatusEmbeddedError:          "embedded error",
}

func (e *GeneralStatusError) Error() string {
	msg, ok := generalStatusText[e.status]
	if !ok {
		msg = "unknown"
	}
	return fmt.Sprintf("cip: general status 0x%02X (%s)", e.status, msg)
}

// Status 返回原始通用状态码，供调用方按码分流（能力探测/质量码映射）。
func (e *GeneralStatusError) Status() byte { return e.status }

// NewGeneralStatusError 构造通用状态错误。
func NewGeneralStatusError(status byte) error {
	return &GeneralStatusError{status: status}
}

// IsGeneralStatusError 判断错误是否为 CIP 通用状态错误（设备已响应但拒绝请求）。
func IsGeneralStatusError(err error) bool {
	var e *GeneralStatusError
	return errors.As(err, &e)
}

// IsAddressStatusError 这条通用状态是否等于设备在说"这个对象/属性我这儿没有"。
//
// 与 IsGeneralStatusError 的关系：后者决定"要不要把整台设备判离线"，
// 这里决定"这个点位该归因到配置错还是通信抖动"。两个问题，两个判据。
//
// 只认明确表示"不存在/不支持"的状态码。0x02 资源不可用、0x06 部分传输这些是
// **可恢复**的设备状态，下一轮原样重试可能就成功，不能据此把点位永久摘掉。
func IsAddressStatusError(err error) bool {
	var e *GeneralStatusError
	if !errors.As(err, &e) {
		return false
	}
	switch e.status {
	case GeneralStatusPathSegmentError,
		GeneralStatusPathDestinationUnknown,
		GeneralStatusServiceNotSupported,
		GeneralStatusAttributeNotSupported,
		GeneralStatusObjectDoesNotExist,
		GeneralStatusInvalidAttribute:
		return true
	default:
		return false
	}
}
