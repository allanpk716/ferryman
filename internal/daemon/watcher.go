package daemon

// watcher.go — 票16：守望轮询 + 等答复窗开窗判定 + 心跳调度 + 用量采集喂入
// （规格 ferryman/daemon.py:35-490 逐字平移；注释即不变量文档，随行搬运）。
//
// DESIGN §4：轮询 stat-only（快）；标题/峰值上下文在会话达总结阈值时懒提取
// （一次读盘）；守望异常永不死（轮询/开窗/调度/记账/采集五段各自吞错）。
//
// 并发纪律（本包铁律见 windows.go 顶部）：SessionState 为共享可变引用，读写
// 均须持台账锁——守望判定点在 ledger.Mu 下一次性抄快照（Python GIL 无锁读的
// Go 形，gate.go sessionSnap 同款范式），读盘/网络一律锁外。开窗临界区双锁
// 同序 windowsMu → ledger.Mu（票13 范式，与 server.subagent 互为对侧握手）。
// 跳调度的临界区"锁内 os.Stat 有意为之"（票04 M3 评审，勿移出）。
//
// Daemon/Accounts/BeatSender/QWatchStats 为 nil = 旧测试形态（Python __new__
// 裸构造 + getattr 容错的 Go 形：nil 检查）。

import (
	"context"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/beat"
	"ferryman/internal/cctrans"
	"ferryman/internal/clock"
	"ferryman/internal/codextrans"
	"ferryman/internal/config"
	"ferryman/internal/extract"
	"ferryman/internal/ferry"
	"ferryman/internal/harvest"
	"ferryman/internal/ledger"
	"ferryman/internal/mathx"
	"ferryman/internal/notify"
	"ferryman/internal/pathsx"
	"ferryman/internal/policy"
	"ferryman/internal/prices"
	"ferryman/internal/qwatch"
	"ferryman/internal/store"
)

// CodexWatchDirs codex 会话目录清单：主目录（默认 ~/.codex/sessions）+ 配置额外目录 +
// Orca 运行时目录（存在时自动追加）（daemon.py:35-50 逐字；注释搬运）。
//
// 2026-09-17 真机抓包发现：经 Orca 启动的 codex 把 CODEX_HOME 重定向到
// %APPDATA%\orca\codex-runtime-home\home\sessions——不扫则这些会话 gate
// 能收到（钩子直报）但永远不被守望/摆渡。
func CodexWatchDirs(watch config.WatchCfg, home string) []string {
	if home == "" { // Python home or Path.home()
		if h, err := os.UserHomeDir(); err == nil {
			home = h
		}
	}
	primary := watch.CodexSessionsDir
	if primary == "" {
		primary = filepath.Join(home, ".codex", "sessions")
	}
	dirs := []string{primary}
	dirs = append(dirs, watch.CodexExtraDirs...)
	orca := filepath.Join(home, "AppData", "Roaming", "orca",
		"codex-runtime-home", "home", "sessions")
	if _, err := os.Stat(orca); err == nil && !slices.Contains(dirs, orca) {
		dirs = append(dirs, orca)
	}
	return dirs
}

// Watcher mtime 轮询：登记台账 + 对达总结阈值的活跃会话懒富化并入队摆渡
// （daemon.py Watcher 1:1；Python threading.Thread 的 Go 形 = Run + Stop）。
type Watcher struct {
	Cfg       *config.Config
	Ledger    *ledger.Ledger
	Store     *store.Store
	Enqueue   func(*ledger.SessionState) bool
	StartedAt float64
	Accounts  *accounts.Accounts // nil = 不采集/不记账（旧调用/测试零改动）
	Daemon    *Daemon            // nil = 不接线（旧调用/测试零改动）；T48 票03：
	//                            停车窗判定 + usage 行喂入闭窗；T51：两窗互斥
	//                            探测（parking_open）与 mode 活值护栏
	BeatSender  beat.Sender // nil = 未接真实 sender——enforce 时降级 observe 演练并告警一次
	QWatchStats *beat.QWatchStats

	ccDir  string
	cxDirs []string

	harvest *harvest.HarvestState // Accounts 非 nil 且 HarvestUsage 时建

	// stampMu 守护两张版本章表（叶子锁，不嵌其他锁）：(agent, sid) → 已判定
	// 过的 last_write。同一写入版本只读盘判定一次；仅内存，重启丢章=重启后
	// 多判一轮（无害）。
	stampMu       sync.Mutex
	qwatchSeen    map[winKey]float64
	qwatchHitSeen map[winKey]float64 // 票04 命中事件去重章：瞬态阻塞不盖
	//                                 _qwatch_seen、会逐轮重判——命中事件靠本章
	//                                 保证每写入版本只落一次。

	beatInFlight      atomic.Bool // 全局同时最多 1 跳在途（守望单线程串行，旗只作跨会话串行化的显式不变量）
	breaker           beat.Breaker
	noopSender        beat.NoopSender
	beatEnforceWarned atomic.Bool

	// ---- 票04：等待窗泳道（双泳道第二道，F8/F9/F4） ----
	// waitLane 每窗一条泳道状态，键 (agent, sid)；守卫锁＝Daemon.windowsMu
	// （泳道状态与窗口表同域同生死：探测/占用/结算全程在 windowsMu 临界区，
	// 不引入新锁序条目——windowsMu 是既有铁律的外层锁，单独取用合法）。
	// 台账锁（ledger.Mu）内绝不触碰本表（铁律第 2 条的引申：泳道判定点
	// 只在 windowsMu 下）。
	waitLane map[winKey]*waitLaneRec
	// waitPolicyFn 策略计算器缝：前缀 → 心跳参数组。公式单源红线——生产
	// 实现（defaultWaitPolicy）只调 policy.Compute，本文件绝不出现第二份
	// τ/cap 公式；测试注入 fake 断言取值调用。
	waitPolicyFn func(prefixTokens int) (policy.HeartbeatPolicy, error)
	// waitBooks 价格表缓存（NewWatcher 一次读盘；config 重启生效，无热加载）。
	waitBooks map[string]prices.PriceBook
	// waitEnforceDowngraded enforce＋渡口关（无 [dock]）的启动降级位：等待窗
	// 侧按 observe 对待（告警一次）。问询守望不受此校验影响——它维持既有
	// "nil sender→observe 演练" 行为。
	waitEnforceDowngraded bool
	waitPolicyWarned      atomic.Bool // 策略不可用（TTL 未测/无 p_cache）告警一次
	waitDrillWarned       atomic.Bool // enforce 无 sender 演练告警一次（问询同款语义）
	// lanePins 两泳道 Pin 对账表（stampMu 叶子锁守护）：已 Pin 会话集合。
	// Pin/Unpin 走 dockPin/dockUnpin 缝（渡口关＝句柄 nil 安全跳过）。
	lanePins  map[winKey]bool
	dockPin   func(sessionID string)
	dockUnpin func(sessionID string)

	detect func(path string, minQuestions int) qwatch.Verdict // 测试注入缝（Python monkeypatch daemon_mod.detect 同位）
	enrich func(*ledger.SessionState)                         // 测试注入缝（Python _enrich 覆写同位）

	// ---- 票02:同模型判热门(ADR-0015 决定一/二;F3 判热时钟口径、F6 跳过原因) ----
	// ReqClock 判热时钟数据源(钉死,见 beat.LastRequestClock):每会话最后上游
	// 请求时刻,含体外心跳重放。台账闲置(闸门语义)不含心跳、完全不动——两钟
	// 各司其职。nil(旧测试裸构造形态)按无观测处理 → 保守判冷。
	ReqClock *beat.LastRequestClock
	// ArmVerdict 实跳臂结论查询缝(票01 config.ArmVerdictResolver 预留):nil =
	// 状态源未装配 → 无结论 = 未启用(D6 启用硬门槛;票04 接线真源)。
	ArmVerdict config.ArmVerdictResolver
	// smSeen 同模型触发版本章(stampMu 叶子锁守护):(agent, sid) → 已判定过的
	// last_write——每写入版本只判一次,防跳过事件逐轮刷屏(qwatchSeen 同款纪律)。
	smSeen map[winKey]float64
	// bookSameModelSkipFn 跳过遥测缝(测试注入);nil = 默认走 bookQwatch 通道记
	// "same_model_skip" 科目(usage/qwatch 同层遥测;Accounts nil 时静默跳过)。
	bookSameModelSkipFn func(st *ledger.SessionState, reason string, ff accounts.Fields)

	stopCh   chan struct{}
	stopOnce sync.Once
}

// extractFacts CC 提取函数缝（Python monkeypatch ferryman.extract.extract
// 的同位补丁面：T14 懒富化单次性等测试以计数替身注入——生产勿动）。
var extractFacts = extract.Extract

