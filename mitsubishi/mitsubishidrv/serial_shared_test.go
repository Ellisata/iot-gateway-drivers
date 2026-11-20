// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package mitsubishi

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/goburrow/serial"
)

// sharedFakePort 记录关闭次数的串口替身。
type sharedFakePort struct {
	closed atomic.Int32
}

func (p *sharedFakePort) Open(*serial.Config) error   { return nil }
func (p *sharedFakePort) Read([]byte) (int, error)    { return 0, serial.ErrTimeout }
func (p *sharedFakePort) Write(b []byte) (int, error) { return len(b), nil }
func (p *sharedFakePort) Close() error {
	p.closed.Add(1)
	return nil
}

type sharedFakeFactory struct {
	mu    sync.Mutex
	ports []*sharedFakePort
	fail  bool
}

func (f *sharedFakeFactory) open(*serial.Config) (serial.Port, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, errors.New("no such port")
	}
	p := &sharedFakePort{}
	f.ports = append(f.ports, p)
	return p, nil
}

func (f *sharedFakeFactory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.ports)
}

func (f *sharedFakeFactory) port(i int) *sharedFakePort {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ports[i]
}

func stubSharedOpener(t *testing.T) *sharedFakeFactory {
	t.Helper()
	f := &sharedFakeFactory{}
	old := openSerialPort
	openSerialPort = f.open
	t.Cleanup(func() { openSerialPort = old })
	return f
}

var sharedSerialSeq atomic.Int64

func uniqueSharedPort() string {
	return fmt.Sprintf("COM%d", 300+int(sharedSerialSeq.Add(1)))
}

// 同一端口、参数相同的两个会话必须共享同一个句柄与总线锁；
// 省略参数（走默认值）与显式默认值必须被算成同一个端口。
func TestSerialSharedAcrossSessions(t *testing.T) {
	f := stubSharedOpener(t)
	port := uniqueSharedPort()

	cfgA := DefaultMCConfig()
	cfgA.Transport = TransportSerial
	cfgA.ComPort = port

	cfgB := DefaultMCConfig()
	cfgB.Transport = TransportSerial
	cfgB.ComPort = port
	cfgB.BaudRate, cfgB.DataBits, cfgB.StopBits, cfgB.Parity = 0, 0, 0, ""

	a, err := newMCSerialClient(cfgA, nil)
	if err != nil {
		t.Fatalf("首个会话连接失败: %v", err)
	}
	b, err := newMCSerialClient(cfgB, nil)
	if err != nil {
		t.Fatalf("第二个会话连接失败: %v", err)
	}

	ca, cb := a.(*mcSerialClient), b.(*mcSerialClient)
	if ca.bus != cb.bus {
		t.Fatal("同一端口、相同参数的两个会话没有共享同一个句柄")
	}
	if got := f.count(); got != 1 {
		t.Errorf("端口被打开 %d 次，want 1（共享句柄）", got)
	}

	opened := f.port(0)
	if err := ca.Close(); err != nil {
		t.Fatalf("Close(1) 失败: %v", err)
	}
	if got := opened.closed.Load(); got != 0 {
		t.Errorf("关掉一个会话就把端口关了（closed=%d）", got)
	}
	if err := cb.Close(); err != nil {
		t.Fatalf("Close(2) 失败: %v", err)
	}
	if got := opened.closed.Load(); got != 1 {
		t.Errorf("最后一个引用释放后端口关闭次数 = %d, want 1", got)
	}
}

// 同端口不同参数必须是明确的配置错误，而不是悄悄再开一个句柄。
func TestSerialParamsConflict(t *testing.T) {
	f := stubSharedOpener(t)
	port := uniqueSharedPort()

	cfg := DefaultMCConfig()
	cfg.Transport = TransportSerial
	cfg.ComPort = port
	first, err := newMCSerialClient(cfg, nil)
	if err != nil {
		t.Fatalf("首个会话连接失败: %v", err)
	}
	defer first.Close()

	conflict := DefaultMCConfig()
	conflict.Transport = TransportSerial
	conflict.ComPort = port
	conflict.BaudRate = 19200
	_, err = newMCSerialClient(conflict, nil)
	if err == nil {
		t.Fatal("同端口不同波特率应当报配置错误")
	}
	for _, want := range []string{port, "baud=19200", "baud=9600", "相同的串口参数"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息 %q 未包含 %q", err.Error(), want)
		}
	}
	if got := f.count(); got != 1 {
		t.Errorf("端口被打开 %d 次，want 1", got)
	}
}
