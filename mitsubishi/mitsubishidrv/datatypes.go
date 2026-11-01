// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package mitsubishi

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/Ellisata/iot-gateway-sdk/driver"
)

// protocolName MC 协议名称（类型注册 scope 前缀，与 driver.dataTypeMap 的 scope 一致）。
const protocolName = "mitsubishi"

// mcScope MC 协议类型注册表作用域缓存。
// protocolName 为常量，作用域前缀固定；每点位直接复用该作用域，避免反复
// 构造 ProtocolScope 与拼接 "{protocol}." 前缀。
var mcScope = driver.GetTypeRegistry().ForProtocol(protocolName)

// mcTypeMeta 类型元数据静态缓存条目。
type mcTypeMeta struct {
	size     int
	kind     string
	isString bool
	dt       *driver.DataType
}

var (
	mcMetaOnce sync.Once
	mcMetaMap  map[string]mcTypeMeta
)

// mcMeta 返回协议内类型元数据静态缓存（键为内部裸名，如 "int16"）。
// 类型注册表在 init() 后不再变化（dataTypeMap 仅查询不注册），
// 按裸名缓存 size/kind/isString 与 DataType；区间计算与解码路径直接查此 map
// （无锁、无字符串拼接），消除每点位对全局注册表 RWMutex 的查询与锁竞争。
func mcMeta() map[string]mcTypeMeta {
	mcMetaOnce.Do(func() {
		m := make(map[string]mcTypeMeta)
		strDT, _ := mcScope.Get("string")
		prefix := protocolName + "."
		for _, full := range driver.GetTypeRegistry().Names() {
			bare, ok := strings.CutPrefix(full, prefix)
			if !ok {
				continue
			}
			dt, ok := mcScope.Get(bare)
			if !ok {
				continue
			}
			m[bare] = mcTypeMeta{size: dt.Size, kind: dt.Kind, isString: dt == strDT, dt: dt}
		}
		mcMetaMap = m
	})
	return mcMetaMap
}

// mcLookup 按裸名查 DataType：优先命中静态缓存（零分配、无锁），
// 未命中回退注册表查询（兼容大小写变体，保持原有不区分大小写语义）。
func mcLookup(name string) (*driver.DataType, bool) {
	if meta, ok := mcMeta()[name]; ok {
		return meta.dt, true
	}
	return mcScope.Get(name)
}

// 重要：本包不注册任何全局别名！
// 所有 MC 类型仅通过 ForProtocol("mitsubishi") 作用域注册，
// 配置层通用类型名统一由本包的 typemap.go（CommonTypeMap/LookupCommonType）映射为内部裸名，
// ProtocolScope.Get(name) 会优先命中 "mitsubishi."+name。