// NewWatcher 构造 Watcher（Python __init__ 1:1）。
func NewWatcher(cfg *config.Config, lg *ledger.Ledger, st *store.Store,
	enqueue func(*ledger.SessionState) bool, startedAt float64,
	acc *accounts.Accounts, d *Daemon, sender beat.Sender, qs *beat.QWatchStats) *Watcher {
	w := &Watcher{
		Cfg:           cfg,
		Ledger:        lg,
		Store:         st,
		Enqueue:       enqueue,
		StartedAt:     startedAt,
		Accounts:      acc,
		Daemon:        d,
		BeatSender:    sender,
		QWatchStats:   qs,
		qwatchSeen:    map[winKey]float64{},
		qwatchHitSeen: map[winKey]float64{},
		waitLane:      map[winKey]*waitLaneRec{},
		lanePins:      map[winKey]bool{},
		ReqClock:      beat.NewLastRequestClock(), // 票02:判热时钟(数据源钉死)
		smSeen:        map[winKey]float64{},       // 票02:同模型触发版本章
		stopCh:        make(chan struct{}),
	}
	w.detect = qwatch.Detect
	w.enrich = w.enrichImpl
	if acc != nil && cfg.Watch.HarvestUsage {
		w.harvest = harvest.NewHarvestState(acc)
	}
	// 票04 等待窗泳道接线：策略缝（生产实现）＋价格表一次读盘＋Pin 缝。
	w.waitPolicyFn = w.defaultWaitPolicy
	w.waitBooks = prices.LoadPrices("")
	w.dockPin = func(sid string) {
		if w.Daemon != nil && w.Daemon.DockSnap != nil { // 渡口关＝句柄 nil 安全跳过
			w.Daemon.DockSnap.Pin(sid)
		}
	}
	w.dockUnpin = func(sid string) {
		if w.Daemon != nil && w.Daemon.DockSnap != nil {
			w.Daemon.DockSnap.Unpin(sid)
		}
	}
	// 启动校验（票04）：mode=enforce 且无 [dock]（渡口关，快照源不存在）→
	// 告警一次并把等待窗侧按 observe 对待。问询守望不受此校验影响。
	if cfg.WaitWindow.Mode == "enforce" && cfg.Dock == nil {
		w.waitEnforceDowngraded = true
		fmt.Printf("[wait] ⚠ [wait_window] mode=enforce 但未配置 [dock]（渡口关，快照源不存在）" +
			"——等待窗侧按 observe 演练对待（问询守望不受影响；配置 [dock] 后重启生效）\n")
	}
	ccDir := cfg.Watch.CCProjectsDir
	if ccDir == "" {
		home, _ := os.UserHomeDir()
		ccDir = filepath.Join(home, ".claude", "projects")
	}
	w.ccDir = ccDir
	w.cxDirs = CodexWatchDirs(cfg.Watch, "") // Python home=None → Path.home()
	return w
}

// Run 守望主循环（Python run 1:1）：先等 poll_interval_s 再轮询；异常打印
// 继续（守望循环永不死）。ctx 与 Stop 双通道退出。
func (w *Watcher) Run(ctx context.Context) {
	interval := time.Duration(w.Cfg.Watch.PollIntervalS * float64(time.Second))
	if interval <= 0 { // config.Validate 已保证 >0；下限仅防裸构造零值
		interval = time.Millisecond
	}
	tk := time.NewTicker(interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-tk.C:
			w.pollGuarded()
		}
	}
}

// pollGuarded 轮询一轮 + 异常兜底（daemon.py:97-100 注释搬运：守望循环永不死）。
func (w *Watcher) pollGuarded() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("[watch] 轮询异常（忽略继续）: %v\n", r)
		}
	}()
	w.pollCC()
	w.pollCodex()
}

// Stop 停止守望线程（threading.Event.set 的 Go 形）。
func (w *Watcher) Stop() { w.stopOnce.Do(func() { close(w.stopCh) }) }

// PollOnce 测试直调一轮（= Python 测试手动 tick 后的 _poll_cc+_poll_codex）。
func (w *Watcher) PollOnce() { w.pollCC(); w.pollCodex() }

// ---- 轮询（daemon.py:105-184 逐字） ----

func (w *Watcher) pollCC() {
	if _, err := os.Stat(w.ccDir); err != nil { // cc_dir.exists()
		return
	}
	_ = filepath.WalkDir(w.ccDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() { // glob 对不可读项静默跳过
			return nil
		}
		if !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		info, err := os.Stat(p)
		if err != nil { // OSError → continue
			return nil
		}
		if hasPathPart(p, "subagents") {
			// T32 + 票01/ADR-0008 分流：子代理转录（<sid>/subagents/agent-*.jsonl）
			// 不是独立会话——不 Touch、不入摆渡队、不参与闲置判定/问询守望/
			// 心跳排程（摆渡与闸门语义零变动，T32/T48 只消费不改变）；唯一新增
			// 行为是喂 usage 采集（随父会话入账）。
			w.harvestSubagentUsage(p, info.Size())
			return nil
		}
		mtime, size := statMTime(info), int(info.Size())
		prev := w.prevQwatchOpen("cc", pathStem(d.Name())) // 票04：touch 前窗口态（关窗事件用）
		st := w.Ledger.Touch("cc", pathStem(d.Name()), p, mtime, size, w.StartedAt)
		w.bookQwatchClose(st, prev)
		w.harvestUsage(p, info.Size(), st)
		w.maybeQwatch(st)
		w.maybeFireBeats(st)
		w.maybeWaitBeats(st) // 票04：等待窗泳道（在问询跳之后——两泳道共用单在途）
		w.reconcilePins(st)  // 票04：两泳道快照 Pin 对账（窗开→Pin/窗关且结算→Unpin）
		w.maybeSameModel(st) // 票02：同模型判热门（先于总结阈值；off 时零开销零行为）
		w.maybeEnqueue(st)
		return nil
	})
}

// prevQwatchOpen touch 前的等答复窗口态（票04 关窗事件的"前"照）：（开窗时刻，
// 实发跳数）二元组——跳数必须在 touch 前快照，Ledger.touch 关窗时会先把
// qwatch_beats_fired 清零，touch 后读现值恒 0（评审 Important 修复）。异常按
// 无窗（Go：台账读不可失败，ok=false 即无窗语义）。
func (w *Watcher) prevQwatchOpen(agent, sid string) qwatchOpenSnap {
	w.Ledger.Mu().Lock()
	defer w.Ledger.Mu().Unlock()
	st := w.Ledger.GetLocked(agent, sid)
	if st == nil || st.QWatchOpenedTS == nil {
		return qwatchOpenSnap{}
	}
	return qwatchOpenSnap{openedTS: *st.QWatchOpenedTS, fired: st.QWatchBeatsFired, ok: true}
}

// qwatchOpenSnap _prev_qwatch_open 的二元组（ok=false ≡ Python None）。
type qwatchOpenSnap struct {
	openedTS float64
	fired    int
	ok       bool
}

// bookQwatchClose 票04 关窗事件：touch 前窗开着、touch 后窗没了 ⇒ 这次新写入
// 关的窗（touch 是关窗唯一入口，本对照即完整的关窗面）。lineage 换 sid 等罕见
// 边角（st 不是原对象）无从回指，不记。dur 以新写入时刻收口。opened_ts
// 与 beats_fired 均取 touch 前快照——touch 关窗已清零，读现值失真。
func (w *Watcher) bookQwatchClose(st *ledger.SessionState, prev qwatchOpenSnap) {
	w.Ledger.Mu().Lock()
	nowOpened := st.QWatchOpenedTS != nil
	lastWrite := st.LastWrite
	w.Ledger.Mu().Unlock()
	if !prev.ok || nowOpened {
		return
	}
	w.bookQwatch("qwatch_close", st, accounts.Fields{
		"opened_ts":    mathx.Round(prev.openedTS, 3),
		"closed_ts":    mathx.Round(lastWrite, 3),
		"dur_s":        mathx.Round(math.Max(0.0, lastWrite-prev.openedTS), 1),
		"beats_fired":  prev.fired,
		"close_reason": "write",
	})
}

// bookQwatch 问询守望事件入账（票04）：走既有台账科目通道（accounts.jsonl），
// 只记元数据与计数，永不落消息正文（隐私铁律）。记账永不弄断守望。
func (w *Watcher) bookQwatch(kind string, st *ledger.SessionState, fields accounts.Fields) {
	if w.Accounts == nil {
		return
	}
	// 身份字段：Agent/SessionID/TranscriptPath 建后不变直读；Cwd 可变（A 修复
	// 同款），台账锁内快照。
	w.Ledger.Mu().Lock()
	cwd := st.Cwd
	w.Ledger.Mu().Unlock()
	ff := make(accounts.Fields, len(fields)+4)
	for k, v := range fields {
		ff[k] = v
	}
	ff["agent"] = st.Agent
	ff["session_id"] = st.SessionID
	ff["lineage_id"] = pathsx.NormPath(st.TranscriptPath)
	ff["project"] = cwd
	if _, err := w.Accounts.Record(kind, -1, ff); err != nil {
		fmt.Printf("[account] %s 记账失败（忽略）: %v\n", kind, err)
	}
}

