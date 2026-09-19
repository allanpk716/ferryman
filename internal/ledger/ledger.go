// Package ledger 台账：session_id → 最后活动/大小/标题/项目/Agent，闲置判定
// 唯一事实源（规格 ferryman/ledger.py 1:1）。
//
// DESIGN §4：
//   - 主键 agent + session_id；辅助键 transcript_path；
//   - 会话族系（lineage）：同一 transcript_path 出现新 session_id → 继承闲置史
//     与交接关联（首条 user 消息 hash 指纹经 E0a 校准偏弱——模板开场白碰撞多
//     ——仅作辅助信号，不进主判定）；
//   - 时钟统一 UTC epoch 秒；闲置 = now − 台账.last_write（mtime 口径）；
//   - lookback=0：启动只登记不触发任何摆渡。
//
// 并发模型（spec §Implementation「并发模型」）：公共方法自带锁；跨包临界区经
// Mu() 共享同一把锁（双锁同序：windowsMu 外层 → ledgerMu 内层）。Go 的
// sync.Mutex 不可重入（Python 侧为 RLock）——持锁期间禁调本包自带锁的公共
// 方法，临界区内读改写用 *Locked 无锁内方法。
// SessionState 为共享可变引用：**读写均须持锁**（Python 靠 GIL 无锁读，Go 读
// 不持锁会 -race）。
package ledger

import (
	"sync"

	"ferryman/internal/clock"
	"ferryman/internal/pathsx"
)

// SubagentEventLeakS 子代理计数泄漏防护：1h 无新事件视为已结束（Stop 丢失场景）。
const SubagentEventLeakS = 3600.0

// QSnap qwatch 开窗瞬间的 (last_write, size)，供两道验新鲜度比对。
type QSnap struct {
	MTime float64
	Size  int
}

// SessionState 一条会话的台账状态（Python SessionState 1:1）。
//
// 共享可变引用：**读写均须持锁**（经 Ledger.Mu 或本包公共方法）。
type SessionState struct {
	Agent          string // "cc" | "codex"
	SessionID      string
	TranscriptPath string
	Cwd            string
	Title          string  // Python str|None 的 Go 形："" 即 None
	LastWrite      float64 // UTC epoch 秒（文件 mtime 口径）
	Size           int
	PeakCtx        int
	ObservedActive bool    // daemon 启动后是否见过其活动（lookback=0 的摆渡闸）
	HandedOffAt    float64 // 最近一次成功摆渡时间（防重复入队）
	EnrichedWrite  float64 // 已富化(标题/峰值)到哪个 last_write 版本；初值 -1
	// T51 等答复窗口（问询守望）：QWatchOpenedTS nil=无窗。开窗在 daemon 问询
	// 守望（命中谓词四条件），关窗只在 Touch 新写入分支（任何新写入=用户已
	// 作答）；plan 开窗即排定（票03调度器：max_beats 跳 × beat_interval_s，
	// 开窗瞬间不跳），snapshot=(last_write,size) 供两道验新鲜度比对。
	QWatchOpenedTS   *float64
	QWatchBeatsFired int
	QWatchPlan       []float64
	QWatchSnapshot   *QSnap
}

// subEnt T32 子代理计数值：(运行数, 最后事件时刻)。仅内存——daemon 重启丢
// 计数由 T31 悬空检测兜底；Stop 丢失由泄漏防护兜底。
type subEnt struct {
	count int
	last  float64
}

// Ledger 台账。零值不可直接用，经 New 构造。
type Ledger struct {
	mu sync.Mutex // Python threading.RLock 的 Go 形：不可重入，临界区走 *Locked 内方法

	byKey  map[[2]string]*SessionState // (agent, session_id) → 状态
	byPath map[string]*SessionState    // norm(path) → 状态（lineage 辅助键）

	lastWrite float64 // last_transcript_write：健康监控用（DESIGN §4）

	subagents map[[2]string]subEnt // T32：(agent, session_id) → (运行数, 最后事件时刻)
}

// New 构造空台账。
func New() *Ledger {
	return &Ledger{
		byKey:     map[[2]string]*SessionState{},
		byPath:    map[string]*SessionState{},
		subagents: map[[2]string]subEnt{},
	}
}

// Mu 暴露台账锁（跨包临界区用：daemon 双锁同序 windowsMu 外层 → ledgerMu 内层）。
// 持锁期间禁调本包自带锁的公共方法（不可重入，死锁）；临界区内需要子代理判定
// 用 SubagentActiveLocked。
func (l *Ledger) Mu() *sync.Mutex { return &l.mu }

// Touch 登记/刷新一条会话（TouchFull 的缺参形态：不更新 cwd/title/peak_ctx）。
// 返回（可能经 lineage 继承的）状态——共享可变引用，读写均须持锁。
func (l *Ledger) Touch(agent, sid, path string, mtime float64, size int,
	daemonStartedAt float64) *SessionState {
	return l.TouchFull(agent, sid, path, mtime, size, "", "", 0, daemonStartedAt)
}

