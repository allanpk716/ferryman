package daemon

// ┌─────────────────────────────────────────────────────────────────────────┐
// │ 锁序铁律（本包全部临界区的唯一顺序——票13 范式，守望/闸门各票复用）        │
// │                                                                         │
// │ windowsMu（外层）→ ledger.Mu()（内层）。凡需双资源的开窗临界区两把同序    │
// │ 全拿；反向嵌套（ledgerMu → windowsMu）即 AB-BA 死锁，绝不出现。           │
// │                                                                         │
// │ 台账锁内只有内存操作：持 ledger.Mu() 的临界区里——                        │
// │   1) 绝不再取 windowsMu 之外的锁；                                       │
// │   2) 绝不调用带 windowsMu 的公共方法（WindowWait/ParkingOpen/NoteUsage/  │
// │      NoteGatePrompt/Subagent/QWatchStop 一律禁入）——探测与读改写用无锁  │
// │      内方法（parkingOpenLocked、ledger.SubagentActiveLocked、           │
// │      ledger.AllSessionsLocked、直读共享引用字段）；                      │
// │   3) 绝不发生记账——账本落盘 I/O 一律锁外（QWatchStop 的"锁外落账"即此   │
// │      纪律；窗口闭账在 windowsMu 下、ledgerMu 之外进行，同合规）。        │
// │                                                                         │
// │ Python 版差异（如实声明）：server.parking_open 靠 GIL 做"无锁读"规避     │
// │ _wlock→ledger.lock 反向嵌套；Go 无 GIL，map 并发读是数据竞争——等价解为  │
// │ 全部探测走 windowsMu：守望开窗临界区本就双锁同序全拿（windowsMu 外层 →  │
// │ ledgerMu 内层），在其中调用 *Locked 探测不构成反向嵌套；临界区外的调用   │
// │ 走自取 windowsMu 的公共方法。互斥的权威握手仍在两侧开窗点的台账锁内      │
// │ 完成（subagent 落新窗在台账锁内复验等答复窗后），任一后到者必看见先到    │
// │ 者的窗。                                                                │
// └─────────────────────────────────────────────────────────────────────────┘
//
// 等待窗口状态机（规格 ferryman/server.py:336-638，语义+注释逐字搬运——
// 这些注释就是不变量文档）。

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"ferryman/internal/accounts"
	"ferryman/internal/cctrans"
	"ferryman/internal/clock"
	"ferryman/internal/ledger"
	"ferryman/internal/mathx"
	"ferryman/internal/pathsx"
)

// winKey 窗口表键 (agent, session_id)。
type winKey = [2]string

// waitWindow 一条等待窗口（Python 窗口 dict 的 Go 形）。StopTS 非 nil = 停车
// 挂起（async 真身仍在跑）；SawAsync = 本窗曾异步启动（锁存，防交错派发丢窗）。
type waitWindow struct {
	OpenedTS float64
	StopTS   *float64
	SawAsync bool
}