func (w *Watcher) pollCodex() {
	// 跨目录按 session_id 去重：~/.codex/sessions 与 Orca runtime 目录可能
	// 互为副本（2026-09-17 实测同 uuid 两份文件）——主目录在前，路径稳定。
	seen := map[string]bool{}
	for _, cx := range w.cxDirs {
		if _, err := os.Stat(cx); err != nil { // cx_dir.exists()
			continue
		}
		_ = filepath.WalkDir(cx, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			name := d.Name()
			if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
				return nil
			}
			info, err := os.Stat(p)
			if err != nil { // OSError → continue
				return nil
			}
			// rollout 文件名 rollout-<ts>-<uuid>.jsonl → session_id 取 uuid 段
			sid := codexSidFromStem(pathStem(name))
			if seen[sid] {
				return nil
			}
			seen[sid] = true
			st := w.Ledger.Touch("codex", sid, p, statMTime(info), int(info.Size()), w.StartedAt)
			w.maybeEnqueue(st)
			return nil
		})
	}
}

// codexSidFromStem Python p.stem.split("-")[-1] if "-" in p.stem else p.stem。
func codexSidFromStem(stem string) string {
	if i := strings.LastIndex(stem, "-"); i >= 0 {
		return stem[i+1:]
	}
	return stem
}

// ---- 入队判定：六道推迟（daemon.py:186-218 逐字） ----

func (w *Watcher) maybeEnqueue(st *ledger.SessionState) {
	th := w.Cfg.ThresholdFor(st.Agent)
	sid := st.SessionID
	w.Ledger.Mu().Lock()
	observed := st.ObservedActive
	lastWrite := st.LastWrite
	handedOff := st.HandedOffAt
	opened := st.QWatchOpenedTS != nil
	w.Ledger.Mu().Unlock()
	if !observed {
		return
	}
	if clock.Now()-lastWrite < th.SummarizeS {
		return
	}
	if handedOff >= lastWrite {
		return // 交接仍覆盖最新活动
	}
	// T51：(死线已到, 窗口开着)
	due, window := w.qwatchDeadline(opened, lastWrite, th)
	if window && !due {
		return // 等答复窗口期间推迟常规入队（写入关窗即恢复）
	}
	if w.Ledger.SubagentActive(st.Agent, sid) {
		return // T32：子代理运行中（钩子计数，内存判定）→ 推迟，不置 handed_off
	}
	// 悬空 tool_use：推迟（不置 handed_off，下轮重查）。死线强制入队时不再因
	// 悬空让步——含 AskUserQuestion：等答复窗拖到死线意味着用户久未作答，
	// 闸门临近，被拦 ⇒ 交接必已存在（失败/超时由骨架降级兜底）。
	if !due && st.Agent == "cc" && cctrans.HasDanglingToolUse(st.TranscriptPath) {
		return
	}
	// T48 票03：异步等待停车未过期 → 推迟，不置 handed_off（20260918 12:20/
	// 14:15 误摆渡案回归）。T51 merge：本道不被死线豁免（due=True 也照样
	// 推迟）——死线只跳过悬空让步，不跳 window_wait（保守；两窗互斥成立时
	// "死线到线遇停车窗"本不可达，此为防御性并存）。
	if w.Daemon != nil && w.Daemon.WindowWait(st.Agent, sid) {
		return
	}
	w.enrich(st) // 懒富化：标题/峰值（每版本一次读盘）
	w.Ledger.Mu().Lock()
	peak := st.PeakCtx
	w.Ledger.Mu().Unlock()
	if peak < th.MinCtxTokens {
		w.Ledger.Mu().Lock()
		st.HandedOffAt = st.LastWrite // 过小会话：标记已处理防反复读盘
		w.Ledger.Mu().Unlock()
		return
	}
	if w.Enqueue(st) {
		w.Ledger.Mu().Lock()
		st.HandedOffAt = clock.Now() // 入队即记（防重复入队；失败由队列重试语义覆盖）
		w.Ledger.Mu().Unlock()
	}
}

// qwatchDeadline T51 等答复窗摆渡死线判定 →（死线已到, 窗口开着）。
//
// 死线 = block_s − ferry_deadline_lead_s：窗口会话闲置达此线即强制入队，
// 赶在闸门拦截（block_s）之前留出交接生成余量。任何异常按 (False, False)
// ——绝不影响摆渡主路径（T48 同款纪律；Go 无异常源，纯内存读天然承接）。
func (w *Watcher) qwatchDeadline(hasWindow bool, lastWrite float64, th config.ThresholdCfg) (due, window bool) {
	if !hasWindow {
		return false, false
	}
	lead := w.Cfg.QuestionWatch.FerryDeadlineLeadS
	return clock.Now()-lastWrite >= th.BlockS-lead, true
}

// ---- T51 等答复窗口开窗判定（daemon.py:234-306 逐字） ----

func (w *Watcher) maybeQwatch(st *ledger.SessionState) {
	// 窗口路径异常不影响守望主路径（daemon.py:305-306）——recover 兜底；
	// 正常路径无 panic 源，与 Python try/except 同口径。
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("[qwatch] 开窗判定异常（忽略继续）: %v\n", r)
		}
	}()
	// 命中谓词四条件全真才开窗（spec 决策 2）：①提问潮（qwatch.detect）；
	// ②悬空集 ⊆ {AskUserQuestion}（Verdict.askuserquestion_dangling 直用，
	// 空集真空真）；③子代理在飞 = 0；④前缀 ≥ min_ctx_tokens。与停车窗互斥、
	// 先开者赢（ferry_daemon.parking_open 探测）。仅 cc（检测器只认 CC jsonl）。
	// 判定按 last_write 版本章缓存（每写一次判一次，不逐轮读盘）；子代理在飞/
	// 停车窗开着属瞬态阻塞，不盖版本章，解除后同版本仍可开窗。任何异常吞掉
	// ——绝不影响守望与摆渡主路径。
	if w.qwatchMode() == "off" || st.Agent != "cc" {
		return // off 零开销；非 cc 直接跳过
	}
	key := winKey{st.Agent, st.SessionID}
	w.Ledger.Mu().Lock()
	opened := st.QWatchOpenedTS != nil
	lastWrite := st.LastWrite
	observed := st.ObservedActive
	path := st.TranscriptPath
	w.Ledger.Mu().Unlock()
	if opened {
		return // 已开窗；关窗只由新写入触发
	}
	if w.stampGet(&w.qwatchSeen, key) == lastWrite {
		return // 该写入版本已判定过
	}
	if !observed {
		return // 与摆渡同纪律：启动后只见登记不动作
	}
	verdict := w.detect(path, w.Cfg.QuestionWatch.MinQuestions)
	if !verdict.IsSurge || !verdict.AskUserQuestionDangling {
		w.stampSet(&w.qwatchSeen, key, lastWrite) // 结论性不满足，随版本缓存
		return                                    // 条件①②
	}
	// 票04 命中事件（证据形态五字段，spec 决策 8）：随版本章去重——瞬态阻塞
	// 不盖 _qwatch_seen、会逐轮重判，命中事件靠专属本章保证每写入版本只落
	// 一次；observe 命中清单即人工复核与漏检对照地基。
	if w.stampGet(&w.qwatchHitSeen, key) != lastWrite {
		w.stampSet(&w.qwatchHitSeen, key, lastWrite)
		w.bookQwatch("qwatch_hit", st, accounts.Fields{
			"unit_count":      verdict.UnitCount,
			"marker_lines":    verdict.BD.MarkerLines,
			"qmark_lines":     verdict.BD.QmarkLines,
			"numbered_lines":  verdict.BD.QualifiedNumberedLines,
			"transcript_path": path,
		})
		if w.QWatchStats != nil {
			w.QWatchStats.RecordHit()
		}
	}
	if w.Ledger.SubagentActive(st.Agent, st.SessionID) {
		return // 条件③（瞬态：不盖版本章）
	}
	if w.Daemon != nil && w.Daemon.ParkingOpen(st.Agent, st.SessionID) {
		return // 两窗互斥先开者赢（瞬态：不盖版本章）
	}
	w.enrich(st) // 条件④要 peak_ctx（懒富化按版本缓存）
	w.Ledger.Mu().Lock()
	peak := st.PeakCtx
	w.Ledger.Mu().Unlock()
	if peak < w.Cfg.ThresholdFor(st.Agent).MinCtxTokens {
		w.stampSet(&w.qwatchSeen, key, lastWrite)
		return // 条件④（结论性：不新写不再变）
	}
	// 开窗 check-then-act 临界区（票03）：与停车窗开窗判定（server.subagent）
	// 共用台账锁，锁内复验全部瞬态条件——毫秒级双窗并存窗口归零。锁外已做
	// 初筛（复验几乎必过），锁内只有内存操作，不持锁读盘。Go 双锁同序
	// windowsMu（外层）→ ledger.Mu（内层）（票13 范式；Daemon 未接线的旧
	// 测试形态只取台账锁）。
	openedNow := false
	if w.Daemon != nil {
		w.Daemon.windowsMu.Lock()
	}
	func() {
		w.Ledger.Mu().Lock()
		defer func() {
			w.Ledger.Mu().Unlock()
			if w.Daemon != nil {
				w.Daemon.windowsMu.Unlock()
			}
		}()
		if st.QWatchOpenedTS != nil {
			return // 复验：并发路径已开窗
		}
		if w.Ledger.SubagentActiveLocked(st.Agent, st.SessionID) {
			return // 复验条件③（瞬态）
		}
		if w.Daemon != nil && w.Daemon.parkingOpenLocked(st.Agent, st.SessionID) {
			return // 复验两窗互斥（先开者赢）
		}
		now := clock.Now()
		st.QWatchOpenedTS = &now
		st.QWatchBeatsFired = 0
		st.QWatchPlan = w.beatPlan(now) // 票03：开窗即排计划
		st.QWatchSnapshot = &ledger.QSnap{MTime: st.LastWrite, Size: st.Size}
		openedNow = true
	}()
	if !openedNow {
		return
	}
	// 票04 开窗事件锁外落账（记账读盘绝不持台账锁——与临界区"锁内只有
	// 内存操作"同纪律）。
	w.Ledger.Mu().Lock()
	prefix := st.PeakCtx
	w.Ledger.Mu().Unlock()
	w.bookQwatch("qwatch_open", st, accounts.Fields{
		"unit_count": verdict.UnitCount, "prefix_tokens": prefix})
	if w.QWatchStats != nil {
		w.QWatchStats.RecordWindowOpened()
	}
}

