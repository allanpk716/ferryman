package daemon

// query_beats_test.go — 票03：GET /beats 验收钉子。
//
// 在飞窗三态夹具：活跃等待窗（est subagents_done）、停车等待窗（est
// main_resumed）、问询窗（est write）；过期/泄漏窗不在在飞清单（只读探测
// 口径）。遥测期望值逐条按构造的 beat 科目流水手算钉死（验收：遥测计数与
// 账本 beat 行一致）。

import (
	"encoding/json"
	"strings"
	"testing"

	"ferryman/internal/accounts"
)

// mustBeat 记一条心跳流水（科目字段白名单必填齐全，price_ver=nil 同生产者
// watcher.bookBeat 形）。
func mustBeat(t *testing.T, e *queryEnv, sid string, ts float64, outcome, lane string, cost float64) {
	t.Helper()
	if _, err := e.acc.Record("beat", ts, accounts.Fields{
		"agent": "cc", "session_id": sid, "lineage_id": "L", "project": "C:/p",
		"provider": "p", "model": "m", "price_ver": nil, "prefix_tokens": 1000,
		"cache_read": 500, "outcome": outcome, "cost_pred": 0.0,
		"cost_actual": cost, "lane": lane,
	}); err != nil {
		t.Fatalf("Record beat: %v", err)
	}
}

