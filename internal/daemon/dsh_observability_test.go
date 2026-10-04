package daemon

// dsh_observability_test.go — dsh 观测面票验收钉子：真机验收（2026-10-04）
// 暴露的三处显示层缺口各一测——
// ①/stats qw["dsh"]：dsh 泳道计数器（与 CC 分账，CC 面零变化）＋台账
//   DshQWatch* 镜像字段族（开窗/逐跳/关窗三态）；
// ②/beats：dsh 等答复窗纳入在飞窗清单（镜像字段源，遥测聚合零改动）；
// ③/session：dsh usage_total 按 session_id 聚合（事件行 lineage 恒空是票05
//   去重的承重标记，不可填值——dsh_dedup.go seedFromAccounts）＋qwatch 块
//   读镜像字段（CC 侧仍读 QWatch 字段族）。
//
// 夹具：守望面复用 dsh_qwatch_test.go（newDshQwFixture/writeDshSession/
// dshForceDue）；查询面复用 query_sessions_test.go（newQueryEnv/getRaw）。

import (
	"encoding/json"
	"testing"

	"ferryman/internal/accounts"
	"ferryman/internal/beat"
)

// ---- ①守望面：镜像字段三态＋泳道计数器＋/stats 回显 ----

func TestDshObservabilityMirrorAndLaneStats(t *testing.T) {
	root := t.TempDir()
	w, d, _, _ := newDshQwFixture(t, root, nil)
	stats := beat.NewQWatchStats()
	w.DshQWatchStats = stats
	d.DshQWatchStats = stats // serve 装配同一实例（/stats 与守望共读）
	p := writeDshSession(t, root, dshMainID, dshHeaderFor(dshMainID),
		dshQwUserSide(2, nowMS())+dshQwAsstUsage(4, nowMS()+1000))
	w.pollDsh()
	if w.dshWindows[winKey{"dsh", dshMainID}] == nil {
		t.Fatal("前提：四条件全真应开窗")
	}

	st := w.Ledger.Get("dsh", dshMainID)
	readMirror := func() (openTS *float64, fired, planned int) {
		w.Ledger.Mu().Lock()
		defer w.Ledger.Mu().Unlock()
		return st.DshQWatchOpenedTS, st.DshQWatchBeatsFired, st.DshQWatchPlanned
	}
	// 开窗：镜像在位、计划满排（beatPlan 420s×2）、跳数 0。
	openTS, fired, planned := readMirror()
	if openTS == nil || fired != 0 || planned != 2 {
		t.Fatalf("开窗镜像 = %v %d %d, want 非 nil 0 2", openTS, fired, planned)
	}
	if got := stats.Snapshot()["windows_opened"]; got != 1 {
		t.Fatalf("dsh 泳道 windows_opened = %v, want 1", got)
	}

	// 发一跳（observe 演练）：镜像 fired=1、planned-1；泳道计数入 observe 桶。
	dshForceDue(w, winKey{"dsh", dshMainID}, 1)
	w.pollDsh()
	openTS, fired, planned = readMirror()
	if openTS == nil || fired != 1 || planned != 1 {
		t.Fatalf("发跳后镜像 = %v %d %d, want 非 nil 1 1", openTS, fired, planned)
	}
	snap := stats.Snapshot()
	if snap["beats_fired"] != 1 {
		t.Fatalf("dsh 泳道 beats_fired = %v, want 1", snap["beats_fired"])
	}
	if bo := snap["beats_by_outcome"].(map[string]int); bo["observe"] != 1 {
		t.Fatalf("dsh 泳道 observe 桶 = %v, want 1", bo)
	}

	// 用户侧写入关窗：镜像 openedTS 清、planned 清零、fired 留终值。
	appendDshBatch(t, p, dshQwUserSide(20, nowMS())) // seq 20/21 > 4：用户侧翻转
	w.pollDsh()
	openTS, fired, planned = readMirror()
	if openTS != nil || planned != 0 || fired != 1 {
		t.Fatalf("关窗后镜像 = %v %d %d, want nil 0 1（终值保留）", openTS, fired, planned)
	}

	// /stats 回显：qw["dsh"] 子块随泳道计数器；CC 主体零变化（QWatchStats
	// 未接线＝全零占位——原 /stats 面的"全零误读"形态被 dsh 子块治住）。
	h := d.Health()
	qw := h["qwatch"].(map[string]any)
	dqw := qw["dsh"].(map[string]any)
	if dqw["windows_opened"] != 1 || dqw["beats_fired"] != 1 {
		t.Fatalf("qw.dsh = %v, want windows_opened=1 beats_fired=1", dqw)
	}
	if qw["windows_opened"] != 0 || qw["beats_fired"] != 0 {
		t.Fatalf("CC 主体应零变化: %v", qw)
	}
	if qw["dsh_mode"] != "observe" {
		t.Fatalf("dsh_mode 回显 = %v", qw["dsh_mode"])
	}
}

// ---- ②③查询面：/beats 纳入 dsh 窗；/session 按 session_id 聚合＋镜像 ----

