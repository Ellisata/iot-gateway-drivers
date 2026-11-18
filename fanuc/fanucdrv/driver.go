// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package fanuc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Ellisata/iot-gateway-sdk/driver"

	cipcore "github.com/Ellisata/iot-gateway-drivers/fanuc/cipcore"
)

// fanucDriver FANUC CIP 协议驱动，实现 driver.Driver（写能力见 write.go）。
//
// 底层是 TCP 长连接 + UCMM 显式报文，**不是串行总线**，因此不实现
// SerialExclusive：采集引擎按非独占驱动处理，只对「重连 + 单次 Read」加锁。
// 驱动内部仍把同一会话上的请求串行化（client.mu）。
//
// 类型名不叫 driver：本包 import 了 SDK 的 driver 包，同包内再声明一个名为
// driver 的类型会让那个 import 在其它文件里被遮蔽，编译期就报
// "driver (package name) is not a type"。
type fanucDriver struct {
	mu     sync.RWMutex
	cfg    *Config
	client *client
	limit  *rateLimiter

	// warned "这个点位取不到值"的日志去重：键 → 上次打日志的时间。
	//
	// 地址不存在、类型不符这类条件在**每一轮**都重新成立；不设间隔地打日志
	// 会让网关日志被同一行刷屏（1s 周期 × 几十个点位 = 每秒几十行），而重复
	// 的行不携带任何新信息。60s 后允许再打一次，免得错过了第一条就永远查不到。
	warnMu sync.Mutex
	warned map[string]time.Time

	// blockBadUntil 块读被设备拒绝/校验失败后的静默期（class → 截止时刻）。
	//
	// 块读编码是 FANUC 厂商扩展，固件版本之间可能不认。被拒绝后如果每轮
	// 还先发一次注定失败的块读，等于白烧请求配额；静默期内直接走单读，
	// 到期再试一次。与 Omron CIP 的批量读静默期同一条取舍。
	blockMu       sync.Mutex
	blockBadUntil map[uint16]time.Time
	blockVerified map[uint16]bool
}

const (
	warnTTL = 60 * time.Second

	// blockMergeGap 块读允许跨越的空闲寄存器数。
	//
	// 数值寄存器在控制器上是连续存在的（没建点位也有值），跨几个空号读回来
	// 不会失败，还能把「R1、R2、R5、R6」这类常见布局合成一块。
	// 16 是保守值：再大的间隙让块读跨越无关点位、增大撞上实型寄存器的概率。
	blockMergeGap = 16

	// blockBadTTL 块读失败后的重试间隔。
	blockBadTTL = 5 * time.Minute
)

// warnf 按 key 限频输出日志。同一条告警 60s 内只打一次。
func (d *fanucDriver) warnf(key, format string, args ...any) {
	d.warnMu.Lock()
	if d.warned == nil {
		d.warned = make(map[string]time.Time)
	}
	now := time.Now()
	if last, ok := d.warned[key]; ok && now.Sub(last) < warnTTL {
		d.warnMu.Unlock()
		return
	}
	d.warned[key] = now
	d.warnMu.Unlock()
	logf(format, args...)
}

func newFANUCDriver() *fanucDriver {
	return &fanucDriver{}
}

// Connect 建立连接（含厂商校验与 Register Session）。
func (d *fanucDriver) Connect(protocolJSON string) error {
	cfg, err := ParseConfig(protocolJSON)
	if err != nil {
		return err
	}

	c, err := newClient(cfg)
	if err != nil {
		return fmt.Errorf("fanuc: connect failed: %w", err)
	}

	d.mu.Lock()
	old := d.client
	d.cfg = cfg
	d.client = c
	d.limit = newRateLimiter(cfg.MaxRequestsPerSecond)
	d.mu.Unlock()

	// 新连接意味着现场可能已经改过配置（补建了寄存器、改了类型），
	// 旧告警的限频状态不再有意义。
	d.warnMu.Lock()
	d.warned = nil
	d.warnMu.Unlock()
	d.clearBlockState()

	if old != nil {
		old.close()
	}
	return nil
}

