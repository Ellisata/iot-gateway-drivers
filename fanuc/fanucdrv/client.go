// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package fanuc

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	cipcore "github.com/Ellisata/iot-gateway-drivers/fanuc/cipcore"
)

// identityTimeout 连接前 ListIdentity 厂商校验的等待上限。
//
// 这是**尽力而为**的检查：部分控制器/网络不回 UDP ListIdentity 或禁了广播。
// 拿不到答案时继续建连（由后续的对象读写决定成败）；只有拿到明确答案且厂商
// 不是 FANUC 时才拒绝 —— 把驱动指到别的厂商的 E/IP 设备上并写入，是真正要
// 防的事故。
const identityTimeout = time.Second

// client EtherNet/IP 显式报文客户端（UCMM 无连接方式）。
//
// 生命周期：TCP(44818) → RegisterSession → 每次读写 SendRRData。
// 单次请求+响应内部持互斥锁；同一会话上的 CIP 封装层不适合多请求并发。
type client struct {
	mu        sync.Mutex
	conn      net.Conn
	timeout   time.Duration
	session   uint32
	connected bool
}

// newClient 建立连接并完成 Register Session。
func newClient(cfg *Config) (*client, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("fanuc: host is required")
	}
	// 先做厂商校验：它不发任何 CIP 请求，也不建立会话，指错设备时
	// 连 RegisterSession 都不会发生。
	if err := verifyVendor(cfg); err != nil {
		return nil, err
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeoutMS * time.Millisecond
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("fanuc: dial %s failed: %w", addr, err)
	}

	c := &client{
		conn:      conn,
		timeout:   timeout,
		connected: true,
	}
	if err := c.registerSession(); err != nil {
		conn.Close()
		c.connected = false
		return nil, err
	}
	return c, nil
}

// verifyVendor 用 UDP ListIdentity 确认对面是 FANUC（厂商号 591）。
//
// 不确定（超时、无应答、帧解析失败）时**放行**：这是防误连的补充检查，
// 不是连通性判据，部分控制器/网络并不回 UDP ListIdentity。但只要拿到了明确的
// 身份且厂商号不是 FANUC，就拒绝连接 —— 别的厂商的 E/IP 设备同样可能挂着
// 0x6B 类（例如 Logix 的 Symbol Object 就是 0x6B），继续读写属于真实风险。
func verifyVendor(cfg *Config) error {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	conn, err := net.DialTimeout("udp", addr, identityTimeout)
	if err != nil {
		logDebug("list identity dial %s failed: %v", addr, err)
		return nil
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(identityTimeout)); err != nil {
		return nil
	}
	if _, err := conn.Write(cipcore.BuildListIdentity()); err != nil {
		logDebug("list identity write %s failed: %v", addr, err)
		return nil
	}

	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil {
		// 无应答是正常情况（禁广播/只答 TCP），不是错误。
		logDebug("list identity read %s failed: %v", addr, err)
		return nil
	}
	ids, err := cipcore.ParseListIdentity(buf[:n])
	if err != nil {
		logDebug("list identity parse %s failed: %v", addr, err)
		return nil
	}
	if len(ids) == 0 {
		return nil
	}
	for _, id := range ids {
		if id.VendorID == cipcore.VendorFANUC {
			logf("verified FANUC device %s (product %q revision %d.%d)",
				addr, id.ProductName, id.RevisionMajor, id.RevisionMinor)
			return nil
		}
	}
	return fmt.Errorf("fanuc: %s 不是 FANUC 设备（ListIdentity 厂商号 %d，期望 %d），拒绝连接",
		addr, ids[0].VendorID, cipcore.VendorFANUC)
}

// registerSession 执行 Register Session 握手，取得会话句柄。
func (c *client) registerSession() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 握手必须有超时。对端「TCP 端口开着、但不讲 EtherNet/IP」是真实情形
	//（把 FANUC 的 PC Interface 端口 60008 当成 E/IP 端口配进来就是这样）：
	// dial 成功，而 RegisterSession 的应答永远不来。不设 deadline 时
	// readFrameLocked 会永久阻塞 —— 网关侧 ping 到 30s 放弃，插件侧这个
	// goroutine 却一直挂着，重复几次就会累积超时并触发卡死重启。
	// 与 roundTripLocked 同一口径：整个握手共用一个超时预算。
	if err := c.conn.SetDeadline(time.Now().Add(c.timeout)); err != nil {
		return fmt.Errorf("fanuc: set handshake deadline failed: %w", err)
	}

	frame := cipcore.BuildRegisterSession(0, cipcore.EncapProtocolVersion)
	if err := c.writeAllLocked(frame); err != nil {
		return fmt.Errorf("fanuc: register session write failed: %w", err)
	}
	resp, err := c.readFrameLocked()
	if err != nil {
		return fmt.Errorf("fanuc: register session response failed: %w", err)
	}
	cmd, session, status, _, err := cipcore.ParseEncapResponse(resp)
	if err != nil {
		return err
	}
	if status != 0 {
		return fmt.Errorf("fanuc: register session status 0x%08X", status)
	}
	if cmd != cipcore.CmdRegisterSession {
		return fmt.Errorf("fanuc: unexpected register session response command 0x%04X", cmd)
	}
	c.session = session
	logf("client connected (session 0x%08X)", session)
	return nil
}

// readAttr 读取一个对象的属性（Get_Attribute_Single）。
//
// 返回通用状态错误时连接仍然可用；传输层错误会让 client 标记断开。
func (c *client) readAttr(class uint16, index uint16) ([]byte, error) {
	return c.readAttrUntil(class, index, time.Now().Add(c.timeout))
}