// Subagent T32：SubagentStart/Stop 事件上报 → 台账计数。非法 event 返回
// error（→400，httpapi 票15 映射）。
func (d *Daemon) Subagent(body map[string]any) (map[string]any, error) {
	event := pyStr(body["event"])
	if event != "start" && event != "stop" {
		return nil, fmt.Errorf("event 必须是 start|stop，得到: '%s'", event)
	}
	agent := pyStrOr(body["agent"], "cc")
	sessionID := pyStr(body["session_id"])
	if sessionID == "" {
		return nil, errors.New("session_id 不能为空")
	}
	count := d.Ledger.SubagentEvent(agent, sessionID, event)
	d.Stats.addSubagentEvent()
	// T41/T48 等待窗口：首个子代理 start 开窗（嵌套不重复开）；计数归零时
	// 同步派发即闭窗（旧语义 subagents_done），异步派发停表停车等主会话恢复。
	key := winKey{agent, sessionID}
	d.windowsMu.Lock()
	defer d.windowsMu.Unlock()
	if count > 0 {
		w := d.windows[key]
		if w == nil || clock.Now()-w.OpenedTS > ledger.SubagentEventLeakS {
			// 首开；或上一轮 Stop 丢失、泄漏超时后重锚（旧窗不沿用，防
			// dur_s 虚高跨泄漏间隙，R10）。旧窗若为停车窗，如实闭账：
			// 按距 stop_ts 超 PARK_EXPIRE_S 判过期（附录#3/#13，非
			// opened_ts）；closed_ts 封顶 now，绝不出现未来时刻。
			var old *waitWindow
			if w != nil {
				delete(d.windows, key)
				old = w
			}
			if old != nil && old.StopTS != nil {
				// 旧窗闭账失败不弄断钩子（丢行可接受）——Acct 打印吞错，
				// 与 Python except 分支同口径。
				d.recordWindowLocked(key, old, "expired",
					math.Min(*old.StopTS+ParkExpireS, clock.Now()))
			}
			// T51 两窗互斥（先开者赢）check-then-act 临界区（票03 移植）：
			// 首开与重锚共用此点——新窗落表与等答复窗复验同在台账锁内
			// 完成，与守望开等答复窗的临界区（daemon._maybe_qwatch，票16
			// 双锁同序复用本范式）互为对侧握手，任一后到者必看见先到者的
			// 窗，毫秒级双窗并存窗口归零。等答复窗开着 → 不开停车窗（等
			// 答复窗只由新写入关窗，start/stop 不动它；互斥双向承重——丢了
			// 它 gate 豁免会与摆渡推迟＋心跳叠加）。锁序铁律：windowsMu
			// 外层 → ledgerMu 内层（gate/note_usage/window_wait 等既有路径
			// 同序；守望侧临界区持台账锁时只做无锁探测 parking_open，
			// 反向嵌套即死锁——Go 形见本文件顶部铁律块）。
			w = nil
			// B 修复（票14 评审）：引用必须在台账锁内经 GetLocked 重取——
			// 锁外 Get 与锁内复验之间的两段式 TOCTOU 归零（锁外取到的引用
			// 可能已非映射的权威条目）。
			d.Ledger.Mu().Lock()
			st := d.Ledger.GetLocked(agent, sessionID)
			qwatchOpen := st != nil && st.QWatchOpenedTS != nil
			if !qwatchOpen {
				w = &waitWindow{OpenedTS: clock.Now(), SawAsync: false}
				d.windows[key] = w
			}
			d.Ledger.Mu().Unlock()
		} else if w.StopTS != nil {
			w.StopTS = nil // 停车窗又来 start：续窗（再派/嵌套）
		}
		// T48（附录#7）：start 时尾判 async 亦置锁存——覆盖"sync start
		// 先于 async stop"的重叠序（停车判定只在计数归零时跑，届时尾部
		// 最后派发已是 sync，唯有此处置位才不丢 async 等待）。
		// T51：互斥挡开时无窗可锁存（w=nil），跳过。
		if w != nil && !w.SawAsync {
			d.latchAsyncIfTail(agent, sessionID, w)
		}
	} else if count == 0 {
		if _, ok := d.windows[key]; ok {
			d.parkOrCloseLocked(key, agent, sessionID)
		}
	}
	return map[string]any{"ok": true, "active": count > 0}, nil
}

// parkOrCloseLocked 计数归零：同步派发→即闭窗（旧语义）；异步派发→停表停车
// （T48）。须持 windowsMu 调用。
//
// saw_async 锁存：本窗曾以异步启动（stop 尾判或 start 尾判置位，后者覆盖
// "sync start 先于 async stop"的重叠序），则后续同步派发的 stop 也停车——
// 交错派发（async A 在飞 + 再派 sync B）不丢 A 的等待（round 0 e2 实验：
// 无锁存时 B 的 stop 会使整窗误闭）。异步判据读主会话转录尾部
// （has_async_launch，cc 轨专属）；判不中（CC 改文案/钩子早于文件落盘的
// 竞态）一律退回旧语义=即闭——宁可少记一个真窗，不误停一个假窗。
func (d *Daemon) parkOrCloseLocked(key winKey, agent, sessionID string) {
	w, ok := d.windows[key]
	if !ok || w.StopTS != nil {
		return
	}
	st := d.Ledger.Get(agent, sessionID)
	path := ""
	if st != nil {
		path = st.TranscriptPath
	}
	if agent == "cc" && path != "" &&
		(w.SawAsync || cctrans.HasAsyncLaunch(path)) {
		now := clock.Now()
		w.StopTS = &now   // 停表停车：async 真身仍在跑
		w.SawAsync = true // 锁存：此后本窗一律停车语义
	} else {
		d.closeWindowLocked(key, "subagents_done", 0)
	}
}