func init() {
	mc := driver.GetTypeRegistry().ForProtocol(protocolName)

	// ========== 注册 MC 标准数据类型（自动添加 "mitsubishi." 前缀） ==========
	// MC 字数据天然小端（低字节在前，32/64 位低字在前），解码统一 binary.LittleEndian。

	mc.Register(&driver.DataType{
		Name:   "bool",
		Kind:   driver.KindBool,
		Size:   1,
		Decode: decodeBool,
		Encode: encodeBool,
		Format: formatBool,
	})

	mc.Register(&driver.DataType{
		Name:   "int16",
		Kind:   driver.KindInt,
		Size:   2,
		Decode: decodeInt16,
		Encode: encodeInt16,
		Format: formatDefault,
	})

	mc.Register(&driver.DataType{
		Name:   "uint16",
		Kind:   driver.KindUInt,
		Size:   2,
		Decode: decodeUint16,
		Encode: encodeUint16,
		Format: formatDefault,
	})

	mc.Register(&driver.DataType{
		Name:   "word",
		Kind:   driver.KindUInt,
		Size:   2,
		Decode: decodeUint16,
		Encode: encodeUint16,
		Format: formatDefault,
	})

	mc.Register(&driver.DataType{
		Name:   "int32",
		Kind:   driver.KindInt,
		Size:   4,
		Decode: decodeInt32,
		Encode: encodeInt32,
		Format: formatDefault,
	})

	mc.Register(&driver.DataType{
		Name:   "uint32",
		Kind:   driver.KindUInt,
		Size:   4,
		Decode: decodeUint32,
		Encode: encodeUint32,
		Format: formatDefault,
	})

	mc.Register(&driver.DataType{
		Name:   "float32",
		Kind:   driver.KindFloat,
		Size:   4,
		Decode: decodeFloat32,
		Encode: encodeFloat32,
		Format: formatFloat,
	})

	mc.Register(&driver.DataType{
		Name:   "float64",
		Kind:   driver.KindFloat,
		Size:   8,
		Decode: decodeFloat64,
		Encode: encodeFloat64,
		Format: formatFloat,
	})

	mc.Register(&driver.DataType{
		Name:   "string",
		Kind:   driver.KindString,
		Size:   0, // 动态长度，跨度由 range.go 按 stringLen（字数）折算
		Decode: decodeString,
		Format: formatDefault,
	})

	// ========== 注册扩展类型 ==========

	mc.Register(&driver.DataType{
		Name:   "uint8",
		Kind:   driver.KindUInt,
		Size:   1,
		Decode: decodeUint8,
		Encode: encodeUint8,
		Format: formatDefault,
	})

	mc.Register(&driver.DataType{
		Name:   "int8",
		Kind:   driver.KindInt,
		Size:   1,
		Decode: decodeInt8,
		Encode: encodeInt8,
		Format: formatDefault,
	})

	mc.Register(&driver.DataType{
		Name:   "bcd",
		Kind:   driver.KindUInt,
		Size:   2,
		Decode: decodeBCD,
		Encode: encodeBCD,
		Format: formatDefault,
	})

	mc.Register(&driver.DataType{
		Name:   "lbcd",
		Kind:   driver.KindUInt,
		Size:   4,
		Decode: decodeLBCD,
		Encode: encodeLBCD,
		Format: formatDefault,
	})
}

// typeKind 解析(裸)内部类型名对应的类别 Kind，供构建 ReadResult 时填充。
// 未注册类型返回 ""（推送通道回退为数值推断）。
func typeKind(name string) string {
	if meta, ok := mcMeta()[name]; ok {
		return meta.kind
	}
	return mcScope.KindOf(name)
}

// ==================== 解码函数 ====================
// MC 数据全部小端，bo 统一为 binary.LittleEndian。
// raw 已由驱动层按地址跨度切好（字单位 2 字节/字，位单位 1 字节/点）。

func decodeBool(raw []byte, _ binary.ByteOrder) (any, error) {
	if len(raw) == 0 {
		return false, nil
	}
	return raw[len(raw)-1]&0x01 != 0, nil
}

func decodeInt16(raw []byte, bo binary.ByteOrder) (any, error) {
	if len(raw) < 2 {
		return nil, fmt.Errorf("int16: need 2 bytes, got %d", len(raw))
	}
	return int16(bo.Uint16(raw[:2])), nil
}

func decodeUint16(raw []byte, bo binary.ByteOrder) (any, error) {
	if len(raw) < 2 {
		return nil, fmt.Errorf("uint16: need 2 bytes, got %d", len(raw))
	}
	return bo.Uint16(raw[:2]), nil
}

func decodeInt32(raw []byte, bo binary.ByteOrder) (any, error) {
	if len(raw) < 4 {
		return nil, fmt.Errorf("int32: need 4 bytes, got %d", len(raw))
	}
	return int32(bo.Uint32(raw[:4])), nil
}

func decodeUint32(raw []byte, bo binary.ByteOrder) (any, error) {
	if len(raw) < 4 {
		return nil, fmt.Errorf("uint32: need 4 bytes, got %d", len(raw))
	}
	return bo.Uint32(raw[:4]), nil
}

func decodeFloat32(raw []byte, bo binary.ByteOrder) (any, error) {
	if len(raw) < 4 {
		return nil, fmt.Errorf("float32: need 4 bytes, got %d", len(raw))
	}
	return math.Float32frombits(bo.Uint32(raw[:4])), nil
}