// Ping 测试连通性。
//
// 两条路径都必须遵守 driver.Ping 的契约：不留下需要调用方释放的资源，
// 且不破坏本实例（Ping 用的临时实例不会被 Close）。
func (d *fanucDriver) Ping(protocolJSON string) error {
	cfg, err := ParseConfig(protocolJSON)
	if err != nil {
		return fmt.Errorf("fanuc: ping parse config failed: %w", err)
	}

	// 复用正在采集的连接：同一台机器人只开一个显式会话，避免占用额外的
	// 显式客户端连接（控制器对同时连接的客户端数有限制）。
	d.mu.RLock()
	live := d.client
	liveCfg := d.cfg
	d.mu.RUnlock()
	if live != nil && live.isConnected() && sameConfig(liveCfg, cfg) {
		if err := pingRead(live); err != nil {
			return fmt.Errorf("fanuc: ping failed (reused connection): %w", err)
		}
		logf("ping success: host=%s (reused connection)", cfg.Host)
		return nil
	}

	c, err := newClient(cfg)
	if err != nil {
		return fmt.Errorf("fanuc: ping connect failed: %w", err)
	}
	defer c.close()

	if err := pingRead(c); err != nil {
		return fmt.Errorf("fanuc: ping failed: %w", err)
	}
	logf("ping success: host=%s", cfg.Host)
	return nil
}

// pingRead 读取 R[1] 作为连通性验证。
//
// 通用状态错误同样证明设备可达（连接通了、设备应答了，只是 R[1] 不存在或
// 类型不符）—— 与"网络不可达/超时"是完全相反的两件事。只有传输层错误才算失败。
func pingRead(c *client) error {
	_, err := c.readAttr(objectIntegerRegister, 1)
	if err != nil && !cipcore.IsGeneralStatusError(err) {
		return err
	}
	return nil
}