// latchAsyncIfTail start 事件处理时尾判 async → 置 saw_async 锁存（T48 附录#7）。
// 须持 windowsMu 调用。
//
// 重叠序"async start→sync start→async stop→sync stop"里停车判定只在计数
// 归零时跑，届时尾部最后派发已是 sync——不在此处置位就会误闭 async 等待。
// 判定读主会话转录尾部（has_async_launch，cc 轨专属）。Python 版此处以
// try/except 打印兜底（"判定失败只是少一个锁存，退回旧语义，绝不弄断
// /subagent 钩子"）；Go 版无异常源——cctrans.HasAsyncLaunch 内部吞一切
// 读失败（坏行/缺字段/OSError 一律 False），Ledger.Get 不可失败，同语义
// 由返回 False 承接。
func (d *Daemon) latchAsyncIfTail(agent, sessionID string, w *waitWindow) {
	st := d.Ledger.Get(agent, sessionID)
	path := ""
	if st != nil {
		path = st.TranscriptPath
	}
	if agent == "cc" && path != "" && cctrans.HasAsyncLaunch(path) {
		w.SawAsync = true
	}
}

// closeWindowLocked 闭等待窗口并入账 window 流水。须持 windowsMu 调用。
//
// T48（附录#10）：先记账后 pop——记账路径异常时窗保留在表（绝不"先丢窗
// 后记账失败"），下次触发可重试；旧版"pop 先行天然幂等"改由调用方全部持
// windowsMu 保证（并发 close 被串行化，不可能双记）。Go 版记账走 Acct
// （打印吞错不外抛，与 Python _acct 同纪律），故 pop 恒达；closedTS<=0
// 映射 Python closed_ts=None → 当前时刻。
func (d *Daemon) closeWindowLocked(key winKey, reason string, closedTS float64) {
	w, ok := d.windows[key]
	if !ok {
		return
	}
	d.recordWindowLocked(key, w, reason, closedTS)
	delete(d.windows, key)
}

// recordWindowLocked window 流水入账（从 _close_window 拆出：重锚旧停车窗等
// "窗已摘、账仍要记"的路径复用）。closedTS<=0 缺省取当前时刻（过期闭窗传
// stop+PARK_EXPIRE_S，如实反映"只观察到这"）。Accounts=nil（旧测试形态）
// 整条跳过。须持 windowsMu 调用。
//
// A 修复（票13 评审 Minor A）：PeakCtx/Cwd 是可变字段（TouchFull 写者并发），
// 在 windowsMu→ledger.Mu 同序双锁下快照（GetLocked）后出锁使用——账本读盘
// 绝不持台账锁（"锁内只有内存操作"铁律）。
func (d *Daemon) recordWindowLocked(key winKey, w *waitWindow, reason string, closedTS float64) {
	if d.Accounts == nil {
		return
	}
	agent, sid := key[0], key[1]
	closed := closedTS
	if closed <= 0 {
		closed = clock.Now()
	}
	d.Ledger.Mu().Lock()
	st := d.Ledger.GetLocked(agent, sid)
	var peak int
	if st != nil {
		peak = st.PeakCtx
	}
	d.Ledger.Mu().Unlock()
	d.Acct("window", st, agent, sid, "", accounts.Fields{
		"opened_ts":     mathx.Round(w.OpenedTS, 3),
		"closed_ts":     mathx.Round(closed, 3),
		"dur_s":         mathx.Round(closed-w.OpenedTS, 1),
		"prefix_tokens": d.windowPrefixLocked(peak, sid, w.OpenedTS),
		"close_reason":  reason,
	})
}