func decodeFloat64(raw []byte, bo binary.ByteOrder) (any, error) {
	if len(raw) < 8 {
		return nil, fmt.Errorf("float64: need 8 bytes, got %d", len(raw))
	}
	return math.Float64frombits(bo.Uint64(raw[:8])), nil
}

// decodeString 解码 MC string：raw 为连续字（每字 2 ASCII 字符，低字节在前），
// 响应字节流即正序 ASCII；去除尾部 NUL 与空白填充。
func decodeString(raw []byte, _ binary.ByteOrder) (any, error) {
	return strings.TrimSpace(strings.TrimRight(string(raw), "\x00")), nil
}

func decodeUint8(raw []byte, _ binary.ByteOrder) (any, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("uint8: need at least 1 byte")
	}
	return raw[0], nil
}

func decodeInt8(raw []byte, _ binary.ByteOrder) (any, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("int8: need at least 1 byte")
	}
	return int8(raw[0]), nil
}

// decodeBCD 解码 2 字节压缩 BCD 码。如 0x1234 → 1234
func decodeBCD(raw []byte, bo binary.ByteOrder) (any, error) {
	if len(raw) < 2 {
		return nil, fmt.Errorf("bcd: need 2 bytes, got %d", len(raw))
	}
	v := bo.Uint16(raw[:2])
	return int(bcdToUint64(uint64(v), 4)), nil
}

// decodeLBCD 解码 4 字节长 BCD 码。如 0x12345678 → 12345678
func decodeLBCD(raw []byte, bo binary.ByteOrder) (any, error) {
	if len(raw) < 4 {
		return nil, fmt.Errorf("lbcd: need 4 bytes, got %d", len(raw))
	}
	v := bo.Uint32(raw[:4])
	return int(bcdToUint64(uint64(v), 8)), nil
}

// ==================== 格式化函数 ====================

func formatBool(val any) string {
	if b, ok := val.(bool); ok {
		if b {
			return "1"
		}
		return "0"
	}
	return "0"
}

func formatFloat(val any) string {
	// 'f' 固定记法最短往返表示：字符串能无损还原解码后的浮点，且永不出现指数。
	switch v := val.(type) {
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32)
	default:
		return fmt.Sprintf("%v", val)
	}
}

func formatDefault(val any) string {
	return fmt.Sprintf("%v", val)
}

// ==================== BCD 工具函数 ====================

func bcdToUint64(bcd uint64, digits int) uint64 {
	var result uint64
	for i := digits - 1; i >= 0; i-- {
		digit := (bcd >> (i * 4)) & 0xF
		result = result*10 + digit
	}
	return result
}

// ==================== 编码函数 ====================
// MC 字数据小端；写路径把 1 字节类型补成一个 16 位字（低字节放值），
// 见 write.go 的 padMCWord。

func encodeBool(val any, _ binary.ByteOrder) ([]byte, error) {
	var v bool
	switch x := val.(type) {
	case bool:
		v = x
	case float64:
		v = x != 0
	case string:
		v = x == "1" || strings.EqualFold(x, "true")
	default:
		return nil, fmt.Errorf("bool: unsupported value type %T", val)
	}
	buf := make([]byte, 1)
	if v {
		buf[0] = 0x01
	}
	return buf, nil
}

func encodeInt16(val any, bo binary.ByteOrder) ([]byte, error) {
	v, err := toInt64(val)
	if err != nil {
		return nil, fmt.Errorf("int16: %w", err)
	}
	buf := make([]byte, 2)
	bo.PutUint16(buf, uint16(int16(v)))
	return buf, nil
}

func encodeUint16(val any, bo binary.ByteOrder) ([]byte, error) {
	v, err := toUint64(val)
	if err != nil {
		return nil, fmt.Errorf("uint16: %w", err)
	}
	buf := make([]byte, 2)
	bo.PutUint16(buf, uint16(v))
	return buf, nil
}

func encodeInt32(val any, bo binary.ByteOrder) ([]byte, error) {
	v, err := toInt64(val)
	if err != nil {
		return nil, fmt.Errorf("int32: %w", err)
	}
	buf := make([]byte, 4)
	bo.PutUint32(buf, uint32(int32(v)))
	return buf, nil
}

