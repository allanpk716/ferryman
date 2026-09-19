// Package beat 心跳调度的纯类型与纯逻辑（规格 ferryman/beat.py 1:1）。
//
// 调度本体挂在守望（daemon，风格对齐 _maybe_qwatch）；本包只放：
// beat 请求/结果形状与可注入发送接口（真实 HTTP 发送属 Q14 段二/三，
// 未授权前不实现——HttpBeatSender 不落地，仅接口位保留）、三态分类
// （HIT/MISS/ERROR）与熔断计数器。全部无 I/O、无消息内容
// （隐私不变量：字段只有元数据与金额）。
package beat

import (
	"sync"

	"ferryman/internal/mathx"
)

// HitRatioThreshold cache_read 占比 ≥ 此值记 HIT；Q14 段二实测后校准。
const HitRatioThreshold = 0.5

// 三态 + 演练（observe 未真发，单独一态入账）。
const (
	OutHit     = "hit"
	OutMiss    = "miss"
	OutError   = "error"
	OutObserve = "observe"
)

// 熔断限额（包级常量）：连续 2 MISS → 降级 enforce→observe；
// 连续 3 ERROR → 暂停当前窗口剩余跳。
const (
	MissLimit  = 2
	ErrorLimit = 3
)

// BeatPlan 一跳的计划快照（请求形状的字段载体；本包不构造真实请求）。
//
// 只传转录路径不传内容——前缀重构（BeatBuilder）由 Q14 按 jsonl 现读。
type BeatPlan struct {
	Agent          string
	SessionID      string
	TranscriptPath string  // 转录绝对路径（Q14 前缀重构用）
	OpenedTS       float64 // 窗口开启时刻
	LastWrite      float64 // 计划基线（开窗快照 mtime）
	Size           int     // 开窗快照 size
	BeatIndex      int     // 第几跳（1 起）
	BeatTS         float64 // 本跳计划时刻
}

// BeatResult 一跳的结果。Sent=false = 未真发（observe 演练）；OK=false = 重试后仍败。
type BeatResult struct {
	Sent            bool
	OK              bool
	InputTokens     int // 实收 input（未命中前缀部分）
	CacheReadTokens int // 实收缓存读
	OutputTokens    int
	CostPred        float64
	CostActual      float64
	Provider        string
	Model           string
	Err             string // 错误类别摘要（不含消息内容）
}

// Sender 可注入发送接口。
//
// 真实实现方职责（Q14 段二/三，本包不做）：直打本地代理 127.0.0.1:15721、
// 发 CC 别名、重放前缀 [system+tools+u1..uN]（不含末轮 assistant 输出）、
// max_tokens=1 封顶输出、429/5xx/超时指数退避重试 1 次（重试语义归 sender，
// 调度器只看最终 BeatResult）、与摆渡路由零共用。
// 时限要求：Send 必须自持秒级超时＋重试并在秒级内返回（守望单线程
// 串行调用）——一次挂起分钟级的 Send 会阻塞守望循环，拖垮全部会话的
// 开窗与两道验。
type Sender interface {
	Send(plan BeatPlan) BeatResult
}

// NoopSender observe 演练发送器：零网络，恒返回未真发结果（入账标 observe）。
type NoopSender struct{}

// Send 恒返回零值 BeatResult（Sent=false）。
func (NoopSender) Send(BeatPlan) BeatResult { return BeatResult{Sent: false} }

// Classify 三态判定：未真发=observe；重试后仍败=error；
// 成功按 cache_read 占比 ≥ 阈值记 hit，否则 miss（含 ≈0 全 miss——
// 单跳 MISS = 全前缀按全价重付，故部分命中也按 miss 计入熔断连击）。
func Classify(r BeatResult) string {
	if !r.Sent {
		return OutObserve
	}
	if !r.OK {
		return OutError
	}
	total := r.CacheReadTokens + r.InputTokens
	ratio := 0.0
	if total != 0 {
		ratio = float64(r.CacheReadTokens) / float64(total)
	}
	if ratio >= HitRatioThreshold {
		return OutHit
	}
	return OutMiss
}

// Breaker 熔断计数器：连续 2 MISS → 降级 enforce→observe；
// 连续 3 ERROR → 暂停当前窗口剩余跳。
//
//   - ERROR 不计入也不重置 MISS 连击（两枚 MISS 之间夹 ERROR 视作仍连续
//     ——ERROR 期间缓存状态未知，不作洗白）；能拿到成败结论（hit/miss）
//     即代理可达，清 ERROR 连击；HIT 清 MISS 连击；
//   - observe 演练无真实信号，不动任何连击；
//   - Record 返回动作："demote" | "pause" | ""（触发后对应连击清零，
//     下个窗口从头计）。
type Breaker struct {
	MissStreak  int
	ErrorStreak int
}

// Record 记一跳 outcome，返回动作（"demote"/"pause"/""）。连击语义逐字平移。
func (b *Breaker) Record(outcome string) string {
	if outcome == OutObserve {
		return ""
	}
	if outcome == OutError {
		b.ErrorStreak++
		if b.ErrorStreak >= ErrorLimit {
			b.ErrorStreak = 0
			return "pause"
		}
		return ""
	}
	b.ErrorStreak = 0
	if outcome == OutMiss {
		b.MissStreak++
		if b.MissStreak >= MissLimit {
			b.MissStreak = 0
			return "demote"
		}
		return ""
	}
	b.MissStreak = 0
	return ""
}

// QWatchStats 问询守望运行计数器（/stats 数据源）：命中数/开窗数/跳数/
// 四道 outcome 计数/累计实收花费。纯内存计数（账本数据不反推跳数——
// 记归记、算归算）；守望线程写、HTTP 线程读，锁保护；
// 重启清零（与子代理计数同水位，内存态丢失可接受）。
type QWatchStats struct {
	mu             sync.Mutex
	hits           int
	windowsOpened  int
	beatsFired     int
	beatsByOutcome map[string]int
	costActual     float64
}

// NewQWatchStats 新计数器（四道 outcome 桶预置零值）。
func NewQWatchStats() *QWatchStats {
	return &QWatchStats{beatsByOutcome: map[string]int{
		OutHit: 0, OutMiss: 0, OutError: 0, OutObserve: 0,
	}}
}

// RecordHit 记一次提问潮命中。
func (s *QWatchStats) RecordHit() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hits++
}

// RecordWindowOpened 记一次开窗。
func (s *QWatchStats) RecordWindowOpened() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.windowsOpened++
}

// RecordBeat 记一跳（含 observe 演练）及其 outcome 与实收花费；
// 未知 outcome 动态建桶（Python get(outcome, 0) + 1 逐字语义）。
func (s *QWatchStats) RecordBeat(outcome string, costActual float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.beatsFired++
	s.beatsByOutcome[outcome]++
	s.costActual += costActual
}

// Snapshot 计数快照：hits/windows_opened/beats_fired/beats_by_outcome/
// cost_actual（mathx.Round 6 位，与 CPython round half-even 同语义）。
func (s *QWatchStats) Snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	bo := make(map[string]int, len(s.beatsByOutcome))
	for k, v := range s.beatsByOutcome {
		bo[k] = v
	}
	return map[string]any{
		"hits":             s.hits,
		"windows_opened":   s.windowsOpened,
		"beats_fired":      s.beatsFired,
		"beats_by_outcome": bo,
		"cost_actual":      mathx.Round(s.costActual, 6),
	}
}