// readAttrUntil 与 readAttr 相同，但整次请求+响应必须在 deadline 之前完成。
//
// 点位扫描用这条路径：扫描的预算（网关钳到 7s）比单次请求超时（默认 5s）只大
// 一点，一次慢请求就可能把整次 RPC 拖过界；而超预算会累积插件卡死计数。
func (c *client) readAttrUntil(class uint16, index uint16, deadline time.Time) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil || !c.connected {
		return nil, fmt.Errorf("fanuc: not connected")
	}
	resp, err := c.roundTripUntilLocked(cipcore.BuildGetAttributeSingle(class, registerInstance, index), deadline)
	if err != nil {
		return nil, err
	}
	return cipcore.ParseExplicitResponse(resp, cipcore.ServiceGetAttributeSingle)
}

// readBlock 从 start 号寄存器起连续读取 count 个（Get_Attribute_All，服务 0x01）。
//
// 返回的数据是 count 个 4 字节值按寄存器号升序拼接。设备拒绝时返回通用状态
// 错误（连接仍可用），由驱动决定回退单读还是暂时禁用块读。
func (c *client) readBlock(class uint16, start uint16, count int) ([]byte, error) {
	return c.readBlockUntil(class, start, count, time.Now().Add(c.timeout))
}

// readBlockUntil 与 readBlock 相同，但整次请求+响应必须在 deadline 之前完成。
func (c *client) readBlockUntil(class uint16, start uint16, count int, deadline time.Time) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil || !c.connected {
		return nil, fmt.Errorf("fanuc: not connected")
	}
	resp, err := c.roundTripUntilLocked(cipcore.BuildGetAttributeBlock(class, start, count), deadline)
	if err != nil {
		return nil, err
	}
	return cipcore.ParseExplicitResponse(resp, cipcore.ServiceGetAttributeAll)
}

// writeAttr 写入一个对象的属性（Set_Attribute_Single）。
func (c *client) writeAttr(class uint16, index uint16, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil || !c.connected {
		return fmt.Errorf("fanuc: not connected")
	}
	resp, err := c.roundTripLocked(cipcore.BuildSetAttributeSingle(class, registerInstance, index, data))
	if err != nil {
		return err
	}
	_, err = cipcore.ParseExplicitResponse(resp, cipcore.ServiceSetAttributeSingle)
	return err
}

// roundTripLocked 发一条 SendRRData 并取回 CIP 响应字节（调用方持有 c.mu）。
func (c *client) roundTripLocked(cipReq []byte) ([]byte, error) {
	return c.roundTripUntilLocked(cipReq, time.Now().Add(c.timeout))
}

// roundTripUntilLocked 与 roundTripLocked 相同，但 deadline 由调用方给定。
//
// 实际套接字截止时刻取「deadline」与「now + c.timeout」的**较小者**：调用方
// 给的预算再大，单次请求也不该超过设备配置的超时。
func (c *client) roundTripUntilLocked(cipReq []byte, deadline time.Time) ([]byte, error) {
	if limit := time.Now().Add(c.timeout); limit.Before(deadline) {
		deadline = limit
	}
	if err := c.conn.SetDeadline(deadline); err != nil {
		c.failLocked()
		return nil, fmt.Errorf("fanuc: set deadline failed: %w", err)
	}
	frame := cipcore.BuildSendRRData(c.session, cipReq)
	if err := c.writeAllLocked(frame); err != nil {
		c.failLocked()
		return nil, fmt.Errorf("fanuc: write failed: %w", err)
	}
	resp, err := c.readFrameLocked()
	if err != nil {
		c.failLocked()
		return nil, fmt.Errorf("fanuc: read failed: %w", err)
	}
	cmd, _, status, payload, err := cipcore.ParseEncapResponse(resp)
	if err != nil {
		c.failLocked()
		return nil, err
	}
	if status != 0 {
		c.failLocked()
		return nil, fmt.Errorf("fanuc: encap status 0x%08X", status)
	}
	if cmd != cipcore.CmdSendRRData {
		c.failLocked()
		return nil, fmt.Errorf("fanuc: unexpected response command 0x%04X", cmd)
	}
	return cipcore.ParseSendRRData(payload)
}

// writeAllLocked 写完整帧。
func (c *client) writeAllLocked(b []byte) error {
	for len(b) > 0 {
		n, err := c.conn.Write(b)
		if err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

// readFrameLocked 读一个完整的封装帧。
func (c *client) readFrameLocked() ([]byte, error) {
	header := make([]byte, cipcore.EncapHeaderSize)
	if _, err := io.ReadFull(c.conn, header); err != nil {
		return nil, err
	}
	length := int(binary.LittleEndian.Uint16(header[2:4]))
	payload := make([]byte, length)
	if _, err := io.ReadFull(c.conn, payload); err != nil {
		return nil, err
	}
	return append(header, payload...), nil
}

// failLocked 标记连接失效并释放套接字。
func (c *client) failLocked() {
	if c.conn != nil {
		c.conn.Close()
	}
	c.connected = false
}

// isConnected 返回连接状态。
func (c *client) isConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

// close 注销会话并关闭连接。
func (c *client) close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		c.connected = false
		return nil
	}
	if c.connected {
		// 尽力通知设备释放会话；失败不影响本地关闭。
		_ = c.conn.SetDeadline(time.Now().Add(200 * time.Millisecond))
		_, _ = c.conn.Write(cipcore.BuildUnregisterSession(c.session))
	}
	err := c.conn.Close()
	c.conn = nil
	c.connected = false
	return err
}