// windowPrefixLocked T46 窗口前缀懒富化：peak_ctx 优先（摆渡提取富化过，行为
// 不变）；缺位时从账本 usage 实报值回落——开窗前（ts <= opened_ts）该会话最后
// 一条的 input+cache_read+cache_creation（API 实报的完整请求输入，比提取器估
// 算准）；开窗前的行一条都没有（时钟毛刺）则退取该会话任意最后一条，再无则 0。
// 任何异常吞成 0——窗口行绝不因富化失败而丢。须持 windowsMu 调用（Python
// 版同在 _wlock 下读账本盘；记账富化永不弄断闭窗，与 _acct 同纪律）。
// A 修复（票13 评审 Minor A）：peakCtx 由调用方在 windowsMu→ledger.Mu 同序
// 双锁下快照传入，本函数不再触碰共享引用。
func (d *Daemon) windowPrefixLocked(peakCtx int, sid string, openedTS float64) int {
	if peakCtx != 0 {
		return peakCtx
	}
	if d.Accounts == nil {
		return 0
	}
	rows := d.Accounts.Read(accounts.ReadOpts{Kind: "usage", Session: sid})
	pool := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		if acctNum(r, "ts") <= openedTS {
			pool = append(pool, r)
		}
	}
	if len(pool) == 0 {
		pool = rows
	}
	if len(pool) == 0 {
		return 0
	}
	sort.SliceStable(pool, func(i, j int) bool { // 稳定序：同 ts 取后写入
		return acctNum(pool[i], "ts") < acctNum(pool[j], "ts")
	})
	latest := pool[len(pool)-1]
	return int(acctNum(latest, "input_tokens") +
		acctNum(latest, "cache_read_tokens") +
		acctNum(latest, "cache_creation_tokens"))
}

// NoteUsage T48 闭窗道：主会话恢复调用（usage 行 ts 晚于停表+ack 宽限）→
// 等待结束，闭窗记 main_resumed。由守望 usage 采集每轮喂新行最大 ts（票03
// 接线）。
//
// ACK_GRACE_S 内的行视为派发确认回合（ack），不闭窗——2026-09-18 实测
// ack 落在 stop 前，此宽限是钩子时序反转时的保险（round 0 评审 #10/#11）。
// ★ 异常边界（附录#10）：本方法被守望主路径调用，保证不炸——Python 以
// try/except 打印兜底；Go 无异常，Acct 打印吞错即此保证的 Go 形。Go 契约
// （票14 顺手清）：闭账路径 pop 恒达——记账失败不保留窗（与 closeWindowLocked
// 的差异声明同口径；TestWindowWaitNeverRaises /
// TestNoteUsageRecordFailureKeepsWindow 钉的就是本契约）。
func (d *Daemon) NoteUsage(agent, sessionID string, ts float64) {
	key := winKey{agent, sessionID}
	d.windowsMu.Lock()
	defer d.windowsMu.Unlock()
	if w := d.windows[key]; w != nil && w.StopTS != nil &&
		ts > *w.StopTS+AckGraceS {
		d.closeWindowLocked(key, "main_resumed", 0)
	}
}

// WindowWait 缺口A 第三道（T48）：异步等待窗在停（stop 已到、主会话未恢复、
// 未过期）。
//
// 懒过期：停表超 PARK_EXPIRE_S → 闭窗记 expired（closed=stop+PARK_EXPIRE_S，
// 此刻必为过去时刻，如实反映"只观察到这"）并返回 false（豁免随之失效）。
// ★ 异常边界（附录#3/#5）：本谓词被闸门/守望主路径直接调用，任何内部异常
// 一律吞掉按 false 返回——宁可漏豁免，不炸主路径（Go 无异常：windowsMu 下
// 全内存操作 + Acct 打印吞错，该保证天然成立）。
func (d *Daemon) WindowWait(agent, sessionID string) bool {
	key := winKey{agent, sessionID}
	d.windowsMu.Lock()
	defer d.windowsMu.Unlock()
	w := d.windows[key]
	if w == nil || w.StopTS == nil {
		return false
	}
	if clock.Now()-*w.StopTS > ParkExpireS {
		d.closeWindowLocked(key, "expired",
			math.Min(*w.StopTS+ParkExpireS, clock.Now()))
		return false
	}
	return true
}

