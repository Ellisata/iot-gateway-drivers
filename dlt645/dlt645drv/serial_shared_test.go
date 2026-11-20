// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package dlt645

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/goburrow/serial"

	"github.com/Ellisata/iot-gateway-sdk/driver"
	"github.com/Ellisata/iot-gateway-sdk/driversdk/serialbus"
)

// newTestSerialClient 构造一个独立的串口传输（drain 等单测用）。
// 用独立的 Bus，避免污染进程级的 serialPorts。
func newTestSerialClient(port io.ReadWriteCloser) *serialClient {
	bus := serialbus.New[io.ReadWriteCloser]()
	h, err := bus.Acquire(
		serialbus.Key{Port: "test-port", Baud: 2400, DataBits: 8, StopBits: 1, Parity: "E"},
		func() (io.ReadWriteCloser, error) { return port, nil },
	)
	if err != nil {
		panic("构造测试串口失败: " + err.Error())
	}
	return &serialClient{bus: h}
}

// fakeRecordedPort 记录打开次数与关闭次数的端口替身。
type fakeRecordedPort struct {
	cfg    serial.Config
	closed atomic.Int32
}

func (p *fakeRecordedPort) Open(*serial.Config) error   { return nil }
func (p *fakeRecordedPort) Read([]byte) (int, error)    { return 0, serial.ErrTimeout }
func (p *fakeRecordedPort) Write(b []byte) (int, error) { return len(b), nil }
func (p *fakeRecordedPort) Close() error {
	p.closed.Add(1)
	return nil
}

// fakeSerialFactory 替换 openSerialPort，统计 opens 并交出可观察的端口。
type fakeSerialFactory struct {
	mu     sync.Mutex
	opens  []*fakeRecordedPort
	failOn bool
}

func (f *fakeSerialFactory) open(cfg *serial.Config) (serial.Port, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failOn {
		return nil, errors.New("no such port")
	}
	p := &fakeRecordedPort{cfg: *cfg}
	f.opens = append(f.opens, p)
	return p, nil
}

func (f *fakeSerialFactory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.opens)
}

func (f *fakeSerialFactory) opened(i int) *fakeRecordedPort {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens[i]
}

// stubSerialOpener 替换打开函数，返回可观察的工厂。
func stubSerialOpener(t *testing.T) *fakeSerialFactory {
	t.Helper()
	f := &fakeSerialFactory{}
	old := openSerialPort
	openSerialPort = f.open
	t.Cleanup(func() { openSerialPort = old })
	return f
}

// testSerialSeq 让同一包内的用例各用各的端口键，互不干扰。
var testSerialSeq atomic.Int64

func uniqueTestPort() string {
	return fmt.Sprintf("COM%d", 100+int(testSerialSeq.Add(1)))
}

// testSerialConfig 造一份串口配置。端口名只用字符串，打开已被替身接管。
func testSerialConfig(port string) *DLT645Config {
	cfg := DefaultDLT645Config(TransportSerial)
	cfg.ComPort = port
	return cfg
}

// 同一端口、参数相同的两个会话必须共享同一个句柄与总线锁。
//
// 这是本修复的全部意义：各开一个句柄时，同一个 tty 上两个读端会瓜分应答字节、
// 竞争 read 拿到 EAGAIN，且任一方 Close 恢复 termios 都会打断另一方（见
// SDK driversdk/serialbus 的包注释）。
func TestSerialPortSharedAcrossSessions(t *testing.T) {
	f := stubSerialOpener(t)
	port := uniqueTestPort()

	// 第二份配置刻意不写参数（走默认值），验"省略 = 默认 2400/E"与"显式默认"
	// 是同一个端口 —— 若默认值只在打开时套、不在建键时套，两者会各开一个句柄。
	cfgA := testSerialConfig(port)
	cfgB := testSerialConfig(port)
	cfgB.BaudRate, cfgB.DataBits, cfgB.StopBits, cfgB.Parity = 0, 0, 0, ""

	a, err := newSerialClient(cfgA, nil)
	if err != nil {
		t.Fatalf("首个会话连接失败: %v", err)
	}
	b, err := newSerialClient(cfgB, nil)
	if err != nil {
		t.Fatalf("第二个会话连接失败: %v", err)
	}

	// 每个会话有自己的 serialClient 包装（dirty 等状态是会话级的），
	// 共享的是底层 serialbus.Handle。
	if a.(*serialClient).bus != b.(*serialClient).bus {
		t.Fatal("同一端口、相同参数的两个会话没有共享同一个句柄")
	}
	if got := f.count(); got != 1 {
		t.Errorf("端口被打开 %d 次，want 1（共享句柄）", got)
	}

	// 最后一个引用关闭前，端口不得关闭。
	opened := f.opened(0)
	if err := a.Close(); err != nil {
		t.Fatalf("Close(1) 失败: %v", err)
	}
	if got := opened.closed.Load(); got != 0 {
		t.Errorf("关掉一个会话就把端口关了（closed=%d），另一个会话的句柄会失效", got)
	}
	if !b.IsConnected() {
		t.Error("关闭一个会话把共享句柄标记成断开了")
	}

	if err := b.Close(); err != nil {
		t.Fatalf("Close(2) 失败: %v", err)
	}
	if got := opened.closed.Load(); got != 1 {
		t.Errorf("最后一个引用释放后端口关闭次数 = %d, want 1", got)
	}
}