// Read 读取点位列表，返回与 addrs 一一对应的结果。
//
// 读取策略：
//   - 同一 class 下的点位按寄存器号排序，连续（或间隙 ≤ blockMergeGap）的
//     合并成块，用 Get_Attribute_All 一次读回，帧数从 N 降到 ceil(N/chunk)；
//   - 块读编码是 FANUC 厂商扩展：首次块读会用单点读交叉校验，设备拒绝、
//     响应长度不对或数据与单点读不一致时，**当场退回单读**并暂停块读
//     5 分钟（blockBadTTL）。校验挡住的是「设备忽略起始号、把 R1..Rn
//     当成 Rstart..Rstart+n-1 返回」这种会静默给出错位数据的实现差异；
//   - 同一寄存器被多个点位引用时只读一次，int32/uint32 共用同一段字节。
//
// 失败分级（与仓库其它驱动一致）：
//   - 本地解析不出地址 / 设备确认该属性不支持 → 只让那个点位 QualityBadAddress；
//   - 设备可恢复地拒绝（资源忙、部分传输）→ QualityBadComms；
//   - 应答字节收到了但解码失败 → QualityBadParse；
//   - 传输层错误（超时、EOF、封装状态非 0）→ 整批返回 error，由采集引擎判离线。
func (d *fanucDriver) Read(addrs []driver.DeviceAddress) ([]driver.ReadResult, error) {
	d.mu.RLock()
	c := d.client
	lim := d.limit
	maxRegisters := defaultMaxRegistersPerRead
	if d.cfg != nil {
		maxRegisters = d.cfg.MaxRegistersPerRead
	}
	d.mu.RUnlock()

	if c == nil || !c.isConnected() {
		return nil, fmt.Errorf("fanuc: not connected")
	}
	if maxRegisters < 1 {
		maxRegisters = 1
	}

	// 先本地解析并按 class 归集唯一寄存器号。解析失败的点位一个字节都不该发出去。
	type classPlan struct {
		class   uint16
		indices []uint16
	}
	plans := make(map[uint16]*classPlan, 2)
	bad := make(map[string]int)
	parsedByID := make(map[string]registerAddress, len(addrs))
	for i := range addrs {
		a := &addrs[i]
		parsed, err := parseRegisterAddress(a.Name, a.DataType)
		if err != nil {
			d.warnf("addr:"+a.Name, "address %q invalid: %v", a.Name, err)
			bad[a.ID] = driver.QualityBadAddress
			continue
		}
		parsedByID[a.ID] = parsed
		p := plans[parsed.Class]
		if p == nil {
			p = &classPlan{class: parsed.Class}
			plans[parsed.Class] = p
		}
		p.indices = append(p.indices, parsed.Index)
	}

	classes := make([]uint16, 0, len(plans))
	for class, p := range plans {
		sort.Slice(p.indices, func(i, j int) bool { return p.indices[i] < p.indices[j] })
		// 去重：同一寄存器的 int32/uint32 点位只需要读一次。
		uniq := p.indices[:0]
		for i, idx := range p.indices {
			if i == 0 || idx != p.indices[i-1] {
				uniq = append(uniq, idx)
			}
		}
		p.indices = uniq
		classes = append(classes, class)
	}
	sort.Slice(classes, func(i, j int) bool { return classes[i] < classes[j] })

	rawByKey := make(map[uint32][]byte, len(addrs))
	qualityByKey := make(map[uint32]int)
	deadline := time.Now().Add(readBudget)

	for _, class := range classes {
		indices := plans[class].indices
		// 块读被本设备证伪（或配置为 1）时，chunk 退化为 1 = 逐点单读。
		chunk := 1
		if maxRegisters > 1 && d.blockUsable(class) {
			chunk = maxRegisters
		}

		for i := 0; i < len(indices); {
			start := indices[i]
			end := start
			j := i + 1
			for j < len(indices) {
				next := indices[j]
				// 间隙太大或本块已到上限就切开；otherwise 把 next 纳入本块。
				if int(next)-int(end)-1 > blockMergeGap {
					break
				}
				if int(next)-int(start)+1 > chunk {
					break
				}
				end = next
				j++
			}
			group := indices[i:j]
			count := int(end) - int(start) + 1

			if chunk > 1 && count > 1 {
				if err := d.readRange(c, lim, class, start, count, group, rawByKey, qualityByKey, deadline); err != nil {
					return nil, err
				}
			} else {
				if err := d.readSingles(c, lim, class, group, rawByKey, qualityByKey, deadline); err != nil {
					return nil, err
				}
			}
			i = j
		}
	}

	// 按原始顺序重组，保证每个点位恰好一条结果。
	byID := make(map[string]driver.ReadResult, len(addrs))
	for i := range addrs {
		a := &addrs[i]
		if q, ok := bad[a.ID]; ok {
			byID[a.ID] = badResult(a, q)
			continue
		}
		parsed, ok := parsedByID[a.ID]
		if !ok {
			byID[a.ID] = badResult(a, driver.QualityBadAddress)
			continue
		}
		key := readKey(parsed.Class, parsed.Index)
		if q, ok := qualityByKey[key]; ok {
			byID[a.ID] = badResult(a, q)
			continue
		}
		raw, ok := rawByKey[key]
		if !ok {
			// 预算耗尽等：没有响应可用。
			byID[a.ID] = badResult(a, driver.QualityBad)
			continue
		}
		value, kind, derr := decodeValue(raw, parsed.DataType)
		if derr != nil {
			d.warnf("dec:"+a.Name, "address %q decode failed: %v", a.Name, derr)
			byID[a.ID] = badResult(a, driver.QualityBadParse)
			continue
		}
		byID[a.ID] = driver.ReadResult{
			DeviceAddressID: a.ID,
			Value:           value,
			DataType:        a.DataType,
			Kind:            kind,
			Quality:         driver.QualityGood,
		}
	}

	results := make([]driver.ReadResult, 0, len(addrs))
	for _, a := range addrs {
		results = append(results, byID[a.ID])
	}
	return results, nil
}