func encodeUint32(val any, bo binary.ByteOrder) ([]byte, error) {
	v, err := toUint64(val)
	if err != nil {
		return nil, fmt.Errorf("uint32: %w", err)
	}
	buf := make([]byte, 4)
	bo.PutUint32(buf, uint32(v))
	return buf, nil
}

func encodeFloat32(val any, bo binary.ByteOrder) ([]byte, error) {
	var v float32
	switch x := val.(type) {
	case float64:
		v = float32(x)
	case float32:
		v = x
	default:
		return nil, fmt.Errorf("float32: unsupported value type %T", val)
	}
	buf := make([]byte, 4)
	bo.PutUint32(buf, math.Float32bits(v))
	return buf, nil
}

func encodeFloat64(val any, bo binary.ByteOrder) ([]byte, error) {
	var v float64
	switch x := val.(type) {
	case float64:
		v = x
	case float32:
		v = float64(x)
	default:
		return nil, fmt.Errorf("float64: unsupported value type %T", val)
	}
	buf := make([]byte, 8)
	bo.PutUint64(buf, math.Float64bits(v))
	return buf, nil
}

func encodeUint8(val any, _ binary.ByteOrder) ([]byte, error) {
	v, err := toUint64(val)
	if err != nil {
		return nil, fmt.Errorf("uint8: %w", err)
	}
	return []byte{byte(v)}, nil
}

func encodeInt8(val any, _ binary.ByteOrder) ([]byte, error) {
	v, err := toInt64(val)
	if err != nil {
		return nil, fmt.Errorf("int8: %w", err)
	}
	return []byte{byte(int8(v))}, nil
}

func encodeBCD(val any, bo binary.ByteOrder) ([]byte, error) {
	v, err := toUint64(val)
	if err != nil {
		return nil, fmt.Errorf("bcd: %w", err)
	}
	if v > 9999 {
		v = v % 10000
	}
	buf := make([]byte, 2)
	bo.PutUint16(buf, uint16(uint64ToBCD(v, 4)))
	return buf, nil
}

func encodeLBCD(val any, bo binary.ByteOrder) ([]byte, error) {
	v, err := toUint64(val)
	if err != nil {
		return nil, fmt.Errorf("lbcd: %w", err)
	}
	if v > 99999999 {
		v = v % 100000000
	}
	buf := make([]byte, 4)
	bo.PutUint32(buf, uint32(uint64ToBCD(v, 8)))
	return buf, nil
}

// ==================== 类型转换工具 ====================

func toInt64(val any) (int64, error) {
	switch v := val.(type) {
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case int32:
		return int64(v), nil
	case int16:
		return int64(v), nil
	case int8:
		return int64(v), nil
	case uint64:
		return int64(v), nil
	case uint32:
		return int64(v), nil
	case uint16:
		return int64(v), nil
	case uint8:
		return int64(v), nil
	case float64:
		return int64(v), nil
	default:
		return 0, fmt.Errorf("cannot convert %T to int64", val)
	}
}

func toUint64(val any) (uint64, error) {
	switch v := val.(type) {
	case uint64:
		return v, nil
	case uint:
		return uint64(v), nil
	case uint32:
		return uint64(v), nil
	case uint16:
		return uint64(v), nil
	case uint8:
		return uint64(v), nil
	case int64:
		if v < 0 {
			return 0, fmt.Errorf("negative value %d cannot convert to uint64", v)
		}
		return uint64(v), nil
	case int:
		if v < 0 {
			return 0, fmt.Errorf("negative value %d cannot convert to uint64", v)
		}
		return uint64(v), nil
	case int32:
		if v < 0 {
			return 0, fmt.Errorf("negative value %d cannot convert to uint64", v)
		}
		return uint64(v), nil
	case float64:
		if v < 0 {
			return 0, fmt.Errorf("negative value %f cannot convert to uint64", v)
		}
		return uint64(v), nil
	default:
		return 0, fmt.Errorf("cannot convert %T to uint64", val)
	}
}

func uint64ToBCD(v uint64, digits int) uint64 {
	var bcd uint64
	for i := digits - 1; i >= 0; i-- {
		digit := v % 10
		bcd |= digit << (i * 4)
		v /= 10
	}
	return bcd
}
