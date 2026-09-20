package daemon

// query_gate_check.go — 票02：GET /gate_check 双模式实现（注册于 queryapi.go
// 的 queryEndpoints 分派表；规格 docs/superpowers/specs/20260920-agent-surface-
// mcp-readonly-spec.md「daemon 只读端点」节 /gate_check 条目，F7 排序键裁定）。
//
// 单会话（?session_id=）：真闸门（gate.go）此刻判定的只读推演镜像——台账闲置
// 时长×总结阈值×拦截阈值×有效交接×待交接标记，三分支判定 allow/warn/block；
// 绝不调用任何写路径（NoteGatePrompt/Pending.Clear/BumpBlocks/EnqueueFerry/
// SavePendingPrompt/MarkBlocked/Stats 一律禁入）。session_id 不存在 → 404。
// 汇总（无 session_id）：逐会话一行（session_id、判定、剩余分钟），limit 同构
// 默认 50；排序键（spec F7 裁定）＝预计拦截时刻（last_write+block_s）升序——
// 等价闲置时长降序；同刻并列时无有效交接者排在有交接者之前，再按 session_id
// 字典序（跨 Agent 同 id 理论碰撞再按 agent，钉全序——台账 map 迭代序随机）。
//
// 推演输入边界（票面裁定）：只依据台账、交接库索引与内存闸门态，不解析 jsonl
// 正文。与真闸门的两处如实声明的保守缺口（预演只会更严、绝不更松）：
//  1. 机器等机器豁免只覆盖停车窗道（T48；waitWindowOpenLocked 零副作用探测）
//     ——计数道无零写访问器（SubagentActive 判定即清理泄漏条目）、悬空道须
//     读转录尾部，均不推演；
//  2. pending 的 TTL 懒删除与新闲置周期清除只做条件推演、不落写——判定与
//     真闸门一致，副作用（delete/Clear）为零。
//
// 红线（queryapi.go 顶部块全文适用）：无消息内容、无凭据字段、纯只读；
// 并发纪律同 query_sessions.go——台账锁内一次抄齐快照，ValidHandoff/pending/
// 窗口探测均在台账锁外进行，绝不嵌套两把锁。

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"

	"ferryman/internal/clock"
	"ferryman/internal/ledger"
	"ferryman/internal/mathx"
	"ferryman/internal/store"
)

// gateCheckResult probeGate 的判定产出（单会话/汇总两模式共用）。
type gateCheckResult struct {
	verdict string // allow | warn | block
	reason  string // 判定依据语汇（既有闸门分支：mode-off/machine-waiting/not-in-window/observe/valid-handoff/pending/no-valid-handoff）
	mode    string // 该 Agent 的闸门三态活值（off/observe/enforce）
	idle    float64
	stale   bool
	handoff *store.Entry // 有效交接（ValidHandoff 纯读；nil=无）
}

// parkedWindow 停车窗在停的零副作用探测（gate.go machineWaiting 第三道
// WindowWait 的只读镜像：waitWindowOpenLocked 判开＋StopTS 判停车——过期
// 停车视同已闭不闭账，懒过期收口留给正规路径）。须在 windowsMu 外调用。
func (d *Daemon) parkedWindow(agent, sessionID string) bool {
	d.windowsMu.Lock()
	defer d.windowsMu.Unlock()
	_, open := d.waitWindowOpenLocked(agent, sessionID)
	if !open {
		return false
	}
	w := d.windows[winKey{agent, sessionID}]
	return w != nil && w.StopTS != nil
}

// probeGate 真闸门七分支的只读推演（gate.go Gate 的镜像，去全部写副作用；
// 分支顺序与真闸门一致：mode off → 机器等机器（只停车窗道）→ observe →
// enforce 的 5/6/7）。bypass/台账 miss 两分支对查询面不存在（查询按
// session_id 定位台账条目，miss 即 404）。
func (d *Daemon) probeGate(agent, sid, cwd string, lastWrite float64) gateCheckResult {
	now := clock.Now()
	idle := now - lastWrite
	th := d.Cfg.ThresholdFor(agent)
	mode := d.Cfg.GateCC
	if agent != "cc" {
		mode = d.Cfg.GateCodex
	}
	var handoff *store.Entry
	if d.Store != nil { // 汇总排序键需要全量行的交接在场性——无条件先算（纯读）
		handoff = d.Store.ValidHandoff(agent, cwd, lastWrite)
	}
	res := gateCheckResult{mode: mode, idle: idle, stale: idle >= th.BlockS,
		handoff: handoff}
	set := func(verdict, reason string) {
		res.verdict, res.reason = verdict, reason
	}
	switch {
	case mode == "off": // 分支 3
		set("allow", "mode-off")
	case d.parkedWindow(agent, sid): // 分支 2.5（T48 停车窗道）
		set("allow", "machine-waiting")
	default:
		// pending 只读镜像：PendingTable.Get 带 TTL 懒删除、Gate 主路径再
		// Clear——均为写，禁入；原样取值后按同口径条件推演。
		d.Pending.mu.Lock()
		rec, pok := d.Pending.t[[2]string{agent, sid}]
		d.Pending.mu.Unlock()
		if pok && now-rec.SetAt > PendingTTLs { // 清除条件之三：24h TTL
			pok = false
		}
		// 清除条件之一：新闲置周期（用户回来过又离开了 summarize 时长）。
		if pok && lastWrite > rec.SetAt && idle >= th.SummarizeS {
			pok = false
		}
		inWindow := res.stale || pok
		switch {
		case mode == "observe": // observe：只警告不拦（有交接也不判 block）
			if !inWindow {
				set("allow", "not-in-window")
			} else {
				set("warn", "observe")
			}
		case !inWindow:
			set("allow", "not-in-window")
		case handoff != nil: // 分支 5：有效交接 → block（路径即判定依据）
			set("block", "valid-handoff")
		case pok: // 分支 6：待交接标记在位（首次警告已过）→ block
			set("block", "pending")
		default: // 分支 7：首次进窗、无交接 → 警告区
			set("warn", "no-valid-handoff")
		}
	}
	return res
}

