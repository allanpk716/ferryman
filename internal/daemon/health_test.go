package daemon

// 规格：ferryman/server.py:640-673 health 逐字的行为钉子（票14）+ T26 宽限/告警
// 语义（Python test_integration.py 的 daemon 面，HTTP 401 语义归票15）。
// /stats 全字段名逐字即 API 契约——本文件逐一钉死。

import (
	"encoding/json"
	"testing"

	"ferryman/internal/accounts"
	"ferryman/internal/beat"
)

func TestHealthFieldNamesVerbatim(t *testing.T) {
	e := newGateEnv(t)
	h := e.d.Health()
	for _, k := range []string{"gate_calls_total", "gate_calls_by_agent",
		"last_gate_call_s_ago", "last_transcript_write_s_ago", "subagents_active",
		"subagent_events_total", "health_alert", "health_msg", "qwatch"} {
		if _, ok := h[k]; !ok {
			t.Fatalf("/stats 缺字段 %s: %v", k, h)
		}
	}
	q, ok := h["qwatch"].(map[string]any)
	if !ok {
		t.Fatalf("qwatch 应为对象: %v", h["qwatch"])
	}
	for _, k := range []string{"hits", "windows_opened", "beats_fired",
		"beats_by_outcome", "cost_actual", "miss_signals", "mode"} {
		if _, ok := q[k]; !ok {
			t.Fatalf("qwatch 缺字段 %s: %v", k, q)
		}
	}
	bo, ok := q["beats_by_outcome"].(map[string]int)
	if !ok {
		t.Fatalf("beats_by_outcome 应为 map: %v", q["beats_by_outcome"])
	}
	for _, k := range []string{"hit", "miss", "error", "observe"} {
		if _, ok := bo[k]; !ok {
			t.Fatalf("beats_by_outcome 缺桶 %s: %v", k, bo)
		}
	}
}

// TestHealthVersionField 版本可见（票02，规格 §A）：/stats 顶层 version——
// 装配时自 main 注入（ServeContext→serveConfig→Daemon.Version，显式传参不做
// 全局单例）；未装配（直接构造 Daemon 的替身/旧调用）回落 dev——非 release
// 构建的如实呈现。
func TestHealthVersionField(t *testing.T) {
	e := newGateEnv(t)
	if v := e.d.Health()["version"]; v != "dev" {
		t.Fatalf("未装配版本应回落 dev: %v", v)
	}
	e.d.Version = "v0.1.0-3-gabcdef"
	if v := e.d.Health()["version"]; v != "v0.1.0-3-gabcdef" {
		t.Fatalf("注入版本应原样上报: %v", v)
	}
}

func TestHealthZeroPlaceholdersWithoutWiring(t *testing.T) {
	// QWatchStats 未接线 → 全零占位（Python qwatch_stats is None 同支）。
	e := newGateEnv(t)
	q := e.d.Health()["qwatch"].(map[string]any)
	if q["hits"] != 0 || q["windows_opened"] != 0 || q["beats_fired"] != 0 ||
		q["cost_actual"] != 0.0 {
		t.Fatalf("未接线应全零: %v", q)
	}
	if bo := q["beats_by_outcome"].(map[string]int); bo["hit"] != 0 || bo["miss"] != 0 ||
		bo["error"] != 0 || bo["observe"] != 0 {
		t.Fatalf("四桶应全零: %v", bo)
	}
	if q["miss_signals"] != 0 { // Accounts=nil → 0
		t.Fatalf("miss_signals = %v, want 0", q["miss_signals"])
	}
}

func TestHealthQWatchLiveCountersAndMode(t *testing.T) {
	// T51 票04：mode 读配置活值——一键停改的是同一处；计数器接线后报活值。
	e := newGateEnv(t)
	e.d.QWatchStats = beat.NewQWatchStats()
	e.d.QWatchStats.RecordHit()
	e.d.QWatchStats.RecordBeat(beat.OutMiss, 0.5)
	e.d.Cfg.QuestionWatch.Mode = "observe"
	q := e.d.Health()["qwatch"].(map[string]any)
	if q["hits"] != 1 || q["beats_fired"] != 1 {
		t.Fatalf("计数应报活值: %v", q)
	}
	if bo := q["beats_by_outcome"].(map[string]int); bo["miss"] != 1 {
		t.Fatalf("outcome 桶应报活值: %v", bo)
	}
	if q["mode"] != "observe" {
		t.Fatalf("mode 应读活值: %v", q["mode"])
	}
	r := e.d.QWatchStop() // 一键停 → mode off，/stats 下次拉取读到
	if r["mode"] != "off" {
		t.Fatalf("一键停返回 = %v", r)
	}
	if q2 := e.d.Health()["qwatch"].(map[string]any); q2["mode"] != "off" {
		t.Fatalf("一键停后 mode = %v, want off", q2["mode"])
	}
}

