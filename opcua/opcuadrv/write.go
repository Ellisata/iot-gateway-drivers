// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package opcua

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gopcua/opcua/ua"

	"github.com/Ellisata/iot-gateway-drivers/internal/writeval"
	"github.com/Ellisata/iot-gateway-sdk/driver"
)

// 写路径（控制下发）。
//
// OPC UA 的 Write 服务写的是**节点属性**，本驱动只写 Variable 的 Value 属性
// （AttributeIDValue）。Method 节点（命令接口）走的是 Call 服务，不在本路径内。
//
// 与读路径刻意分开：读按 MaxBatch 合并请求；写是控制动作，逐点一次
// WriteRequest，让每个点位的服务器状态码独立回传 —— 批量写不是事务，一个点
// 被拒不应影响其它点。链路级失败才返回 error（调用方按"状态未知"回读确认）。
//
// 可写性不在这里预判：节点是否可写由服务器按 AccessLevel / UserAccessLevel
// 判定，拒绝时回 BadNotWritable / BadUserAccessDenied，本驱动映射成
// WriteCodeNotWritable。多读一次 AccessLevel 既多一次往返，也挡不住服务器
// 在两次请求之间改变权限。

// 编译期断言：声明 Writable 的协议必须实现 driver.Writer，否则 driversdk.Serve
// 启动失败。
var _ driver.Writer = (*opcuaDriver)(nil)

// Write 实现 driver.Writer。
func (d *opcuaDriver) Write(reqs []driver.WriteRequest) ([]driver.WriteResult, error) {
	d.mu.RLock()
	client := d.client
	cfg := d.config
	d.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		return nil, fmt.Errorf("opcua: not connected")
	}
	if cfg == nil {
		cfg = DefaultOpcUaConfig()
	}

	// 与 modbus/s7 同一取舍：逐点写，已处理的点仍然带结果返回；遇到链路级
	// 失败立即中止，调用方必须把整批视为"状态未知"并通过回读确认。
	results := make([]driver.WriteResult, len(reqs))
	for i := range reqs {
		res, fatal := d.writeOne(client, cfg, reqs[i])
		results[i] = res
		if fatal != nil {
			return results, fatal
		}
	}
	return results, nil
}

// writeOne 写入一个点位。协议层否定应答进 WriteResult，链路错误单独返回。
func (d *opcuaDriver) writeOne(client *OpcUaClient, cfg *OpcUaConfig,
	req driver.WriteRequest) (driver.WriteResult, error) {
	res := driver.WriteResult{DeviceAddressID: req.DeviceAddressID}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	// 先解析地址再解析值：地址非法是配置错误，优先于值格式错误暴露出来。
	nid, err := d.nodeIDFor(ctx, client, req.Name)
	if err != nil {
		return writeResolveFailure(client, req, err)
	}

	variant, val, err := parseWriteValue(req.DataType, req.Value)
	if err != nil {
		res.Code = driver.WriteCodeBadValue
		res.Message = err.Error()
		return res, nil
	}

	status, err := client.WriteValue(ctx, nid, variant)
	if err != nil {
		var sc ua.StatusCode
		if errors.As(err, &sc) {
			if writeStatusIsTransport(sc) {
				client.MarkDisconnected()
				res.Code = driver.WriteCodeTransport
				res.Message = sc.Error()
				return res, fmt.Errorf("opcua: write %q failed: %w", req.Name, err)
			}
			// 服务级失败（服务器不支持 Write、会话权限不足等）：这个是
			// "服务器对这次请求的否定答复"，与链路失败不同，不触发回读。
			res.Code = writeCodeForStatus(sc)
			res.Message = sc.Error()
			return res, nil
		}
		// 网络/超时：写入是否到达设备未知。
		client.MarkDisconnected()
		res.Code = driver.WriteCodeTransport
		res.Message = err.Error()
		return res, fmt.Errorf("opcua: write %q failed: %w", req.Name, err)
	}

	res.Code = writeCodeForStatus(status)
	if res.Code == driver.WriteCodeOK {
		// 与 Read 共用 formatVariant：写下去的值与回读值在同一套表示下进审计，
		// 否则 float 的 'g' 表示、bool 的 "1"/"0"、byteString 的 hex 会两套写法。
		res.Value = formatVariant(val)
	} else {
		res.Message = status.Error()
	}
	return res, nil
}

