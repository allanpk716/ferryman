package daemon

// query_sessions.go — 票01：GET /sessions 与 GET /session 实现（注册于
// queryapi.go 的 queryEndpoints 分派表）。
//
// 红线（queryapi.go 顶部块全文适用）：无消息内容、无凭据字段、纯只读。
// 并发纪律：SessionState 为共享可变引用——台账锁内一次抄齐快照（gate.go
// sessionSnap 同范式）；窗口表经 windowsMu 只读探测（waitWindowOpenLocked
// 零副作用，懒过期闭窗的正规路径不动）；账本 Read 与交接库查询锁外进行，
// 绝不嵌套两把锁。

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"ferryman/internal/accounts"
	"ferryman/internal/clock"
	"ferryman/internal/ledger"
	"ferryman/internal/mathx"
	"ferryman/internal/pathsx"
)

// sessionsDefaultLimit /sessions 的 limit 缺省值（D13：工具响应默认分页/
// 最近 N，N≈50）。
const sessionsDefaultLimit = 50

// ---- GET /sessions ----

// handleSessions 台账内存态全量清单：query agent（精确过滤）、cwd（项目路径
// 前缀过滤）、limit（默认 50）；按最后写入倒序（并列按 agent、session_id
// 字典序稳定），行七要素＝session_id/agent/cwd/idle_s/stale/peak_ctx/
// lineage_id。闲置判定（凉/未凉）按该 Agent 的拦截阈值（idle ≥ block_s 即凉
// ——与闸门 inWindow 同口径）；族系＝归一化转录路径（lineage 唯一键形）。
func handleSessions(d *Daemon, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	agentF := qsOr(q, "agent", "")
	cwdF := qsOr(q, "cwd", "")
	limit := sessionsDefaultLimit
	if raw := qsOr(q, "limit", ""); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			badRequest(w, fmt.Errorf("limit 必须为正整数，得到: %q", raw))
			return
		}
		limit = n
	}
	now := clock.Now()
	// 台账锁内一次抄齐（过滤也在锁内——只碰内存，铁律合规）。
	type row struct {
		sid, agent, cwd, lineage string
		lastWrite                float64
		peak                     int
	}
	var rows []row
	d.Ledger.Mu().Lock()
	for _, st := range d.Ledger.AllSessionsLocked() {
		if agentF != "" && st.Agent != agentF {
			continue
		}
		if cwdF != "" && !strings.HasPrefix(st.Cwd, cwdF) {
			continue
		}
		rows = append(rows, row{sid: st.SessionID, agent: st.Agent, cwd: st.Cwd,
			lineage:   pathsx.NormPath(st.TranscriptPath),
			lastWrite: st.LastWrite, peak: st.PeakCtx})
	}
	d.Ledger.Mu().Unlock()
	sort.Slice(rows, func(i, j int) bool { // 全序（map 迭代序随机，须钉稳定）
		if rows[i].lastWrite != rows[j].lastWrite {
			return rows[i].lastWrite > rows[j].lastWrite
		}
		if rows[i].agent != rows[j].agent {
			return rows[i].agent < rows[j].agent
		}
		return rows[i].sid < rows[j].sid
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	out := make([]map[string]any, 0, len(rows)) // 空台账 → [] 而非 null
	for _, x := range rows {
		idle := now - x.lastWrite
		out = append(out, map[string]any{
			"session_id": x.sid,
			"agent":      x.agent,
			"cwd":        x.cwd,
			"idle_s":     mathx.Round(idle, 1),
			"stale":      idle >= d.Cfg.ThresholdFor(x.agent).BlockS,
			"peak_ctx":   x.peak,
			"lineage_id": x.lineage,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// ---- GET /session?id=<session_id> ----

// handleSessionDetail 单会话聚合四段：台账条目＋四列会话总账＋有效交接覆盖
// ＋窗口状态（等待窗/问询窗，含停车标志）。id 不存在 → 404 JSON；缺 id →
// 400。
func handleSessionDetail(d *Daemon, w http.ResponseWriter, r *http.Request) {
	id := qsOr(r.URL.Query(), "id", "")
	if id == "" {
		badRequest(w, errors.New("缺少 query 参数 id"))
		return
	}
	// 台账条目快照（锁内抄齐；跨 Agent 同 id 理论碰撞取 agent 字典序首个）。
	d.Ledger.Mu().Lock()
	var found *ledger.SessionState
	for _, st := range d.Ledger.AllSessionsLocked() {
		if st.SessionID == id && (found == nil || st.Agent < found.Agent) {
			found = st
		}
	}
	if found == nil {
		d.Ledger.Mu().Unlock()
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "session not found"})
		return
	}
	agent, path, cwd, title := found.Agent, found.TranscriptPath, found.Cwd, found.Title
	lastWrite, handedOff := found.LastWrite, found.HandedOffAt
	size, peak, observed := found.Size, found.PeakCtx, found.ObservedActive
	contentTS := found.ContentTS // 内容时钟（ADR-0013）——有效交接判定基准
	// 问询窗（等答复窗口）挂在台账态上——快照一并抄出。
	var qwOpen bool
	var qwOpenedTS float64
	if found.QWatchOpenedTS != nil {
		qwOpen = true
		qwOpenedTS = *found.QWatchOpenedTS
	}
	qwFired, qwPlanned := found.QWatchBeatsFired, len(found.QWatchPlan)
	lineage := pathsx.NormPath(path)
	d.Ledger.Mu().Unlock()

	// 等待窗状态：windowsMu 下只读探测。waitWindowOpenLocked 零副作用（活跃窗
	// 按计数道泄漏口径、停车窗按 PARK_EXPIRE_S 判，懒过期闭窗留给正规路径）
	// ——本面绝不触发闭窗记账（只读红线）。
	d.windowsMu.Lock()
	openedTS, waitOpen := d.waitWindowOpenLocked(agent, id)
	wrec := d.windows[winKey{agent, id}]
	parked := wrec != nil && wrec.StopTS != nil
	d.windowsMu.Unlock()
	var waitOpened any
	if wrec != nil {
		waitOpened = openedTS
	}

	// 有效交接覆盖：复用闸门 block 的唯一依据（同 agent+cwd、fresh|skeleton、
	// covers_until ≥ 覆盖基准含 60s 容差、24h 新鲜窗）。基准＝内容时钟
	// （ADR-0013，0=未算回落 last_write）。ValidHandoff 纯读（无落盘）；
	// 无有效交接 → null。
	bar := contentTS
	if bar <= 0 {
		bar = lastWrite
	}
	var handoff any
	if d.Store != nil {
		if h := d.Store.ValidHandoff(agent, cwd, bar); h != nil {
			handoff = map[string]any{
				"handoff_id":     h.HandoffID,
				"status":         h.Status, // fresh | skeleton
				"covers_until":   h.CoversUntil,
				"covers_until_s": h.CoversUntilS,
				"created_at":     h.CreatedAt,
				"title":          h.Title,
			}
		}
	}

	// 四列会话总账（CONTEXT「会话总账」：主转录＋各子代理转录四列加总）。
	// 账本 usage 科目为既有口径（internal/accounts）：按族系（lineage_id＝
	// 归一化主转录路径）加总 input/output/缓存写/缓存读四列——主会话与（今
	// 后按同谱系入账的）子代理流水一并覆盖。Accounts 未接线 → 全零。
	var in, cr, cc, outN, reqs int
	if d.Accounts != nil {
		for _, e := range d.Accounts.Read(accounts.ReadOpts{Kind: "usage", Lineage: lineage}) {
			in += int(acctNum(e, "input_tokens"))
			cr += int(acctNum(e, "cache_read_tokens"))
			cc += int(acctNum(e, "cache_creation_tokens"))
			outN += int(acctNum(e, "output_tokens"))
			reqs++
		}
	}

	var qwOpened any
	if qwOpen {
		qwOpened = qwOpenedTS
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ledger": map[string]any{
			"session_id":      id,
			"agent":           agent,
			"transcript_path": path,
			"cwd":             cwd,
			"title":           title,
			"last_write":      mathx.Round(lastWrite, 3),
			"idle_s":          mathx.Round(clock.Now()-lastWrite, 1),
			"size":            size,
			"peak_ctx":        peak,
			"observed_active": observed,
			"handed_off_at":   handedOff,
			"content_ts":      mathx.Round(contentTS, 3), // 内容时钟（ADR-0013；0=未算）
			"lineage_id":      lineage,
		},
		"usage_total": map[string]any{
			"input_tokens":          in,
			"cache_read_tokens":     cr,
			"cache_creation_tokens": cc,
			"output_tokens":         outN,
			"requests":              reqs,
		},
		"handoff": handoff, // nil → null
		"windows": map[string]any{
			"wait": map[string]any{
				"open":      waitOpen,
				"parked":    parked,
				"opened_ts": waitOpened,
			},
			"qwatch": map[string]any{
				"open":              qwOpen,
				"opened_ts":         qwOpened,
				"beats_fired":       qwFired,
				"planned_remaining": qwPlanned,
			},
		},
	})
}
