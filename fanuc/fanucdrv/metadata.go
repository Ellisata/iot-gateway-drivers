// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package fanuc

import (
	"sort"

	"github.com/Ellisata/iot-gateway-sdk/driver"
)

// NewDriver 创建 FANUC CIP 驱动实例（插件工厂用）。
func NewDriver() driver.Driver {
	return newFANUCDriver()
}

// Types 返回上报给网关的插件类型表（driversdk.Meta.Types）。
//
// 上报**全部**已注册类型，而不只是 CommonTypeMap 能到达的那些：网关的
// NormalizeAddressType 靠 reg.Get(data_type) 判断地址里存的内部名是否仍然
// 有效 —— 漏报一个类型，历史配置里写着它的点位就会被判为"漂移"、按
// common_data_type 改写成别的类型并静默回写库。
//
// 每项整体拷贝 *dt（Decode/Encode/Format 一并带走），只把 Name 换成裸名。
// 绝不能只填 Name/Kind/Size：driversdk.Serve 会用同一批指针重新注册进插件
// 进程的注册表，瘦身版会把 Decode 换成 nil，解析时 panic。
func Types() []*driver.DataType {
	cache := fanucTypeCache()
	out := make([]*driver.DataType, 0, len(cache))
	for bare, dt := range cache {
		if dt == nil {
			continue
		}
		copied := *dt
		copied.Name = bare
		out = append(out, &copied)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