// gateBlockInMin 离拦截阈值剩余分钟（已越线钳 0；1 位小数与 idle_s 同精度）。
func gateBlockInMin(blockS, idle float64) float64 {
	rem := (blockS - idle) / 60
	if rem < 0 {
		rem = 0
	}
	return mathx.Round(rem, 1)
}

// ---- GET /gate_check ----

// handleGateCheck 双模式入口：带 session_id＝单会话判定；无＝汇总。
func handleGateCheck(d *Daemon, w http.ResponseWriter, r *http.Request) {
	sid := qsOr(r.URL.Query(), "session_id", "")
	if sid == "" {
		d.handleGateCheckSummary(w, r)
		return
	}
	// 台账锁内一次抄齐（跨 Agent 同 id 理论碰撞取 agent 字典序首个——票01 同口径）。
	d.Ledger.Mu().Lock()
	var found *ledger.SessionState
	for _, st := range d.Ledger.AllSessionsLocked() {
		if st.SessionID == sid && (found == nil || st.Agent < found.Agent) {
			found = st
		}
	}
	var agent, cwd string
	var lastWrite float64
	if found != nil {
		agent, cwd, lastWrite = found.Agent, found.Cwd, found.LastWrite
	}
	d.Ledger.Mu().Unlock()
	if found == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "session not found"})
		return
	}
	th := d.Cfg.ThresholdFor(agent)
	res := d.probeGate(agent, sid, cwd, lastWrite)
	basis := map[string]any{"reason": res.reason}
	if res.handoff != nil { // 判定依据：有效交接要素（路径可以；正文永不）
		basis["handoff_id"] = res.handoff.HandoffID
		basis["handoff_status"] = res.handoff.Status
		basis["handoff_path"] = res.handoff.Path
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":   sid,
		"agent":        agent,
		"mode":         res.mode,
		"verdict":      res.verdict,
		"stale":        res.stale,
		"idle_s":       mathx.Round(res.idle, 1),
		"block_in_min": gateBlockInMin(th.BlockS, res.idle),
		"basis":        basis,
	})
}

// handleGateCheckSummary 汇总模式：全台账逐会话一行，排序键见文件头（F7），
// limit 同构默认 50（先排序后裁尾）。
func (d *Daemon) handleGateCheckSummary(w http.ResponseWriter, r *http.Request) {
	limit := sessionsDefaultLimit
	if raw := qsOr(r.URL.Query(), "limit", ""); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			badRequest(w, fmt.Errorf("limit 必须为正整数，得到: %q", raw))
			return
		}
		limit = n
	}
	type row struct {
		sid, agent, verdict string
		blockAt, blockInMin float64
		hasHandoff          bool
	}
	// 台账锁内只抄字段快照；判定推演（ValidHandoff/pending/窗口）全在锁外。
	d.Ledger.Mu().Lock()
	type snapEnt struct {
		agent, sid, cwd string
		lastWrite       float64
	}
	snaps := make([]snapEnt, 0, 16)
	for _, st := range d.Ledger.AllSessionsLocked() {
		snaps = append(snaps, snapEnt{agent: st.Agent, sid: st.SessionID,
			cwd: st.Cwd, lastWrite: st.LastWrite})
	}
	d.Ledger.Mu().Unlock()

	rows := make([]row, 0, len(snaps))
	for _, s := range snaps {
		th := d.Cfg.ThresholdFor(s.agent)
		res := d.probeGate(s.agent, s.sid, s.cwd, s.lastWrite)
		rows = append(rows, row{sid: s.sid, agent: s.agent, verdict: res.verdict,
			blockAt:    s.lastWrite + th.BlockS,
			blockInMin: gateBlockInMin(th.BlockS, res.idle),
			hasHandoff: res.handoff != nil})
	}
	// 全序（map 迭代序随机，须钉稳定）：临近度升序 → 无交接者在前 → sid → agent。
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].blockAt != rows[j].blockAt {
			return rows[i].blockAt < rows[j].blockAt
		}
		if rows[i].hasHandoff != rows[j].hasHandoff {
			return !rows[i].hasHandoff
		}
		if rows[i].sid != rows[j].sid {
			return rows[i].sid < rows[j].sid
		}
		return rows[i].agent < rows[j].agent
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	out := make([]map[string]any, 0, len(rows)) // 空台账 → [] 而非 null
	for _, x := range rows {
		out = append(out, map[string]any{
			"session_id":   x.sid,
			"verdict":      x.verdict,
			"block_in_min": x.blockInMin,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"gate_checks": out})
}