// stampGet/stampSet 版本章表读写（stampMu 叶子锁）。
func (w *Watcher) stampGet(m *map[winKey]float64, k winKey) float64 {
	w.stampMu.Lock()
	defer w.stampMu.Unlock()
	return (*m)[k]
}

func (w *Watcher) stampSet(m *map[winKey]float64, k winKey, v float64) {
	w.stampMu.Lock()
	defer w.stampMu.Unlock()
	if *m == nil {
		*m = map[winKey]float64{}
	}
	(*m)[k] = v
}

// qwatchMode 读 question_watch.mode 活值：接线了 Daemon 走 cfgMu 护栏（与
// 一键停/熔断降级互斥）；旧测试形态（Daemon=nil）直读配置。
func (w *Watcher) qwatchMode() string {
	if w.Daemon != nil {
		return w.Daemon.GetQWatchMode()
	}
	return w.Cfg.QuestionWatch.Mode
}

// setQWatchMode 写 question_watch.mode 活值（熔断降级 enforce→observe）：
// 接线了 Daemon 走护栏方法 SetQWatchMode（config 写点收敛）；旧测试形态
// 直写配置（Python 同为属性直写）。
func (w *Watcher) setQWatchMode(v string) {
	if w.Daemon != nil {
		w.Daemon.SetQWatchMode(v)
		return
	}
	w.Cfg.QuestionWatch.Mode = v
}

// ---- T51 票03 心跳调度（spec 决策 4-7；daemon.py:308-439 逐字） ----

// beatPlan 开窗排计划（决策 4）：max_beats 跳、每跳间隔 beat_interval_s。
// 首跳在 t0+interval——开窗瞬间不跳（末条落盘本身已刷新缓存）。
func (w *Watcher) beatPlan(t0 float64) []float64 {
	qw := w.Cfg.QuestionWatch
	n := qw.MaxBeats
	if n < 0 {
		n = 0 // Python max(0, max_beats)
	}
	plan := make([]float64, 0, n)
	for i := 1; i <= n; i++ {
		plan = append(plan, t0+float64(i)*qw.BeatIntervalS)
	}
	return plan
}

// maybeFireBeats 心跳调度入口（守望轮询循环内，风格对齐 _maybe_qwatch）：到期
// 跳逐发。到期先两道验（无新写入＋复 stat 新鲜度），任一不符取消本跳并作废
// 剩余计划；"任何新写入取消剩余跳"的关窗在 Ledger.touch（清窗连着清计划）。
// 一切异常吞掉——绝不影响守望与摆渡主路径。
func (w *Watcher) maybeFireBeats(st *ledger.SessionState) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("[qwatch] 心跳调度异常（忽略继续）: %v\n", r)
		}
	}()
	if w.qwatchMode() == "off" || st.Agent != "cc" {
		return // off 零开销；窗口只在 cc 侧存在
	}
	now := clock.Now()
	found, minDue := false, 0.0
	w.Ledger.Mu().Lock()
	for _, t := range st.QWatchPlan {
		if t <= now && (!found || t < minDue) {
			found, minDue = true, t
		}
	}
	w.Ledger.Mu().Unlock()
	if found {
		w.fireOneBeat(st, minDue) // 一轮至多一发（全局串行节奏）
	}
}

// fireOneBeat 单跳：两道验＋在途占用（台账锁临界区内）→ 锁外发送 → 结账。
func (w *Watcher) fireOneBeat(st *ledger.SessionState, beatTS float64) {
	w.Ledger.Mu().Lock() // 两道验-占用-出队与台账写（关窗清计划）串行
	if st.QWatchOpenedTS == nil {
		w.Ledger.Mu().Unlock()
		return // 窗已被新写入关掉，计划随窗作废
	}
	if w.beatInFlight.Load() {
		w.Ledger.Mu().Unlock()
		return // 全局同时最多 1 跳在途（跨会话串行）
	}
	snap := st.QWatchSnapshot
	if snap == nil || st.LastWrite != snap.MTime {
		st.QWatchPlan = nil // 验①台账版本章：计划基线后见过新写入
		w.Ledger.Mu().Unlock()
		fmt.Printf("[qwatch] 跳取消：台账有新写入（%s），剩余计划作废\n", runeCap8(st.SessionID))
		return
	}
	// 验②复 stat 转录：mtime+size 与开窗快照一致。
	// ★ 锁内 os.Stat 有意为之（daemon.py:345-348 注释搬运，勿移出）：两道验
	// （版本章＋新鲜度）与出队/在途占用必须同一临界区内完成才是原子的——挪
	// 到锁外会重新打开"验完被并发关窗/并发跳"的窗口（票04 M3 评审注明）。
	fresh := false
	if sb, err := os.Stat(st.TranscriptPath); err == nil {
		fresh = statMTime(sb) == snap.MTime && int(sb.Size()) == snap.Size
	}
	if !fresh {
		st.QWatchPlan = nil // 两道验不过：本跳取消＋作废剩余计划
		w.Ledger.Mu().Unlock()
		fmt.Printf("[qwatch] 跳取消：转录新鲜度不符（%s），剩余计划作废\n", runeCap8(st.SessionID))
		return
	}
	for i, t := range st.QWatchPlan { // Python qwatch_plan.remove(beat_ts)
		if t == beatTS {
			st.QWatchPlan = append(st.QWatchPlan[:i:i], st.QWatchPlan[i+1:]...)
			break
		}
	}
	st.QWatchBeatsFired++
	w.beatInFlight.Store(true)
	w.Ledger.Mu().Unlock()
	defer w.beatInFlight.Store(false) // Python finally（跨临界区复位，atomic 防 -race）
	result := w.sendBeat(st, beatTS)  // 网络绝不持台账锁
	w.settleBeat(st, result)
}

// sendBeat 选发送器（决策 5）：observe → NoopSender（零网络）；enforce → 注入的
// 真实 sender；未注入（Q14 段二/三前）则告警一次并按 observe 演练。
func (w *Watcher) sendBeat(st *ledger.SessionState, beatTS float64) beat.BeatResult {
	w.Ledger.Mu().Lock()
	plan := beat.BeatPlan{
		Agent: st.Agent, SessionID: st.SessionID,
		TranscriptPath: st.TranscriptPath,
		OpenedTS:       0, // Python st.qwatch_opened_ts or 0.0
		LastWrite:      st.LastWrite,
		Size:           st.Size,
		BeatIndex:      st.QWatchBeatsFired,
		BeatTS:         beatTS,
	}
	if st.QWatchOpenedTS != nil {
		plan.OpenedTS = *st.QWatchOpenedTS
	}
	w.Ledger.Mu().Unlock()
	mode := w.qwatchMode()
	if mode == "enforce" && w.BeatSender != nil {
		var r beat.BeatResult
		func() { // 发送器炸掉按一跳 ERROR 记（Python except Exception 同口径）
			defer func() {
				if p := recover(); p != nil {
					r = beat.BeatResult{Sent: true, OK: false,
						Err: fmt.Sprintf("sender-raise:%T", p)}
				}
			}()
			r = w.BeatSender.Send(plan)
		}()
		return r
	}
	if mode == "enforce" && !w.beatEnforceWarned.Swap(true) { // 只告警一次（旧值 false = 首次）
		fmt.Printf("[qwatch] ⚠ mode=enforce 但未注入真实 BeatSender（Q14 段二/三" +
			"前不授权真发）——心跳按 observe 演练记账\n")
	}
	return w.noopSender.Send(plan)
}

