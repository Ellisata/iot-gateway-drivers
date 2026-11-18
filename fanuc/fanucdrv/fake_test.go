// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package fanuc

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"sync"
	"testing"
)

// fakeFANUC 一个最小的 FANUC EtherNet/IP 模拟器。
//
// **刻意不复用 cipcore 的请求构造与解析**：假设备与驱动是同一份理解的两次
// 独立实现时，测试才是一次真正的交叉验证 —— 两边在同一个字节上写错
// （尤其是路径长度按字计、16 位段补 pad）会两边一起通过、现场一起失败。
//
// 支持的能力画像：
//
//	vendor != VendorFANUC → ListIdentity 报告别的厂商（厂商校验用例）
//	noRealClass = true    → 类 0x6C 回 0x08（R-30iA 早期软件画像）
//	noRealClass = false   → 整型/实型对象都支持（R-30iB / Plus 画像）
type fakeFANUC struct {
	ln  net.Listener
	udp *net.UDPConn

	vendor      uint16
	product     string
	noRealClass bool

	// 块读（服务 0x01）的两种故障画像：
	//	blockUnsupported = true  设备不认块读 → 0x08
	//	blockShift       = true  设备忽略起始属性、总从 R[1] 返回 → 数据错位
	// 驱动应在两种情况下都退回单读，且第二种必须被交叉校验发现。
	blockUnsupported bool
	blockShift       bool

	mu      sync.Mutex
	conns   map[net.Conn]struct{}
	ints    map[uint16]int32
	reals   map[uint16]float32
	reads   map[uint32]int
	blocks  int
	singles int
	closed  bool
}

func newFakeFANUC(t testing.TB, vendor uint16, noRealClass bool) *fakeFANUC {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		ln.Close()
		t.Fatalf("listen udp %d: %v", port, err)
	}
	f := &fakeFANUC{
		ln:          ln,
		udp:         udp,
		vendor:      vendor,
		product:     "R-30iB Plus",
		noRealClass: noRealClass,
		conns:       make(map[net.Conn]struct{}),
		ints:        make(map[uint16]int32),
		reals:       make(map[uint16]float32),
		reads:       make(map[uint32]int),
	}
	go f.acceptLoop()
	go f.udpLoop()
	t.Cleanup(f.close)
	return f
}

func (f *fakeFANUC) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeFANUC) setInt(index uint16, v int32) {
	f.mu.Lock()
	f.ints[index] = v
	f.mu.Unlock()
}

func (f *fakeFANUC) setReal(index uint16, v float32) {
	f.mu.Lock()
	f.reals[index] = v
	f.mu.Unlock()
}

func (f *fakeFANUC) intValue(index uint16) (int32, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.ints[index]
	return v, ok
}

func (f *fakeFANUC) realValue(index uint16) (float32, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.reals[index]
	return v, ok
}

func (f *fakeFANUC) readCount(class, index uint16) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads[uint32(class)<<16|uint32(index)]
}

func (f *fakeFANUC) setBlockUnsupported(v bool) {
	f.mu.Lock()
	f.blockUnsupported = v
	f.mu.Unlock()
}

func (f *fakeFANUC) setBlockShift(v bool) {
	f.mu.Lock()
	f.blockShift = v
	f.mu.Unlock()
}

func (f *fakeFANUC) blockCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.blocks
}

func (f *fakeFANUC) singleCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.singles
}

func (f *fakeFANUC) close() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	conns := make([]net.Conn, 0, len(f.conns))
	for c := range f.conns {
		conns = append(conns, c)
	}
	f.mu.Unlock()

	f.ln.Close()
	f.udp.Close()
	for _, c := range conns {
		c.Close()
	}
}

func (f *fakeFANUC) acceptLoop() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			conn.Close()
			return
		}
		f.conns[conn] = struct{}{}
		f.mu.Unlock()
		go f.handleConn(conn)
	}
}