func readKey(class, index uint16) uint32 {
	return uint32(class)<<16 | uint32(index)
}

// readRange 用一次块读覆盖 [start, start+count-1]，成功时把每个寄存器的
// 4 字节原始数据放进 rawByKey；失败时退回逐点单读（可能仍能救回大部分值）。
func (d *fanucDriver) readRange(c *client, lim *rateLimiter, class uint16, start uint16, count int,
	group []uint16, rawByKey map[uint32][]byte, qualityByKey map[uint32]int, deadline time.Time) error {

	if time.Now().After(deadline) {
		d.deferGroup(class, group, qualityByKey)
		return nil
	}
	if lim != nil {
		lim.wait()
	}
	data, err := c.readBlock(class, start, count)
	if err != nil {
		if !cipcore.IsGeneralStatusError(err) {
			return fmt.Errorf("fanuc: block read R[%d..R[%d] failed: %w", start, int(start)+count-1, err)
		}
		if blockFailureDisables(err) {
			d.markBlockBad(class, err)
		}
		d.warnf(fmt.Sprintf("blockfail:%02X:%d", class, start),
			"block read R[%d..R[%d] rejected, falling back to single reads: %v",
			start, int(start)+count-1, err)
		return d.readSingles(c, lim, class, group, rawByKey, qualityByKey, deadline)
	}
	if len(data) < count*4 {
		reason := fmt.Errorf("short block response: %d bytes for %d registers", len(data), count)
		d.markBlockBad(class, reason)
		d.warnf(fmt.Sprintf("blockshort:%02X:%d", class, start),
			"block read R[%d..R[%d] unusable: %v; falling back to single reads",
			start, int(start)+count-1, reason)
		return d.readSingles(c, lim, class, group, rawByKey, qualityByKey, deadline)
	}

	if !d.blockIsVerified(class) {
		ok, verr := d.verifyBlock(c, lim, class, start, count, group, data, deadline)
		if verr != nil {
			return verr
		}
		if !ok {
			return d.readSingles(c, lim, class, group, rawByKey, qualityByKey, deadline)
		}
	}

	for i := 0; i < count; i++ {
		key := readKey(class, uint16(int(start)+i))
		rawByKey[key] = data[i*4 : i*4+4]
		delete(qualityByKey, key)
	}
	return nil
}

// verifyBlock 首次块读的交叉校验。
//
// 只校验**本块里实际存在的首、末两个点位**：如果设备忽略了起始属性、总是从
// R[1] 开始返回，这两个值几乎必然对不上；两个都对上仍偶然相等的概率可以忽略。
// 校验本身要用 1~2 次单点读，但每个 class 在每个静默期内只做一次。
func (d *fanucDriver) verifyBlock(c *client, lim *rateLimiter, class uint16, start uint16, count int,
	group []uint16, data []byte, deadline time.Time) (bool, error) {

	first := group[0]
	if ok, err := d.verifyOne(c, lim, class, first, int(first)-int(start), data, deadline); err != nil || !ok {
		return ok, err
	}
	last := group[len(group)-1]
	if last != first {
		if ok, err := d.verifyOne(c, lim, class, last, int(last)-int(start), data, deadline); err != nil || !ok {
			return ok, err
		}
	}
	d.markBlockVerified(class)
	logf("block read verified for class 0x%02X (R[%d..R[%d], %d registers)", class, start, int(start)+count-1, count)
	return true, nil
}

