// Package daemon 守护核心：闸门状态机/等待窗口与停车/守望/HTTP 面/摆渡工人
// 的编排层（规格 ferryman/server.py + daemon.py 逐字平移）。
//
// 票13 范围：本包 daemon.go（骨架/常量/GateStats/PendingTable）+ windows.go
// （等待窗口状态机：Subagent 事件→开窗/重锚/锁存/闭窗四因、WindowWait、
// ParkingOpen、NoteUsage、NoteGatePrompt、QWatchStop、Acct）。gate/restore/
// health 归票14，httpapi 归票15，watcher 归票16。
//
// 并发模型（spec §Implementation「并发模型」，windows.go 顶部有完整锁序铁律）：
// 双锁同序 windowsMu 外层 → ledgerMu 内层；凡需双资源的开窗临界区两把全拿、
// 同序获取；台账公共方法自带锁、临界区用无锁内方法；SessionState 为共享可变
// 引用，读写均须持锁；禁止同 goroutine 重入加锁。
package daemon

import (
	"fmt"
	"sync"

	"ferryman/internal/accounts"
	"ferryman/internal/beat"
	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/dock"
	"ferryman/internal/ledger"
	"ferryman/internal/store"
)

// 常量（server.py:34-48 逐字；注释一并搬运——这些注释就是不变量文档）。
const (
	// DegradeAfterBlocks DESIGNS §6.10-6：连续兜底拦截 3 次 → 降级。
	DegradeAfterBlocks = 3
	// PendingTTLs pending 24h 存活上界（PENDING_TTL_S）。
	PendingTTLs = 86400.0
	// WarnContextCap 警告 additionalContext 的字符上限。
	WarnContextCap = 2000
	// HealthGraceS daemon 启动宽限：计数器刚归零不足以判钩子失效（防误报，T26）。
	HealthGraceS = 600.0
	// ParkExpireS T48 停车过期上界：停表后 PARK_EXPIRE_S 内无主会话恢复调用
	// 即闭窗记 expired。数值沿用 SUBAGENT_EVENT_LEAK_S（与计数道泄漏界同值，
	// 一致性优先），独立命名留单一改点。运营后果（如实声明）：过期即豁免
	// 失效+摆渡可恢复入队——即使 async 真身仍在跑（>1h 的 async 等待接受
	// 失明；2026-09-18 实测最长等待 34.8min）。
	ParkExpireS = 3600.0
	// AckGraceS T48 ack 宽限：stop 后 ACK_GRACE_S 内的 usage 行视为"派发确认
	// 回合"（ack）而非恢复，不闭窗。2026-09-18 实测 ack 均落在 stop 前（钩子
	// 时序：Stop 晚于 ack 落盘 0.2-3.2min），此宽限是时序反转时的廉价保险；
	// 真 async 若 90s 内完成，其窗口数据本就边际。
	AckGraceS = 90.0
	// QWatchMissScanS 票06 漏检关联扫描窗：24h（覆盖 30min 回看＋复活间隙）。
	QWatchMissScanS = 86400.0
)

// GateStats 闸门/子代理事件计数（/stats 数据源，T26 健康监控）。
type GateStats struct {
	mu sync.Mutex

	Total          int
	ByAgent        map[string]int
	Bypass         int
	Blocks         int
	Warns          int
	LastCall       float64
	SubagentEvents int // T32：SubagentStart/Stop 累计事件数（端到端验证/观察）
}

// Hit 记一次 gate 调用（按 agent 分桶 + 全局最近调用时刻）。
func (s *GateStats) Hit(agent string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Total++
	if s.ByAgent == nil {
		s.ByAgent = map[string]int{}
	}
	s.ByAgent[agent]++
	s.LastCall = clock.Now()
}

// addSubagentEvent T32：/subagent 事件累计（Python with stats.lock 直加的 Go 形）。
func (s *GateStats) addSubagentEvent() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.SubagentEvents++
}

// PendingRec 待交接标记值（Python pending 表值的 Go 形）。
type PendingRec struct {
	SetAt  float64
	Blocks int
}

// PendingTable 待交接标记（内存态；重启丢失可接受——最坏重走一次警告段）。
type PendingTable struct {
	mu sync.Mutex
	t  map[[2]string]PendingRec
}

