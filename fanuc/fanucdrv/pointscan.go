// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package fanuc

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Ellisata/iot-gateway-sdk/driver"

	cipcore "github.com/Ellisata/iot-gateway-drivers/fanuc/cipcore"
)

// ==================== 点位范围扫描 ====================
//
// 对一台**已存在的设备**扫一段寄存器号，把读得到的地址列出来。
//
// **这与 Rockwell.CIP 的点位扫描不是一回事**：那边的 Symbol Object（类 0x6B）
// 是一张标签表，可以一次枚举出设备上全部点位；FANUC 的数值寄存器对象
// （0x6B 整型 / 0x6C 实型）把寄存器号放在**属性段**，没有任何"列出全部寄存器"
// 的服务 —— 必须给定号码去读。所以这里产出的是一份**候选清单**：
// 「R[start]..R[start+count-1] 这段里哪些读得到」，而不是这台设备的点位表。
//
// 由此带来三条必须写进用户可见文案的局限：
//
//  1. **整型与实型是两个对象**。同一个 R[n] 只属于其中一个（用另一个类读会回
//     0x14），一次扫描只能选一种类型；两类都要看请扫两次。
//  2. **控制器不告诉你寄存器总数**。范围由用户指定；超出末尾的号码表现为
//     "读不到"，与"这里恰好是另一类的寄存器"在结果里长得一样。
//  3. **扫描能在预算内没扫完**。网关把这条同步 HTTP 链路的预算钳到 7s
//     （pluginhost 的 pointScanGatewayBudgetCapMs），大范围或需要逐点降级的
//     范围会返回部分结果并置 Incomplete。
//
// 读取策略与采集路径（driver.Read）刻意不同，原因见 scanRead 与 walk 的注释：
// 扫描需要跨块二分降级与逐请求 deadline，而采集路径的块读静默期、交叉校验
// 状态都是为"持续采集同一批点位"设计的，不适合一次性的范围探测。

// 点位扫描的规模与预算。
const (
	// pointScanMaxPoints 单次扫描最多返回多少个点位。
	//
	// 上限存在的意义不是性能而是**事故半径**：应答帧要走 pluginwire 的写锁，
	// 而那份锁是该插件进程上所有协议共用的 —— 一份几百 KB 的应答会把它们一起
	// 头阻塞。2000 个点（每个一行、几十字节）是几十 KB 的量级，一次能过管道。
	//
	// 与 PointDiscovery().MaxPoints 必须一致 —— 后者只是把它报给网关与前端，
	// 由 driversdk.Meta.Validate 校验落在 [1,20000] 内。
	pointScanMaxPoints = 2000

	// pointScanBudgetMs 本协议声明的整段扫描预算（毫秒）。
	//
	// 与其它驱动一样报一个比网关上限大的值：真正生效的永远是
	// min(本值, pluginhost 的 7000ms)。声明成 20000 只是如实表达"上千个寄存器
	// 本来就需要这么久"，而不是承诺能让它跑完。
	pointScanBudgetMs = 20000

	// pointScanZeroNote 值为 0 的点位带的说明。
	//
	// 判定用**原始字节全 0**而不是解析后的值：float32 的 0x00000001 会解成
	// 一个极小的非零数，按解析值判零会漏掉它。
	pointScanZeroNote = "当前值为 0"
)

