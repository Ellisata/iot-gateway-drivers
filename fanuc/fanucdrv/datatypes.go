// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package fanuc

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/Ellisata/iot-gateway-sdk/driver"
)

// fanucScope 类型注册表作用域缓存。
var fanucScope = driver.GetTypeRegistry().ForProtocol(protocolScope)

// littleEndian FANUC CIP 数据统一小端。集中成一个变量，避免每个调用点
// 各写一遍 binary.LittleEndian（写错成 BigEndian 的症状是数值完全错乱）。
var littleEndian = binary.LittleEndian

var (
	fanucLookupOnce sync.Once
	fanucTypes      map[string]*driver.DataType
)

// fanucTypeCache 返回本协议类型元数据的静态缓存（键为内部裸名）。
//
// 类型注册表在 init() 后不再变化，按裸名缓存 *DataType 可消除每点位对全局
// 注册表 RWMutex 的查询与锁竞争。metadata.go 的 Types() 同样直读本表上报网关，
// 不另写 map 字面量，避免与注册表漂移。
func fanucTypeCache() map[string]*driver.DataType {
	fanucLookupOnce.Do(func() {
		m := make(map[string]*driver.DataType)
		prefix := protocolScope + "."
		for _, full := range driver.GetTypeRegistry().Names() {
			bare, ok := strings.CutPrefix(full, prefix)
			if !ok {
				continue
			}
			dt, ok := fanucScope.Get(bare)
			if !ok {
				continue
			}
			m[bare] = dt
		}
		fanucTypes = m
	})
	return fanucTypes
}

// fanucLookup 按裸名查 DataType：优先命中静态缓存，未命中回退注册表查询
// （兼容历史配置里的大小写变体）。
func fanucLookup(name string) (*driver.DataType, bool) {
	if dt, ok := fanucTypeCache()[name]; ok {
		return dt, true
	}
	return fanucScope.Get(name)
}

// init 注册 FANUC CIP 的三种数值类型。
//
// FANUC 数值寄存器只有「整型 / 实型」两种对象，值都是 32 位、小端传输：
//
//   - int32   → 类 0x6B
//   - uint32  → 类 0x6B（同一段字节的无符号解释，便于读位域/状态字）
//   - float32 → 类 0x6C
//
// 不注册 bool/string：寄存器不是布尔位，字符串寄存器（SR）走另一个对象，
// 其载荷格式尚未在真机核实（P1/P2 的事）。没有注册的类型不会被前端/网关
// 接受，比一个"读了但永远错"的类型安全。
func init() {
	f := driver.GetTypeRegistry().ForProtocol(protocolScope)

	f.Register(&driver.DataType{
		Name:   typeInt32,
		Kind:   driver.KindInt,
		Size:   4,
		Decode: decodeInt32,
		Encode: encodeInt32,
		Format: formatSigned,
	})

	f.Register(&driver.DataType{
		Name:   typeUint32,
		Kind:   driver.KindUInt,
		Size:   4,
		Decode: decodeUint32,
		Encode: encodeUint32,
		Format: formatUnsigned,
	})

	f.Register(&driver.DataType{
		Name:   typeFloat32,
		Kind:   driver.KindFloat,
		Size:   4,
		Decode: decodeFloat32,
		Encode: encodeFloat32,
		Format: formatFloat,
	})
}

// ==================== 解码（FANUC CIP 原生小端） ====================

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

// ==================== 编码 ====================

func encodeInt32(val any, bo binary.ByteOrder) ([]byte, error) {
	v, err := toInt64(val)
	if err != nil {
		return nil, fmt.Errorf("int32: %w", err)
	}
	if v < math.MinInt32 || v > math.MaxInt32 {
		return nil, fmt.Errorf("int32: %d out of range", v)
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
	if v > math.MaxUint32 {
		return nil, fmt.Errorf("uint32: %d out of range", v)
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

// ==================== 格式化 ====================

func formatSigned(val any) string {
	switch v := val.(type) {
	case int32:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case int:
		return strconv.Itoa(v)
	default:
		return fmt.Sprintf("%v", val)
	}
}

func formatUnsigned(val any) string {
	switch v := val.(type) {
	case uint32:
		return strconv.FormatUint(uint64(v), 10)
	case uint64:
		return strconv.FormatUint(v, 10)
	case uint:
		return strconv.FormatUint(uint64(v), 10)
	default:
		return fmt.Sprintf("%v", val)
	}
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

// ==================== 类型转换 ====================

func toInt64(val any) (int64, error) {
	switch v := val.(type) {
	case float64:
		// JSON 数字默认解成 float64；只在整数值时接受，避免把 1.5 静默截成 1。
		if v != math.Trunc(v) {
			return 0, fmt.Errorf("non-integer value %v", v)
		}
		return int64(v), nil
	case float32:
		if float32(math.Trunc(float64(v))) != v {
			return 0, fmt.Errorf("non-integer value %v", v)
		}
		return int64(v), nil
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
		if v > math.MaxInt64 {
			return 0, fmt.Errorf("value %d overflows int64", v)
		}
		return int64(v), nil
	case uint32:
		return int64(v), nil
	case uint16:
		return int64(v), nil
	case uint8:
		return int64(v), nil
	case string:
		return strconv.ParseInt(v, 10, 64)
	default:
		return 0, fmt.Errorf("cannot convert %T to int64", val)
	}
}

func toUint64(val any) (uint64, error) {
	switch v := val.(type) {
	case float64:
		if v < 0 || v != math.Trunc(v) {
			return 0, fmt.Errorf("non-integer or negative value %v", v)
		}
		return uint64(v), nil
	case float32:
		if v < 0 || float32(math.Trunc(float64(v))) != v {
			return 0, fmt.Errorf("non-integer or negative value %v", v)
		}
		return uint64(v), nil
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
			return 0, fmt.Errorf("negative value %d", v)
		}
		return uint64(v), nil
	case int:
		if v < 0 {
			return 0, fmt.Errorf("negative value %d", v)
		}
		return uint64(v), nil
	case int32:
		if v < 0 {
			return 0, fmt.Errorf("negative value %d", v)
		}
		return uint64(v), nil
	case string:
		return strconv.ParseUint(v, 10, 64)
	default:
		return 0, fmt.Errorf("cannot convert %T to uint64", val)
	}
}