// writeResolveFailure 把地址解析失败分成地址级与链路级。
//
//   - 本地语法错误、服务器明确说"没有这个节点/路径"→ 地址级；
//   - 超时、会话/安全通道已关闭 → 链路级（标记断开并返回 error）；
//   - 其余传输错误（ctx 超时、连接被重置）→ 链路级。
func writeResolveFailure(client *OpcUaClient, req driver.WriteRequest,
	err error) (driver.WriteResult, error) {
	res := driver.WriteResult{DeviceAddressID: req.DeviceAddressID}

	var pe *parseError
	if errors.As(err, &pe) {
		res.Code = driver.WriteCodeBadAddress
		res.Message = err.Error()
		return res, nil
	}

	var sc ua.StatusCode
	if errors.As(err, &sc) {
		if writeStatusIsTransport(sc) {
			client.MarkDisconnected()
			res.Code = driver.WriteCodeTransport
			res.Message = sc.Error()
			return res, fmt.Errorf("opcua: resolve write address %q failed: %w", req.Name, err)
		}
		// 浏览路径解析不出来的典型答复是 BadNoMatch；服务级否定一律按地址级
		// 处理 —— 这是"这个地址在服务器上办不到"，不是链路抖动。
		res.Code = driver.WriteCodeBadAddress
		res.Message = sc.Error()
		return res, nil
	}

	client.MarkDisconnected()
	res.Code = driver.WriteCodeTransport
	res.Message = err.Error()
	return res, fmt.Errorf("opcua: resolve write address %q failed: %w", req.Name, err)
}

// writeStatusIsTransport 判定一个状态码是否意味着链路/会话已经不可用。
//
// 这些码不是"这台设备拒绝了这个点"，而是"现在整条会话写不了"：继续下发
// 后续点位只会产生一串同样的失败，按链路失败中止更接近事实。
func writeStatusIsTransport(code ua.StatusCode) bool {
	switch code {
	case ua.StatusBadTimeout,
		ua.StatusBadCommunicationError,
		ua.StatusBadServerNotConnected,
		ua.StatusBadConnectionClosed,
		ua.StatusBadConnectionRejected,
		ua.StatusBadSecureChannelClosed,
		ua.StatusBadSessionClosed,
		ua.StatusBadSessionIDInvalid:
		return true
	}
	return false
}

// writeCodeForStatus 把 OPC UA 状态码映射成网关写结果码。
//
// 只认**这个点位**层面的语义；链路类状态码由 writeStatusIsTransport 先行拦下，
// 到这里只剩服务级否定与成功。
func writeCodeForStatus(code ua.StatusCode) int {
	if code>>30 == 0 { // severity 00 = Good
		return driver.WriteCodeOK
	}
	switch code {
	case ua.StatusBadNodeIDUnknown, // 节点不存在
		ua.StatusBadNodeIDInvalid, // 节点 ID 语法非法
		ua.StatusBadNotFound,
		ua.StatusBadNoMatch,
		ua.StatusBadAttributeIDInvalid: // 该节点没有 Value 属性
		return driver.WriteCodeBadAddress

	case ua.StatusBadNotWritable, // AccessLevel 不允许写
		ua.StatusBadUserAccessDenied,         // 当前用户无写权限
		ua.StatusBadWriteNotSupported,        // 服务器不支持这种写组合
		ua.StatusBadSecurityModeInsufficient: // 当前安全模式不允许写
		return driver.WriteCodeNotWritable

	case ua.StatusBadTypeMismatch, // 值的类型与节点类型不一致
		ua.StatusBadOutOfRange, // 值越界
		ua.StatusBadDataEncodingInvalid,
		ua.StatusBadEncodingLimitsExceeded:
		return driver.WriteCodeBadValue

	case ua.StatusBadServiceUnsupported: // 服务器没有实现 Write 服务
		return driver.WriteCodeUnsupported
	}
	return driver.WriteCodeRejected
}