// Get 取标记；超 PENDING_TTL（24h）视为过期：删除并按不存在返回
// （pending 清除条件之三；get 即懒清理）。
func (p *PendingTable) Get(k [2]string) (PendingRec, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.t == nil {
		return PendingRec{}, false
	}
	rec, ok := p.t[k]
	if ok && clock.Now()-rec.SetAt > PendingTTLs {
		delete(p.t, k)
		return PendingRec{}, false
	}
	return rec, ok
}

// Set 置标记（blocks 归零起步）。
func (p *PendingTable) Set(k [2]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.t == nil {
		p.t = map[[2]string]PendingRec{}
	}
	p.t[k] = PendingRec{SetAt: clock.Now()}
}

// BumpBlocks 取或建标记后 blocks+1，返回累计值（分支 6 连续拦截计数）。
func (p *PendingTable) BumpBlocks(k [2]string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.t == nil {
		p.t = map[[2]string]PendingRec{}
	}
	rec, ok := p.t[k]
	if !ok {
		rec = PendingRec{SetAt: clock.Now()}
	}
	rec.Blocks++
	p.t[k] = rec
	return rec.Blocks
}

// Clear 清除标记（H 就绪 / 新闲置周期 / 降级放行时）。
func (p *PendingTable) Clear(k [2]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.t, k)
}

// Daemon server 与 daemon 编排层共享的状态容器（ledger/store/queue 由 daemon 注入）。
//
// 字段语义（server.py:105-131 注释逐字搬运）：
// Accounts 为 nil = 不记账（旧测试零改动）；QWatchStats 为 nil = 未接线
// （T51 票04 问询守望计数器——health 报全零占位，旧调用零改动）。
type Daemon struct {
	Cfg          *config.Config
	Ledger       *ledger.Ledger
	Store        *store.Store
	EnqueueFerry func(*ledger.SessionState) bool // 台账状态入队摆渡
	Accounts     *accounts.Accounts              // nil = 不记账（旧测试零改动）
	Stats        *GateStats
	Pending      *PendingTable
	QWatchStats  *beat.QWatchStats   // nil = 未接线
	DshQWatchStats *beat.QWatchStats // nil = 未接线（dsh 泳道计数器——与 CC 的
	//                                 QWatchStats 分账：CC /stats 面零变化的原
	//                                 约束不动，dsh 指标在 qw["dsh"] 子块回显；
	//                                 构造后装配赋值，同 Version 先例）
	DockSnap     *dock.SnapshotStore // nil = 渡口未启用（票01 F11：无 [dock] 节零行为；serve 接线注入，DockSnapshot() 只读转交）

	// HeatClock 判热时钟（「热缓存不拦」2026-10-04 用户拍板）：watcher.ReqClock
	// 同一实例，serve 装配赋值（同 DockSnap 先例）；闸门拦窗内先问钟——缓存
	// 在 PredictHot 必活带则放行不拦。nil = 未接线（旧测试直构形态）→ 闸门
	// 判冷走原拦截，零行为差异。
	HeatClock *beat.LastRequestClock

	// cfgMu 骑手（票13 评审 Minor C）：question_watch.mode 运行时活值的并发
	// 护栏——QWatchStop 写（一键停）与 Health/守望读之间的读写串行化。独立小
	// 锁，临界区只有字段读写，不嵌套其他锁（无锁序约束）。
	cfgMu sync.Mutex

	// NotifyBlock 通知 seam（T25 异步道）：gate block 分支起 goroutine 调用；
	// nil 回落 notify.NotifyBlock（gate.go）。测试注入录制替身（Python
	// monkeypatch notify_mod.notify_block 同位）。票08 加 project/sessionTitle
	// 两参（标题降级链接线——seam 加宽，注入方同步更新）。
	NotifyBlock func(handoffPath, agent, sessionID, project, sessionTitle string, cfg *config.Config)

	// windowsMu：C6 外层锁。窗口表被 HTTP 线程（gate/subagent）与守望线程
	// （note_usage/window_wait，票03 接线）双头读写，本锁串行化；"先记后
	// pop"的原子性靠它（并发 close 被串行化，不可能双记）。锁序铁律与
	// "台账锁内只有内存操作"纪律见 windows.go 顶部——铁律注释以此为准。
	windowsMu sync.Mutex
	// windows T41/T48 等待窗口表：(agent, sid) → 窗口。stop_ts 非 nil = 停车
	// 挂起（async 真身仍在跑）；saw_async = 本窗曾异步启动（锁存，防交错派发
	// 丢窗）。内存态，重启丢失可接受（同 PendingTable）——丢窗 = 该次等待不
	// 入账，宁缺毋错。泄漏兜底（R10）：Stop 丢失致旧窗滞留时，下次 start 超
	// 过台账泄漏阈值（SUBAGENT_EVENT_LEAK_S）即重锚新窗（旧停车窗如实闭账，
	// 见 subagent()）；此后若无新 start，滞留窗永不闭、不记——重锚只覆盖
	// "泄漏后又来 start"的路径。
	windows map[winKey]*waitWindow

	// dshFed dsh 事件接管表（票05 跨源去重,策略 a）：已有插件事件流量的会话
	// 集合——pollDsh 文件守望面据此让位（单源化）。DshEvent 标记、NewDaemon
	// 自账本回种（重启恢复）;守望经 w.Daemon 查询。叶子锁自带,nil 安全。
	dshFed *dshEventFed

	// compactMu dsh 压缩指令槽锁（票02，dsh-hot-compaction）：守望线程（入槽，
	// 票03）与 HTTP 线程（poll 读清）双头读写串行化——独立小锁，临界区纯内存，
	// 不嵌套其他锁（无锁序约束，cfgMu 同款）。
	compactMu sync.Mutex
	// dshCompactSlot 压缩指令槽：sid → 指令（票02，spec「架构与契约」）。仅内
	// 存——重启丢槽＝该轮不压缩（触发面下轮重判重入槽，无害，同 PendingTable）。
	dshCompactSlot map[string]*dshCompactCmd

	// Version 版本号（票02，规格 §A）：serveConfig 装配时自 main 经
	// ServeContext 传入（显式传参不做全局单例；直接构造 Daemon 的替身不装 =
	// 空，Health 回落 dev）。同 DockSnap 的"构造后装配赋值"先例。
	Version string

	StartedAt float64
}