// settleBeat 结账：逐跳入账（含 observe 演练）→ 熔断判定 → 动作（决策 6/7）。
func (w *Watcher) settleBeat(st *ledger.SessionState, result beat.BeatResult) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("[qwatch] 心跳结账异常（忽略）: %v\n", r)
		}
	}()
	w.noteUpstreamRequest(st.SessionID, result) // 票02:真发重放计入判热时钟(F3)
	outcome := beat.Classify(result)
	w.bookBeat(st, outcome, result, "qwatch")
	if w.QWatchStats != nil { // 票04：/stats 计数与累计实收
		w.QWatchStats.RecordBeat(outcome, result.CostActual)
	}
	action := w.breaker.Record(outcome)
	if action == "demote" && w.qwatchMode() == "enforce" {
		w.setQWatchMode("observe") // 安全降级；人工复核后拨回（护栏方法，config 写点收敛）
		w.qwatchAlert(st, "问询守望熔断降级",
			fmt.Sprintf("连续 %d 跳 MISS，mode 已自动 enforce→observe（人工复核 observe 数据后拨回）",
				beat.MissLimit))
	} else if action == "pause" {
		w.Ledger.Mu().Lock()
		st.QWatchPlan = nil // 暂停当前窗口剩余跳（窗口本身不关）
		w.Ledger.Mu().Unlock()
		w.qwatchAlert(st, "问询守望错误熔断",
			fmt.Sprintf("连续 %d 跳 ERROR，已暂停当前窗口剩余心跳", beat.ErrorLimit))
	}
}

// bookBeat 逐跳入既有费用账本 beat 科目（决策 7，对齐既有科目不另起炉灶）：
// 时间/会话/token/费用/三态＋泳道标记（lane=qwatch|wait，票04 双泳道可区分）；
// observe 演练跳标 observe。只记元数据与金额——隐私铁律。记账永不弄断调度。
func (w *Watcher) bookBeat(st *ledger.SessionState, outcome string, result beat.BeatResult, lane string) {
	if w.Accounts == nil {
		return
	}
	w.Ledger.Mu().Lock()
	peak := st.PeakCtx // 可变字段：锁内快照（A 修复同款）
	w.Ledger.Mu().Unlock()
	w.bookQwatch("beat", st, accounts.Fields{
		"lane":     lane,
		"provider": result.Provider, "model": result.Model, "price_ver": nil,
		"prefix_tokens": peak, "cache_read": result.CacheReadTokens,
		"cost_pred": result.CostPred, "cost_actual": result.CostActual,
		"outcome": outcome,
	})
}

// qwatchAlert 告警（仓库既有惯例）：控制台 + 双通道通知异步线程（notify_alert，
// enabled=False 时静默）。任何故障只吞——通知是尽力而为的旁路。
// 票08：推送标题走 BuildTitle 降级链（项目名＋台账会话标题，不再含裸
// session id）；事件名移正文开头，sid 移正文尾部小字（notify.AlertCopy 单源）。
func (w *Watcher) qwatchAlert(st *ledger.SessionState, evTitle, msg string) {
	fmt.Printf("[qwatch] ⚠ %s: %s\n", evTitle, msg)
	title, body := w.alertCopy(st, evTitle, msg)
	go func() {
		defer func() { _ = recover() }() // 旁路故障绝不影响调度
		notify.NotifyAlert(title, body, w.Cfg)
	}()
}

// alertCopy 台账快照 → notify.AlertCopy（票08 接线）。Title/Cwd 是可变字段：
// ledgerMu 下快照（共享引用纪律）。st 为 nil 防御退裸标题（无 sid 小字）。
func (w *Watcher) alertCopy(st *ledger.SessionState, evTitle, msg string) (string, string) {
	if st == nil {
		return "Ferryman", evTitle + "：" + msg
	}
	w.Ledger.Mu().Lock()
	cwd, stTitle := st.Cwd, st.Title
	w.Ledger.Mu().Unlock()
	return notify.AlertCopy(cwd, stTitle, evTitle, msg, st.SessionID)
}

// ---- 票04：等待窗泳道（双泳道第二道；风格对齐 maybeFireBeats/fireOneBeat） ----

// waitLaneRec 等待窗泳道的每窗状态（F9 按窗计熔断的载体）。守卫锁＝
// Daemon.windowsMu（与窗口表同域；见 Watcher.waitLane 注释）。
type waitLaneRec struct {
	windowTS    float64 // 窗口 OpenedTS——识别重开（变更＝新窗：旧泳道收尾、状态重置）
	anchorWrite float64 // 建泳道时的 last_write——窗口收尾"主会话未回归"判据
	fired       int     // 本窗已跳数（第 k 跳在闲置 k·τ 到点）
	errStreak   int     // 连续 transport-ERROR 计（HIT/MISS 清零——Breaker 同语义）
	stopped     bool    // F9 停本窗剩余跳（1 MISS / 3 连 ERROR；新窗另起）
	costActual  float64 // 本窗累计实收（收尾行汇总用）
}

// waitEffectiveMode 等待窗侧生效 mode：enforce＋渡口关 → observe（NewWatcher
// 判定一次并告警）；off（含裸构造零值容错）与 observe 原样。
func (w *Watcher) waitEffectiveMode() string {
	if w.waitEnforceDowngraded {
		return "observe"
	}
	return w.Cfg.WaitWindow.Mode
}

// maybeWaitBeats 等待窗泳道调度入口（守望轮询循环内，pollCC 在问询跳之后
// 调用——两泳道共用单在途，先后即串行）。判据（F8）：排跳 ⇔ 等待窗开着
// （windows.go 现有窗口状态，含停车未过期窗＝异步子代理仍在跑；停车满 1h
// 懒过期窗口已闭→不排；纯工具等待无子代理→无窗→自然不覆盖）且主会话闲置
// 满 τ 且前缀 ≥ 计算器 MinPrefixTokens。主会话恢复写入→窗口已闭（NoteUsage/
// NoteGatePrompt 既有道）→不排。间隔与等待上限全部从策略计算器取——本函数
// 只消费 τ/cap/min_prefix，绝不出现第二份公式（公式单源红线）。一切异常吞掉
// ——绝不影响守望与摆渡主路径。
func (w *Watcher) maybeWaitBeats(st *ledger.SessionState) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("[wait] 等待窗泳道调度异常（忽略继续）: %v\n", r)
		}
	}()
	if w.Daemon == nil || st.Agent != "cc" {
		return // 泳道依赖窗口表（Daemon 接线）；心跳前缀源＝渡口 CC 流量，仅 cc
	}
	mode := w.waitEffectiveMode()
	if mode != "observe" && mode != "enforce" {
		return // off 零开销（含零值容错：非 observe/enforce 一律按关）
	}
	d := w.Daemon
	w.Ledger.Mu().Lock()
	lastWrite := st.LastWrite
	w.Ledger.Mu().Unlock()
	key := winKey{st.Agent, st.SessionID}
	d.windowsMu.Lock()
	openedTS, open := d.waitWindowOpenLocked(st.Agent, st.SessionID)
	if !open {
		if rec := w.waitLane[key]; rec != nil {
			delete(w.waitLane, key)
			w.settleWaitLaneLocked(st, rec, lastWrite, "window_closed")
		}
		d.windowsMu.Unlock()
		return
	}
	rec := w.waitLane[key]
	if rec == nil || rec.windowTS != openedTS {
		if rec != nil {
			// 同轮间隙关+开（收尾没见到闭态）：旧泳道如实收尾再另起新泳道
			delete(w.waitLane, key)
			w.settleWaitLaneLocked(st, rec, lastWrite, "reopened")
		}
		w.waitLane[key] = &waitLaneRec{windowTS: openedTS, anchorWrite: lastWrite}
		d.windowsMu.Unlock()
		return // 本轮只登记：开窗瞬间的派发请求本身已刷缓存（首跳在闲置满 τ）
	}
	stopped, fired := rec.stopped, rec.fired
	d.windowsMu.Unlock()
	if stopped {
		return // F9 停本窗：剩余跳不排（窗口照常开，重开新窗另起）
	}
	// 锁外：懒富化（读盘）＋策略现算——绝不持 windowsMu 读盘/推导。
	w.enrich(st)
	w.Ledger.Mu().Lock()
	peak := st.PeakCtx
	w.Ledger.Mu().Unlock()
	pol, perr := w.waitPolicyFn(peak)
	if perr != nil {
		// 无策略（TTL 未实测/无 p_cache，Q16 宁可不跳不造数）：告警一次，
		// 本进程内等待窗泳道零排跳（窗口/Pin 照常——只缺节律）。
		if w.waitPolicyWarned.CompareAndSwap(false, true) {
			w.qwatchAlert(st, "等待窗心跳无策略",
				fmt.Sprintf("策略计算器不可用（%v）——等待窗泳道本进程内不排跳；配置 [heartbeat] ttl_s 与 [prices.*] 后重启生效", perr))
		}
		return
	}
	if peak < pol.MinPrefixTokens {
		return // 前缀不足（计算器输出）：保温无经济性，不排
	}
	// 节律只取计算器输出：第 k 跳在闲置 k·τ 到点（对应 policy.StrategyCosts
	// 的 ⌈wait/τ⌉ 口径），等待上限 cap 只向下夹紧（manual_wait_cap_s）。
	k := float64(fired + 1)
	dueIdle := k * pol.TauS
	capS := pol.WorthwhileCapS
	if manual := w.Cfg.WaitWindow.ManualWaitCapS; manual > 0 && manual < capS {
		capS = manual // 手动只能往下收（policy_viewer.Derive 同口径）
	}
	if dueIdle > capS {
		return // 超上限（expire 档）：剩余等待不保温
	}
	if clock.Now()-lastWrite < dueIdle {
		return // 未到点（主会话闲置未满 τ·k）
	}
	w.fireWaitBeat(st, rec, mode, lastWrite+dueIdle)
}

