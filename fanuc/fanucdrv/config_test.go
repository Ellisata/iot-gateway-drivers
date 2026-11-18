// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package fanuc

import (
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := ParseConfig("")
	if err != nil {
		t.Fatalf("ParseConfig(\"\") = %v", err)
	}
	if cfg.Host != "127.0.0.1" || cfg.Port != 44818 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.TimeoutMS != 5000 || cfg.Timeout != 5*time.Second {
		t.Fatalf("unexpected timeout: %+v", cfg)
	}
	if cfg.MaxRequestsPerSecond != 10 {
		t.Fatalf("max rps = %d", cfg.MaxRequestsPerSecond)
	}
	if cfg.MaxRegistersPerRead != defaultMaxRegistersPerRead {
		t.Fatalf("max registers per read = %d, want %d", cfg.MaxRegistersPerRead, defaultMaxRegistersPerRead)
	}
}

func TestParseConfigStringsAndNumbers(t *testing.T) {
	// form-create 的不同控件可能把数字回传成字符串；两种形态都要接受。
	cfg, err := ParseConfig(`{"host":"192.168.1.10","port":"44818","timeoutMs":"8000","maxRequestsPerSecond":"20"}`)
	if err != nil {
		t.Fatalf("ParseConfig() = %v", err)
	}
	if cfg.Host != "192.168.1.10" || cfg.Port != 44818 || cfg.TimeoutMS != 8000 || cfg.MaxRequestsPerSecond != 20 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestParseConfigClamps(t *testing.T) {
	cfg, err := ParseConfig(`{"timeoutMs":1,"maxRequestsPerSecond":9999,"maxRegistersPerRead":9999}`)
	if err != nil {
		t.Fatalf("ParseConfig() = %v", err)
	}
	if cfg.TimeoutMS != 200 {
		t.Fatalf("timeout = %d, want clamped 200", cfg.TimeoutMS)
	}
	if cfg.MaxRequestsPerSecond != maxRequestsPerSec {
		t.Fatalf("rps = %d, want clamped %d", cfg.MaxRequestsPerSecond, maxRequestsPerSec)
	}
	if cfg.MaxRegistersPerRead != 124 {
		t.Fatalf("block size = %d, want clamped 124", cfg.MaxRegistersPerRead)
	}
}

func TestParseConfigBlockReadOff(t *testing.T) {
	cfg, err := ParseConfig(`{"maxRegistersPerRead":1}`)
	if err != nil {
		t.Fatalf("ParseConfig() = %v", err)
	}
	if cfg.MaxRegistersPerRead != 1 {
		t.Fatalf("block size = %d, want 1 (disabled)", cfg.MaxRegistersPerRead)
	}
}

func TestParseConfigInvalidJSON(t *testing.T) {
	if _, err := ParseConfig(`{"host":`); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestSameConfig(t *testing.T) {
	a, _ := ParseConfig(`{"host":"10.0.0.1","port":44818,"timeoutMs":5000,"maxRequestsPerSecond":10}`)
	b, _ := ParseConfig(`{"host":"10.0.0.1","port":44818,"timeoutMs":5000,"maxRequestsPerSecond":10}`)
	c, _ := ParseConfig(`{"host":"10.0.0.2","port":44818,"timeoutMs":5000,"maxRequestsPerSecond":10}`)
	if !sameConfig(a, b) {
		t.Fatal("identical configs should match")
	}
	if sameConfig(a, c) {
		t.Fatal("different hosts should not match")
	}
	if sameConfig(nil, a) || sameConfig(a, nil) {
		t.Fatal("nil config must not match")
	}
}

func TestConfigFieldNames(t *testing.T) {
	got := ConfigFieldNames()
	sort.Strings(got)
	want := []string{"host", "maxRegistersPerRead", "maxRequestsPerSecond", "port", "timeoutMs"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ConfigFieldNames() = %v, want %v", got, want)
	}
}
