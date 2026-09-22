package daemon

// 闸门状态机（规格 ferryman/server.py:135-334 逐字平移，票14）。
//
// 七分支顺序是权威序（server.py gate()）：
// 步0 闭窗钩子 → 1 强续/!! bypass → 2 台账 miss 放行 → 3 mode off →
// 2.5 机器等机器豁免 → observe 只警告 → enforce 完整状态机（5/6/7）。
// _warn_ctx/_cache_info_ctx/_notify_block/_acct 一并落本文件（_acct 已在
// windows.go 票13 落地，此处只接线）。
//
// 并发纪律（包注释「并发模型」）：st 为共享可变引用，Gate 只读其字段——
// 台账锁内一次性抄出快照（Python GIL 无锁读的 Go 形）；记账与摆渡入队
// 均在锁外。

import (
	"fmt"
	"strings"

	"ferryman/internal/accounts"
	"ferryman/internal/cctrans"
	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/ledger"
	"ferryman/internal/mathx"
	"ferryman/internal/notify"
	"ferryman/internal/qwatch"
	"ferryman/internal/store"
)

// ---- 骑手（票13 评审 Minor C）：question_watch.mode 运行时活值并发护栏 ----
//
// mode 是运行时开关：QWatchStop 写（一键停）、Health 读（/stats）、守望线程
// 读（票16 接线）。Go 的字符串读写无 Python GIL 兜底——读写收敛为 Daemon
// 方法，cfgMu 串行化。Gate 本身不读该值（server.py gate() 无此读，逐字平移
// 不添）；方法位即统一读写的唯一通道。

// GetQWatchMode 读 question_watch.mode 活值（/stats qwatch 节、守望侧共用）。
func (d *Daemon) GetQWatchMode() string {
	d.cfgMu.Lock()
	defer d.cfgMu.Unlock()
	return d.Cfg.QuestionWatch.Mode
}

// SetQWatchModeOff 一键停的 mode 置 off（QWatchStop 专用写通道）。
func (d *Daemon) SetQWatchModeOff() {
	d.cfgMu.Lock()
	d.Cfg.QuestionWatch.Mode = "off"
	d.cfgMu.Unlock()
}

// SetQWatchMode mode 通用写通道（票16 熔断降级 enforce→observe 用；一键停的
// off 写走 SetQWatchModeOff，同一 cfgMu 护栏）——question_watch.mode 的运行时
// 写点收敛于这对方法，守望侧经 Daemon 罩面调用。
func (d *Daemon) SetQWatchMode(v string) {
	d.cfgMu.Lock()
	d.Cfg.QuestionWatch.Mode = v
	d.cfgMu.Unlock()
}

// sessionSnap Gate 用的一次性台账快照（Python 直接读 st.* 的 Go 形——
// 共享可变引用读写均须持锁，见包注释并发模型）。
type sessionSnap struct {
	sid       string
	path      string
	lastWrite float64
	observed  bool
	peak      int
	contentTS float64 // 内容时钟（ADR-0013；0=未算 → coversBar 回落 lastWrite）
}

// coversBar 有效交接判定的覆盖基准（ADR-0013）：优先内容时钟（转录内最后
// 带时间戳记录——CC 状态块幻影写入不推进它），回落文件时钟。旧口径
// （covers ≥ mtime）在状态块写入下永不满足 → 交接结构性无效 → 闸门拦而
// 无交接可用；内容时钟口径恢复"被拦 ⇒ 交接必已存在"的可达性。
func (s sessionSnap) coversBar() float64 {
	if s.contentTS > 0 {
		return s.contentTS
	}
	return s.lastWrite
}

