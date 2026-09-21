package daemon

// 健康（规格 ferryman/server.py:640-673 逐字平移，票14）：/stats 全字段名
// 逐字即 API 契约——gate_calls_total / gate_calls_by_agent /
// last_gate_call_s_ago / last_transcript_write_s_ago / subagents_active /
// subagent_events_total / health_alert / health_msg / qwatch（含
// miss_signals 与 mode 活值）。glm_balance 为票07 Go 侧增量字段（余额展示行，
// 按需查询一次；Python 契约字段不动）；version 为票02 Go 侧增量字段（发布与
// 自升级规格 §A：版本可见）。

import (
	"errors"

	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/dock"
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
	// 版本可见（票02，规格 §A）：/stats 顶层 version——面板页脚显示、兼作
	// 升级探活校验；装配未注入（空串）回落 dev，与 `ferryman version` 缺省同位。
	ver := d.Version
	if ver == "" {
		ver = "dev"
	}
	return map[string]any{
		"version":                     ver,
		"gate_calls_total":            total,
		"gate_calls_by_agent":         byAgent,
		"last_gate_call_s_ago":        lastCallAgo,
		"last_transcript_write_s_ago": lastWriteAgo,
		"subagents_active":            d.Ledger.SubagentsActiveCount(),
		"subagent_events_total":       subagentEvents,
		"health_alert":                alert,
		"health_msg":                  msg,
		"qwatch":                      qw,
		"glm_balance":                 d.glmBalance(),
	}
}

// glmBalance 票07：余额展示行值（/stats 面板拉取时按需查询一次，无后台轮询/
// 定时器）。票01 起按 active 条目取值（ActiveUpstream 单源——无表时旧单值
// 兜底包装）。未配置（无条目/无 api_key/无 balance_url——不配不显示）→
// 「未配置」（零 HTTP）；失败 →「余额查询失败：<类别>」（类别文案 dock 侧
// 单源，永不携带真钥）；成功 → 余额数值字面量。
func (d *Daemon) glmBalance() string {
	var up *config.DockUpstream
	if d.Cfg != nil && d.Cfg.Dock != nil {
		_, up = d.Cfg.Dock.ActiveUpstream()
	}
	info, err := dock.FetchBalance(up)
	if errors.Is(err, dock.ErrBalanceNotConfigured) {
		return "未配置"
	}
	if err != nil {
		return "余额查询失败：" + err.Error()
	}
	return info.Balance
}