// fireWaitBeat 单跳：验窗-占用-登记同一临界区（windowsMu——等待窗的窗态在
// 窗口表而非台账，与 fireOneBeat 的台账临界区同型对位）→ 锁外发送 → 结账。
// 在途占用与问询守望共用全局 beatInFlight（多窗排队，绝不并行）。
func (w *Watcher) fireWaitBeat(st *ledger.SessionState, rec *waitLaneRec,
	mode string, beatTS float64) {
	d := w.Daemon
	d.windowsMu.Lock()
	openedTS, open := d.waitWindowOpenLocked(st.Agent, st.SessionID)
	if !open || openedTS != rec.windowTS || rec.stopped {
		d.windowsMu.Unlock()
		return // 窗已关/已换新窗/本窗已停：本跳取消
	}
	if w.beatInFlight.Load() {
		d.windowsMu.Unlock()
		return // 全局同时最多 1 跳在途（跨泳道串行；下轮再试）
	}
	rec.fired++
	idx := rec.fired
	windowTS := rec.windowTS
	w.beatInFlight.Store(true) // 占用与验窗同临界区（fireOneBeat 同型）
	d.windowsMu.Unlock()
	defer w.beatInFlight.Store(false)                         // fireOneBeat 同款（atomic 防 -race）
	result := w.sendWaitBeat(st, mode, windowTS, idx, beatTS) // 网络绝不持锁
	w.settleWaitBeat(st, rec, result)
}

// sendWaitBeat 选发送器（问询 sendBeat 同型）：observe/降级 → NoopSender
// （零网络）；enforce → 注入的真实 sender；未注入则告警一次并按 observe 演练。
func (w *Watcher) sendWaitBeat(st *ledger.SessionState, mode string,
	openedTS float64, beatIdx int, beatTS float64) beat.BeatResult {
	w.Ledger.Mu().Lock()
	plan := beat.BeatPlan{
		Agent: st.Agent, SessionID: st.SessionID,
		TranscriptPath: st.TranscriptPath,
		OpenedTS:       openedTS, // 等待窗 OpenedTS（非台账 QWatchOpenedTS）
		LastWrite:      st.LastWrite,
		Size:           st.Size,
		BeatIndex:      beatIdx,
		BeatTS:         beatTS,
	}
	w.Ledger.Mu().Unlock()
	if mode == "enforce" && w.BeatSender != nil {
		var r beat.BeatResult
		func() { // 发送器炸掉按一跳 ERROR 记（sendBeat 同口径）
			defer func() {
				if p := recover(); p != nil {
					r = beat.BeatResult{Sent: true, OK: false,
						Err: fmt.Sprintf("sender-raise:%T", p)}
				}
			}()
			r = w.BeatSender.Send(plan)
		}()
		return r
	}
	if mode == "enforce" && !w.waitDrillWarned.Swap(true) { // 只告警一次
		fmt.Printf("[wait] ⚠ mode=enforce 但未注入真实 BeatSender（渡口未起）" +
			"——等待窗心跳按 observe 演练记账\n")
	}
	return w.noopSender.Send(plan)
}

// settleWaitBeat 结账：逐跳入账（lane=wait）→ 窗口级熔断（F9 按窗计，与问询
// 守望的全局 Breaker 互不相干）：1 MISS→停本窗剩余跳＋告警（建议复测 TTL，
// 不自改配置）；连续 3 transport-ERROR→停本窗。记账在 windowsMu 下、
// ledgerMu 之外（recordWindowLocked 同款合规；bookBeat 内部短暂取台账锁为
// windowsMu→ledgerMu 正序）。
func (w *Watcher) settleWaitBeat(st *ledger.SessionState,
	rec *waitLaneRec, result beat.BeatResult) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("[wait] 等待窗心跳结账异常（忽略）: %v\n", r)
		}
	}()
	w.noteUpstreamRequest(st.SessionID, result) // 票02:真发重放计入判热时钟(F3)
	outcome := beat.Classify(result)
	w.bookBeat(st, outcome, result, "wait")
	alertTitle, alertMsg := "", ""
	w.Daemon.windowsMu.Lock()
	rec.costActual += result.CostActual
	switch outcome {
	case beat.OutMiss:
		rec.stopped = true // F9：1 MISS 即停本窗剩余跳
		alertTitle = "等待窗心跳熔断"
		// 票08：裸 sid 移正文尾部小字（qwatchAlert→AlertCopy 统一追加）。
		alertMsg = "1 跳 MISS：停本窗剩余跳——建议复测 TTL" +
			"（experiments/cache-ttl 套件），配置不自改"
	case beat.OutError:
		rec.errStreak++
		if rec.errStreak >= beat.ErrorLimit {
			rec.stopped = true
			alertTitle = "等待窗心跳错误熔断"
			alertMsg = fmt.Sprintf("连续 %d 跳 transport-ERROR：停本窗剩余跳",
				beat.ErrorLimit)
		}
	default:
		rec.errStreak = 0 // hit/observe：能拿到结论即清 ERROR 连击（Breaker 同语义）
	}
	w.Daemon.windowsMu.Unlock()
	if alertTitle != "" {
		w.qwatchAlert(st, alertTitle, alertMsg)
	}
}

// settleWaitLaneLocked 等待窗泳道收尾：结算一行 wait_close（泳道汇总；主会话
// 未回归且已跳＝无效保温，标 useless_warm——spec「无效保温单列入账」）。
// 须持 windowsMu 调用；记账（accounts I/O）在 windowsMu 下、ledgerMu 之外
// ——recordWindowLocked 同款合规。
func (w *Watcher) settleWaitLaneLocked(st *ledger.SessionState, rec *waitLaneRec,
	lastWrite float64, reason string) {
	if rec.fired <= 0 {
		return // 未跳过（策略缺失/前缀不足/未到点）：窗口流水已有 window 科目，不另记
	}
	// 主会话回归判据：last_write 越过建泳道基线（恢复写入经 Touch 入账）。
	mainResumed := lastWrite > rec.anchorWrite
	now := clock.Now()
	ff := accounts.Fields{
		"lane":         "wait",
		"opened_ts":    mathx.Round(rec.windowTS, 3),
		"closed_ts":    mathx.Round(now, 3),
		"dur_s":        mathx.Round(math.Max(0.0, now-rec.windowTS), 1),
		"beats_fired":  rec.fired,
		"cost_actual":  mathx.Round(rec.costActual, 6),
		"main_resumed": mainResumed,
		"useless_warm": !mainResumed, // 无效保温：跳了、主会话没回来（恒写——白名单必填语义）
		"close_reason": reason,
	}
	w.bookQwatch("wait_close", st, ff)
}

// defaultWaitPolicy 生产策略缝实现：只调 policy.Compute（公式单源红线）。
// 输入：前缀＝peak_ctx（懒富化）、TTL＝[heartbeat].ttl_s（实测值）、价格本＝
// [prices.*]（NewWatcher 一次读盘）按 FerryProvider 选——report.bookFor 同
// 口径：key 命中→取之，否则仅一本→取唯一本，再否则不可算（Q16 不造数）；
// 版本＝At(now)，早于一切版本回落末版（report 同款）。
func (w *Watcher) defaultWaitPolicy(prefixTokens int) (policy.HeartbeatPolicy, error) {
	ttl := w.Cfg.Heartbeat.TTLS
	if ttl <= 0 {
		return policy.HeartbeatPolicy{}, policy.ErrTTLUnset
	}
	var book *prices.PriceBook
	if b, ok := w.waitBooks[w.Cfg.FerryProvider]; ok {
		book = &b
	} else if len(w.waitBooks) == 1 {
		for _, b := range w.waitBooks {
			book = &b
		}
	}
	if book == nil || len(book.Versions) == 0 {
		return policy.HeartbeatPolicy{}, fmt.Errorf(
			"无可用品价格表（[prices.*]，provider=%q）", w.Cfg.FerryProvider)
	}
	pv := book.At(clock.Now())
	if pv == nil {
		pv = &book.Versions[len(book.Versions)-1]
	}
	return policy.Compute(*book, *pv, ttl, prefixTokens, policy.DefaultBeatOutTokens,
		policy.DefaultSafety, policy.DefaultGraceS, policy.DefaultMinPrefixTokens)
}