// parseWriteValue 把网关下发的字符串按点位类型解析成 OPC UA Variant。
//
// 返回值第二项是解析后的 Go 原始值，仅用于成功后按 Read 的同一套规则格式化。
//
// 类型名以插件自己的类型表为准（types.go 的 init）：OPC UA 的宽度信息只存在于
// 类型名里（int16 与 int64 的 Kind 都是 Int，但 Variant 类型不同），因此这里
// **按内部裸名分支**，不按 Kind 猜宽度。
func parseWriteValue(dataType, raw string) (*ua.Variant, any, error) {
	dt, ok := opcuaLookup(dataType)
	if !ok || dt == nil {
		return nil, nil, fmt.Errorf("unsupported data type %q", dataType)
	}
	// 注册表里存的是带 scope 前缀的名字（opcua.int16），统一剥成裸名再分支。
	name := strings.ToLower(strings.TrimPrefix(dt.Name, protocolName+"."))
	s := strings.TrimSpace(raw)

	var (
		val any
		err error
	)
	switch name {
	case "bool":
		// 复用各插件共享的解析规则（1/true/on/yes 与 0/false/off/no）。
		val, err = writeval.Parse(&driver.DataType{Kind: driver.KindBool}, raw)
	case "byte":
		var v uint64
		v, err = strconv.ParseUint(s, 0, 8)
		val = uint8(v)
	case "char":
		var v int64
		v, err = strconv.ParseInt(s, 0, 8)
		val = int8(v)
	case "int16":
		var v int64
		v, err = strconv.ParseInt(s, 0, 16)
		val = int16(v)
	case "uint16":
		var v uint64
		v, err = strconv.ParseUint(s, 0, 16)
		val = uint16(v)
	case "int32":
		var v int64
		v, err = strconv.ParseInt(s, 0, 32)
		val = int32(v)
	case "uint32":
		var v uint64
		v, err = strconv.ParseUint(s, 0, 32)
		val = uint32(v)
	case "int64":
		val, err = strconv.ParseInt(s, 0, 64)
	case "uint64":
		val, err = strconv.ParseUint(s, 0, 64)
	case "float32":
		var v float64
		v, err = strconv.ParseFloat(s, 32)
		val = float32(v)
	case "float64":
		val, err = strconv.ParseFloat(s, 64)
	case "string":
		// 保留原始字符串（含首尾空白）：控制字符串可能就是带空格的配方名。
		val = raw
	case "bytestring":
		val, err = parseByteString(s)
	case "datetime":
		val, err = parseOpcUaTime(s)
	default:
		return nil, nil, fmt.Errorf("unsupported data type %q", dataType)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("invalid %s value %q: %w", dataType, raw, err)
	}

	variant, err := ua.NewVariant(val)
	if err != nil {
		return nil, nil, fmt.Errorf("encode %s value %q: %w", dataType, raw, err)
	}
	return variant, val, nil
}

// parseByteString 解析 ByteString：按 Read 的表示接受十六进制串（可带 0x 前缀）。
func parseByteString(s string) ([]byte, error) {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if s == "" {
		return []byte{}, nil
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("byteString must be hex: %w", err)
	}
	return b, nil
}

// parseOpcUaTime 解析 DateTime。
//
// 接受 Read 输出的 "2006-01-02 15:04:05"（按 UTC 解释，OPC UA DateTime 本身
// 就是 UTC），也接受 RFC3339。不做"裸数字当 Unix 时间戳"的猜测：8 位数字的
// 年月日缩写与秒级时间戳无法可靠区分。
func parseOpcUaTime(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, nil
		}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf(`datetime must be "YYYY-MM-DD HH:MM:SS" or RFC3339`)
}