func (d *fanucDriver) verifyOne(c *client, lim *rateLimiter, class uint16, index uint16, offset int,
	data []byte, deadline time.Time) (bool, error) {

	if time.Now().After(deadline) {
		return false, nil
	}
	if lim != nil {
		lim.wait()
	}
	single, err := c.readAttr(class, index)
	if err != nil {
		if !cipcore.IsGeneralStatusError(err) {
			return false, fmt.Errorf("fanuc: verify block read R[%d] failed: %w", index, err)
		}
		d.markBlockBad(class, fmt.Errorf("verify R[%d]: %w", index, err))
		return false, nil
	}
	off := offset * 4
	if len(single) < 4 || off < 0 || off+4 > len(data) || !bytes.Equal(single[:4], data[off:off+4]) {
		d.markBlockBad(class, fmt.Errorf("block data at R[%d] differs from single read", index))
		return false, nil
	}
	return true, nil
}

// readSingles 逐点单读一组寄存器（块读关闭、被拒或校验失败时的路径）。
func (d *fanucDriver) readSingles(c *client, lim *rateLimiter, class uint16, group []uint16,
	rawByKey map[uint32][]byte, qualityByKey map[uint32]int, deadline time.Time) error {

	for _, index := range group {
		if err := d.readOne(c, lim, class, index, rawByKey, qualityByKey, deadline); err != nil {
			return err
		}
	}
	return nil
}

// readOne 单点读一个寄存器。通用状态错误只落在该点位上；传输层错误上抛。
func (d *fanucDriver) readOne(c *client, lim *rateLimiter, class uint16, index uint16,
	rawByKey map[uint32][]byte, qualityByKey map[uint32]int, deadline time.Time) error {

	key := readKey(class, index)
	if time.Now().After(deadline) {
		// 预算耗尽属于"暂缓"，不是设备判定（见 driver.QualityBad 的说明）。
		qualityByKey[key] = driver.QualityBad
		d.warnf("budget", "read budget exhausted, deferring remaining points (first deferred: R[%d])", index)
		return nil
	}
	if lim != nil {
		lim.wait()
	}
	data, err := c.readAttr(class, index)
	if err != nil {
		if cipcore.IsGeneralStatusError(err) {
			qualityByKey[key] = qualityOfCIPErr(err)
			delete(rawByKey, key)
			d.warnf(fmt.Sprintf("rej:%02X:%d", class, index),
				"register R[%d] (class 0x%02X) rejected: %v", index, class, err)
			return nil
		}
		return fmt.Errorf("fanuc: read R[%d] failed: %w", index, err)
	}
	rawByKey[key] = data
	delete(qualityByKey, key)
	return nil
}

func (d *fanucDriver) deferGroup(class uint16, group []uint16, qualityByKey map[uint32]int) {
	for _, index := range group {
		qualityByKey[readKey(class, index)] = driver.QualityBad
	}
	if len(group) > 0 {
		d.warnf("budget", "read budget exhausted, deferring remaining points (first deferred: R[%d])", group[0])
	}
}

// ==================== 块读状态 ====================

func (d *fanucDriver) blockUsable(class uint16) bool {
	d.blockMu.Lock()
	defer d.blockMu.Unlock()
	until, ok := d.blockBadUntil[class]
	return !ok || time.Now().After(until)
}

func (d *fanucDriver) blockIsVerified(class uint16) bool {
	d.blockMu.Lock()
	defer d.blockMu.Unlock()
	return d.blockVerified[class]
}

func (d *fanucDriver) markBlockVerified(class uint16) {
	d.blockMu.Lock()
	if d.blockVerified == nil {
		d.blockVerified = make(map[uint16]bool)
	}
	d.blockVerified[class] = true
	d.blockMu.Unlock()
}

// markBlockBad 暂停某个 class 的块读并清除校验标记（到期重试时会重新校验）。
func (d *fanucDriver) markBlockBad(class uint16, reason error) {
	d.blockMu.Lock()
	if d.blockBadUntil == nil {
		d.blockBadUntil = make(map[uint16]time.Time)
	}
	d.blockBadUntil[class] = time.Now().Add(blockBadTTL)
	if d.blockVerified != nil {
		delete(d.blockVerified, class)
	}
	d.blockMu.Unlock()

	d.warnf(fmt.Sprintf("blockbad:%02X", class),
		"block read for class 0x%02X disabled for %s, using single reads: %v",
		class, blockBadTTL, reason)
}