// ParkingOpen T51 两窗互斥探测（守望开等答复窗前调用）：停车窗是否"有效开着"。
// 公共形态自取 windowsMu；守望开窗临界区（双锁同序全拿）内用 parkingOpenLocked。
// 任何异常按未开（false），绝不影响守望主路径。
func (d *Daemon) ParkingOpen(agent, sessionID string) bool {
	d.windowsMu.Lock()
	defer d.windowsMu.Unlock()
	return d.parkingOpenLocked(agent, sessionID)
}

// parkingOpenLocked ParkingOpen 的已持锁内方法——仅供已持 windowsMu 的临界区
// （守望开窗的双锁同序临界区，票16 复用本范式）调用。
//
// 委托 T48 窗口状态的无副作用版（merge 改造，勿调 window_wait——它带懒过期
// 闭窗记账副作用，且不得在台账锁内触发）：活跃窗按计数道泄漏口径
// （SUBAGENT_EVENT_LEAK_S）判；停车窗按停表过期口径（PARK_EXPIRE_S）判——
// 与 window_wait 的豁免口径一致，过期旧窗视同已闭（不闭账，留给
// window_wait/重锚的正规路径如实收口）。
// Python 版此读为 GIL 原子的无锁读（本探测会在守望开窗临界区内于台账锁下
// 被调用，若此处再取 _wlock，将与 server.subagent 的 _wlock→ledger.lock 锁序
// 反向嵌套 AB-BA 死锁）；Go 版等价解见本文件顶部铁律块——临界区统一双锁
// 同序全拿，windowsMu 下读表即"同临界区内的一致视图"。
func (d *Daemon) parkingOpenLocked(agent, sessionID string) bool {
	_, open := d.waitWindowOpenLocked(agent, sessionID)
	return open
}

// waitWindowOpenLocked 窗口开着的只读探测（票04 等待窗泳道的最小读接口，
// parkingOpenLocked 的加细版）：开着返回 (opened_ts, true)。口径与
// parkingOpenLocked 完全一致（活跃窗按泄漏判、停车窗按 PARK_EXPIRE_S 判——
// 停车满 1h 视同已闭，F8"停车满 1h 懒过期窗口已闭→不排"），零副作用——
// 懒过期闭账留给 window_wait/重锚的正规路径。多回传 opened_ts 供泳道识别
// "重开新窗"（重置泳道状态、旧泳道如实收尾）。须持 windowsMu 调用。
func (d *Daemon) waitWindowOpenLocked(agent, sessionID string) (float64, bool) {
	w := d.windows[winKey{agent, sessionID}]
	if w == nil {
		return 0, false
	}
	if w.StopTS == nil {
		if clock.Now()-w.OpenedTS > ledger.SubagentEventLeakS {
			return 0, false
		}
		return w.OpenedTS, true
	}
	if clock.Now()-*w.StopTS > ParkExpireS {
		return 0, false
	}
	return w.OpenedTS, true
}

// NoteGatePrompt gate 步 0（server.py:143-151 逐字）：主会话来讯 = 等待提前
// 结束（词汇表"等待窗口"）——强续/bypass prompt 亦算恢复写入，故 gate 于
// bypass 判定之前调用本钩子（R1；且台账 miss 也要闭）。T48：停车窗（async
// 真身仍在跑）不因 prompt 闭——等待没结束，缺口 A 由 gate 的 machine-waiting
// 豁免接住放行；只有未停车窗照旧闭。票14 gate.go 接线。
func (d *Daemon) NoteGatePrompt(agent, sessionID string) {
	key := winKey{agent, sessionID}
	d.windowsMu.Lock()
	defer d.windowsMu.Unlock()
	if w := d.windows[key]; w != nil && w.StopTS == nil {
		d.closeWindowLocked(key, "prompt", 0)
	}
}