// reconcilePins 两泳道快照 Pin 对账（F4 不变式的落地）：等待窗（泳道开启时）
// 或问询窗开着 → Pin(sessionID)；两窗皆闭且最后一跳已结算（对账点在
// maybeFireBeats/maybeWaitBeats 之后，fire 同步结算）→ Unpin。每轮幂等；
// 渡口关（句柄 nil）时 dockPin/dockUnpin 安全跳过。只处理 cc（快照按 CC
// 会话归档）。锁序：windowsMu 探测与 ledgerMu 读各自独立短暂获取，绝不嵌套。
func (w *Watcher) reconcilePins(st *ledger.SessionState) {
	if w.Daemon == nil || st.Agent != "cc" {
		return
	}
	key := winKey{st.Agent, st.SessionID}
	w.Daemon.windowsMu.Lock()
	_, waitOpen := w.Daemon.waitWindowOpenLocked(st.Agent, st.SessionID)
	w.Daemon.windowsMu.Unlock()
	if waitOpen && w.Cfg.WaitWindow.Mode == "off" {
		waitOpen = false // 泳道关＝无心跳读者，不占快照（mode 三元里 off 才免钉）
	}
	w.Ledger.Mu().Lock()
	qwatchOpen := st.QWatchOpenedTS != nil
	w.Ledger.Mu().Unlock()
	shouldPin := waitOpen || qwatchOpen
	w.stampMu.Lock()
	pinned := w.lanePins[key]
	if shouldPin == pinned {
		w.stampMu.Unlock()
		return
	}
	if shouldPin {
		w.lanePins[key] = true
	} else {
		delete(w.lanePins, key)
	}
	w.stampMu.Unlock()
	if shouldPin {
		w.dockPin(st.SessionID)
	} else {
		w.dockUnpin(st.SessionID)
	}
}

// ---- 票02:同模型判热门(ADR-0015 决定一/决定二;F1 冷分支静默交还、
// F3 判热时钟口径、F6 跳过原因编码) ----

// maybeSameModel 同模型触发点:台账闲置达生效阈值(票01 CeilingFor 单源,冷启动
// 种子 20min)先判热,再决定是否进同模型档。判冷/上游不在白名单/上游未启用
// 三种情况:该次触发零模型调用、静默交还既有调度——第三方/骨架仍走总结阈值
// (25 分钟档一行不改),遥测记"同模型跳过"事件,原因独立编码(F6)。
//
// 两个时钟各司其职(ADR-0015 决定一):触发时钟=台账闲置(与总结阈值同基,
// 闸门语义完全不动);判热时钟=ReqClock(距该会话最后一次上游请求,含体外
// 心跳重放)。一切异常吞掉——绝不影响守望与摆渡主路径。
func (w *Watcher) maybeSameModel(st *ledger.SessionState) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("[samemodel] 判热门异常（忽略继续）: %v\n", r)
		}
	}()
	if w.Cfg == nil || !w.Cfg.SameModel.Enabled || st.Agent != "cc" {
		return // off 零开销(默认);同模型仅 CC 轨(ADR-0015 决定一)
	}
	key := winKey{st.Agent, st.SessionID}
	w.Ledger.Mu().Lock()
	observed := st.ObservedActive
	lastWrite := st.LastWrite
	handedOff := st.HandedOffAt
	opened := st.QWatchOpenedTS != nil
	w.Ledger.Mu().Unlock()
	if !observed {
		return // 与摆渡同纪律:启动后只见登记不动作
	}
	if w.stampGet(&w.smSeen, key) == lastWrite {
		return // 该写入版本已判定过(每版本一次,防跳过事件逐轮刷屏)
	}
	now := clock.Now()
	upstream := w.activeUpstreamName()
	effS := w.Cfg.SameModel.CeilingFor(upstream) * 60 // 生效阈值(分钟→秒);无覆盖=全局种子
	if now-lastWrite < effS {
		return // 未到触发点(不盖版本章——闲置继续增长后仍要判)
	}
	// 瞬态让路(不盖版本章):等答复窗/子代理在飞/等待窗(含停车)期间,时机归
	// 各窗口机制管;同模型的提前只服务"普通闲置"的会话。
	if opened {
		return
	}
	if w.Ledger.SubagentActive(st.Agent, st.SessionID) {
		return
	}
	if w.Daemon != nil && w.Daemon.WindowWait(st.Agent, st.SessionID) {
		return
	}
	if handedOff >= lastWrite {
		w.stampSet(&w.smSeen, key, lastWrite)
		return // 既有调度已接管(交接覆盖最新活动):此项无谓,静默盖章
	}
	// 门序:白名单 → 启用 → 判热(前两道纯配置判定,过了才有资格花判热成本;
	// 首个拦下的门即上报原因,防误统计)。
	reason := w.sameModelSkipReason(st.SessionID, upstream, now)
	w.stampSet(&w.smSeen, key, lastWrite)
	if reason == "" {
		// 判热+白名单+已启用 → 进同模型档。执行体(追加重放)是票03 竖切;
		// 本票到此为止:零模型调用,静默交还既有调度(与冷分支同款静默)。
		return
	}
	w.emitSameModelSkip(st, reason, upstream, now, now-lastWrite)
}

// activeUpstreamName 渡口活动上游条目键(白名单按 [dock.upstreams] 键登记)。
// 渡口关/旧单值形态(无条目键)返回 ""——空名不入白名单=whitelist_miss,如实。
func (w *Watcher) activeUpstreamName() string {
	if w.Cfg.Dock == nil {
		return ""
	}
	name, _ := w.Cfg.Dock.ActiveUpstream()
	return name
}

// sameModelEnabled 该上游是否已过追加重放实跳臂硬门槛(D6):缝未装配(nil)
// 或无结论 → 未启用,如实(白名单预置 ≠ 启用;票04 接线真源)。
func (w *Watcher) sameModelEnabled(upstream string) bool {
	if w.ArmVerdict == nil {
		return false
	}
	has, enabled := w.ArmVerdict(upstream)
	return has && enabled
}

// heatClockSeconds 判热时钟读数(秒;now − 最后上游请求)。无观测返回 -1。
func (w *Watcher) heatClockSeconds(sid string, now float64) float64 {
	if w.ReqClock == nil {
		return -1
	}
	last, ok := w.ReqClock.Last(sid)
	if !ok {
		return -1
	}
	return now - last
}

// sameModelHot 判热:判热时钟 + 该上游 TTL 观测闭式预判(ferry.PredictHot,
// 公式单源)。无最后请求观测(如重启后)→ 保守判冷。
func (w *Watcher) sameModelHot(sid string, now float64) bool {
	clockS := w.heatClockSeconds(sid, now)
	if clockS < 0 {
		return false // 无观测 → 保守判冷(F5 同款,绝不伪造热)
	}
	return ferry.PredictHot(clockS, ferry.TTLObs{TTLS: w.Cfg.Heartbeat.TTLS})
}

// sameModelSkipReason 门序判定,返回跳过原因;"" = 判热进档。
func (w *Watcher) sameModelSkipReason(sid, upstream string, now float64) string {
	if !slices.Contains(w.Cfg.SameModel.Upstreams, upstream) {
		return ferry.SameModelSkipWhitelistMiss
	}
	if !w.sameModelEnabled(upstream) {
		return ferry.SameModelSkipNotEnabled
	}
	if !w.sameModelHot(sid, now) {
		return ferry.SameModelSkipCold
	}
	return ""
}

// emitSameModelSkip "同模型跳过"遥测事件(usage/qwatch 同层;不入 handoff 科目
// ——决定六)。只记元数据(原因/上游/闲置与判热两钟读数/TTL/生效阈值),
// 永不落消息正文(隐私铁律);clock_s=-1 = 无最后请求观测(保守判冷形态)。
// 记账永不弄断守望(bookQwatch 同款)。
func (w *Watcher) emitSameModelSkip(st *ledger.SessionState, reason, upstream string, now, idleS float64) {
	ff := accounts.Fields{
		"reason":        reason,
		"upstream":      upstream,
		"idle_s":        mathx.Round(idleS, 1),
		"clock_s":       mathx.Round(w.heatClockSeconds(st.SessionID, now), 1),
		"ttl_s":         mathx.Round(w.Cfg.Heartbeat.TTLS, 1),
		"threshold_min": mathx.Round(w.Cfg.SameModel.CeilingFor(upstream), 3),
	}
	if w.bookSameModelSkipFn != nil {
		w.bookSameModelSkipFn(st, reason, ff)
		return
	}
	w.bookQwatch("same_model_skip", st, ff)
}

