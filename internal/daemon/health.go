package daemon

// 健康（规格 ferryman/server.py:640-673 逐字平移，票14）：/stats 全字段名
// 逐字即 API 契约——gate_calls_total / gate_calls_by_agent /
// last_gate_call_s_ago / last_transcript_write_s_ago / subagents_active /
// subagent_events_total / health_alert / health_msg / qwatch（含
// miss_signals 与 mode 活值）。

import (
	"ferryman/internal/clock"
	"ferryman/internal/mathx"
)

// snapshot 健康面的一次性抄表（Python with stats.lock 读 total/last_call、
// 返回处再读 by_agent/subagent_events——Go 并发语义下同一把锁一次抄齐，
// 语义等价且更严）。
func (s *GateStats) snapshot() (total int, lastCall float64, byAgent map[string]int, subagentEvents int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	by := make(map[string]int, len(s.ByAgent))
	for k, v := range s.ByAgent {
		by[k] = v
	}
	return s.Total, s.LastCall, by, s.SubagentEvents
}

// Health /stats 数据源（server.py:640-673 逐字）。mode 经 cfgMu 读活值
// （骑手：票13 评审 Minor C——QWatchStop 写与本读之间有并发护栏）。
func (d *Daemon) Health() map[string]any {
	now := clock.Now()
	total, lastCall, byAgent, subagentEvents := d.Stats.snapshot()
	lastWrite := d.Ledger.LastTranscriptWrite()
	// 启动宽限：计数器刚归零 + 会话可能在无人发 prompt 的情况下持续写文件
	// （自主/后台会话），不足以判定钩子失效；过宽限期才允许告警（防误报，T26）。
	inGrace := now-d.StartedAt < HealthGraceS
	alert := !inGrace && (now-lastWrite < 3600) &&
		(total == 0 || now-lastCall > 3600)
	// T51 票04：问询守望计数器（命中/开窗/跳数/四道 outcome/累计实收花费/
	// 当前 mode）。mode 读配置活值——熔断降级与一键停改的是同一处。
	var qw map[string]any
	if d.QWatchStats != nil {
		qw = d.QWatchStats.Snapshot()
	} else {
		qw = map[string]any{"hits": 0, "windows_opened": 0, "beats_fired": 0,
			"beats_by_outcome": map[string]int{"hit": 0, "miss": 0,
				"error": 0, "observe": 0},
			"cost_actual": 0.0}
	}
	// 票06：漏检关联计数（账本近 24h 行现算，粗粒度 observe 期信号）
	qw["miss_signals"] = d.qwatchMissSignals()
	qw["mode"] = d.GetQWatchMode()
	var lastCallAgo, lastWriteAgo any // Python … if last_call else None
	if lastCall != 0 {
		lastCallAgo = mathx.Round(now-lastCall, 1)
	}
	if lastWrite != 0 {
		lastWriteAgo = mathx.Round(now-lastWrite, 1)
	}
	msg := "ok"
	switch {
	case alert:
		msg = "疑似钩子失效：1h 内有会话写入但 gate 零调用"
	case inGrace:
		msg = "启动宽限中"
	}
	return map[string]any{
		"gate_calls_total":            total,
		"gate_calls_by_agent":         byAgent,
		"last_gate_call_s_ago":        lastCallAgo,
		"last_transcript_write_s_ago": lastWriteAgo,
		"subagents_active":            d.Ledger.SubagentsActiveCount(),
		"subagent_events_total":       subagentEvents,
		"health_alert":                alert,
		"health_msg":                  msg,
		"qwatch":                      qw,
	}
}