// Gate 闸门状态机主入口（server.py:135-249 逐字；七分支顺序即权威序）。
func (d *Daemon) Gate(body map[string]any) map[string]any {
	agent := pyStrOr(body["agent"], "cc")
	sessionID := pyStr(body["session_id"])
	transcriptPath := pyStr(body["transcript_path"])
	cwd := pyStr(body["cwd"])
	prompt := pyStr(body["prompt"])
	d.Stats.Hit(agent)

	// 0. 主会话来讯 = 等待提前结束（词汇表"等待窗口"）——强续/bypass prompt 亦算
	//    恢复写入，故闭窗钩子置于 bypass 判定之前（R1；且台账 miss 也要闭）。
	//    T48：停车窗（async 真身仍在跑）不因 prompt 闭——等待没结束，缺口 A
	//    由下方 2.5 的 machine-waiting 豁免接住放行；只有未停车窗照旧闭。
	d.NoteGatePrompt(agent, sessionID)

	// 1. 魔法前缀：单次放行（「强续」为主——CC 下 ! 首字符触发 bash 模式，!! 打不出来；
	//    !! 保留匹配以兼容 Codex）
	if strings.HasPrefix(prompt, "强续") || strings.HasPrefix(prompt, "!!") {
		d.Stats.addBypass()
		stB := d.Ledger.Get(agent, sessionID) // 只取一次（lineage 尽力而为）
		peak := 0
		if stB != nil {
			d.Ledger.Mu().Lock()
			peak = stB.PeakCtx
			d.Ledger.Mu().Unlock()
		}
		d.Acct("bypass", stB, agent, sessionID, "",
			accounts.Fields{"prefix_tokens": peak})
		return map[string]any{"decision": "allow", "reason": "bypass"}
	}
	st := d.Ledger.Get(agent, sessionID)
	if st == nil {
		st = d.Ledger.GetByPath(transcriptPath)
	}

	// 2. 无台账（daemon 没见过）→ 放行（保守；台账 miss 不拦）
	if st == nil {
		return map[string]any{"decision": "allow", "reason": "no-ledger"}
	}

	mode := d.Cfg.GateCC
	if agent != "cc" {
		mode = d.Cfg.GateCodex
	}
	if mode == "off" {
		return map[string]any{"decision": "allow", "reason": "mode-off"}
	}

	// 台账快照（锁内抄齐；其后闸门逻辑不持台账锁）。
	d.Ledger.Mu().Lock()
	snap := sessionSnap{sid: st.SessionID, path: st.TranscriptPath,
		lastWrite: st.LastWrite, observed: st.ObservedActive, peak: st.PeakCtx,
		contentTS: st.ContentTS}
	d.Ledger.Mu().Unlock()

	// 2.5 机器等机器豁免（缺口A，rev2 规格 docs/superpowers/specs/
	//     20260918-antfeedinglog子代理等待场景-consensus.rev2.md）：
	//     子代理在飞 或 jsonl 尾部悬空 tool_use → 放行本次输入，不警告/不拦截/
	//     不入队摆渡/不动 pending（含 observe 模式的警告与入队路径）。
	//     与三泳道裁决对齐：机器等机器不归闸门；摆渡路径同款检查见 daemon.py:123-125。
	if d.machineWaiting(agent, snap.sid, snap.path) {
		return map[string]any{"decision": "allow", "reason": "machine-waiting",
			"additional_context": ("[Ferryman] 检测到会话仍有未完成的工具调用/子代理，已放行本次输入" +
				"（不承诺生效时机）。若确认已卡死：Esc 中断后重发，" +
				"或以「强续」开头强制继续。")}
	}

	th := d.Cfg.ThresholdFor(agent)
	idle := clock.Now() - snap.lastWrite
	key := [2]string{agent, snap.sid}
	// pending 清除条件之一：新闲置周期（用户回来过又离开了 summarize 时长）
	p, pok := d.Pending.Get(key)
	if pok && snap.lastWrite > p.SetAt && idle >= th.SummarizeS {
		d.Pending.Clear(key)
		pok = false
	}
	inWindow := idle >= th.BlockS || pok

	// observe：只警告不拦（验证期默认）
	if mode == "observe" {
		if inWindow {
			h := d.Store.ValidHandoff(agent, cwd, snap.coversBar())
			if h == nil && snap.observed && snap.peak >= th.MinCtxTokens {
				d.EnqueueFerry(st)
			}
			return map[string]any{"decision": "allow",
				"additional_context": d.warnCtx(idle, h, false)}
		}
		return allowAllow(d.cacheInfoCtx(idle, th))
	}

	// enforce：完整状态机
	if !inWindow {
		return allowAllow(d.cacheInfoCtx(idle, th))
	}
	h := d.Store.ValidHandoff(agent, cwd, snap.coversBar())
	if h != nil { // 分支 5
		d.Pending.Clear(key)
		d.Stats.addBlocks()
		d.Acct("block", st, "", "", "", accounts.Fields{
			"prefix_tokens": snap.peak, "idle_s": mathx.Round(idle, 1)})
		d.Store.SavePendingPrompt(snap.sid, prompt)
		d.Store.MarkBlocked(h.HandoffID)
		d.notifyBlock(st, h, idle)
		return map[string]any{"decision": "block",
			"reason": fmt.Sprintf("此会话已闲置 %.0f 分钟，缓存已失效；"+
				"你刚输入的内容没有发出去，已原样保存、不会丢。\n"+
				"接下来这样做（约 10 秒）：\n"+
				"  1. 输入 /clear 清空上下文（或另开一个新会话，效果相同）\n"+
				"  2. 新会话开场会自动收到两样东西：干到哪的交接 + 你刚这句原话"+
				"（所以不用重新打字）\n"+
				"  3. 随便发一个字（如「继续」）——它会接着你刚那句继续干\n"+
				"不想换会话：以「强续」开头重发你的内容，本会话强制继续"+
				"（注意：原话只随 /clear 自动带回，强续必须自己带上）。\n"+
				"交接文档: %s", idle/60, h.Path),
			"suppressOriginalPrompt": true,
			"handoff_path":           h.Path}
	}
	if pok { // 分支 6
		n := d.Pending.BumpBlocks(key)
		if n > DegradeAfterBlocks { // 连续 3 次 block 之后降级（DESIGN §6.10-6）
			d.Pending.Clear(key)
			return map[string]any{"decision": "allow",
				"additional_context": d.warnCtx(idle, nil, true)}
		}
		d.Stats.addBlocks()
		d.Acct("block", st, "", "", "", accounts.Fields{
			"prefix_tokens": snap.peak, "idle_s": mathx.Round(idle, 1)})
		d.Store.SavePendingPrompt(snap.sid, prompt)
		return map[string]any{"decision": "block",
			"reason": fmt.Sprintf("此会话闲置超时被拦（第 %d 次）；你刚输入的内容已保存、不会丢。\n"+
				"继续干活：每次以「强续」开头发消息（每一条都会放行，无需数次数，"+
				"内容须自己带上）；或 /clear 换会话（若交接已生成会自动注入"+
				"交接与你的原话）。", n),
			"suppressOriginalPrompt": true}
	}
	// 分支 7：警告一次 + 置 pending + 触发摆渡
	d.Pending.Set(key)
	d.Stats.addWarns()
	if snap.observed && snap.peak >= th.MinCtxTokens {
		d.EnqueueFerry(st)
	}
	return map[string]any{"decision": "allow",
		"additional_context": d.warnCtx(idle, nil, true)}
}

