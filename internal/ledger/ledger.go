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

// DshRunStaleS dsh 运行态失效上界（票 A 判活地基，dsh-heartbeat 规格「判活」
// 节）：置位/刷新距今超过它即判失效——有界失效回正常闸门路径（与 CC 悬空道/
// SubagentEventLeakS 同哲学）。同值 3600 但独立命名：语义不同源，将来各自可调。
const DshRunStaleS = 3600.0

// DshRunGraceS dsh 豁免宽限下限（2026-10-05 漏拦案）：运行态置位/刷新距今
// **不足此值不豁免**。用户回流时 runtime 先发 turn/start / status=running、
// 插件才问闸——刚置的运行态是本输入自己的信号而非「机器在跑」，照旧豁免＝
// 凉会话永拦不住（当晚 20:55/22:18 两枪实测：闲置 2h39m/67min＋fresh 交接
// 在位仍静默放行，全量重付 68k）。
const DshRunGraceS = 15.0

// DshRunGateCapS dsh 豁免效期上界（闸门 Gated 判定专用，2026-10-05 漏拦案
// 第二洞）：status=running 是「agent 有活动」不是「机器在产出」——用户点开
// 会话/上轮开始都会置位，残留可活到 DshRunStaleS（1h），期间（35min~1h 闲置
// 段）任何回流都会吃豁免漏拦。闸门豁免的真正保护域＝「凉会话且近处有机器
// 活动」＝长生成中途/多步循环；单步生成超此窗无产出的事件断流极罕见，且
// 闲置<拦截线时豁免与否同为 allow（not-in-window），收紧无副作用。
// DshRunStaleS（1h）保留为运行态本体上界（qwatch 开窗等原语义用途不变）。
const DshRunGateCapS = 600.0

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
	LastWrite      float64 // UTC epoch 秒（文件时钟：mtime 口径）
	Size           int
	PeakCtx        int
	ObservedActive bool    // daemon 启动后是否见过其活动（lookback=0 的摆渡闸）；2026-09-30 起另含"重启观察窗"：watcher 对 mtime 近 24h（=交接新鲜窗）的存量会话补置 true——daemon 死过不改变"它近期活跃"的事实
	HandedOffAt    float64 // 最近一次成功摆渡时间（防重复入队）
	// 内容时钟三字段（ADR-0013，防 CC 状态块幻影写入反复重摆渡）：
	//   ContentTS        转录内最后带时间戳记录的 epoch（懒尾解析，0=未算）；
	//   ContentStamp     ContentTS 计算所依据的 last_write 版本（mtime 变才重算）；
	//   HandledContentTS 最近一次处置（摆渡 covers / 小会话标记）所覆盖的内容
	//                    时钟——入队重查的判定基准：内容未越过它即不再摆渡。
	// 均仅内存（同 HandedOffAt）：daemon 重启归零=重启后多摆渡一轮（无害，既有
	// 语义不变）。
	ContentTS        float64
	ContentStamp     float64
	HandledContentTS float64
	EnrichedWrite    float64 // 已富化(标题/峰值)到哪个 last_write 版本；初值 -1
	// T51 等答复窗口（问询守望）：QWatchOpenedTS nil=无窗。开窗在 daemon 问询
	// 守望（命中谓词四条件），关窗只在 Touch 新写入分支（任何新写入=用户已
	// 作答）；plan 开窗即排定（票03调度器：max_beats 跳 × beat_interval_s，
	// 开窗瞬间不跳），snapshot=(last_write,size) 供两道验新鲜度比对。
	QWatchOpenedTS   *float64
	QWatchBeatsFired int
	QWatchPlan       []float64
	QWatchSnapshot   *QSnap
	// dsh 判活地基（票 A，dsh-heartbeat 规格「判活」节）：主会话（parent 空）
	// 运行/终结态，时间戳形（nil=无态），仅内存——daemon 重启归零＝插件下一
	// 事件重新置位（同 HandedOffAt 纪律，无害）。子会话不 Touch、无 SessionState
	//（dsh_receive.go 随父入账分流），其态在 Ledger.dshChildRuns（键=子键）。
	DshRunningTS  *float64
	DshDisposedTS *float64
	// dsh 等答复窗镜像（dsh 观测面票）：与 CC 的 QWatchOpenedTS 分立——Touch
	// 的"任何新写入清窗"是 CC 语义（用户写入关窗），dsh 机器侧写入不清窗
	//（关窗只认用户侧翻转或 block_s 到期，watcher_dsh.go closeDshWindow）；
	// 本字段族只作只读面（/beats、/session）的抄表源：守望单线程写、HTTP
	// 线程台账锁内读。BeatsFired 为当前（或刚关）窗的计数——开窗清零、逐跳
	// 递增、关窗保留终值；Planned 为剩余计划跳数（开窗排满、逐跳递减、
	// 关窗/熔断暂停清零）。
	DshQWatchOpenedTS   *float64
	DshQWatchBeatsFired int
	DshQWatchPlanned    int
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

	// dsh 判活地基（票 A）：族系子键表 parent→children＋子会话运行/终结态小表
	//（子会话不 Touch、无 SessionState 可挂——随父入账分流的镜像，键=子键）。
	// 仅内存，子键来源见 DshChildSeen 头注。
	dshChildren       map[string]map[string]bool
	dshChildRuns      map[string]*dshRunEnt
	dshChildrenSeeded bool // 族系子键账本回种防重闸（DshChildrenSeedClaim test-and-set）
}