// 同端口不同参数必须是明确的配置错误，而不是悄悄再开一个句柄。
func TestSerialPortParamsConflict(t *testing.T) {
	f := stubSerialOpener(t)
	port := uniqueTestPort()

	first, err := newSerialClient(testSerialConfig(port), nil)
	if err != nil {
		t.Fatalf("首个会话连接失败: %v", err)
	}
	defer first.Close()

	cfg := testSerialConfig(port)
	cfg.BaudRate = 9600
	_, err = newSerialClient(cfg, nil)
	if err == nil {
		t.Fatal("同端口不同波特率应当报配置错误，实际打开了第二个句柄")
	}
	for _, want := range []string{port, "baud=9600", "baud=2400", "相同的串口参数"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息 %q 未包含 %q", err.Error(), want)
		}
	}
	if got := f.count(); got != 1 {
		t.Errorf("端口被打开 %d 次，want 1（参数冲突时不得再开句柄）", got)
	}
}

// 打开失败的错误里必须带上统一提示：现场看到的原始错误既不说明是什么操作，
// 也不给任何可照做的动作。
func TestSerialOpenFailureHint(t *testing.T) {
	f := stubSerialOpener(t)
	f.failOn = true

	_, err := newSerialClient(testSerialConfig(uniqueTestPort()), nil)
	if err == nil {
		t.Fatal("打开失败时应当返回错误")
	}
	if !strings.Contains(err.Error(), driver.SerialOpenFailureHint) {
		t.Errorf("错误信息缺少串口占用提示: %q", err.Error())
	}
}

// lockedProxyPort 记录 Lock/Unlock/Close 的代理字节流替身。
type lockedProxyPort struct {
	locked   int
	unlocked int
	closed   bool
}

func (p *lockedProxyPort) Read([]byte) (int, error)    { return 0, serial.ErrTimeout }
func (p *lockedProxyPort) Write(b []byte) (int, error) { return len(b), nil }
func (p *lockedProxyPort) Close() error                { p.closed = true; return nil }
func (p *lockedProxyPort) Lock()                       { p.locked++ }
func (p *lockedProxyPort) Unlock()                     { p.unlocked++ }

// 注入的 opener 必须被用来取得字节流；资源的 Lock/Unlock 要经 serialbus
// 的总线锁转发（代理模式下的跨进程锁就靠它）。
func TestSerialUsesInjectedOpener(t *testing.T) {
	port := &lockedProxyPort{}
	var gotOpts driver.SerialOpenOptions
	opener := func(opts driver.SerialOpenOptions) (io.ReadWriteCloser, error) {
		gotOpts = opts
		return port, nil
	}

	cfg := testSerialConfig(uniqueTestPort())
	tr, err := newSerialClient(cfg, opener)
	if err != nil {
		t.Fatalf("代理模式建连失败: %v", err)
	}
	defer tr.Close()

	if gotOpts.Port != cfg.ComPort || gotOpts.BaudRate != 2400 || gotOpts.Parity != "E" {
		t.Errorf("opener 收到的参数 = %+v", gotOpts)
	}
	if gotOpts.ReadTimeout != serialReadTimeout {
		t.Errorf("ReadTimeout = %v, want %v", gotOpts.ReadTimeout, serialReadTimeout)
	}

	tr.Lock()
	if port.locked != 1 {
		t.Errorf("总线锁没有转发到代理资源（locked=%d）", port.locked)
	}
	tr.Unlock()
	if port.unlocked != 1 {
		t.Errorf("总线解锁没有转发到代理资源（unlocked=%d）", port.unlocked)
	}

	if err := tr.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
	if !port.closed {
		t.Error("最后一个引用释放后没有关闭代理字节流")
	}
}
