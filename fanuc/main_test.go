// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package main

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"github.com/Ellisata/iot-gateway-sdk/driver"

	fanucdrv "github.com/Ellisata/iot-gateway-drivers/fanuc/fanucdrv"
)

// TestProtocolIdentityPinned 协议名与 scope 发布即冻结。
//
// 它们同时是 iot_protocol.name 与 device.protocol_id 的解析目标；改一个字，
// 存量设备的点位会静默采不到值，而界面上看不出任何异常。
func TestProtocolIdentityPinned(t *testing.T) {
	if protocolName != "FANUC.CIP" {
		t.Fatalf("protocolName = %q, want FANUC.CIP", protocolName)
	}
	if pluginScope != "fanuc" {
		t.Fatalf("pluginScope = %q, want fanuc", pluginScope)
	}
}

func TestMetaValid(t *testing.T) {
	m := meta()
	if err := m.Validate(); err != nil {
		t.Fatalf("Meta.Validate() = %v，插件会被网关拒绝加载", err)
	}
	if len(m.Types) == 0 {
		t.Fatal("Meta.Types 为空")
	}
	if len(m.TypeMap) == 0 {
		t.Fatal("Meta.TypeMap 为空")
	}
	for _, p := range m.Protocols {
		if !json.Valid(p.FormJSON) {
			t.Errorf("协议 %q 的 FormJSON 不是合法 JSON", p.Name)
		}
		if p.Discovery == nil {
			t.Errorf("协议 %q 应声明 Discovery（扫描能力见 fanucdrv.Discovery）", p.Name)
		}
		if p.PointDiscovery == nil {
			t.Errorf("协议 %q 应声明 PointDiscovery（点扫能力见 fanucdrv.PointDiscovery）", p.Name)
		}
		if !p.Writable {
			t.Errorf("协议 %q 应声明 Writable（驱动实现了 driver.Writer）", p.Name)
		}
	}
}

// ==================== 扫描能力 ====================

// TestProbersMatchDiscovery 声明了 Discovery 的协议必须有对应的 prober，反之亦然。
//
// driversdk.Serve 启动时也查这一条，但那时插件已经被网关拉起、失败表现为
// "这个插件唯一的协议一起不可用"；在 CI 里失败能直接定位到 main.go 漏配。
func TestProbersMatchDiscovery(t *testing.T) {
	ps := probers()
	declared := make(map[string]bool)
	for _, p := range meta().Protocols {
		if p.Discovery == nil {
			continue
		}
		declared[p.Name] = true
		if _, ok := ps[p.Name]; !ok {
			t.Errorf("协议 %q 声明了 Discovery 却没有对应的 prober"+
				"（漏配会让插件**启动即失败**）", p.Name)
		}
	}
	for name := range ps {
		if !declared[name] {
			t.Errorf("协议 %q 有 prober 但没声明 Discovery，它永远不会被调用", name)
		}
	}
	if len(declared) == 0 {
		t.Fatal("FANUC.CIP 应声明局域网扫描能力，否则设备扫描页的协议下拉里没有它")
	}
}

// TestDiscoverySpecUsesBothTargetModes 广播与逐主机两种目标都要扫。
//
// 只声明 broadcast 时，禁广播的受管网络上症状是"扫描正常结束但一台设备都没有"，
// 而用户完全无从判断是网络还是配置；只声明 unicast 则退化成 254 次数据报往返。
// 这个组合是刻意的，因此逐字钉住。
func TestDiscoverySpecUsesBothTargetModes(t *testing.T) {
	d := fanucdrv.Discovery()
	if d.DefaultPort != 44818 {
		t.Errorf("DefaultPort = %d，期望 44818（EtherNet/IP 标准端口）", d.DefaultPort)
	}
	if d.TargetMode != driver.TargetModeBoth {
		t.Errorf("TargetMode = %q，期望 %q", d.TargetMode, driver.TargetModeBoth)
	}
	if d.Transport != driver.ProbeTransportUDP {
		t.Errorf("Transport = %q，期望 %q —— ListIdentity 走 UDP；填 tcp 会让网关的 TCP 预筛把所有设备判死",
			d.Transport, driver.ProbeTransportUDP)
	}
	if d.Title == "" {
		t.Error("扫描声明缺少 Title")
	}
	if err := meta().Validate(); err != nil {
		t.Fatalf("Meta 校验失败（含扫描声明）: %v", err)
	}
}

// TestPointFindersMatchPointDiscovery 声明了点位扫描能力的协议必须有对应的
// finder，反之亦然 —— 与 TestProbersMatchDiscovery 同构。
//
// 漏配一个会让插件**启动即失败**（整个插件的所有协议一起不可用），而多配一个
// finder 永远不会被调用，留着只会让人以为"这个协议能点扫"。
func TestPointFindersMatchPointDiscovery(t *testing.T) {
	fs := finders()
	declared := make(map[string]bool)
	for _, p := range meta().Protocols {
		if p.PointDiscovery == nil {
			continue
		}
		declared[p.Name] = true
		if _, ok := fs[p.Name]; !ok {
			t.Errorf("协议 %q 声明了 PointDiscovery 却没有对应的 finder"+
				"（漏配会让插件**启动即失败**）", p.Name)
		}
	}
	for name := range fs {
		if !declared[name] {
			t.Errorf("协议 %q 有 finder 但没声明 PointDiscovery，它永远不会被调用", name)
		}
	}
	if len(declared) == 0 {
		t.Fatal("FANUC.CIP 应声明点位扫描能力，否则设备地址页不会出现「自动发现点位」按钮")
	}

	// 默认类型必须是**通用类型名**（TypeMap 的 key）：写成内部裸名 "int32"
	// 会被 Meta.Validate 拒掉，而那条报错指向 TypeMap，不容易一眼看出。
	pd := fanucdrv.PointDiscovery()
	if pd.DefaultCommonDataType != driver.TypeLong {
		t.Errorf("DefaultCommonDataType = %q，期望 %q", pd.DefaultCommonDataType, driver.TypeLong)
	}
	if _, ok := meta().TypeMap[pd.DefaultCommonDataType]; !ok {
		t.Errorf("DefaultCommonDataType %q 不是 TypeMap 的键 —— Meta.Validate 会拒掉插件",
			pd.DefaultCommonDataType)
	}
}

func TestFactoriesMatchProtocols(t *testing.T) {
	fs := factories()
	m := meta()

	for _, p := range m.Protocols {
		factory, ok := fs[p.Name]
		if !ok {
			t.Errorf("协议 %q 没有对应的工厂", p.Name)
			continue
		}
		if d := factory(); d == nil {
			t.Errorf("协议 %q 的工厂返回了 nil", p.Name)
		}
	}
	if len(fs) != len(m.Protocols) {
		t.Errorf("工厂数 %d != 协议数 %d", len(fs), len(m.Protocols))
	}
}

// TestFormFieldsMatchConfig 表单里的 field 必须与 protocol_json 的解析键一一对应。
//
// 错开一个字母的症状是"用户填了那个参数，但它根本不生效" —— 没有报错、
// 没有日志，表单看起来一切正常。
func TestFormFieldsMatchConfig(t *testing.T) {
	var form struct {
		Rule []struct {
			Field string `json:"field"`
		} `json:"rule"`
	}
	if err := json.Unmarshal([]byte(formFANUC), &form); err != nil {
		t.Fatalf("formFANUC 不是合法 JSON: %v", err)
	}

	var got []string
	for _, r := range form.Rule {
		got = append(got, r.Field)
	}
	want := fanucdrv.ConfigFieldNames()
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("表单字段 = %v, 配置字段 = %v", got, want)
	}
}