// pointScanForm 点位扫描的参数表单（form-create schema）。
//
// 字段名与 pointScanOptionsRaw 的 json tag 一一对应。文案要把"一次只能扫一类"
// 说清楚 —— 否则用户扫完整型以为拿到了完整清单，实型寄存器会被误读成"不存在"。
const pointScanForm = `{
  "rule": [
    {"type":"select","field":"dataType","title":"{{$t.dataType_title}}","$required":false,"value":"Long",
     "info":"{{$t.dataType_info}}",
     "options":[{"label":"{{$t.dataType_option_0}}","value":"Long"},{"label":"{{$t.dataType_option_1}}","value":"DWord"},{"label":"{{$t.dataType_option_2}}","value":"Float"}]},
    {"type":"inputNumber","field":"startAddress","title":"{{$t.startAddress_title}}","$required":false,"value":1,
     "info":"{{$t.startAddress_info}}"},
    {"type":"inputNumber","field":"count","title":"{{$t.count_title}}","$required":true,"value":100,
     "info":"{{$t.count_info}}"}
  ],
  "options": {
    "form":{"inline":false,"hideRequiredAsterisk":false,"labelPosition":"right","size":"default","labelWidth":"125px"},
    "resetBtn":{"show":false,"innerText":"重置"},
    "submitBtn":{"show":false,"innerText":"提交"},
    "language":{"zh-cn":{"dataType_title":"寄存器类型","dataType_info":"整型与实型在控制器上是两个不同的对象（0x6B / 0x6C），同一个 R[n] 只属于其中一个。一次扫描只能选一种类型；两类都要看请分别扫一次","dataType_option_0":"整型 (Long, 0x6B)","dataType_option_1":"无符号整型 (DWord, 0x6B)","dataType_option_2":"实型 (Float, 0x6C)","startAddress_title":"起始寄存器号","startAddress_info":"从 1 开始，与点位地址写法一致（起始 1 对应 R[1]）","count_title":"扫描数量","count_info":"从起始号起连续探测多少个寄存器号。连续有效区间用块读（单帧最多 124 个）快速扫过；中间遇到另一类寄存器或越界时自动二分降级，耗时随之增加。整段扫描有预算上限，超时会返回已完成的部分并把「未扫完」标出来"},"en":{"dataType_title":"Register type","dataType_info":"Integer and real registers are two different objects on the controller (0x6B / 0x6C), and each R[n] belongs to only one of them. A scan covers one type at a time; scan twice to see both","dataType_option_0":"Integer (Long, 0x6B)","dataType_option_1":"Unsigned integer (DWord, 0x6B)","dataType_option_2":"Real (Float, 0x6C)","startAddress_title":"Start register number","startAddress_info":"Starts at 1, consistent with point address syntax (start 1 is R[1])","count_title":"Scan count","count_info":"How many consecutive register numbers to probe from the start. Contiguous valid ranges are read in blocks (up to 124 per request); when the block hits a register of the other type or runs past the end, the scan degrades by binary splits. A full scan has a time budget. On timeout it returns the completed part and marks the scan as incomplete"}}
  }
}`

// PointDiscovery 返回 FANUC.CIP 的点位扫描能力声明，供 main.go 填进
// Meta.Protocols。
//
// Description 是用户理解这个功能性质的**主要来源**（常驻显示在弹窗顶部），
// 因此三条局限必须写在里面：候选清单、一类一次、可能未扫完。
func PointDiscovery() *driver.PointDiscoverySpec {
	return &driver.PointDiscoverySpec{
		Title: "扫描 FANUC 数值寄存器",
		Description: "按所选类型扫一段寄存器号，列出**读得到**的 R[n] 与当前值。" +
			"注意：控制器**不会告诉你它有哪些寄存器**，范围由你指定 —— " +
			"整型与实型是两个不同的对象（0x6B / 0x6C），同一个 R[n] 只属于其中一个，一次扫描只能选一种类型；" +
			"没列出来的号码既可能不存在、也可能属于另一种类型。请把结果当作候选清单，而不是点位表。" +
			"大范围或中间夹杂另一种类型的寄存器时，扫描会二分降级并可能超出预算，" +
			"此时返回已完成的部分并把「未扫完」标出来",
		TitleI18n: map[string]string{
			"en": "Scan FANUC numeric registers",
		},
		DescriptionI18n: map[string]string{
			"en": "Scans a range of register numbers of the selected type and lists the R[n] that **can be read** with their current values. Note that the controller **does not tell you which registers exist**, so you specify the range: integer and real registers are two different objects (0x6B / 0x6C), each R[n] belongs to only one of them, and a scan covers one type at a time. A number not listed may not exist or may belong to the other type. Treat the result as a candidate list, not a point table. Large ranges, or ranges interleaved with registers of the other type, degrade by binary splits and may exceed the budget; in that case the completed part is returned and the scan is marked incomplete",
		},
		FormJSON: json.RawMessage(pointScanForm),
		// "Long" 是 TypeMap 的键（内部 int32）—— 写成裸名 "int32" 会被
		// Meta.Validate 拒掉。它只是类型下拉的初值；扫描结果里
		// AssumedCommonDataType 会反映用户实际选的类型。
		DefaultCommonDataType: driver.TypeLong,
		MaxBudgetMs:           pointScanBudgetMs,
		MaxPoints:             pointScanMaxPoints,
	}
}