func (f *fakeFANUC) udpLoop() {
	buf := make([]byte, 2048)
	for {
		n, from, err := f.udp.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if n < 24 || binary.LittleEndian.Uint16(buf[0:2]) != 0x0063 {
			continue
		}
		_, _ = f.udp.WriteToUDP(f.identityResponse(), from)
	}
}

func (f *fakeFANUC) handleConn(conn net.Conn) {
	defer func() {
		conn.Close()
		f.mu.Lock()
		delete(f.conns, conn)
		f.mu.Unlock()
	}()

	for {
		header := make([]byte, 24)
		if _, err := io.ReadFull(conn, header); err != nil {
			return
		}
		length := int(binary.LittleEndian.Uint16(header[2:4]))
		payload := make([]byte, length)
		if _, err := io.ReadFull(conn, payload); err != nil {
			return
		}

		cmd := binary.LittleEndian.Uint16(header[0:2])
		session := binary.LittleEndian.Uint32(header[4:8])
		var resp []byte
		switch cmd {
		case 0x0065: // RegisterSession：回会话句柄 1，载荷原样回显
			resp = buildEncapFrame(0x0065, 1, payload)
		case 0x006F: // SendRRData
			resp = f.sendRRDataResp(session, payload)
		case 0x0063: // ListIdentity（TCP 也支持，虽然驱动只用 UDP）
			resp = f.identityResponse()
		case 0x0066: // UnregisterSession
			return
		default:
			return
		}
		if _, err := conn.Write(resp); err != nil {
			return
		}
	}
}

func (f *fakeFANUC) sendRRDataResp(session uint32, cpf []byte) []byte {
	cipReq := extractCIPItem(cpf)
	cipResp := f.handleCIP(cipReq)

	body := make([]byte, 8) // 接口句柄(4) + 超时(2) + 项数(2)
	binary.LittleEndian.PutUint16(body[6:8], 2)
	body = append(body, 0, 0, 0, 0) // Null 地址项
	body = append(body, 0xB2, 0x00, byte(len(cipResp)), byte(len(cipResp)>>8))
	body = append(body, cipResp...)
	return buildEncapFrame(0x006F, session, body)
}

func (f *fakeFANUC) handleCIP(req []byte) []byte {
	if len(req) < 2 {
		return buildCIPResponse(0x00, 0x03, nil)
	}
	service := req[0]
	pathLen := int(req[1]) * 2
	if len(req) < 2+pathLen {
		return buildCIPResponse(service, 0x04, nil)
	}
	class, instance, attr, err := parseCIPPath(req[2 : 2+pathLen])
	if err != nil {
		return buildCIPResponse(service, 0x04, nil)
	}
	data := req[2+pathLen:]

	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads[uint32(class)<<16|uint32(attr)]++

	switch service {
	case 0x01:
		return f.getAttributeAll(class, instance, attr)
	case 0x0E:
		if instance != 1 {
			return buildCIPResponse(service, 0x05, nil)
		}
		f.singles++
		return f.getAttribute(class, attr)
	case 0x10:
		if instance != 1 {
			return buildCIPResponse(service, 0x05, nil)
		}
		return f.setAttribute(class, attr, data)
	default:
		return buildCIPResponse(service, 0x08, nil)
	}
}

