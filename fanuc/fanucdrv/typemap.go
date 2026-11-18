// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package fanuc

import "github.com/Ellisata/iot-gateway-sdk/driver"

// commonTypeMap 通用类型名 → 内部类型名。
//
// 只映射 FANUC 真正有的三种 32 位数值类型：
//
//	Long  (32 位有符号) → int32   （类 0x6B）
//	DWord (32 位无符号) → uint32  （类 0x6B）
//	Float (32 位浮点)   → float32 （类 0x6C）
//
// Short/Word/Byte 等 16 位以下类型不映射：FANUC 的数值寄存器对象固定返回
// 32 位，把它们映射过来会让用户以为能按 16 位读，实际拿到的是整 32 位值。
// String 也不映射 —— 字符串寄存器走 0x6D，载荷格式尚未在真机核实。
var commonTypeMap = map[string]string{
	driver.TypeLong:  typeInt32,
	driver.TypeDWord: typeUint32,
	driver.TypeFloat: typeFloat32,
}

// CommonTypeMap 返回通用类型名 → 内部裸名，供 driversdk.Meta.TypeMap 使用。
// 返回副本，调用方修改不会影响包内真源。
func CommonTypeMap() map[string]string {
	out := make(map[string]string, len(commonTypeMap))
	for name, internal := range commonTypeMap {
		out[name] = internal
	}
	return out
}