// allowAllow {"decision": "allow", **({"additional_context": info} if info else {})}
// 的 Go 形：info 为空（Python None）不带键。
func allowAllow(info string) map[string]any {
	if info == "" {
		return map[string]any{"decision": "allow"}
	}
	return map[string]any{"decision": "allow", "additional_context": info}
}

// machineWaiting _machine_waiting（server.py:251-277 逐字）：缺口A：机器等机器
// 判定——子代理计数>0 / T48 异步停车窗 / 悬空 tool_use。
//
// 三道 OR 互为兜底：守护重启丢内存计数→悬空道兜底；泄漏期已过计数清零
// 而 tool_result 仍缺→悬空道兜底；T48 异步派发计数早归零而真身在跑→
// 停车窗道兜底（window_wait）。上界分道（rev2 如实声明）：计数道 1h
// 泄漏保护（ledger.py:28）；停车窗道 PARK_EXPIRE_S=1h（过期即豁免失效）；
// 悬空道无时间上界、有内容量上界——尾部 256KB 滑窗（transcripts.py:73），
// 悬空事件被后续内容顶出窗口即失效。
// Python 三道 try/except（豁免判定异常不影响闸门主路径）的 Go 形：
// SubagentActive/WindowWait/HasDanglingToolUse 各自保证不 panic（内部吞错
// 按 false 返回），"宁可走正常闸门路径，不误豁免"由无异常源承接。
func (d *Daemon) machineWaiting(agent, sessionID, path string) bool {
	if d.Ledger.SubagentActive(agent, sessionID) {
		return true
	}
	if d.WindowWait(agent, sessionID) { // T48 第三道：异步停车窗（停表未过期）
		return true
	}
	if path == "" {
		return false
	}
	return cctrans.HasDanglingToolUse(path)
}