// QWatchStop 一键停（T51 票04）：mode 置 off＋取消全部在飞计划与未关窗口。
//
// 关窗而非只清计划：窗口还连着摆渡推迟与死线（_qwatch_deadline），停守望
// 必须连摆渡侧一并松开。只动内存（台账锁内清字段，关窗事件锁外落账
// close_reason=stop——四类事件口径完整；锁内只有内存操作，记账读盘不持台账
// 锁）；重启后仍以配置文件的 mode 为准（运行时开关不落盘）。
func (d *Daemon) QWatchStop() map[string]any {
	d.SetQWatchModeOff() // 骑手（票13 评审 Minor C）：mode 写经 cfgMu 护栏
	cancelled := 0
	type stopEnt struct {
		st       *ledger.SessionState
		openedTS float64
		fired    int
	}
	var stopped []stopEnt
	d.Ledger.Mu().Lock()
	for _, st := range d.Ledger.AllSessionsLocked() {
		if st.QWatchOpenedTS == nil && len(st.QWatchPlan) == 0 {
			continue
		}
		if st.QWatchOpenedTS != nil {
			stopped = append(stopped, stopEnt{st: st, openedTS: *st.QWatchOpenedTS,
				fired: st.QWatchBeatsFired})
		}
		st.QWatchOpenedTS = nil
		st.QWatchBeatsFired = 0
		st.QWatchPlan = nil
		st.QWatchSnapshot = nil
		cancelled++
	}
	d.Ledger.Mu().Unlock()
	for _, e := range stopped { // 锁外落账（记账读盘不持台账锁）
		closed := clock.Now()
		d.Acct("qwatch_close", e.st, "", "", "", accounts.Fields{
			"opened_ts":    mathx.Round(e.openedTS, 3),
			"closed_ts":    mathx.Round(closed, 3),
			"dur_s":        mathx.Round(math.Max(0.0, closed-e.openedTS), 1),
			"beats_fired":  e.fired,
			"close_reason": "stop",
		})
	}
	fmt.Printf("[qwatch] 一键停：mode→off，已取消 %d 个会话的窗口/计划\n", cancelled)
	return map[string]any{"ok": true, "mode": "off", "cancelled": cancelled}
}

// Acct 记账薄封装（Python _acct 1:1）：st 优先（lineage 用归一化 transcript
// 路径），无 st 用显式覆盖参数。lineage_id/project 可显式覆盖（inject 需按
// 交接源会话解析谱系，R9）。异常只打印——记账永不弄断闸门（与
// _book_handoff 同纪律）。
func (d *Daemon) Acct(kind string, st *ledger.SessionState,
	agentOv, sidOv, lineageOv string, f accounts.Fields) {
	if d.Accounts == nil {
		return
	}
	var agent, sessionID, lineageID, project string
	if st != nil {
		agent, sessionID = st.Agent, st.SessionID
		if st.TranscriptPath != "" {
			lineageID = pathsx.NormPath(st.TranscriptPath)
		} else {
			lineageID = sessionID
		}
		// A 修复（票13 评审 Minor A）：Cwd 可变（TouchFull 写者并发），台账锁内
		// 快照；Agent/SessionID/TranscriptPath 建后不变，直读。调用方（gate/
		// restore/QWatchStop/recordWindowLocked）均不持台账锁，此处加锁无
		// 重入/反序风险。
		d.Ledger.Mu().Lock()
		project = st.Cwd
		d.Ledger.Mu().Unlock()
	} else {
		agent, sessionID = agentOv, sidOv
		lineageID = sessionID // 无台账线索：lineage 退化为 session 自身
	}
	if lineageOv != "" {
		lineageID = lineageOv
	}
	ff := make(accounts.Fields, len(f)+4)
	for k, v := range f {
		if k == "project" { // project 可随字段覆盖（Python fields.pop("project")）
			project = pyStr(v)
			continue
		}
		ff[k] = v
	}
	ff["agent"] = agent
	ff["session_id"] = sessionID
	ff["lineage_id"] = lineageID
	ff["project"] = project
	if _, err := d.Accounts.Record(kind, -1, ff); err != nil {
		fmt.Printf("[account] %s 记账失败（忽略，闸门不受影响）: %v\n", kind, err)
	}
}

// acctNum 账本行数值字段的宽松收形（JSON 解码 float64；缺/型不符按 0）。
func acctNum(row map[string]any, k string) float64 {
	if v, ok := row[k].(float64); ok {
		return v
	}
	return 0
}