// SessionAccess 与 driversdk.SessionAccess 同形，但**刻意本地声明而不 import
// driversdk**：与 probe.go 的 Probe 收原始参数（而不是 driversdk.ProbeRequest）
// 是同一条取舍 —— 协议实现只依赖 driver 这一份契约，本包因此也能脱离插件进程
// 单独测试。
type SessionAccess interface {
	Do(fn func(drv driver.Driver) error) error
}

// registerScanReader 扫描路径对驱动实例的最小要求。
//
// **刻意不进 driver.Driver 接口**：扫描是冷路径，只有本包的 *fanucDriver 实现它；
// 把它塞进通用接口会逼每个驱动、每个测试桩都实现一个只有扫描用的方法。
type registerScanReader interface {
	// scanRead 读 class 类从 start 起连续 count 个寄存器。
	//
	// 成功返回 count*4 字节（按寄存器号升序）；设备拒绝时返回
	// *cipcore.GeneralStatusError；连接/超时错误原样返回；
	// 数据长度不足返回 errScanShortData。
	scanRead(class uint16, start uint16, count int, deadline time.Time) ([]byte, error)
}

// pointScanOptions 解析后的扫描参数。
type pointScanOptions struct {
	class      uint16 // 0x6B 或 0x6C
	dataType   string // 内部裸名：int32 / uint32 / float32
	commonType string // 通用类型名：Long / DWord / Float
	start      uint16
	count      int
}

// pointScanOptionsRaw 扫描选项的原始形态。键名与 pointScanForm 的 field 一一对应。
//
// 数值用 jsonInt：form-create 的 inputNumber 与某些控件产出的 JSON 形态不一致
// （数字或数字字符串），声明成 int 时一份带字符串的选项会让整次扫描全失败。
type pointScanOptionsRaw struct {
	DataType     string  `json:"dataType"`
	StartAddress jsonInt `json:"startAddress"`
	Count        jsonInt `json:"count"`
}

// parsePointScanOptions 解析扫描选项。
//
// 与设备扫描的选项不同：那边缺项时静默回默认值，这里**数量缺失或不合法一律
// 报错** —— "扫多少"没有合理的默认值，猜一个数字会让用户拿到一份他并没有
// 要求的长清单。类型与起始号有自然默认值（Long / R[1]）。
func parsePointScanOptions(options json.RawMessage) (pointScanOptions, error) {
	opt := pointScanOptions{
		class:      objectIntegerRegister,
		dataType:   typeInt32,
		commonType: driver.TypeLong,
		start:      1,
	}
	if len(options) == 0 {
		return opt, errors.New("扫描选项为空：需要指定扫描数量（寄存器类型与起始号可省略）")
	}

	var raw pointScanOptionsRaw
	if err := json.Unmarshal(options, &raw); err != nil {
		return opt, fmt.Errorf("解析扫描选项失败: %w", err)
	}

	switch strings.ToLower(strings.TrimSpace(raw.DataType)) {
	case "", "long", typeInt32:
		opt.class, opt.dataType, opt.commonType = objectIntegerRegister, typeInt32, driver.TypeLong
	case "dword", typeUint32:
		opt.class, opt.dataType, opt.commonType = objectIntegerRegister, typeUint32, driver.TypeDWord
	case "float", typeFloat32:
		opt.class, opt.dataType, opt.commonType = objectRealRegister, typeFloat32, driver.TypeFloat
	default:
		return opt, fmt.Errorf("不支持的寄存器类型 %q（可用 Long / DWord / Float）", raw.DataType)
	}

	if raw.StartAddress.set {
		if raw.StartAddress.val < 1 || raw.StartAddress.val > maxRegisterIndex {
			return opt, fmt.Errorf("起始寄存器号 %d 超出 [1,%d]", raw.StartAddress.val, maxRegisterIndex)
		}
		opt.start = uint16(raw.StartAddress.val)
	}
	if !raw.Count.set || raw.Count.val <= 0 {
		return opt, errors.New("扫描数量必须大于 0")
	}
	// 属性号是 16 位：越过 65535 的部分不存在 —— 截断而不是报错，
	// 与 Modbus 的扫描选项同一条取舍。
	end := int(opt.start) + raw.Count.val
	if end > maxRegisterIndex+1 {
		end = maxRegisterIndex + 1
	}
	opt.count = end - int(opt.start)
	return opt, nil
}

