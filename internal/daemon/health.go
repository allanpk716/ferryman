package daemon

// 健康（规格 ferryman/server.py:640-673 逐字平移，票14）：/stats 全字段名
// 逐字即 API 契约——gate_calls_total / gate_calls_by_agent /
// last_gate_call_s_ago / last_transcript_write_s_ago / subagents_active /
// subagent_events_total / health_alert / health_msg / qwatch（含
// miss_signals 与 mode 活值）。version 为票02 Go 侧增量字段（发布与
// 自升级规格 §A：版本可见）。（glm_balance 行已于 2026-09-25 下线：编码套餐
// key 不适用 user/balance，恒为失败文案的死面；悬浮窗余额走 quota 端点。）

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
	// 票03（dsh 等答复窗）：dsh 分档活值回显——与 CC mode 同一护栏读法。
	qw["dsh_mode"] = d.GetDshQWatchMode()
	// dsh 观测面票：dsh 泳道计数器子块（与 CC 分账——上方 qw 主体仍是 CC 面，
	// 零变化；hits 对 dsh 恒 0 如实记——dsh 无提问潮，扳机是"最后说话人"
	// 检测态，同 qwatch_open unit_count 的口径）。nil=未接线（health 报全零
	// 占位，与 CC 侧同形）。
	var dshQw map[string]any
	if d.DshQWatchStats != nil {
		dshQw = d.DshQWatchStats.Snapshot()
	} else {
		dshQw = map[string]any{"hits": 0, "windows_opened": 0, "beats_fired": 0,
			"beats_by_outcome": map[string]int{"hit": 0, "miss": 0,
				"error": 0, "observe": 0},
			"cost_actual": 0.0}
	}
	qw["dsh"] = dshQw
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
	// 票01 W1：渡口代理面统计（升级静默门数据面）——只覆盖 15722 真实 CC 流量
	// （心跳自产重放与管理口流量不在内，口径见 dock.Server）；DockSnap nil＝
	// 未启用 0/0。"零请求视为静默成立"由消费方（票02）判定，此处如实暴露。
	dockInflight, dockLastTS := d.dockProxyStats()
	// dsh-host-guard 票03（spec F 主动暴露面）：pollers 段——票02 基线读面单源
	//（dshPollerStates 纯计算现值，读面不评估、不落盘、不打跃迁行）。形状：
	// {name, last_seen(epoch 秒——与 last_request_ts 同风格), age_s, state}；
	// 空表（首建前盲区）＝空数组、键恒在——消费方（doctor 哨兵/看门）按
	// 「无 poller 数据」注记，不误报。
	pollerRows := make([]map[string]any, 0)
	for _, p := range d.dshPollerStates() {
		pollerRows = append(pollerRows, map[string]any{
			"name":      p.Name,
			"last_seen": int64(p.LastSeen),
			"age_s":     mathx.Round(now-p.LastSeen, 1),
			"state":     p.State,
		})
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
		"dock_inflight":               dockInflight,
		"last_request_ts":             dockLastTS,
		"pollers":                     pollerRows,
	}
}