// getAttributeAll 模拟 FANUC 块读：instance 高字节 = 数量，低字节 = 实例 1，
// attribute = 起始寄存器号。blockShift 画像故意忽略起始号，用来验证驱动的交叉校验。
func (f *fakeFANUC) getAttributeAll(class uint16, instance uint16, start uint16) []byte {
	count := int(instance >> 8)
	if instance&0xFF != 1 || count < 1 {
		return buildCIPResponse(0x01, 0x05, nil)
	}
	f.blocks++ // 计数的是「收到的块读请求」，失败的也算
	if f.blockUnsupported {
		return buildCIPResponse(0x01, 0x08, nil)
	}
	if f.blockShift {
		start = 1
	}

	buf := make([]byte, 0, count*4)
	for i := 0; i < count; i++ {
		idx := uint16(int(start) + i)
		switch class {
		case 0x6B:
			if _, ok := f.reals[idx]; ok {
				return buildCIPResponse(0x01, 0x14, nil)
			}
			v, ok := f.ints[idx]
			if !ok {
				return buildCIPResponse(0x01, 0x05, nil)
			}
			buf = append(buf, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
		case 0x6C:
			if f.noRealClass {
				return buildCIPResponse(0x01, 0x08, nil)
			}
			if _, ok := f.ints[idx]; ok {
				return buildCIPResponse(0x01, 0x14, nil)
			}
			v, ok := f.reals[idx]
			if !ok {
				return buildCIPResponse(0x01, 0x05, nil)
			}
			bits := floatBits(v)
			buf = append(buf, byte(bits), byte(bits>>8), byte(bits>>16), byte(bits>>24))
		default:
			return buildCIPResponse(0x01, 0x16, nil)
		}
	}
	return buildCIPResponse(0x01, 0x00, buf)
}

func (f *fakeFANUC) getAttribute(class, attr uint16) []byte {
	switch class {
	case 0x6B: // 整型寄存器
		if _, ok := f.reals[attr]; ok {
			return buildCIPResponse(0x0E, 0x14, nil) // 用整型对象读实型寄存器
		}
		v, ok := f.ints[attr]
		if !ok {
			return buildCIPResponse(0x0E, 0x05, nil)
		}
		buf := make([]byte, 4)
		binary.LittleEndian.PutUint32(buf, uint32(v))
		return buildCIPResponse(0x0E, 0x00, buf)
	case 0x6C: // 实型寄存器
		if f.noRealClass {
			return buildCIPResponse(0x0E, 0x08, nil)
		}
		if _, ok := f.ints[attr]; ok {
			return buildCIPResponse(0x0E, 0x14, nil)
		}
		v, ok := f.reals[attr]
		if !ok {
			return buildCIPResponse(0x0E, 0x05, nil)
		}
		buf := make([]byte, 4)
		binary.LittleEndian.PutUint32(buf, floatBits(v))
		return buildCIPResponse(0x0E, 0x00, buf)
	default:
		return buildCIPResponse(0x0E, 0x16, nil) // 对象不存在
	}
}

func (f *fakeFANUC) setAttribute(class, attr uint16, data []byte) []byte {
	if len(data) != 4 {
		return buildCIPResponse(0x10, 0x13, nil)
	}
	v := binary.LittleEndian.Uint32(data)
	switch class {
	case 0x6B:
		if _, ok := f.reals[attr]; ok {
			return buildCIPResponse(0x10, 0x14, nil)
		}
		f.ints[attr] = int32(v)
		return buildCIPResponse(0x10, 0x00, nil)
	case 0x6C:
		if f.noRealClass {
			return buildCIPResponse(0x10, 0x08, nil)
		}
		if _, ok := f.ints[attr]; ok {
			return buildCIPResponse(0x10, 0x14, nil)
		}
		f.reals[attr] = floatFromBits(v)
		return buildCIPResponse(0x10, 0x00, nil)
	default:
		return buildCIPResponse(0x10, 0x16, nil)
	}
}

// ==================== 帧构造（独立实现） ====================

func buildEncapFrame(cmd uint16, session uint32, payload []byte) []byte {
	h := make([]byte, 24)
	binary.LittleEndian.PutUint16(h[0:2], cmd)
	binary.LittleEndian.PutUint16(h[2:4], uint16(len(payload)))
	binary.LittleEndian.PutUint32(h[4:8], session)
	return append(h, payload...)
}

func buildCIPResponse(service, status byte, data []byte) []byte {
	out := []byte{service | 0x80, 0x00, status, 0x00}
	return append(out, data...)
}

func extractCIPItem(cpf []byte) []byte {
	if len(cpf) < 8 {
		return nil
	}
	count := int(binary.LittleEndian.Uint16(cpf[6:8]))
	offset := 8
	for i := 0; i < count && offset+4 <= len(cpf); i++ {
		itemType := binary.LittleEndian.Uint16(cpf[offset : offset+2])
		itemLen := int(binary.LittleEndian.Uint16(cpf[offset+2 : offset+4]))
		offset += 4
		if offset+itemLen > len(cpf) {
			return nil
		}
		if itemType == 0x00B2 {
			return cpf[offset : offset+itemLen]
		}
		offset += itemLen
	}
	return nil
}

// parseCIPPath 独立解析类/实例/属性逻辑段。
//
// 8 位段 2 字节；16 位段 4 字节（含末尾 pad）。这条规则写错的话，测试里的
// 路径解析会和驱动的路径构造一起错，所以这里按规范手写，不调用 cipcore。
func parseCIPPath(path []byte) (class, instance, attr uint16, err error) {
	for i := 0; i < len(path); {
		switch path[i] {
		case 0x20:
			if i+2 > len(path) {
				return 0, 0, 0, errors.New("truncated class segment")
			}
			class = uint16(path[i+1])
			i += 2
		case 0x21:
			if i+4 > len(path) {
				return 0, 0, 0, errors.New("truncated class segment")
			}
			class = binary.LittleEndian.Uint16(path[i+1 : i+3])
			i += 4
		case 0x24:
			if i+2 > len(path) {
				return 0, 0, 0, errors.New("truncated instance segment")
			}
			instance = uint16(path[i+1])
			i += 2

		case 0x25:
			if i+4 > len(path) {
				return 0, 0, 0, errors.New("truncated instance segment")
			}
			instance = binary.LittleEndian.Uint16(path[i+1 : i+3])
			i += 4
		case 0x30:
			if i+2 > len(path) {
				return 0, 0, 0, errors.New("truncated attribute segment")
			}
			attr = uint16(path[i+1])
			i += 2

		case 0x31:
			if i+4 > len(path) {
				return 0, 0, 0, errors.New("truncated attribute segment")
			}
			attr = binary.LittleEndian.Uint16(path[i+1 : i+3])
			i += 4
		default:
			return 0, 0, 0, fmt.Errorf("unsupported path segment 0x%02X", path[i])
		}
	}
	return class, instance, attr, nil
}

func (f *fakeFANUC) identityResponse() []byte {
	item := make([]byte, 0, 64)
	// 封装协议版本
	item = append(item, 0x01, 0x00)
	// Socket Address：family(2 LE) | port(2 BE) | ip(4) | zero(8)
	sa := make([]byte, 16)
	binary.LittleEndian.PutUint16(sa[0:2], 2)
	binary.BigEndian.PutUint16(sa[2:4], uint16(f.port()))
	copy(sa[4:8], net.IPv4(127, 0, 0, 1).To4())
	item = append(item, sa...)
	// vendor / device type / product code
	appendLE16 := func(v uint16) { item = append(item, byte(v), byte(v>>8)) }
	appendLE16(f.vendor)
	appendLE16(0x000C)
	appendLE16(0x1234)
	// revision 9.40
	item = append(item, 9, 40)
	// status(2) + serial(4)
	appendLE16(0)
	item = append(item, 0xEF, 0xBE, 0xAD, 0xDE)
	// product name: length + chars, padded to even
	item = append(item, byte(len(f.product)))
	item = append(item, []byte(f.product)...)
	if len(item)%2 == 1 {
		item = append(item, 0)
	}
	item = append(item, 0) // state

	payload := make([]byte, 2)
	binary.LittleEndian.PutUint16(payload, 1)
	payload = append(payload, 0x0C, 0x00) // Identity item type
	payload = append(payload, byte(len(item)), byte(len(item)>>8))
	payload = append(payload, item...)
	return buildEncapFrame(0x0063, 0, payload)
}

func floatBits(v float32) uint32     { return math.Float32bits(v) }
func floatFromBits(v uint32) float32 { return math.Float32frombits(v) }