// errPointScanStop 内部哨兵：停止扫描，但**不是错误**（达到 MaxPoints 或预算耗尽）。
var errPointScanStop = errors.New("fanuc: point scan stop")

// errScanDeadline 预算耗尽（由 scanRead 或 walk 返回）。
var errScanDeadline = errors.New("fanuc: point scan deadline exceeded")

// errScanShortData 块读应答长度不足 count*4 字节。
var errScanShortData = errors.New("fanuc: point scan short response")

// ScanPoints 扫描一段 FANUC 数值寄存器范围。
//
// sess 是拿驱动实例的唯一入口：**每块读一次 Do**，块与块之间锁是放开的
// （对 TCP 设备本来也不会命中复用 —— fanuc 未实现 ConnectionMatcher，
// selectSession 走临时实例；这条结构是为将来复用留的）。
//
// 返回的 error 与结果**可以同时非 nil**：中止性错误（设备掉线）发生时，
// 已经扫到的部分一并返回 —— 丢掉它们对用户没有任何好处。
func ScanPoints(sess SessionAccess, options json.RawMessage, deadline time.Time) (*driver.PointScanResult, error) {
	opt, err := parsePointScanOptions(options)
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, errors.New("fanuc: 没有可用的采集会话，无法扫描")
	}

	sc := &pointScanner{
		opt:      opt,
		deadline: deadline,
		result: &driver.PointScanResult{
			AssumedCommonDataType: opt.commonType,
		},
		started: time.Now(),
	}

	// 预检：驱动实例必须支持扫描。这是"插件与协议能力对不上"的编程错误，
	// 在开始读设备之前报出来，而不是读到一半才发现类型断言失败。
	var preflightErr error
	if err := sess.Do(func(drv driver.Driver) error {
		if _, ok := drv.(registerScanReader); !ok {
			preflightErr = errors.New("fanuc: 当前驱动实例不支持点位扫描")
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if preflightErr != nil {
		return nil, preflightErr
	}

	for done := 0; done < opt.count; {
		if time.Now().After(deadline) {
			sc.incomplete = true
			break
		}
		qty := opt.count - done
		if qty > cipcore.MaxBlockRegisters {
			qty = cipcore.MaxBlockRegisters
		}
		start := uint16(int(opt.start) + done)

		err := sess.Do(func(drv driver.Driver) error {
			rs, ok := drv.(registerScanReader)
			if !ok {
				return errors.New("fanuc: 当前驱动实例不支持点位扫描")
			}
			return sc.walk(rs, start, qty)
		})
		done += qty
		if err != nil {
			if errors.Is(err, errPointScanStop) {
				break
			}
			return sc.finish(), err
		}
	}

	return sc.finish(), nil
}

// pointScanner 一次扫描的可变状态。
type pointScanner struct {
	opt      pointScanOptions
	deadline time.Time
	started  time.Time
	result   *driver.PointScanResult

	// incomplete 预算耗尽（块间或降级途中）。与 Truncated 是两回事：
	// 那个是"结果条数到上限"，这个是"范围还没扫完"。
	incomplete bool

	// blockUnusable 块读在本设备/本次扫描上不可用（服务不支持或交叉校验失败）。
	// 置位后本类剩余范围全部走单读，不再浪费请求去试注定失败的块读。
	blockUnusable bool

	// blockVerified 首次成功块读是否已被单点读交叉校验过。
	blockVerified bool
}

// walk 扫一个区间，失败时对半降级。
//
// 降级的形状是「整段 → 对半 → … → 单点」：设备对含空洞（另一种类型的寄存器
// 或越界号）的区间会整段拒绝（0x14 / 0x05），只有一路拆到单点才能把"哪几个
// 号码真的读不到"摘出来。采集路径的 driver.Read 是"整块失败就逐点单读"，
// 不做二分 —— 对一次性的范围扫描来说，那是 O(n) 次请求而不是 O(log n)。
func (sc *pointScanner) walk(rs registerScanReader, start uint16, count int) error {
	if count <= 0 {
		return nil
	}
	if time.Now().After(sc.deadline) {
		sc.incomplete = true
		return errPointScanStop
	}

	if sc.blockUnusable && count > 1 {
		return sc.walkSingles(rs, start, count)
	}

	data, err := rs.scanRead(sc.opt.class, start, count, sc.deadline)
	if err == nil {
		if count > 1 && !sc.blockVerified {
			ok, verr := sc.verifyBlock(rs, start, count, data)
			if verr != nil {
				return verr
			}
			if !ok {
				// 设备忽略了起始属性（总是从 R[1] 返回）或数据错位：
				// 整块不可信，改走单读，绝不把错位的数据当结果。
				sc.blockUnusable = true
				return sc.walkSingles(rs, start, count)
			}
			sc.blockVerified = true
		}
		return sc.emit(start, count, data)
	}

	if errors.Is(err, errScanDeadline) {
		sc.incomplete = true
		return errPointScanStop
	}
	if errors.Is(err, errScanShortData) {
		if count == 1 {
			sc.markUnreadable(1)
			return nil
		}
		return sc.split(rs, start, count)
	}

	var ge *cipcore.GeneralStatusError
	if !errors.As(err, &ge) {
		// 连接类错误（超时、EOF、封装状态）：**中止而不是记成"地址不存在"**。
		return fmt.Errorf("扫描 R[%d..R[%d] 时连接中断: %w", start, int(start)+count-1, err)
	}

	if count == 1 {
		if cipcore.IsAddressStatusError(err) {
			sc.markUnreadable(1)
			return nil
		}
		// 资源忙、部分传输之类的**瞬态**拒绝不是"地址不存在"，
		// 按错误中止并带回已完成的部分，别让用户误以为这段号码都不存在。
		return fmt.Errorf("扫描 R[%d] 时设备拒绝: %w", start, err)
	}

	switch ge.Status() {
	case cipcore.GeneralStatusObjectDoesNotExist:
		// 对象级不存在：这个类在控制器上根本没有（如 R-30iA 早期软件没有 0x6C）。
		// 整段按"读不到"记账，不再逐点浪费请求。
		sc.markUnreadable(count)
		return nil
	case cipcore.GeneralStatusServiceNotSupported:
		// 可能是"块读服务不支持"，也可能是"这个类不支持" —— 用一次单点读区分。
		return sc.probeServiceSupport(rs, start, count)
	}

	if cipcore.IsAddressStatusError(err) ||
		ge.Status() == cipcore.GeneralStatusTooMuchData ||
		ge.Status() == cipcore.GeneralStatusDataTooLarge {
		return sc.split(rs, start, count)
	}
	return fmt.Errorf("扫描 R[%d..R[%d] 时设备拒绝: %w", start, int(start)+count-1, err)
}

// split 把区间对半拆开继续扫。
func (sc *pointScanner) split(rs registerScanReader, start uint16, count int) error {
	half := count / 2
	if err := sc.walk(rs, start, half); err != nil {
		return err
	}
	return sc.walk(rs, start+uint16(half), count-half)
}

// walkSingles 逐点单读一个区间（块读不可用时）。
func (sc *pointScanner) walkSingles(rs registerScanReader, start uint16, count int) error {
	for i := 0; i < count; i++ {
		if time.Now().After(sc.deadline) {
			sc.incomplete = true
			return errPointScanStop
		}
		idx := start + uint16(i)
		data, err := rs.scanRead(sc.opt.class, idx, 1, sc.deadline)
		if err == nil {
			if err := sc.emit(idx, 1, data); err != nil {
				return err
			}
			continue
		}
		if errors.Is(err, errScanDeadline) {
			sc.incomplete = true
			return errPointScanStop
		}
		if errors.Is(err, errScanShortData) {
			sc.markUnreadable(1)
			continue
		}
		var ge *cipcore.GeneralStatusError
		if errors.As(err, &ge) {
			if cipcore.IsAddressStatusError(err) {
				sc.markUnreadable(1)
				continue
			}
			return fmt.Errorf("扫描 R[%d] 时设备拒绝: %w", idx, err)
		}
		return fmt.Errorf("扫描 R[%d] 时连接中断: %w", idx, err)
	}
	return nil
}

// probeServiceSupport 收到 0x08（服务不支持）时的分流。
//
// 块读服务不支持时，单点读仍然可用；类本身不支持时，单点也回 0x08。
// 一次单点读就能把两者区分开，比"对整段做二分"或"逐点试一遍"便宜得多。
func (sc *pointScanner) probeServiceSupport(rs registerScanReader, start uint16, count int) error {
	// 块读回 0x08 之后不再重试块读：要么服务不认，要么类本身不存在，
	// 两种情况都不会因为换个区间就好转。
	sc.blockUnusable = true

	data, err := rs.scanRead(sc.opt.class, start, 1, sc.deadline)
	if err == nil {
		// 单点能读 → 只是块读服务不认。先产出这个点，其余走单读。
		if err := sc.emit(start, 1, data); err != nil {
			return err
		}
		if count > 1 {
			return sc.walkSingles(rs, start+1, count-1)
		}
		return nil
	}
	if errors.Is(err, errScanDeadline) {
		sc.incomplete = true
		return errPointScanStop
	}
	var ge *cipcore.GeneralStatusError
	if errors.As(err, &ge) {
		if ge.Status() == cipcore.GeneralStatusServiceNotSupported ||
			ge.Status() == cipcore.GeneralStatusObjectDoesNotExist {
			// 类/服务整体不支持：整段读不到。
			sc.markUnreadable(count)
			return nil
		}
		if cipcore.IsAddressStatusError(err) {
			// 只是起始号不存在，块读服务本身未必不支持 —— 走二分继续。
			return sc.split(rs, start, count)
		}
	}
	return err
}

// verifyBlock 首次成功块读的交叉校验。
//
// 与采集路径的 verifyBlock 同一条理由：FANUC 的块读编码是厂商扩展，有的固件
// 会忽略 attribute 段的起始号、总是从 R[1] 返回。只校验首、末两个点 —— 两个
// 都对上仍偶然相等的概率可以忽略。
func (sc *pointScanner) verifyBlock(rs registerScanReader, start uint16, count int, data []byte) (bool, error) {
	if len(data) < count*4 {
		return false, nil
	}
	if ok, err := sc.verifyOne(rs, start, 0, data); err != nil || !ok {
		return ok, err
	}
	last := uint16(int(start) + count - 1)
	if last != start {
		if ok, err := sc.verifyOne(rs, last, (count-1)*4, data); err != nil || !ok {
			return ok, err
		}
	}
	return true, nil
}

func (sc *pointScanner) verifyOne(rs registerScanReader, index uint16, offset int, data []byte) (bool, error) {
	single, err := rs.scanRead(sc.opt.class, index, 1, sc.deadline)
	if err != nil {
		if errors.Is(err, errScanDeadline) {
			sc.incomplete = true
			return false, errPointScanStop
		}
		var ge *cipcore.GeneralStatusError
		if errors.As(err, &ge) && cipcore.IsAddressStatusError(err) {
			// 块读成功但单点读被判不存在：数据不可信。
			return false, nil
		}
		return false, err
	}
	if len(single) < 4 || offset < 0 || offset+4 > len(data) {
		return false, nil
	}
	return string(single[:4]) == string(data[offset:offset+4]), nil
}

// emit 把一次成功读取的结果按寄存器逐个产出。
//
// 逐个产出而不是整段写进 Points，是为了让 MaxPoints 截断**精确**：达到上限时
// 立刻停手，超出的号码既不计入 Scanned/Readable，也不进列表。
func (sc *pointScanner) emit(start uint16, count int, data []byte) error {
	if len(data) < count*4 {
		sc.markUnreadable(count)
		return nil
	}
	for i := 0; i < count; i++ {
		if len(sc.result.Points) >= pointScanMaxPoints {
			sc.result.Truncated = true
			return errPointScanStop
		}
		raw := data[i*4 : i*4+4]
		sc.result.Scanned++
		sc.result.Readable++
		sc.result.Points = append(sc.result.Points, sc.buildPoint(uint16(int(start)+i), raw))
	}
	return nil
}

// markUnreadable 记一段"设备明确说读不到"的号码。
func (sc *pointScanner) markUnreadable(count int) {
	sc.result.Scanned += count
	sc.result.Unreadable += count
}

// buildPoint 组装一个点位。
func (sc *pointScanner) buildPoint(index uint16, raw []byte) driver.DiscoveredPoint {
	p := driver.DiscoveredPoint{Name: formatRegisterName(index)}
	if allZero(raw) {
		// Value 留空、只给 Note：零值点位在界面上是"没有值"的那一类，
		// 前端据此实现"显示 0 值"这枚开关（插件侧刻意不做过滤）。
		p.Note = pointScanZeroNote
		return p
	}
	v, _, err := decodeValue(raw, sc.opt.dataType)
	if err != nil {
		// 解析失败不改判"读到了"：地址确实是读到了，只是值渲染不出来。
		p.Note = "值解析失败：" + err.Error()
		return p
	}
	p.Value = v
	return p
}

// finish 收尾：填耗时与两个标志，并把 nil 切片规整成空切片。
func (sc *pointScanner) finish() *driver.PointScanResult {
	if sc.incomplete {
		sc.result.Incomplete = true
	}
	sc.result.ElapsedMs = int(time.Since(sc.started).Milliseconds())
	if sc.result.Points == nil {
		// nil 切片过线是 JSON null，前端得为它多写一条分支。
		sc.result.Points = []driver.DiscoveredPoint{}
	}
	return sc.result
}

// formatRegisterName 寄存器号的点位地址写法。必须能被 parseRegisterAddress
// 原样还原 —— 这条不变量由 pointscan_test.go 的往返测试钉住。
func formatRegisterName(index uint16) string {
	return fmt.Sprintf("R[%d]", index)
}

// allZero 原始字节是否全为 0。
func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// ==================== 驱动侧的扫描读 ====================

// scanRead 实现 registerScanReader。
//
// **不经过 driver.Read**：那条路为持续采集设计 —— 块读失败会触发 5 分钟静默期、
// 首次块读的交叉校验状态会被记在驱动实例上，而扫描是一次性的范围探测，既不该
// 污染采集状态，也需要在每次请求上遵守扫描剩余预算（driver.Read 的预算固定 45s）。
func (d *fanucDriver) scanRead(class uint16, start uint16, count int, deadline time.Time) ([]byte, error) {
	d.mu.RLock()
	c := d.client
	lim := d.limit
	d.mu.RUnlock()

	if c == nil || !c.isConnected() {
		return nil, fmt.Errorf("fanuc: not connected")
	}
	if lim != nil {
		lim.wait()
	}
	if !time.Now().Before(deadline) {
		return nil, errScanDeadline
	}

	var (
		data []byte
		err  error
	)
	if count <= 1 {
		data, err = c.readAttrUntil(class, start, deadline)
	} else {
		data, err = c.readBlockUntil(class, start, count, deadline)
	}
	if err != nil {
		// 超时只有在**扫描预算本身**已经耗尽时才折算成"未扫完"；
		// 设备配置的单次请求超时（默认 5s）触发得更早时，那是连接问题，
		// 按传输错误中止并带回已完成的部分 —— 不能把"设备不回话"伪装成
		// 一份看起来正常的残缺清单。
		if isTimeoutErr(err) && !time.Now().Before(deadline) {
			return nil, errScanDeadline
		}
		return nil, err
	}
	if len(data) < count*4 {
		return nil, errScanShortData
	}
	return data, nil
}

// isTimeoutErr 判断错误是否由套接字截止时刻触发。
//
// 扫描侧把它映射成"预算耗尽"而不是"设备拒绝"：两者的收尾方式不同 ——
// 前者把 Incomplete 置位正常返回，后者会中止扫描。
func isTimeoutErr(err error) bool {
	var ne interface{ Timeout() bool }
	return errors.As(err, &ne) && ne.Timeout()
}