// TouchFull 登记/刷新一条会话（Python touch 全参 1:1）。覆盖分支逐字：
// size=0 不覆盖（Python size or st.size）；cwd/title/peak_ctx 零值不覆盖
// （Python if cwd:/if title:/if peak_ctx:）；mtime 严格大于 last_write 才推进
// ——推进时 mtime ≥ daemonStartedAt 置 observed_active，且任何 qwatch 开窗
// 立即关窗（新写入=用户已作答）。全局 last_transcript_write 取 max（不回退）。
func (l *Ledger) TouchFull(agent, sid, path string, mtime float64, size int,
	cwd, title string, peakCtx int, daemonStartedAt float64) *SessionState {
	key := [2]string{agent, sid}
	norm := pathsx.NormPath(path)
	l.mu.Lock()
	defer l.mu.Unlock()
	if mtime > l.lastWrite {
		l.lastWrite = mtime
	}
	st, ok := l.byKey[key]
	if !ok {
		st = &SessionState{Agent: agent, SessionID: sid, TranscriptPath: path,
			EnrichedWrite: -1}
		// lineage：同 transcript_path 换了 session_id → 继承闲置史
		if prev, pok := l.byPath[norm]; pok && prev.Agent == agent {
			st.LastWrite = prev.LastWrite
			st.Cwd = prev.Cwd
			st.Title = prev.Title
			st.PeakCtx = prev.PeakCtx
			st.HandedOffAt = prev.HandedOffAt
		}
		l.byKey[key] = st
		l.byPath[norm] = st
	} else {
		l.byPath[norm] = st
	}
	if size != 0 {
		st.Size = size
	}
	if cwd != "" {
		st.Cwd = cwd
	}
	if title != "" {
		st.Title = title
	}
	if peakCtx != 0 {
		st.PeakCtx = peakCtx
	}
	if mtime > st.LastWrite {
		st.LastWrite = mtime
		if mtime >= daemonStartedAt {
			st.ObservedActive = true
		}
		if st.QWatchOpenedTS != nil { // T51：任何新写入关窗
			st.QWatchOpenedTS = nil
			st.QWatchBeatsFired = 0
			st.QWatchPlan = nil
			st.QWatchSnapshot = nil
		}
	}
	return st
}

// Get 主键 (agent, session_id) 查状态；无则 nil。
// 共享可变引用——读写均须持锁。
func (l *Ledger) Get(agent, sid string) *SessionState {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.GetLocked(agent, sid)
}

// GetLocked Get 的无锁内方法——仅供已持 l.Mu() 的临界区（daemon 双锁同序的
// 开窗复验/闭账快照等，票13 评审 Minor A）调用；不经临界区的调用方一律走
// 自带锁的 Get。返回共享可变引用：字段读写须在本临界区内完成（快照读法）。
func (l *Ledger) GetLocked(agent, sid string) *SessionState {
	return l.byKey[[2]string{agent, sid}]
}

// GetByPath 辅助键（归一化路径）查状态；无则 nil。
// 共享可变引用——读写均须持锁。
func (l *Ledger) GetByPath(path string) *SessionState {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.byPath[pathsx.NormPath(path)]
}

// AllSessions 全部状态（列表新建，元素仍为共享引用——读写均须持锁）。
func (l *Ledger) AllSessions() []*SessionState {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.AllSessionsLocked()
}

// AllSessionsLocked AllSessions 的无锁内方法——仅供已持 l.Mu() 的临界区
// （daemon QWatchStop 的台账锁内清窗等）调用；不经临界区的调用方一律走
// 自带锁的 AllSessions。锁内只有内存操作（spec「并发模型」纪律）。
func (l *Ledger) AllSessionsLocked() []*SessionState {
	out := make([]*SessionState, 0, len(l.byKey))
	for _, st := range l.byKey {
		out = append(out, st)
	}
	return out
}

// LastTranscriptWrite 全局最近转录写入时刻（健康监控用，DESIGN §4）。
func (l *Ledger) LastTranscriptWrite() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastWrite
}

// SubagentEvent SubagentStart/Stop 事件计数（嵌套各计一次，探针实测同属主
// 会话）。返回当前运行数；stop 下限钳 0，归零即删条目（Python count==0 → pop）。
func (l *Ledger) SubagentEvent(agent, sid, event string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := [2]string{agent, sid}
	count := 0
	if ent, ok := l.subagents[key]; ok {
		count = ent.count
	}
	if event == "start" {
		count++
	} else {
		count = max(0, count-1) // 非 start 一律按 stop 钳 0（Python else 分支）
	}
	if count == 0 {
		delete(l.subagents, key)
	} else {
		l.subagents[key] = subEnt{count: count, last: clock.Now()}
	}
	return count
}

// SubagentActive 该会话是否有子代理运行中（含泄漏防护：事件超 1h 未更新 →
// 视为 0 并清理）。
func (l *Ledger) SubagentActive(agent, sid string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.SubagentActiveLocked(agent, sid)
}

// SubagentActiveLocked SubagentActive 的无锁内方法——仅供已持 l.Mu() 的临界区
// （daemon 双锁 check-then-act）调用；不经临界区的调用方一律走自带锁的
// SubagentActive。
func (l *Ledger) SubagentActiveLocked(agent, sid string) bool {
	key := [2]string{agent, sid}
	ent, ok := l.subagents[key]
	if !ok {
		return false
	}
	if clock.Now()-ent.last > SubagentEventLeakS {
		delete(l.subagents, key) // 判定即清理
		return false
	}
	return ent.count > 0
}

// SubagentsActiveCount 全部会话运行中子代理总数（同泄漏口径：过期条目不计）。
func (l *Ledger) SubagentsActiveCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := clock.Now() - SubagentEventLeakS
	n := 0
	for _, ent := range l.subagents {
		if ent.last > cutoff {
			n += ent.count
		}
	}
	return n
}

// MarkHandedOff 记最近一次成功摆渡时刻（防重复入队）。st 为共享可变引用，
// 本方法自带锁完成写入。
func (l *Ledger) MarkHandedOff(st *SessionState) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st.HandedOffAt = clock.Now()
}