// warnCtx _warn_ctx（server.py:279-289 逐字）。
// observe 永不拦——"将被拦"只在 enforce 成立（2026-09-18 文案缺陷修复：
// 两模式共用一句空头支票，用户按文案预期被拦却没拦）。
func (d *Daemon) warnCtx(idle float64, h *store.Entry, willBlock bool) string {
	tail := "交接生成中，下次提交将被拦。"
	if !willBlock {
		tail = "交接生成中（observe 模式只提醒不拦；enforce 才会真拦）。"
	}
	doc := tail
	if h != nil {
		doc = "交接文档: " + h.Path
	}
	txt := fmt.Sprintf("[Ferryman] 本会话已闲置 %.0f 分钟，缓存大概率已失效，"+
		"继续使用将全量重付 input。", idle/60) +
		doc + "建议 /clear 后开新会话（自动注入交接）。"
	return mathx.RuneTrunc(txt, WarnContextCap) // txt[:WARN_CONTEXT_CAP] 按码点
}

// cacheInfoCtx _cache_info_ctx（server.py:291-298 逐字）：T44b 缓存死线纯提醒：
// 只报信息，不拦、不触发摆渡、不置 pending。返回 "" ≡ Python None。
func (d *Daemon) cacheInfoCtx(idle float64, th config.ThresholdCfg) string {
	w := th.CacheWarnS
	if w == 0 || !(w <= idle && idle < th.BlockS) {
		return ""
	}
	return fmt.Sprintf("[Ferryman] 提示：本会话已闲置 %.0f 分钟，缓存大概率已失效——"+
		"这条消息的前缀将按全价计费（一次性差额）。无需操作；"+
		"闲置满 %.0f 分钟后会有交接备好，届时可换新会话。", idle/60, th.BlockS/60)
}

// notifyBlock _notify_block（server.py:300-309 逐字）：T25 Pushover/Toast 双通道
// （文案带交接路径）；异步 goroutine——gate 返回不等通知，通道内任何故障
// 自行吞掉（绝不影响 block 决策）。NotifyBlock 字段为注入 seam（Python
// monkeypatch notify_mod.notify_block 同位；nil 回落真通道）。
func (d *Daemon) notifyBlock(st *ledger.SessionState, h *store.Entry, idle float64) {
	agent, sid := st.Agent, st.SessionID // goroutine 只碰本地副本（共享引用纪律）
	// 票08 R1（评审补丁）：Cwd/Title 是可变字段（ai-title 更新），无锁直读与
	// 仓库纪律（台账锁内快照——见 serve.go enqueue/alertCopy）不一致；agent/sid
	// 创建后不可变，维持原样。本函数不在台账锁内（评审核实），短持快照安全。
	d.Ledger.Mu().Lock()
	cwd, title := st.Cwd, st.Title
	d.Ledger.Mu().Unlock()
	fmt.Printf("[gate] BLOCK %s/%s idle=%.0fm handoff=%s\n",
		agent, runeCap8(sid), idle/60, h.HandoffID)
	fn := d.NotifyBlock
	if fn == nil {
		fn = notify.NotifyBlock
	}
	path, cfg := h.Path, d.Cfg
	go fn(path, agent, sid, cwd, title, cfg)
}

// runeCap8 Python s[:8]：按码点截断。
func runeCap8(s string) string {
	if rs := []rune(s); len(rs) > 8 {
		return string(rs[:8])
	}
	return s
}

// qwatchMissSignals _qwatch_miss_signals（server.py:595-607 逐字）：票06 漏检
// 关联计数：全量重付的闲置复活请求 × 此前 30 分钟内末条疑似提问命中且其间无
// 真跳保温（口径见 qwatch.correlate_miss_signals）。/stats 拉取时扫近 24h 账本
// 行现算（无内存态，重启不丢口径）；只出计数（隐私铁律）。
// Python try/except（账本读失败按无信号）的 Go 形：Accounts.Read 读失败按空
// 行集、CorrelateMissSignals 纯函数不 panic——按 0 的语义由无异常源承接。
func (d *Daemon) qwatchMissSignals() int {
	if d.Accounts == nil {
		return 0
	}
	return qwatch.CorrelateMissSignals(
		d.Accounts.Read(accounts.ReadOpts{Since: clock.Now() - QWatchMissScanS}))
}

// ---- GateStats 计数通道（Python stats.bypass/blocks/warns += 1 的锁内形） ----

func (s *GateStats) addBypass() { s.mu.Lock(); s.Bypass++; s.mu.Unlock() }
func (s *GateStats) addBlocks() { s.mu.Lock(); s.Blocks++; s.mu.Unlock() }
func (s *GateStats) addWarns()  { s.mu.Lock(); s.Warns++; s.mu.Unlock() }