func (d *fanucDriver) clearBlockState() {
	d.blockMu.Lock()
	d.blockBadUntil = nil
	d.blockVerified = nil
	d.blockMu.Unlock()
}

// blockFailureDisables 判断这次块读失败是否意味着「本设备不认块读编码」，
// 需要暂停块读一段时间。
//
// 只认地址/服务/数据长度这类**配置或固件能力**错误；0x02 资源忙、0x06 部分
// 传输是瞬态，下一轮原样重试即可，不能因此把块读停 5 分钟。
func blockFailureDisables(err error) bool {
	var ge *cipcore.GeneralStatusError
	if !errors.As(err, &ge) {
		return false
	}
	switch ge.Status() {
	case cipcore.GeneralStatusTooMuchData, cipcore.GeneralStatusDataTooLarge:
		return true
	}
	return cipcore.IsAddressStatusError(err)
}

// badResult 构造一个没有值、带质量码的结果。Value 必须为空 ——
// 下游按 QualityUsable 判断可用性，不读 Value。
func badResult(a *driver.DeviceAddress, quality int) driver.ReadResult {
	return driver.ReadResult{
		DeviceAddressID: a.ID,
		Value:           "",
		DataType:        a.DataType,
		Kind:            typeKind(a.DataType),
		Quality:         quality,
	}
}

// decodeValue 把 4 字节寄存器值按点位类型解码并格式化。
func decodeValue(raw []byte, dataType string) (value, kind string, err error) {
	dt, ok := fanucLookup(dataType)
	if !ok {
		return "", "", fmt.Errorf("unsupported data type %q", dataType)
	}
	if dt.Decode == nil || dt.Format == nil {
		return "", "", fmt.Errorf("data type %q has no decoder", dataType)
	}
	if len(raw) < dt.Size || dt.Size != 4 {
		return "", "", fmt.Errorf("need %d bytes, got %d", dt.Size, len(raw))
	}
	v, err := dt.Decode(raw[:4], binary.LittleEndian)
	if err != nil {
		return "", "", err
	}
	return dt.Format(v), dt.Kind, nil
}

// qualityOfCIPErr 把 CIP 通用状态错误映射为质量码。
//
// 地址级（对象/属性不存在、服务不支持、类型不符）是配置错，永不自我恢复；
// 其余（资源忙、部分传输…）是可恢复的设备拒绝，下一轮重试可能拿到值。
func qualityOfCIPErr(err error) int {
	if cipcore.IsAddressStatusError(err) {
		return driver.QualityBadAddress
	}
	return driver.QualityBadComms
}

// IsConnected 返回连接状态。
func (d *fanucDriver) IsConnected() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.client != nil && d.client.isConnected()
}

// Close 关闭连接并释放资源。
func (d *fanucDriver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	var err error
	if d.client != nil {
		err = d.client.close()
		d.client = nil
	}
	d.cfg = nil
	d.limit = nil
	return err
}

// ==================== 限流 ====================

// rateLimiter 简单的匀速限流器：两次请求之间至少间隔 1/rps 秒。
//
// 不用令牌桶：显式消息是请求/响应式的，没有"突发一批"的合理场景；
// 匀速间隔反而更适配机器人控制器有限的空闲 CPU。
type rateLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func newRateLimiter(rps int) *rateLimiter {
	if rps <= 0 {
		return nil
	}
	return &rateLimiter{interval: time.Second / time.Duration(rps)}
}

// wait 阻塞到下一次请求被允许。睡眠在锁外进行，不阻塞其它 goroutine
// 计算自己的时间片。
func (l *rateLimiter) wait() {
	l.mu.Lock()
	now := time.Now()
	next := l.next
	if next.After(now) {
		l.next = next.Add(l.interval)
		l.mu.Unlock()
		time.Sleep(next.Sub(now))
		return
	}
	l.next = now.Add(l.interval)
	l.mu.Unlock()
}