// NewDaemon 构造 Daemon（Python FerryDaemon.__init__ 1:1；startedAt<=0 映射
// Python started_at=None → now_s()）。
func NewDaemon(cfg *config.Config, lg *ledger.Ledger, st *store.Store,
	enqueue func(*ledger.SessionState) bool, acc *accounts.Accounts,
	startedAt float64, qs *beat.QWatchStats) *Daemon {
	if startedAt <= 0 {
		startedAt = clock.Now()
	}
	// dsh 事件接管表：自账本回种（事件行 lineage 恒空可辨识）——daemon 重启
	// 后 pollDsh 对接管会话不重放文件尾（票05 跨源去重）。
	dshFed := &dshEventFed{}
	dshFed.seedFromAccounts(acc)
	return &Daemon{
		Cfg:            cfg,
		Ledger:         lg,
		Store:          st,
		EnqueueFerry:   enqueue,
		Accounts:       acc,
		Stats:          &GateStats{ByAgent: map[string]int{}},
		Pending:        &PendingTable{},
		QWatchStats:    qs,
		windows:        map[winKey]*waitWindow{},
		dshFed:         dshFed,
		dshCompactSlot: map[string]*dshCompactCmd{},
		StartedAt:      startedAt,
	}
}

// pyStr Python str() 的宽松收形（JSON 解码值 → 字符串；nil → ""）。
func pyStr(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	default:
		return fmt.Sprintf("%v", x)
	}
}

// pyStrOr Python str(body.get(k) or default)：空串回退默认（agent 缺省 "cc"）。
func pyStrOr(v any, def string) string {
	if s := pyStr(v); s != "" {
		return s
	}
	return def
}

// dockProxyStats 渡口代理面统计抄表（票01 W1 静默门数据面）：/stats 的
// dock_inflight 与 last_request_ts 数据源。经 DockSnap 只读转发（回写侧在
// dock.Server，读侧同源无二次抄表）；DockSnap 为 nil（无 [dock] 配置 / 渡口
// 构造或绑定失败降级）＝0/0——未启用如实报零，不报错。
func (d *Daemon) dockProxyStats() (inflight int, lastTS int64) {
	if d.DockSnap == nil {
		return 0, 0
	}
	return d.DockSnap.ProxyStats()
}