// noteUpstreamRequest 票02:一次真发拿到结论的上游请求观测入判热时钟(F3 数据
// 源②:心跳重放)。Sent 且 OK 才计——miss 亦计(上游已处理全前缀,缓存重建);
// transport-ERROR 缓存状态未知不计;observe 演练(Sent=false)未真发不计。
// 时刻取结算点现在(发送已在秒级前完成,对 20min 量级的判热时钟误差可忽略)。
func (w *Watcher) noteUpstreamRequest(sid string, result beat.BeatResult) {
	if w.ReqClock == nil || !result.Sent || !result.OK {
		return
	}
	w.ReqClock.Note(sid, clock.Now())
}

// ---- 懒富化与用量采集（daemon.py:441-490 逐字） ----

// enrichImpl 标题/峰值上下文懒提取；同一 last_write 版本只做一次。
func (w *Watcher) enrichImpl(st *ledger.SessionState) {
	w.Ledger.Mu().Lock()
	enriched := st.EnrichedWrite
	lastWrite := st.LastWrite
	agent := st.Agent
	path := st.TranscriptPath
	cwdEmpty := st.Cwd == ""
	w.Ledger.Mu().Unlock()
	if enriched == lastWrite {
		return
	}
	if agent == "cc" {
		facts, _, _ := extractFacts(path)
		w.Ledger.Mu().Lock()
		if facts.Title != "" {
			st.Title = facts.Title
		}
		if st.Cwd == "" { // Python if not st.cwd: st.cwd = facts.cwd or ""
			st.Cwd = facts.Cwd
		}
		st.PeakCtx = facts.PeakCtx
		st.EnrichedWrite = st.LastWrite // 版本章取写时刻的 last_write
		w.Ledger.Mu().Unlock()
	} else {
		turns := codextrans.TokenCountTurns(path)
		peak := 0
		for _, t := range turns {
			if t.InputTokens > peak { // Python max(..., default=0)
				peak = t.InputTokens
			}
		}
		cwd := ""
		if cwdEmpty { // session_meta 首行的 cwd（T23：gate/归还匹配必需）
			cwd = codextrans.SessionCwd(path)
		}
		w.Ledger.Mu().Lock()
		st.PeakCtx = peak
		if st.Cwd == "" {
			st.Cwd = cwd
		}
		st.EnrichedWrite = st.LastWrite
		w.Ledger.Mu().Unlock()
	}
}

// harvestUsage T42 用量采集：账本故障不得弄断守望（故障隔离不变量，模式同
// _book_handoff）。harvest=nil（旧测试形态）视同不采集。
func (w *Watcher) harvestUsage(path string, size int64, st *ledger.SessionState) {
	if w.harvest == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("[harvest] 用量采集失败（忽略继续）: %s: %v\n", filepath.Base(path), r)
		}
	}()
	rows := w.harvest.MaybeHarvest(path, size, "cc")
	if len(rows) == 0 {
		return
	}
	lineage := pathsx.NormPath(path)
	w.Ledger.Mu().Lock()
	sid, cwd, agent := st.SessionID, st.Cwd, st.Agent
	w.Ledger.Mu().Unlock()
	var tsMax float64
	for _, r := range rows {
		ts := -1.0 // Python ts=None → 账本盖章 now；Go Record ts<0 同语义
		if r.TS != nil {
			ts = *r.TS
		}
		project := r.Project // Python r["project"] or st.cwd
		if project == "" {
			project = cwd
		}
		if _, err := w.Accounts.Record("usage", ts, accounts.Fields{
			"agent": "cc", "session_id": sid, "lineage_id": lineage,
			"project": project, "model": r.Model, "title": r.Title,
			"input_tokens": r.InputTokens, "cache_read_tokens": r.CacheReadTokens,
			"cache_creation_tokens": r.CacheCreationTokens,
			"output_tokens":         r.OutputTokens, "offset": int(r.Offset),
			"subagent": "", // 主会话行恒空串（白名单必填语义，票01）
		}); err != nil {
			fmt.Printf("[harvest] 用量采集失败（忽略继续）: %s: %v\n", filepath.Base(path), err)
			return
		}
		if r.TS != nil && *r.TS > tsMax {
			tsMax = *r.TS
		}
	}
	// T48 票03：新 usage 行的最大 ts 喂给停车状态机——主会话恢复调用
	// 的闭窗判据（note_usage 保证不炸；agent 取会话自身，不硬编码 cc）。
	// 坏行（无 timestamp）滤掉不参与 max；default=0 时 ts 越线判据不成立=安全 no-op。
	// 票02:同一 tsMax 入判热时钟(F3 数据源①:主转录 usage 行=真实上游流量的
	// 最后请求时刻;子代理转录不经此处——子代理前缀 ≠ 主会话前缀,不刷主会话
	// 缓存)。台账闲置(闸门语义)不动。
	if tsMax > 0 && w.ReqClock != nil {
		w.ReqClock.Note(sid, tsMax)
	}
	if w.Daemon != nil {
		w.Daemon.NoteUsage(agent, sid, tsMax)
	}
}

// harvestSubagentUsage 票01/ADR-0008 子代理转录 usage 采集：行随父会话入账
// （session_id=行内父 sid、lineage_id=父转录归一键、subagent=文件 stem，四列
// 取自该子代理自己的 assistant 记录）。故障隔离与 harvestUsage 同款——
// harvest=nil（旧测试形态）视同不采集，一切异常吞掉，记账永不弄断守望；
// 绝不喂 NoteUsage/排程/摆渡（子代理完成 ≠ 主会话恢复，T48 语义零变动）。
func (w *Watcher) harvestSubagentUsage(path string, size int64) {
	if w.harvest == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("[harvest] 子代理用量采集失败（忽略继续）: %s: %v\n", filepath.Base(path), r)
		}
	}()
	rows := w.harvest.MaybeHarvestSubagent(path, size, "cc")
	if len(rows) == 0 {
		return
	}
	lineage := w.subagentLineage(path, rows[0].SessionID)
	for _, r := range rows {
		ts := -1.0 // Python ts=None → 账本盖章 now（harvestUsage 同款）
		if r.TS != nil {
			ts = *r.TS
		}
		if _, err := w.Accounts.Record("usage", ts, accounts.Fields{
			"agent": "cc", "session_id": r.SessionID, "lineage_id": lineage,
			"project": r.Project, "model": r.Model, "title": r.Title,
			"input_tokens": r.InputTokens, "cache_read_tokens": r.CacheReadTokens,
			"cache_creation_tokens": r.CacheCreationTokens,
			"output_tokens":         r.OutputTokens, "offset": int(r.Offset),
			"subagent": r.Subagent, // 值=文件 stem（agent-<agentId>，账本行自带恢复键成分）
		}); err != nil {
			fmt.Printf("[harvest] 子代理用量采集失败（忽略继续）: %s: %v\n", filepath.Base(path), err)
			return
		}
	}
}

// subagentLineage 子代理行的族系键＝父转录路径的归一键（rev1·F4/F9）：台账有
// 父会话 → 以台账 TranscriptPath 为准（与路径剥离结果不一致记一行可诊断日志，
// 不阻断入账）；台账无 → 用从子代理文件自身路径剥离构造的结果。
func (w *Watcher) subagentLineage(subPath, parentSid string) string {
	derived := pathsx.NormPath(parentTranscriptPath(subPath, parentSid))
	w.Ledger.Mu().Lock()
	var have string
	if st := w.Ledger.GetLocked("cc", parentSid); st != nil {
		have = pathsx.NormPath(st.TranscriptPath) // 建后不变字段，锁内快照（先例 prevQwatchOpen）
	}
	w.Ledger.Mu().Unlock()
	if have == "" {
		return derived
	}
	if have != derived {
		fmt.Printf("[harvest] 子代理父转录与路径剥离推导不一致（以台账为准）: 父=%s 台账=%s 推导=%s\n",
			runeCap8(parentSid), have, derived)
	}
	return have
}

// parentTranscriptPath 从子代理文件自身路径剥离尾段 /<父sid>/subagents/<stem>
// 得同目录父转录文件（projects/<munged>/<父sid>.jsonl 兄弟文件，实测布局）。
func parentTranscriptPath(subPath, parentSid string) string {
	munged := filepath.Dir(filepath.Dir(filepath.Dir(subPath))) // .../subagents → <父sid> → <munged>
	return filepath.Join(munged, parentSid+".jsonl")
}

// ---- 小工具 ----

// statMTime os.Stat 的 mtime → float64 epoch 秒（time.mktime/st_mtime 同域；
// 与 Touch 存的 LastWrite、两道验②的比对值同源同换算）。
func statMTime(info os.FileInfo) float64 {
	return float64(info.ModTime().UnixNano()) / 1e9
}

// pathStem Python Path.stem：去最后一个后缀。
func pathStem(name string) string {
	ext := filepath.Ext(name)
	if ext == name {
		return name
	}
	return strings.TrimSuffix(name, ext)
}

// hasPathPart Python "subagents" in p.parts。
func hasPathPart(p, want string) bool {
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if part == want {
			return true
		}
	}
	return false
}