func TestQuerySessionAndBeatsDshObservability(t *testing.T) {
	e := newQueryEnv(t)

	// 未接线形态：qw["dsh"] 全零占位（与 CC 侧同形，nil 守卫）。
	qw := e.d.Health()["qwatch"].(map[string]any)
	dqw := qw["dsh"].(map[string]any)
	if dqw["windows_opened"] != 0 || dqw["beats_fired"] != 0 {
		t.Fatalf("未接线 qw.dsh 应全零: %v", dqw)
	}

	// dsh 会话：台账登记（事件面合成路径）＋镜像开窗态（agent=dsh 读 Dsh 镜像）。
	e.qreg("dsh", "session-xyz1", "dsh-event://session-xyz1", `C:\proj`, 5, 30000)
	e.led.Mu().Lock()
	opened := e.t0 - 50
	e.led.GetLocked("dsh", "session-xyz1").DshQWatchOpenedTS = &opened
	e.led.GetLocked("dsh", "session-xyz1").DshQWatchBeatsFired = 1
	e.led.GetLocked("dsh", "session-xyz1").DshQWatchPlanned = 1
	e.led.Mu().Unlock()

	// 事件面 usage 行（lineage 恒空——票05 承重标记）＋随父入账的子行：
	// /session 的 dsh usage_total 按 session_id 聚合应全覆盖（历史行同口径）。
	mustDshUsage := func(sid, sub string, in, cr int) {
		t.Helper()
		if _, err := e.acc.Record("usage", e.t0-10, accounts.Fields{
			"agent": "dsh", "session_id": sid, "lineage_id": "", "project": "C:/proj",
			"model": "glm-5.3", "title": "t", "input_tokens": in, "cache_read_tokens": cr,
			"cache_creation_tokens": 0, "output_tokens": 1, "offset": 0, "subagent": sub,
		}); err != nil {
			t.Fatalf("Record usage: %v", err)
		}
	}
	mustDshUsage("session-xyz1", "", 100, 900)
	mustDshUsage("session-xyz1", "session-child9", 50, 450)
	// dsh 心跳行（lane=qwatch、agent=dsh；开窗后一跳 observe）。
	if _, err := e.acc.Record("beat", e.t0-40, accounts.Fields{
		"agent": "dsh", "session_id": "session-xyz1", "lineage_id": "", "project": "C:/proj",
		"provider": "p", "model": "m", "price_ver": nil, "prefix_tokens": 30000,
		"cache_read": 0, "outcome": "observe", "cost_pred": 0.0,
		"cost_actual": 0.0, "lane": "qwatch",
	}); err != nil {
		t.Fatalf("Record beat: %v", err)
	}

	// ③/session：usage_total 按 sid 聚合（两行全覆盖）＋qwatch 块读镜像。
	code, raw := getRaw(t, e.port, "/session?id=session-xyz1", e.token)
	if code != 200 {
		t.Fatalf("GET /session = %d %q", code, raw)
	}
	var sess struct {
		UsageTotal map[string]float64 `json:"usage_total"`
		Windows    struct {
			Qwatch map[string]any `json:"qwatch"`
		} `json:"windows"`
	}
	if err := json.Unmarshal(raw, &sess); err != nil {
		t.Fatalf("响应非 JSON: %v (%q)", err, raw)
	}
	if sess.UsageTotal["requests"] != 2 || sess.UsageTotal["input_tokens"] != 150 ||
		sess.UsageTotal["cache_read_tokens"] != 1350 {
		t.Fatalf("usage_total = %v（按 sid 聚合：主行＋子行，lineage 空不丢）", sess.UsageTotal)
	}
	if sess.Windows.Qwatch["open"] != true || sess.Windows.Qwatch["beats_fired"] != float64(1) ||
		sess.Windows.Qwatch["planned_remaining"] != float64(1) {
		t.Fatalf("qwatch 块 = %v（dsh 读 Dsh 镜像字段）", sess.Windows.Qwatch)
	}

	// ②/beats：dsh 等答复窗入在飞清单，遥测聚合与 beat 行一致。
	code, raw = getRaw(t, e.port, "/beats", e.token)
	if code != 200 {
		t.Fatalf("GET /beats = %d %q", code, raw)
	}
	var beats struct {
		Windows []map[string]any `json:"windows"`
	}
	if err := json.Unmarshal(raw, &beats); err != nil {
		t.Fatalf("响应非 JSON: %v (%q)", err, raw)
	}
	found := false
	for _, win := range beats.Windows {
		if win["session_id"] == "session-xyz1" {
			found = true
			if win["agent"] != "dsh" || win["kind"] != "qwatch" || win["est_close_reason"] != "write" {
				t.Fatalf("dsh 窗字段 = %v", win)
			}
			tel := win["telemetry"].(map[string]any)
			if tel["beats_fired"] != float64(1) || tel["observe"] != float64(1) {
				t.Fatalf("dsh 窗遥测 = %v（应为 1 跳 observe）", tel)
			}
		}
	}
	if !found {
		t.Fatalf("dsh 等答复窗不在 /beats 在飞清单: %q", raw)
	}
}
