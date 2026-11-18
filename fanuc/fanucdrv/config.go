// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package fanuc

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	cipcore "github.com/Ellisata/iot-gateway-drivers/fanuc/cipcore"
)

// 默认值。
const (
	defaultPort       = 44818 // EtherNet/IP 显式消息标准端口
	defaultTimeoutMS  = 5000
	defaultMaxRPS     = 10
	maxRequestsPerSec = 50

	// defaultMaxRegistersPerRead 块读默认携带的连续寄存器数。
	//
	// 取 100 而不是协议上限 124：给不同固件的实现差异留一点余量，单块响应
	// 400 字节，远小于封装层容量。设为 1 即关闭块读，逐寄存器单读。
	defaultMaxRegistersPerRead = 100

	// readBudget 单次 Read 的总预算。
	//
	// 必须显著低于网关给插件的 Read 超时（pluginhost 的 readTimeoutMs，默认
	// 60s）。越过的表现是网关判未连接 → 重连 → 重新探测，比慢一轮糟得多。
	readBudget = 45 * time.Second
)

// Config FANUC CIP 协议配置（对应 device.protocol_json）。
type Config struct {
	Host string // 机器人 IP 或主机名
	Port int    // 默认 44818

	TimeoutMS int
	Timeout   time.Duration

	// MaxRequestsPerSecond 请求限流。
	//
	// 显式消息与机器人自身的运动控制共用 CPU；现场还可能同时跑示教器和
	// 上位机。默认 10 次/秒是保守值 —— 优先级是"不打扰产线"，其次才是采集速度。
	MaxRequestsPerSecond int

	// MaxRegistersPerRead 单次 Get_Attribute_All 块读携带的连续寄存器数（1..124）。
	//
	// 1 = 关闭块读，逐寄存器单读。块读把一轮的帧数从 N 降到 ceil(N/chunk)，
	// 是点位容量的主要杠杆；编码是 FANUC 厂商扩展，首次块读会用单点读交叉校验，
	// 不匹配或设备拒绝时自动退回单读（见 driver.Read）。
	MaxRegistersPerRead int
}

// DefaultConfig 返回默认配置。
func DefaultConfig() *Config {
	return &Config{
		Host:                 "127.0.0.1",
		Port:                 defaultPort,
		TimeoutMS:            defaultTimeoutMS,
		Timeout:              defaultTimeoutMS * time.Millisecond,
		MaxRequestsPerSecond: defaultMaxRPS,
		MaxRegistersPerRead:  defaultMaxRegistersPerRead,
	}
}

// configRaw 匹配 protocol_json 的原始形状。字段名与插件表单的 field 一一对应。
type configRaw struct {
	Host                 string  `json:"host"`
	Port                 jsonInt `json:"port"`
	TimeoutMS            jsonInt `json:"timeoutMs"`
	MaxRequestsPerSecond jsonInt `json:"maxRequestsPerSecond"`
	MaxRegistersPerRead  jsonInt `json:"maxRegistersPerRead"`
}

// ConfigFieldNames 返回 protocol_json 接受的字段名（configRaw 的 json tag）。
//
// 导出给插件入口的测试用：表单里的每个 field 必须与它们逐一对应。错开一个
// 字母的症状是"用户填了那个参数，但它根本不生效" —— 没有报错、没有日志。
func ConfigFieldNames() []string {
	rt := reflect.TypeOf(configRaw{})
	out := make([]string, 0, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		out = append(out, name)
	}
	return out
}

// ParseConfig 从 device.protocol_json 解析配置。
//
// 未设置的字段用默认值；数值非法时回退默认值（对齐其它驱动的容错风格）。
// host 保持原样（空白由 client 层判定），因为"host 为空"是构造默认配置时
// 的合法中间态。
func ParseConfig(protocolJSON string) (*Config, error) {
	cfg := DefaultConfig()
	if strings.TrimSpace(protocolJSON) == "" {
		return cfg, nil
	}

	var raw configRaw
	if err := json.Unmarshal([]byte(protocolJSON), &raw); err != nil {
		return nil, fmt.Errorf("fanuc config: invalid JSON: %w", err)
	}

	if h := strings.TrimSpace(raw.Host); h != "" {
		cfg.Host = h
	}
	if raw.Port.set && raw.Port.val > 0 && raw.Port.val <= 65535 {
		cfg.Port = raw.Port.val
	}
	if raw.TimeoutMS.set && raw.TimeoutMS.val > 0 {
		cfg.TimeoutMS = raw.TimeoutMS.val
	}
	if raw.MaxRequestsPerSecond.set && raw.MaxRequestsPerSecond.val > 0 {
		cfg.MaxRequestsPerSecond = raw.MaxRequestsPerSecond.val
	}
	if raw.MaxRegistersPerRead.set && raw.MaxRegistersPerRead.val > 0 {
		cfg.MaxRegistersPerRead = raw.MaxRegistersPerRead.val
	}

	// ---- 钳制 ----
	// 超时下限 200ms：再短连一次 RegisterSession + 一次显式请求都完不成。
	// 上限 30s：低于网关切给插件的 connect/read 超时量级。
	cfg.TimeoutMS = clamp(cfg.TimeoutMS, 200, 30000)
	cfg.Timeout = time.Duration(cfg.TimeoutMS) * time.Millisecond
	cfg.MaxRequestsPerSecond = clamp(cfg.MaxRequestsPerSecond, 1, maxRequestsPerSec)
	cfg.MaxRegistersPerRead = clamp(cfg.MaxRegistersPerRead, 1, cipcore.MaxBlockRegisters)

	return cfg, nil
}

// sameConfig 判断两份配置的关键连接参数是否一致，用于 Ping 复用已有连接。
func sameConfig(a, b *Config) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Host == b.Host && a.Port == b.Port &&
		a.TimeoutMS == b.TimeoutMS &&
		a.MaxRequestsPerSecond == b.MaxRequestsPerSecond &&
		a.MaxRegistersPerRead == b.MaxRegistersPerRead
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// jsonInt 容错整数：同时接受 JSON 数字与数字字符串。
//
// form-create 的不同控件（inputNumber / select / input）回传的 JSON 形态不一致；
// 声明成 int 时一份带字符串的表单会让整个 protocol_json 解析失败，而不是
// 那一个字段失效 —— 用户看到的会是"设备连不上"。
type jsonInt struct {
	set bool
	val int
}

func (j *jsonInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		return nil
	}
	var v int
	if _, err := fmt.Sscanf(s, "%d", &v); err != nil {
		// 认不出就当作没配，走默认值。只有影响速度/超时的数值，不会静默
		// 改变数据语义。
		return nil
	}
	j.set = true
	j.val = v
	return nil
}
