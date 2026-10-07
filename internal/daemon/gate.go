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
	"math"
	"strings"

	"ferryman/internal/accounts"
	"ferryman/internal/cctrans"
	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/ferry"
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

	// 1. 魔法前缀：放行 + 解除本轮拦截（「强续」为主——CC 下 ! 首字符触发 bash
	//    模式，!! 打不出来；!! 保留匹配以兼容 Codex）。强续=用户明确选择留在本
	//    会话，必须清 pending：inWindow 含 `|| pok`，pending 不清则活跃会话被
	//    永久卡在拦截窗口（2026-09-30 06703fbd 案：强续放行后 58.6s 的下一条
	//    消息仍被分支6第3次拦截——pending 原只在闲置≥summarize_s 的新周期才清，
	//    正在对话的用户永不满足，强续沦为每条消息都要带前缀的跑步机）。清除后
	//    若再长闲置，从分支7警告重新起圈，拦截保护不丢。
	if strings.HasPrefix(prompt, "强续") || strings.HasPrefix(prompt, "!!") {
		d.Stats.addBypass()
		stB := d.Ledger.Get(agent, sessionID) // 只取一次（lineage 尽力而为）
		if stB == nil {
			stB = d.Ledger.GetByPath(transcriptPath) // 与分支6/7 的 pending 键同源解析
		}
		peak := 0
		if stB != nil {
			d.Ledger.Mu().Lock()
			peak = stB.PeakCtx
			d.Ledger.Mu().Unlock()
			d.Pending.Clear([2]string{agent, stB.SessionID})
		}
		d.Pending.Clear([2]string{agent, sessionID})
		// 票02（D2 消耗语义/D4 保守面）：dsh 强续即消耗该会话现行交接——之后
		// 新会话要么拿到含最新进展的新交接、要么明说没有，绝不端旧快照。消耗
		// 键=会话自身的交接（Entry.SessionID），不动同 (agent,cwd) 其他会话；
		// cc/codex 永不消耗（行为逐字零变化）。会话键与分支内 stB 同源解析：
		// 命中取其 SessionID（建后不可变字段，直读同 Acct 纪律）；台账 miss
		// 回落原始 session_id（与 Acct 的覆盖参数同口径）。
		if agent == "dsh" && d.Store != nil {
			sid := sessionID
			if stB != nil {
				sid = stB.SessionID
			}
			d.Store.ConsumeHandoffs(agent, sid)
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
	if agent == "dsh" {
		// dsh-gate-ux P3 前置：独立档 gate.dsh_mode。空值（直构 Config 绕过
		// Load 回落的老测试/替身形态）运行时回落 codex_mode＝旧行为零变化。
		mode = d.Cfg.GateDsh
		if mode == "" {
			mode = d.Cfg.GateCodex
		}
	} else if agent != "cc" {
		mode = d.Cfg.GateCodex
	}
	if mode == "off" {
		return map[string]any{"decision": "allow", "reason": "mode-off"}
	}

	// 闲置锚点换内容时钟（ADR-0013 的幻影写入免疫，2026-09-30 603fef0f 案）：
	// CC 对开着会话周期性落无时间戳状态块（mode/snapshot/lastPrompt，实测一场
	// 会话 149 条），文件时钟被反复顶新——开窗口的会话闲置钟被稀释、永不进拦窗
	// （真闲置 78 分钟被压成 17 分钟，只提醒不拦）。内容时钟=转录内最后带时间戳
	// 记录；watcher 侧只在摆渡入队路径懒刷新（活跃会话不经过），闸门须自算。
	// 取不到（0）回落文件时钟＝旧行为（fail-open）。仅 CC：幻影写与 cctrans 的
	// timestamp 解析都是 CC 转录语义，codex 维持文件时钟（ADR-0013 范围）。
	if agent == "cc" {
		d.contentClockFresh(st)
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
		// 尾句按 agent 分支（2026-10-05 漏拦案顺带）：dsh 无 Esc 中断，换
		// 中性指引；cc 逐字零变化。
		tail := "（不承诺生效时机）。若确认已卡死：Esc 中断后重发，" +
			"或以「强续」开头强制继续。"
		if agent == "dsh" {
			tail = "（不承诺生效时机）。若确认已卡死：以「强续」开头重发强制继续。"
		}
		return map[string]any{"decision": "allow", "reason": "machine-waiting",
			"additional_context": ("[Ferryman] 检测到会话仍有未完成的工具调用/子代理，已放行本次输入" + tail)}
	}

	th := d.Cfg.ThresholdFor(agent)
	idle := clock.Now() - snap.coversBar() // coversBar=内容时钟优先，幻影写免疫
	key := [2]string{agent, snap.sid}
	// pending 清除条件之一：新闲置周期（用户回来过又离开了 summarize 时长）
	p, pok := d.Pending.Get(key)
	if pok && snap.coversBar() > p.SetAt && idle >= th.SummarizeS {
		d.Pending.Clear(key)
		pok = false
	}
	inWindow := idle >= th.BlockS || pok

	// 热缓存不拦（2026-10-04 用户拍板）：拦窗到了但判热时钟仍在必活带
	// （同模型重放/心跳/用户自己的请求都喂钟，F3 口径）——缓存未死，这条
	// 消息按折扣价，拦它=误拦便宜请求（35~50min 保温覆盖带的错位形态：
	// 修好空键存键后此带会被「正确地拦」，故补本道）。放行+提示死线；
	// pending 同强续款清掉（保温抬高了续用的经济性，拦截前提暂时不成立；
	// 缓存再死、再长闲置则从分支7重新起圈，保护不丢）。冷/无钟/无观测
	// → 判冷，原状态机零变动（绝不因缺数据放行）。dsh 同道（票03
	// dsh-gate-ux 解锁）：现状 dsh 无钟数据恒判冷、行为零差异——解锁是为
	// 将来 dsh 心跳真发喂钟后不误拦（判热钟两 agent 共用同一实例）。
	if inWindow {
		if hot, remain := d.cacheHot(snap.sid); hot {
			d.Pending.Clear(key)
			d.gateWarn(agent, snap.sid, "hot-allow", idle)
			return allowAllow(d.hotCtx(idle, remain))
		}
		// 票04 dsh-hot-compaction gate 联动：已压缩短前缀不拦——拦窗内先查压缩
		// 标记（判定单源票02 DshCompressedActive：标记在∧未过死线∧标记后无
		// 机器产出流量，gate 只消费不重复实现）：有效 ∧ 当前前缀低于放行线
		// （v0.9.3 票1 解耦：dshCompactPassLine＝max(pass_floor_tokens,
		// pass_ratio×压前峰值)，不再与 min_peak_tokens 共用一条线——首单事故
		// 2026-10-07 实锚：60562 压到 20562 撞 20000 线照样拦，放行线必须自成
		// 一线）→ 全量重付的前提不成立，放行不拦。pending 同 hot-allow 款清掉
		//（红利失效后再长闲置从分支7 重新起圈，保护不丢）；否则照旧拦。标记
		// 唯一置位口 = /dsh/compacted（票02），只挂 dsh 会话——cc/codex 查无
		// 标记＝行为零变化。
		if prefix, prePeak, ok := d.DshCompressedActive(snap.sid); ok &&
			prefix < d.dshCompactPassLine(prePeak) {
			d.Pending.Clear(key)
			d.gateWarn(agent, snap.sid, "compacted-short-prefix", idle)
			return map[string]any{"decision": "allow",
				"reason":             "compacted-short-prefix",
				"additional_context": d.compactedShortCtx(idle)}
		}
	}

	// observe：只警告不拦（验证期默认）
	if mode == "observe" {
		if inWindow {
			h := d.Store.ValidHandoff(agent, cwd, snap.coversBar())
			// dsh 摆渡已接线（票03 dsh-gate-ux）：材料=渡口主快照
			//（worker.doDsh；接法乙 2026-10-03 后快照键可得）——旧"终局修复2"
			// 排除（材料不可得，入队必败→空骨架→covers=0 循环不收敛）随其
			// 作废，两 agent 同机制入队。
			if h == nil && snap.observed && snap.peak >= th.MinCtxTokens {
				d.EnqueueFerry(st)
			}
			d.gateWarn(agent, snap.sid, "observe", idle)
			return map[string]any{"decision": "allow",
				"additional_context": d.warnCtx(agent, idle, h, false)}
		}
		return allowAllow(d.cacheInfoCtx(idle, th))
	}

	// enforce：完整状态机
	if !inWindow {
		return allowAllow(d.cacheInfoCtx(idle, th))
	}
	h := d.Store.ValidHandoff(agent, cwd, snap.coversBar())
	if agent == "dsh" && d.Cfg.GateDshAutoContinue { // 票2：无卡直续（先于分支5/6 一切副作用）
		return d.dshAutoContinue(sessionID, transcriptPath, st, snap.peak, idle)
	}
	if h != nil { // 分支 5
		d.Pending.Clear(key)
		d.Stats.addBlocks()
		d.Acct("block", st, "", "", "", accounts.Fields{
			"prefix_tokens": snap.peak, "idle_s": mathx.Round(idle, 1)})
		d.Store.SavePendingPromptFor(agent, cwd, snap.sid, prompt)
		d.Store.MarkBlocked(h.HandoffID)
		d.notifyBlock(st, h, idle)
		// 【推荐】段按 agent 分支（票01/D3）：dsh 无 /clear 命令，换「新建会话」
		// 引导；cc 逐字零变化。强续段落与交接文档行两 agent 同文。
		rec := "\n【推荐】/clear 换新会话（约 10 秒，进度和原话自动带过去）：\n" +
			"  1. 输入 /clear\n" +
			"  2. 随便发一个字（如「继续」）\n" +
			"  新会话开场自动收到：本会话的进度交接 + 你这条原话，接着原话继续干。\n"
		if agent == "dsh" {
			rec = "\n【推荐】新建会话（桌面端点新建 / web 端 new session），" +
				"开场自动收到：本会话的进度交接+你这条原话。\n"
		}
		return map[string]any{"decision": "block",
			"reason": fmt.Sprintf("此会话已闲置 %.0f 分钟（缓存已失效）。"+
				"你刚输入的内容没有发出去，原话已保存：%s\n"+
				"%s"+
				"\n【不想换会话】以「强续」开头重发你的内容（例：「强续 %s」），"+
				"解除本轮拦截、留在本会话继续。\n"+
				"交接文档: %s", idle/60, blockPreview(prompt), rec, blockExample(prompt), h.Path),
			"suppressOriginalPrompt": true,
			"handoff_path":           h.Path}
	}
	if pok { // 分支 6
		n := d.Pending.BumpBlocks(key)
		if n > DegradeAfterBlocks { // 连续 3 次 block 之后降级（DESIGN §6.10-6）
			d.Pending.Clear(key)
			d.gateWarn(agent, snap.sid, "enforce-degrade", idle)
			return map[string]any{"decision": "allow",
				"additional_context": d.warnCtx(agent, idle, nil, true)}
		}
		d.Stats.addBlocks()
		d.Acct("block", st, "", "", "", accounts.Fields{
			"prefix_tokens": snap.peak, "idle_s": mathx.Round(idle, 1)})
		d.Store.SavePendingPromptFor(agent, cwd, snap.sid, prompt)
		// 【或换会话】段同上按 agent 分支（票01/D3）：分支6语义——交接若已生成
		// 会一并带给新会话，没好则只带回原话；cc 逐字零变化。
		alt := "\n【或 /clear 换新会话】开场发一个字即可；本会话的交接若已生成会" +
			"一并带给新会话，此刻还没好则新会话只会带回你这条原话（之前的进度" +
			"需要自己简述两句）。"
		if agent == "dsh" {
			alt = "\n【或新建会话】（桌面端点新建 / web 端 new session）开场发一个字即可；" +
				"本会话的交接若已生成会一并带给新会话，此刻还没好则新会话只会带回" +
				"你这条原话（之前的进度需要自己简述两句）。"
		}
		return map[string]any{"decision": "block",
			"reason": fmt.Sprintf("此会话闲置超时被拦（第 %d 次）。"+
				"你刚输入的内容没有发出去，原话已保存：%s\n"+
				"\n【现在就能继续】以「强续」开头重发你的内容（例：「强续 %s」），"+
				"解除本轮拦截、留在本会话。\n"+
				"%s", n, blockPreview(prompt), blockExample(prompt), alt),
			"suppressOriginalPrompt": true}
	}
	// 分支 7：警告一次 + 置 pending + 触发摆渡
	d.Pending.Set(key)
	d.Stats.addWarns()
	d.gateWarn(agent, snap.sid, "enforce", idle)
	if snap.observed && snap.peak >= th.MinCtxTokens { // dsh 已接线（票03 dsh-gate-ux；observe 分支同注）
		d.EnqueueFerry(st)
	}
	return map[string]any{"decision": "allow",
		"additional_context": d.warnCtx(agent, idle, nil, true)}
}

// allowAllow {"decision": "allow", **({"additional_context": info} if info else {})}
// 的 Go 形：info 为空（Python None）不带键。
func allowAllow(info string) map[string]any {
	if info == "" {
		return map[string]any{"decision": "allow"}
	}
	return map[string]any{"decision": "allow", "additional_context": info}
}

// contentClockFresh 闸门用的内容时钟现算：与 watcher.contentClock 同款缓存
// 纪律（ContentStamp==LastWrite 版本一致直接用缓存，否则读尾重算并回填台账；
// 读不到按 0 返回，调用侧 coversBar 回落文件时钟＝旧行为 fail-open）。两处
// 共用 ContentTS/ContentStamp 缓存字段，同值幂等，互踩无害。
func (d *Daemon) contentClockFresh(st *ledger.SessionState) float64 {
	d.Ledger.Mu().Lock()
	path := st.TranscriptPath
	lastWrite := st.LastWrite
	ts, stamp := st.ContentTS, st.ContentStamp
	d.Ledger.Mu().Unlock()
	if stamp == lastWrite {
		return ts
	}
	ts2, ok := cctrans.LastTimestamp(path)
	d.Ledger.Mu().Lock()
	if st.LastWrite == lastWrite {
		st.ContentTS, st.ContentStamp = ts2, lastWrite
	}
	d.Ledger.Mu().Unlock()
	if !ok {
		return 0
	}
	return ts2
}

// blockPreview 拦截文案里的原话预览（30 码点；空 prompt 占位）——"已保存"
// 看得见，用户才敢放心 /clear。
func blockPreview(prompt string) string {
	if pv := mathx.RuneTrunc(strings.TrimSpace(prompt), 30); pv != "" {
		return pv
	}
	return "（这条消息内容为空）"
}

// blockExample 强续前缀示例（同源截短；空 prompt 占位）。
func blockExample(prompt string) string {
	if ex := mathx.RuneTrunc(strings.TrimSpace(prompt), 20); ex != "" {
		return ex
	}
	return "你的内容"
}

// machineWaiting _machine_waiting（server.py:251-277 逐字）：缺口A：机器等机器
// 判定——子代理计数>0 / T48 异步停车窗 / 悬空 tool_use；dsh 道（票 A 判活
// 地基，dsh-heartbeat 规格「判活」节）：台账运行态（含族系子键）在效 → 豁免。
//
// cc/codex 三道 OR 互为兜底：守护重启丢内存计数→悬空道兜底；泄漏期已过计数清零
// 而 tool_result 仍缺→悬空道兜底；T48 异步派发计数早归零而真身在跑→
// 停车窗道兜底（window_wait）。上界分道（rev2 如实声明）：计数道 1h
// 泄漏保护（ledger.py:28）；停车窗道 PARK_EXPIRE_S=1h（过期即豁免失效）；
// 悬空道无时间上界、有内容量上界——尾部 256KB 滑窗（transcripts.py:73），
// 悬空事件被后续内容顶出窗口即失效。dsh 道上界＝ledger.DshRunStaleS（1h，
// 有界失效回正常闸门路径，同哲学）。
// Python 三道 try/except（豁免判定异常不影响闸门主路径）的 Go 形：
// SubagentActive/WindowWait/HasDanglingToolUse 各自保证不 panic（内部吞错
// 按 false 返回），"宁可走正常闸门路径，不误豁免"由无异常源承接。
// agent=="dsh" 跳过悬空道：cctrans 是 CC 转录语义，对 dsh 文件/事件封套是恒
// False 的白读——不白读，判活由台账运行态承接（长任务中段不被闲置钟误判，
// 闸门升 enforce 的前置）。
func (d *Daemon) machineWaiting(agent, sessionID, path string) bool {
	if d.Ledger.SubagentActive(agent, sessionID) {
		return true
	}
	if d.WindowWait(agent, sessionID) { // T48 第三道：异步停车窗（停表未过期）
		return true
	}
	if agent == "dsh" {
		// dsh 判活道（票 A）：族系运行态在效（含族系子键）；悬空道跳过（上文
		// 头注）。Gated 版＝DshRunGraceS 豁免宽限（2026-10-05 漏拦案：本输入
		// 自己的 status=running 不得自豁免凉会话，见 ledger.go 注释）。
		return d.Ledger.DshFamilyRunningGated(sessionID)
	}
	if path == "" {
		return false
	}
	return cctrans.HasDanglingToolUse(path)
}

// cacheHot 判热（「热缓存不拦」）：判热时钟=距该会话最后一次真实上游请求
// （喂钟口径 F3：心跳/问询/等待泳道重放、追加重放、主流量 usage 皆计）。
// 在 ferry.PredictHot 必活带（clock_s ≤ safety·TTL，公式单源）⇒ 缓存还热，
// 返回（true, 距缓存死线剩余秒）。无钟/无 TTL/无观测 → (false,0) 保守判冷
// ——绝不伪造热（同模型泳道 sameModelHot 同纪律）。
func (d *Daemon) cacheHot(sid string) (bool, float64) {
	if d.HeatClock == nil || d.Cfg.Heartbeat.TTLS <= 0 {
		return false, 0
	}
	last, ok := d.HeatClock.Last(sid)
	if !ok {
		return false, 0
	}
	clockS := clock.Now() - last
	if !ferry.PredictHot(clockS, ferry.TTLObs{TTLS: d.Cfg.Heartbeat.TTLS}) {
		return false, 0
	}
	return true, math.Max(0, d.Cfg.Heartbeat.TTLS-clockS)
}

// hotCtx 热缓存放行提示（拦窗内但判热必活带）：本条按折扣价、死线何时到。
// RuneTrunc 同 warnCtx（提示不过长，钩子注入面有上限）。
func (d *Daemon) hotCtx(idle, remainS float64) string {
	return mathx.RuneTrunc(fmt.Sprintf("[Ferryman] 本会话已闲置 %.0f 分钟，但缓存仍热"+
		"（近期保温/请求焐热），本条按折扣价，放行不拦。缓存约 %.0f 分钟后过期；"+
		"之后再长闲置会被正常拦（交接自动备好）。", idle/60, remainS/60), WarnContextCap)
}

// compactedShortCtx 已压缩短前缀的放行提示（票04 gate 联动，hotCtx 同款）：
// 说明为何不拦（已压缩归档、前缀已短）＋红利边界（标记失效后照旧拦）。
func (d *Daemon) compactedShortCtx(idle float64) string {
	return mathx.RuneTrunc(fmt.Sprintf("[Ferryman] 本会话已闲置 %.0f 分钟，但已被"+
		"压缩归档（前缀已短，重付便宜），本条放行不拦；压缩红利失效后再长闲置"+
		"会被正常拦（交接自动备好）。", idle/60), WarnContextCap)
}

// dshAutoContinue v0.9.3 票2（2026-10-07 用户拍板）：dsh 拦截改自动强续无卡
// 直续。用户工作流里手动强续是唯一真实选择，卡只添一步点击；压缩链接管
// 「省钱」职责后，拦截的「知情」职责改由横幅（additional_context 报冷重付
// 价）承担。语义镜像步1 手动强续：清 pending（06703fbd 跑步机案同款）、消耗
// 现行交接（D2：绝不端旧快照）、bypass 记账（reason 区分自动/手动）；gate.log
// 落 auto-continue 痕（与手动强续可区分可数）。cc/codex 永不走本道（调用点
// agent=="dsh" 钉死，行为零变化）。缺省关（config [gate].dsh_auto_continue）。
func (d *Daemon) dshAutoContinue(sessionID, transcriptPath string, st *ledger.SessionState, peak int, idle float64) map[string]any {
	d.Stats.addBypass()
	if st != nil {
		d.Pending.Clear([2]string{"dsh", st.SessionID})
	}
	d.Pending.Clear([2]string{"dsh", sessionID})
	if d.Store != nil { // D2 消耗语义同手动强续（票02）；台账 miss 回落原始 id 同口径
		sid := sessionID
		if st != nil {
			sid = st.SessionID
		}
		d.Store.ConsumeHandoffs("dsh", sid)
	}
	// 票03 闸门同步兜底（先补后记——时序前提，TestGateDshAutoContinuePeakBackfill
	// 钉死）：PeakCtx=0（重启贫血且 dsh_boot_replay 回放无料：接法乙前的历史
	// 流量无归因键，2026-10-07 生产实锚横幅"约 0 tokens"实付 17,693）→ 优先
	// 账本回放、无行按转录粗估，横幅与 bypass 行同源取补值；双无 → 横幅降级
	// "重付额度未知"。补值只喂本函数两处取值（只读账本/转录，不入共享态）。
	// peak 非零的常规强续零变化；cc/codex 永不走本道。
	peakKnown := peak > 0
	if !peakKnown {
		if p := dshGatePeakBackfill(d.Accounts, st, sessionID, transcriptPath, clock.Now()); p > 0 {
			peak, peakKnown = p, true
		}
	}
	d.Acct("bypass", st, "dsh", sessionID, transcriptPath,
		accounts.Fields{"prefix_tokens": peak}) // reason/idle_s 不落账（隐私不变量白名单）——自动/手动的区分归 gate.log mode=auto-continue
	d.gateWarn("dsh", sessionID, "auto-continue", idle)
	return map[string]any{"decision": "allow", "reason": "auto-strong-continue",
		"additional_context": d.autoContinueCtx(idle, peak, peakKnown)}
}

// autoContinueCtx 自动强续横幅（compactedShortCtx 同款形态）：只报事实
//（闲置时长＋冷重付量），不加动作指引——本道的设计前提就是用户不想要步骤。
// 票03：peak 无任何补值来源（known=false）时价格段降级为"重付额度未知"——
// 绝不显示"约 0 tokens"（生产实锚 2026-10-07 15:41:18 案）。known=true 的
// 文案逐字不变（既有用例钉死）。
func (d *Daemon) autoContinueCtx(idle float64, peak int, known bool) string {
	price := fmt.Sprintf("本条将全价冷重付约 %d tokens input", peak)
	if !known {
		price = "本条将全价冷重付，重付额度未知"
	}
	return mathx.RuneTrunc(fmt.Sprintf("[Ferryman] 本会话已闲置 %.0f 分钟（缓存已失效），"+
		"已自动强续放行、无弹窗直续：%s。"+
		"若想省这笔，下次可在此会话闲置后让压缩先行（自动），或换新会话开场。", idle/60, price), WarnContextCap)
}

// dshCompactPassLine 放行线（v0.9.3 票1 解耦，2026-10-07 首单事故实锚：
// 60562 压到 20562 撞 min_peak 20000 线照样拦——一个旋钮两个语义的耦合 bug）：
// max(pass_floor_tokens, pass_ratio×压前峰值)。地板盖压得干净的会话
//（glm-5.3 提示地板≈9.4K，压好落 9~12K）；比例腿盖肥会话——压掉一半以上＝
// 剩余冷重付已是系统可达下限，再拦只逼「更贵强续」或「丢活上下文」二选一。
// min_peak_tokens 只管「值得压」（触发面条件②），与放行无关。PrePeak=0
//（不可得：未 harvest/历史标记）→ 比例腿失效只剩地板。
func (d *Daemon) dshCompactPassLine(prePeak int) int {
	line := d.Cfg.DshCompact.PassFloorTokens
	if r := int(d.Cfg.DshCompact.PassRatio * float64(prePeak)); r > line {
		line = r
	}
	return line
}

// warnCtx _warn_ctx（server.py:279-289；cc 逐字）。
// observe 永不拦——"将被拦"只在 enforce 成立（2026-09-18 文案缺陷修复：
// 两模式共用一句空头支票，用户按文案预期被拦却没拦）。
// 尾句按 agent 分支（票01/D3）：dsh 无 /clear，换「新建会话」引导；cc 逐字零变化。
func (d *Daemon) warnCtx(agent string, idle float64, h *store.Entry, willBlock bool) string {
	tail := "交接生成中，下次提交将被拦。"
	if !willBlock {
		tail = "交接生成中（observe 模式只提醒不拦；enforce 才会真拦）。"
	}
	doc := tail
	if h != nil {
		doc = "交接文档: " + h.Path
	}
	tip := "建议 /clear 后开新会话（自动注入交接）。"
	if agent == "dsh" {
		tip = "建议新建会话（桌面端点新建 / web 端 new session），开场自动注入交接。"
	}
	txt := fmt.Sprintf("[Ferryman] 本会话已闲置 %.0f 分钟，缓存大概率已失效，"+
		"继续使用将全量重付 input。", idle/60) +
		doc + tip
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

// runeCap16 按码点截 16 位（v0.9.3 票4）：dsh 会话键 "session-<uuid>" 的前 8
// 位全被 "session-" 吃掉，日志侧一律用本款（shortSid 16 位同动机）。
func runeCap16(s string) string {
	if rs := []rune(s); len(rs) > 16 {
		return string(rs[:16])
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