// New 构造空台账。
func New() *Ledger {
	return &Ledger{
		byKey:        map[[2]string]*SessionState{},
		byPath:       map[string]*SessionState{},
		subagents:    map[[2]string]subEnt{},
		dshChildren:  map[string]map[string]bool{},
		dshChildRuns: map[string]*dshRunEnt{},
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
			st.HandledContentTS = prev.HandledContentTS // 内容时钟：处置边界随族系继承（ContentTS 缓存不继承，重算一次）
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

// ---- dsh 判活地基（票 A，dsh-heartbeat 规格「判活」节）----
//
// 运行/终结态维护与族系判定。事件面（daemon DshEvent，HTTP goroutine）写、
// 闸门（machineWaiting dsh 道）读——公共方法全部自带锁、锁内只内存操作（本包
// 并发模型纪律）；查询对过期态「判定即清理」（SubagentActive 泄漏防护同款）。
// 状态仅内存：daemon 重启归零＝插件下一事件重新置位（同 HandedOffAt 纪律，
// 无害）。主会话（parent 空）两态挂 SessionState（DshRunningTS/DshDisposedTS）；
// 子会话态在 dshChildRuns。

// dshRunEnt dsh 子会话运行/终结态（主会话同款两字段挂 SessionState）。
type dshRunEnt struct {
	runningTS  *float64 // nil=无运行态
	disposedTS *float64 // nil=未终结
}

// dshChildRunEnt 取/建子条目（调用方持锁）。
func (l *Ledger) dshChildRunEnt(sid string) *dshRunEnt {
	ent, ok := l.dshChildRuns[sid]
	if !ok {
		ent = &dshRunEnt{}
		l.dshChildRuns[sid] = ent
	}
	return ent
}

// dshChildPrune 双空条目删除（调用方持锁；子会话终态无消费后不占内存）。
func (l *Ledger) dshChildPrune(sid string, ent *dshRunEnt) {
	if ent.runningTS == nil && ent.disposedTS == nil {
		delete(l.dshChildRuns, sid)
	}
}

// DshMainRunSet 置/清主会话运行态：running=true 置位（ts=事件时刻）并清终结
// 态（规格：同键新 running 清除终结态）；running=false=idle 显式复位（规格：
// idle 优先于刷新）。会话未登记＝无态可维护，no-op。
func (l *Ledger) DshMainRunSet(sid string, running bool, ts float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.byKey[[2]string{"dsh", sid}]
	if st == nil {
		return
	}
	if running {
		t := ts
		st.DshRunningTS = &t
		st.DshDisposedTS = nil
	} else {
		st.DshRunningTS = nil
	}
}

// DshChildRunSet DshMainRunSet 的子会话形（态在 dshChildRuns，键=子键）。
func (l *Ledger) DshChildRunSet(sid string, running bool, ts float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	ent := l.dshChildRunEnt(sid)
	if running {
		t := ts
		ent.runningTS = &t
		ent.disposedTS = nil
	} else {
		ent.runningTS = nil
		l.dshChildPrune(sid, ent)
	}
}

// DshMainDisposed 置主会话终结态并清运行态（终结即不在跑——否则 disposed
// 会话仍吃豁免直至上界）。会话未登记＝无态可维护，no-op。
func (l *Ledger) DshMainDisposed(sid string, ts float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.byKey[[2]string{"dsh", sid}]
	if st == nil {
		return
	}
	t := ts
	st.DshDisposedTS = &t
	st.DshRunningTS = nil
}

// DshChildDisposed DshMainDisposed 的子会话形。
func (l *Ledger) DshChildDisposed(sid string, ts float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	ent := l.dshChildRunEnt(sid)
	t := ts
	ent.disposedTS = &t
	ent.runningTS = nil
	l.dshChildPrune(sid, ent)
}

// DshDisposedClear 清终结态（resume 载荷——事件 body 带 source=resume；主/子
// 两处都试：同一键不会既是主又是子，双试无害）。
func (l *Ledger) DshDisposedClear(sid string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if st := l.byKey[[2]string{"dsh", sid}]; st != nil {
		st.DshDisposedTS = nil
	}
	if ent, ok := l.dshChildRuns[sid]; ok {
		ent.disposedTS = nil
		l.dshChildPrune(sid, ent)
	}
}

// DshRunRefresh 运行态刷新：同键已白名单活动事件（turn/start、assistant/
// message——fed 会话走事件口，本方法即事件口路径；未接管会话的文件面检测态
// 推进随 dsh-heartbeat 票 03 补）推进运行态时间戳。仅运行态在位时推进（刷新
// 不置位——置位只认 status=running）；到达的事件本身即活性证据，过期态同样
// 照推（失效上界只在查询侧判定）。
func (l *Ledger) DshRunRefresh(sid string, ts float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if st := l.byKey[[2]string{"dsh", sid}]; st != nil && st.DshRunningTS != nil {
		t := ts
		st.DshRunningTS = &t
	}
	if ent, ok := l.dshChildRuns[sid]; ok && ent.runningTS != nil {
		t := ts
		ent.runningTS = &t
	}
}

// DshChildSeen 登记族系子键（parent→child）。子键两个来源（规格「判活」）：
// ①fed 子会话直报——daemon DshEvent 对 parent 非空的事件登记（live）；②台账
// usage 行 subagent 列回种——daemon 侧 dshEnsureChildrenSeeded（重启恢复，
// DshGate 首问惰性触发、DshChildrenSeedClaim 防重）。
func (l *Ledger) DshChildSeen(parent, child string) {
	if parent == "" || child == "" || parent == child {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.dshChildren[parent] == nil {
		l.dshChildren[parent] = map[string]bool{}
	}
	l.dshChildren[parent][child] = true
}

// DshHasChild 族系子键查询（回种/直报登记的可观察面；等答复窗票 03 复用）。
func (l *Ledger) DshHasChild(parent, child string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dshChildren[parent][child]
}

// DshChildrenSeedClaim 族系回种防重闸（test-and-set）：首调返回 true（调用方
// 获回种执行权），其后 false。回种本体在 daemon 侧——锁外读账本（一次性全量，
// dshFed.seedFromAccounts 同款成本），经 DshChildSeen 锁内写。
func (l *Ledger) DshChildrenSeedClaim() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.dshChildrenSeeded {
		return false
	}
	l.dshChildrenSeeded = true
	return true
}

// dshRunAgeEffective 运行态年龄在效判定（主/子两道共用）：floor≤age≤cap。
// 原语义（floor=0/cap=DshRunStaleS）：负 age——置位钟略超前判定钟——照旧
// 在效，回归零变化。Gated（floor=DshRunGraceS/cap=DshRunGateCapS）：宽限
// 下限防本输入信号自豁免＋效期上界防 running 残留（见两常量注释）。
// 调用方持锁。
func dshRunAgeEffective(age, floor, cap float64) bool {
	if floor > 0 && age < floor {
		return false
	}
	return age <= cap
}

// DshFamilyRunning 族系在跑判定（**原语义**，qwatch 开窗等处共用）：本键运行态
// 在效 OR 任一已知子键运行态在效。在效＝置位/刷新距今 ≤ DshRunStaleS；过期
// 态判定即清理。
func (l *Ledger) DshFamilyRunning(sid string) bool {
	return l.dshFamilyRunningWindow(sid, 0, DshRunStaleS)
}

// DshFamilyRunningGated 闸门豁免专用（machineWaiting dsh 道）：同
// DshFamilyRunning 但窗口＝DshRunGraceS ≤ 距今 ≤ DshRunGateCapS（2026-10-05
// 漏拦案两洞：①下限——刚置位的运行态多半是本输入自己的 turn/start /
// status=running 信号，不得自豁免凉会话；②上界——running 残留可活到 1h，
// 35min~1h 闲置段的回流全吃豁免漏拦）。豁免真正的保护域＝「凉会话且近处有
// 机器活动」（长生成中途/多步循环，步内产出间隔≪600s）。宽限内与上界外
// （未过 DshRunStaleS）不清理运行态。
func (l *Ledger) DshFamilyRunningGated(sid string) bool {
	return l.dshFamilyRunningWindow(sid, DshRunGraceS, DshRunGateCapS)
}

// dshFamilyRunningWindow 族系在跑判定的公共实现（窗口参数化；调用方无锁
// 进入，此处持锁）。
func (l *Ledger) dshFamilyRunningWindow(sid string, floor, cap float64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := clock.Now()
	if st := l.byKey[[2]string{"dsh", sid}]; st != nil && st.DshRunningTS != nil {
		age := now - *st.DshRunningTS
		if dshRunAgeEffective(age, floor, cap) {
			return true
		}
		if age > DshRunStaleS {
			st.DshRunningTS = nil // 判定即清理（有界失效）
		}
	}
	for child := range l.dshChildren[sid] {
		ent, ok := l.dshChildRuns[child]
		if !ok || ent.runningTS == nil {
			continue
		}
		age := now - *ent.runningTS
		if dshRunAgeEffective(age, floor, cap) {
			return true
		}
		if age > DshRunStaleS {
			ent.runningTS = nil
			l.dshChildPrune(child, ent)
		}
	}
	return false
}