func TestQueryBeatsInFlightWindowsAndTelemetry(t *testing.T) {
	e := newQueryEnv(t)

	// 在飞窗构造：活跃等待窗＋停车等待窗＋泄漏过期窗（不列）＋停车满 1h 窗
	// （不列）＋问询窗。
	e.d.windowsMu.Lock()
	e.d.windows[winKey{"cc", "b-wait"}] = &waitWindow{OpenedTS: e.t0 - 100}
	pStop := e.t0 - 30
	e.d.windows[winKey{"cc", "b-park"}] = &waitWindow{OpenedTS: e.t0 - 200, StopTS: &pStop}
	stale := e.t0 - 100000
	e.d.windows[winKey{"cc", "b-stale"}] = &waitWindow{OpenedTS: stale}               // 活跃窗泄漏口径过期
	e.d.windows[winKey{"cc", "b-exp"}] = &waitWindow{OpenedTS: stale, StopTS: &stale} // 停车满 1h 懒过期
	e.d.windowsMu.Unlock()

	e.led.TouchFull("cc", "b-qw", `C:\tmp\b-qw.jsonl`, e.t0-5, 10, `C:\proj`, "", 100, 0)
	e.led.Mu().Lock()
	qwOpened := e.t0 - 50
	e.led.GetLocked("cc", "b-qw").QWatchOpenedTS = &qwOpened
	e.led.Mu().Unlock()

	// 心跳流水（期望逐窗手算）：
	//   b-wait（lane=wait，开窗 t0-100）：hit@-90(0.1)＋miss@-80(0.2)＋
	//   observe@-70(0)＋hit@-85(0.05，最深观测 15s) → 4 跳 hit2/miss1/observe1，
	//   花费 0.35，ttl_observed=15；开窗前的 hit@-200 属上一窗不数；
	//   他会话行不串窗。
	mustBeat(t, e, "b-wait", e.t0-90, "hit", "wait", 0.1)
	mustBeat(t, e, "b-wait", e.t0-80, "miss", "wait", 0.2)
	mustBeat(t, e, "b-wait", e.t0-70, "observe", "wait", 0)
	mustBeat(t, e, "b-wait", e.t0-85, "hit", "wait", 0.05)
	mustBeat(t, e, "b-wait", e.t0-200, "hit", "wait", 5.0)
	mustBeat(t, e, "b-other", e.t0-60, "hit", "wait", 9.9)
	//   b-qw（lane=qwatch，开窗 t0-50）：hit@-40(0.05，观测 10s)＋error@-30(0)
	//   → 2 跳 hit1/error1 花费 0.05；lane=wait 的行不属问询窗不数。
	mustBeat(t, e, "b-qw", e.t0-40, "hit", "qwatch", 0.05)
	mustBeat(t, e, "b-qw", e.t0-30, "error", "qwatch", 0)
	mustBeat(t, e, "b-qw", e.t0-20, "hit", "wait", 3.0)

	code, raw := getRaw(t, e.port, "/beats", e.token)
	if code != 200 {
		t.Fatalf("GET /beats = %d %q", code, raw)
	}
	var resp struct {
		Windows []map[string]any `json:"windows"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("响应非 JSON: %v (%q)", err, raw)
	}
	if len(resp.Windows) != 3 {
		t.Fatalf("在飞窗应 3（stale/expired 不列）: %d %v", len(resp.Windows), raw)
	}
	// 稳定序：开窗时刻升序。
	wantOrder := []string{"b-park", "b-wait", "b-qw"}
	for i, w := range resp.Windows {
		if w["session_id"] != wantOrder[i] {
			t.Fatalf("第 %d 窗 = %v, want %s（开窗升序）", i, w["session_id"], wantOrder[i])
		}
	}

	bySid := map[string]map[string]any{}
	for _, w := range resp.Windows {
		bySid[w["session_id"].(string)] = w
		for _, k := range []string{"session_id", "agent", "kind", "opened_ts",
			"parked", "est_close_reason", "telemetry"} {
			if _, ok := w[k]; !ok {
				t.Fatalf("在飞窗字段缺失 %q: %v", k, w)
			}
		}
	}

	// 停车等待窗：停车标志＋预估 main_resumed；未跳 → 遥测零值＋TTL null。
	wp := bySid["b-park"]
	if wp["kind"] != "wait" || wp["parked"] != true || wp["est_close_reason"] != "main_resumed" {
		t.Fatalf("停车窗: %v", wp)
	}
	if wp["opened_ts"] != e.t0-200.0 {
		t.Fatalf("停车窗 opened_ts = %v", wp["opened_ts"])
	}
	tp := wp["telemetry"].(map[string]any)
	if tp["beats_fired"] != 0.0 || tp["cost_actual"] != 0.0 || tp["ttl_observed_s"] != nil {
		t.Fatalf("未跳窗遥测应零值/TTL null: %v", tp)
	}

	// 活跃等待窗：est subagents_done；遥测与流水一致。
	wa := bySid["b-wait"]
	if wa["kind"] != "wait" || wa["parked"] != false || wa["est_close_reason"] != "subagents_done" {
		t.Fatalf("活跃窗: %v", wa)
	}
	ta := wa["telemetry"].(map[string]any)
	if ta["beats_fired"] != 4.0 || ta["hit"] != 2.0 || ta["miss"] != 1.0 ||
		ta["error"] != 0.0 || ta["observe"] != 1.0 {
		t.Fatalf("b-wait 遥测计数: %v", ta)
	}
	if ta["ttl_observed_s"] != 15.0 {
		t.Fatalf("TTL 观测应取最强 hit 深度 15s: %v", ta["ttl_observed_s"])
	}
	if ta["cost_actual"] != 0.35 {
		t.Fatalf("累计实收 = %v, want 0.35", ta["cost_actual"])
	}

	// 问询窗：est write；lane 过滤后遥测 2 跳。
	wq := bySid["b-qw"]
	if wq["kind"] != "qwatch" || wq["parked"] != false || wq["est_close_reason"] != "write" {
		t.Fatalf("问询窗: %v", wq)
	}
	tq := wq["telemetry"].(map[string]any)
	if tq["beats_fired"] != 2.0 || tq["hit"] != 1.0 || tq["error"] != 1.0 ||
		tq["ttl_observed_s"] != 10.0 || tq["cost_actual"] != 0.05 {
		t.Fatalf("b-qw 遥测: %v", tq)
	}

	// 红线反向断言：响应无 token 串。
	if strings.Contains(string(raw), e.token) {
		t.Fatalf("/beats 泄漏 token: %q", raw)
	}
}

func TestQueryBeatsEmptyReturnsEmptyArray(t *testing.T) {
	e := newQueryEnv(t)
	code, raw := getRaw(t, e.port, "/beats", e.token)
	if code != 200 {
		t.Fatalf("GET /beats = %d %q", code, raw)
	}
	if !strings.Contains(string(raw), `"windows":[]`) {
		t.Fatalf("无在飞窗应返回空数组（非 null/非错误）: %q", raw)
	}
}
