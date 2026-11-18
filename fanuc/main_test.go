// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package main

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"

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
		if p.Discovery != nil {
			t.Errorf("协议 %q 声明了 Discovery，但 P0 没有实现 prober", p.Name)
		}
		if p.PointDiscovery != nil {
			t.Errorf("协议 %q 声明了 PointDiscovery，但 P0 没有实现 finder", p.Name)
		}
		if !p.Writable {
			t.Errorf("协议 %q 应声明 Writable（驱动实现了 driver.Writer）", p.Name)
		}
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