func TestHealthGateCounters(t *testing.T) {
	e := newGateEnv(t)
	e.sub(t, "start", "hs1") // 子代理事件与活跃计数
	h := e.d.Health()
	if h["subagents_active"] != 1 {
		t.Fatalf("subagents_active = %v, want 1", h["subagents_active"])
	}
	if h["subagent_events_total"].(int) < 1 {
		t.Fatalf("subagent_events_total = %v", h["subagent_events_total"])
	}
	if h["gate_calls_total"] != 0 {
		t.Fatalf("gate_calls_total = %v, want 0", h["gate_calls_total"])
	}
	if h["last_gate_call_s_ago"] != nil {
		t.Fatalf("零调用时 last_gate_call_s_ago 应为 nil: %v", h["last_gate_call_s_ago"])
	}
	e.d.Gate(gateBody("hs1", "C:/no-such-hs1.jsonl", "C:/proj"))
	h = e.d.Health()
	if h["gate_calls_total"] != 1 {
		t.Fatalf("gate_calls_total = %v, want 1", h["gate_calls_total"])
	}
	byAgent, ok := h["gate_calls_by_agent"].(map[string]int)
	if !ok || byAgent["cc"] != 1 {
		t.Fatalf("gate_calls_by_agent = %v", h["gate_calls_by_agent"])
	}
	if ago, ok := h["last_gate_call_s_ago"].(float64); !ok || ago < 0 {
		t.Fatalf("last_gate_call_s_ago = %v", h["last_gate_call_s_ago"])
	}
	if h["last_transcript_write_s_ago"] != nil {
		// 本用例未登记会话 → 台账 last_write=0 → nil
		t.Fatalf("last_transcript_write_s_ago = %v, want nil", h["last_transcript_write_s_ago"])
	}
	if h["health_alert"] != false {
		t.Fatalf("默认应不告警: %v", h["health_msg"])
	}
	// 新建 daemon 在启动宽限内（started_at=now）→ 宽限文案（Python 同支）。
	if h["health_msg"] != "启动宽限中" {
		t.Fatalf("health_msg = %v, want 启动宽限中", h["health_msg"])
	}
}

func TestHealthAlertAfterGrace(t *testing.T) {
	// Python test_integration.py::test_t15_auth_and_health 的 daemon 面：
	// 场景前提：已过启动宽限期 + 1h 内有写入但 gate 零调用 → 告警；有调用 → 解除。
	e := newGateEnv(t)
	e.d.StartedAt = *e.now - 601
	e.led.TouchFull("cc", "w", "C:/w.jsonl", *e.now, 10, "C:/p", "", 0, 0) // 1h 内有写入
	h := e.d.Health()
	if h["health_alert"] != true {
		t.Fatalf("过宽限+零调用+1h内有写入应告警: %v", h)
	}
	if h["health_msg"] != "疑似钩子失效：1h 内有会话写入但 gate 零调用" {
		t.Fatalf("health_msg = %v", h["health_msg"])
	}
	e.d.Stats.Hit("cc") // 一旦有调用 → 解除
	h2 := e.d.Health()
	if h2["health_alert"] != false {
		t.Fatal("有调用后应解除告警")
	}
	if h2["health_msg"] != "ok" { // 宽限已过 + 无告警 → ok
		t.Fatalf("health_msg = %v, want ok", h2["health_msg"])
	}
}

func TestHealthGracePeriodAfterDaemonRestart(t *testing.T) {
	// Python test_integration.py::test_health_grace_period_after_daemon_restart：
	// 重启后计数器归零而自主会话仍在写——宽限期内不误报（T26）。
	e := newGateEnv(t)
	e.d.Stats.Total = 0
	e.led.TouchFull("cc", "w", "C:/w.jsonl", *e.now, 10, "C:/p", "", 0, 0)
	h := e.d.Health()
	if h["health_alert"] != false { // 启动 <10min：宽限，不误报
		t.Fatalf("宽限期内不得告警: %v", h)
	}
	if h["health_msg"] != "启动宽限中" {
		t.Fatalf("宽限文案 = %v", h["health_msg"])
	}
	e.d.StartedAt = *e.now - 601 // 宽限期已过，同条件才告警
	if (e.d.Health())["health_alert"] != true {
		t.Fatal("宽限期已过应告警")
	}
}

// ---- dsh-host-guard 票03（spec F 主动暴露面）：/stats 增 pollers 段 ----

// TestStatsPollersSegment /stats 应答增 pollers 段：形状逐字段钉死——name /
// last_seen（epoch 秒，与 last_request_ts 同风格）/ age_s / state（online|
// stale|retired|offline）。状态=票02 基线读面（dshPollerStates）的纯计算现值：
// 读面不评估、不落盘、不打跃迁行；首建前盲区＝空数组、键恒在（消费方按
// 「无 poller 数据」注记不误报）。末段钉 wire 形状：JSON 序列化后字段名
// snake_case（/stats 经 writeJSON 出网的样子）。
func TestStatsPollersSegment(t *testing.T) {
	e := newPollerEnv(t)

	// 首建前盲区：键恒在、空数组。
	h := e.d.Health()
	pl, ok := h["pollers"].([]map[string]any)
	if !ok {
		t.Fatalf("pollers 段应恒在且为数组: %T", h["pollers"])
	}
	if len(pl) != 0 {
		t.Fatalf("首建前 pollers 应为空: %v", pl)
	}

	// 心跳后：单条目全字段（online；冻结时钟下 age_s=0）。
	e.d.dshPollerBeat("desktop", false, e.t0)
	h = e.d.Health()
	pl = h["pollers"].([]map[string]any)
	if len(pl) != 1 {
		t.Fatalf("应恰一条 poller: %v", pl)
	}
	p := pl[0]
	if p["name"] != "desktop" || p["state"] != "online" {
		t.Fatalf("name/state 不符: %v", p)
	}
	if p["last_seen"] != int64(e.t0) {
		t.Fatalf("last_seen 应为 epoch 秒 %v, got %v (%T)", int64(e.t0), p["last_seen"], p["last_seen"])
	}
	if p["age_s"] != 0.0 {
		t.Fatalf("age_s 应为 0: %v", p["age_s"])
	}

	// 静默越 90s 窗 → 读面现值 stale（age_s 同步前进）。
	e.advance(91)
	h = e.d.Health()
	p = h["pollers"].([]map[string]any)[0]
	if p["state"] != "stale" {
		t.Fatalf("静默 91s 读面应 stale: %v", p)
	}
	if p["age_s"] != 91.0 {
		t.Fatalf("age_s 应为 91: %v", p["age_s"])
	}

	// wire 形状：JSON 往返后逐字段 snake_case、last_seen 整数 epoch 秒。
	raw, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Pollers []struct {
			Name     string  `json:"name"`
			LastSeen int64   `json:"last_seen"`
			AgeS     float64 `json:"age_s"`
			State    string  `json:"state"`
		} `json:"pollers"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("pollers 段 JSON 往返失败: %v\n%s", err, raw)
	}
	if len(wire.Pollers) != 1 || wire.Pollers[0].Name != "desktop" ||
		wire.Pollers[0].LastSeen != int64(e.t0) || wire.Pollers[0].AgeS != 91 ||
		wire.Pollers[0].State != "stale" {
		t.Fatalf("wire 形状不符: %s", raw)
	}
}

func TestHealthMissSignalsCorrelation(t *testing.T) {
	// 票06 漏检关联：闲置复活（cache_read=0）× 30min 内命中 × 无真跳 → 计 1；
	// 其间有真跳保温（outcome≠observe）→ 不计。
	w := newWenv(t)
	mustRecord := func(kind string, ts float64, f accounts.Fields) {
		t.Helper()
		if _, err := w.acc.Record(kind, ts, f); err != nil {
			t.Fatal(err)
		}
	}
	mustRecord("usage", w.t0-4000, accounts.Fields{
		"agent": "cc", "session_id": "ms1", "lineage_id": "L", "project": "C:/p",
		"model": "m", "title": "", "input_tokens": 100, "cache_read_tokens": 100,
		"cache_creation_tokens": 0, "output_tokens": 1, "offset": 0, "subagent": ""})
	mustRecord("usage", w.t0, accounts.Fields{
		"agent": "cc", "session_id": "ms1", "lineage_id": "L", "project": "C:/p",
		"model": "m", "title": "", "input_tokens": 900, "cache_read_tokens": 0,
		"cache_creation_tokens": 0, "output_tokens": 1, "offset": 0, "subagent": ""})
	mustRecord("qwatch_hit", w.t0-1000, accounts.Fields{
		"agent": "cc", "session_id": "ms1", "lineage_id": "L", "project": "C:/p",
		"unit_count": 1, "marker_lines": "", "qmark_lines": "", "numbered_lines": "",
		"transcript_path": "C:/x.jsonl"})
	if got := w.d.Health()["qwatch"].(map[string]any)["miss_signals"]; got != 1 {
		t.Fatalf("miss_signals = %v, want 1", got)
	}
	mustRecord("beat", w.t0-500, accounts.Fields{ // 真跳保温 → 洗白
		"agent": "cc", "session_id": "ms1", "lineage_id": "L", "project": "C:/p",
		"provider": "p", "model": "m", "price_ver": "v", "prefix_tokens": 1,
		"cache_read": 1, "outcome": "hit", "cost_pred": 0.0, "cost_actual": 0.0,
		"lane": "qwatch"}) // 票04：beat 科目泳道标记为必填
	if got := w.d.Health()["qwatch"].(map[string]any)["miss_signals"]; got != 0 {
		t.Fatalf("真跳后 miss_signals = %v, want 0", got)
	}
}
