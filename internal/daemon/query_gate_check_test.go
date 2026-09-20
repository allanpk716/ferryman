package daemon

// query_gate_check_test.go — 票02：GET /gate_check 双模式验收钉子。
//
// 单会话（?session_id=）：未凉 allow / 已凉有交接 block / 已凉无交接 warn
// 三态＋剩余分钟与拦截阈值一致＋pending 只读镜像＋mode 三态＋停车窗豁免；
// 汇总（无 session_id）：排序键三规则（临近度升序、并列 sid 字典序、无交接
// 者在前）＋limit 默认 50；404/401；零状态写入（前后字节不变）；红线反向
// 断言（无消息内容/无凭据）。
//
// 夹具复用票01 的 queryEnv（真 Daemon＋临时端口＋冻结时钟）与 gate_test 的
// isoUTC；ValidHandoff 的 cwd 匹配写法沿用 query_sessions_test.go 先例。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ferryman/internal/config"
	"ferryman/internal/ledger"
)

// gateCheckSingle 调单会话模式，200 时解析为 JSON。
func gateCheckSingle(t *testing.T, e *queryEnv, sid string) (int, map[string]any) {
	t.Helper()
	code, raw := getRaw(t, e.port, "/gate_check?session_id="+sid, e.token)
	if code == 200 {
		var resp map[string]any
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("响应非 JSON: %v (%q)", err, raw)
		}
		return code, resp
	}
	return code, nil
}

// gateCheckSummary 调汇总模式，返回行集。
func gateCheckSummary(t *testing.T, e *queryEnv, query string) []map[string]any {
	t.Helper()
	code, raw := getRaw(t, e.port, "/gate_check"+query, e.token)
	if code != 200 {
		t.Fatalf("GET /gate_check%s = %d %q", query, code, raw)
	}
	var resp struct {
		GateChecks []map[string]any `json:"gate_checks"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("响应非 JSON: %v (%q)", err, raw)
	}
	return resp.GateChecks
}

// ---- 单会话：三态判定 ----

func TestGateCheckSingleThreeStates(t *testing.T) {
	e := newQueryEnv(t)
	e.qreg("cc", "gc-fresh", `C:\tmp\gc-fresh.jsonl`, `C:\proj-a`, 5, 9000)
	e.qreg("cc", "gc-block", `C:\tmp\gc-block.jsonl`, `C:\proj-b`, 40, 9000)
	e.qreg("cc", "gc-warn", `C:\tmp\gc-warn.jsonl`, `C:\proj-c`, 31, 9000)
	en := e.st.SaveHandoff("gc-block", "cc", `C:\proj-b`, "T",
		isoUTC(e.t0), "fresh", "MD-BODY-无关紧要-测试内")

	// 未凉：allow＋剩余分钟 (30-5)/60≈0.4。
	code, resp := gateCheckSingle(t, e, "gc-fresh")
	if code != 200 {
		t.Fatalf("单会话 gc-fresh = %d", code)
	}
	if resp["verdict"] != "allow" || resp["stale"] != false || resp["mode"] != "enforce" {
		t.Fatalf("未凉应 allow/enforce: %v", resp)
	}
	basis := resp["basis"].(map[string]any)
	if basis["reason"] != "not-in-window" {
		t.Fatalf("未凉 basis = %v", basis)
	}
	if got, ok := resp["block_in_min"].(float64); !ok || got != 0.4 {
		t.Fatalf("gc-fresh block_in_min = %v, want 0.4（(30-5)/60）", resp["block_in_min"])
	}

	// 已凉有交接：block＋判定依据＝有效交接路径。
	code, resp = gateCheckSingle(t, e, "gc-block")
	if code != 200 {
		t.Fatalf("单会话 gc-block = %d", code)
	}
	if resp["verdict"] != "block" || resp["stale"] != true {
		t.Fatalf("已凉有交接应 block: %v", resp)
	}
	if got, ok := resp["block_in_min"].(float64); !ok || got != 0.0 {
		t.Fatalf("已凉 block_in_min = %v, want 0", resp["block_in_min"])
	}
	basis = resp["basis"].(map[string]any)
	if basis["reason"] != "valid-handoff" || basis["handoff_path"] != en.Path ||
		basis["handoff_id"] != en.HandoffID || basis["handoff_status"] != "fresh" {
		t.Fatalf("block 判定依据应为有效交接三要素: %v", basis)
	}

	// 已凉无交接：warn（首次提交分支——真闸门警告放行＋置 pending 的镜像）。
	code, resp = gateCheckSingle(t, e, "gc-warn")
	if code != 200 {
		t.Fatalf("单会话 gc-warn = %d", code)
	}
	if resp["verdict"] != "warn" {
		t.Fatalf("已凉无交接应 warn: %v", resp)
	}
	basisWarn, ok := resp["basis"].(map[string]any)
	if !ok || basisWarn["reason"] != "no-valid-handoff" {
		t.Fatalf("warn basis = %v", resp["basis"])
	}
	if _, has := basisWarn["handoff_path"]; has {
		t.Fatalf("无交接不得带 handoff_path: %v", basisWarn)
	}

	// session_id 不存在 → 404 JSON。
	code, raw := getRaw(t, e.port, "/gate_check?session_id=ghost", e.token)
	if code != 404 {
		t.Fatalf("未知 session_id = %d %q, want 404", code, raw)
	}
	var e404 map[string]any
	if err := json.Unmarshal(raw, &e404); err != nil || e404["error"] == nil {
		t.Fatalf("404 应为 JSON error: %q", raw)
	}
}

// ---- 单会话：剩余分钟与拦截阈值一致（分钟换算） ----

func TestGateCheckRemainingMinutesMatchesThreshold(t *testing.T) {
	e := newQueryEnv(t)
	e.d.Cfg.Thresholds = config.ThresholdCfg{SummarizeS: 1000, BlockS: 3000,
		MinCtxTokens: 100, CacheWarnS: 720}
	e.qreg("cc", "gc-r1", `C:\tmp\gc-r1.jsonl`, `C:\proj-r`, 60, 100)   // (3000-60)/60 = 49
	e.qreg("cc", "gc-r2", `C:\tmp\gc-r2.jsonl`, `C:\proj-r`, 2400, 100) // (3000-2400)/60 = 10
	e.qreg("cc", "gc-r3", `C:\tmp\gc-r3.jsonl`, `C:\proj-r`, 3000, 100) // 恰达阈值 → 0、凉
	e.qreg("cc", "gc-r4", `C:\tmp\gc-r4.jsonl`, `C:\proj-r`, 3300, 100) // 超阈值 → 0、凉

	for sid, want := range map[string]float64{
		"gc-r1": 49, "gc-r2": 10, "gc-r3": 0, "gc-r4": 0} {
		_, resp := gateCheckSingle(t, e, sid)
		if got, ok := resp["block_in_min"].(float64); !ok || got != want {
			t.Fatalf("%s block_in_min = %v, want %v", sid, resp["block_in_min"], want)
		}
	}
	for sid, wantStale := range map[string]bool{
		"gc-r1": false, "gc-r2": false, "gc-r3": true, "gc-r4": true} {
		_, resp := gateCheckSingle(t, e, sid)
		if resp["stale"] != wantStale {
			t.Fatalf("%s stale = %v, want %v", sid, resp["stale"], wantStale)
		}
	}
}

// ---- 单会话：pending 只读镜像（待交接标记分支6：有 pending 无交接 → block；
//      新闲置周期的 pending 视同已清 → warn；全程不得真清除/计数） ----

func TestGateCheckPendingMirror(t *testing.T) {
	e := newQueryEnv(t)
	e.qreg("cc", "gc-p1", `C:\tmp\gc-p1.jsonl`, `C:\proj-p1`, 40, 100)
	e.qreg("cc", "gc-p2", `C:\tmp\gc-p2.jsonl`, `C:\proj-p2`, 40, 100)
	e.d.Pending.Set([2]string{"cc", "gc-p1"}) // SetAt=t0，晚于 lastWrite(t0-40)——在位
	e.d.Pending.mu.Lock()
	e.d.Pending.t[[2]string{"cc", "gc-p2"}] = PendingRec{SetAt: e.t0 - 50} // 早于 lastWrite → 新周期
	e.d.Pending.mu.Unlock()

	_, resp := gateCheckSingle(t, e, "gc-p1")
	if resp["verdict"] != "block" {
		t.Fatalf("pending 在位应 block: %v", resp)
	}
	if basis := resp["basis"].(map[string]any); basis["reason"] != "pending" {
		t.Fatalf("pending block basis = %v", basis)
	}
	_, resp = gateCheckSingle(t, e, "gc-p2")
	if resp["verdict"] != "warn" {
		t.Fatalf("新闲置周期 pending 应视同已清 → warn: %v", resp)
	}
	// pending 记录原样（只读镜像不得真清除）。
	e.d.Pending.mu.Lock()
	_, p1ok := e.d.Pending.t[[2]string{"cc", "gc-p1"}]
	_, p2ok := e.d.Pending.t[[2]string{"cc", "gc-p2"}]
	e.d.Pending.mu.Unlock()
	if !p1ok || !p2ok {
		t.Fatalf("gate_check 不得清除 pending: p1=%v p2=%v", p1ok, p2ok)
	}
}

// ---- 单会话：闸门三态 off/observe ----

func TestGateCheckModeOffAndObserve(t *testing.T) {
	e := newQueryEnv(t)
	e.qreg("cc", "gc-m1", `C:\tmp\gc-m1.jsonl`, `C:\proj-m`, 40, 100)
	e.st.SaveHandoff("gc-m1", "cc", `C:\proj-m`, "T", isoUTC(e.t0), "fresh", "md")

	e.d.Cfg.GateCC = "off"
	_, resp := gateCheckSingle(t, e, "gc-m1")
	if resp["verdict"] != "allow" || resp["mode"] != "off" {
		t.Fatalf("mode off 应 allow/off: %v", resp)
	}
	if basis := resp["basis"].(map[string]any); basis["reason"] != "mode-off" {
		t.Fatalf("mode-off basis = %v", basis)
	}

	e.d.Cfg.GateCC = "observe" // observe 只警告不拦——有交接也不判 block
	_, resp = gateCheckSingle(t, e, "gc-m1")
	if resp["verdict"] != "warn" || resp["mode"] != "observe" {
		t.Fatalf("observe 已凉应 warn（不得 block）: %v", resp)
	}
	if basis := resp["basis"].(map[string]any); basis["reason"] != "observe" {
		t.Fatalf("observe basis = %v", basis)
	}

	e.qreg("cc", "gc-m2", `C:\tmp\gc-m2.jsonl`, `C:\proj-m2`, 5, 100)
	_, resp = gateCheckSingle(t, e, "gc-m2")
	if resp["verdict"] != "allow" {
		t.Fatalf("observe 未凉应 allow: %v", resp)
	}
}

// ---- 单会话：机器等机器豁免——只读推演只覆盖停车窗道（T48） ----

func TestGateCheckMachineWaitingParkedWindow(t *testing.T) {
	e := newQueryEnv(t)
	e.qreg("cc", "gc-mw", `C:\tmp\gc-mw.jsonl`, `C:\proj-mw`, 9999, 100)
	e.st.SaveHandoff("gc-mw", "cc", `C:\proj-mw`, "T", isoUTC(e.t0), "fresh", "md")
	if _, err := e.d.Subagent(map[string]any{"event": "start", "agent": "cc",
		"session_id": "gc-mw"}); err != nil {
		t.Fatal(err)
	}
	e.d.windowsMu.Lock()
	parked := e.t0 - 30
	e.d.windows[winKey{"cc", "gc-mw"}].StopTS = &parked
	e.d.windowsMu.Unlock()

	_, resp := gateCheckSingle(t, e, "gc-mw")
	if resp["verdict"] != "allow" {
		t.Fatalf("停车窗应豁免 allow: %v", resp)
	}
	if basis := resp["basis"].(map[string]any); basis["reason"] != "machine-waiting" {
		t.Fatalf("machine-waiting basis = %v", basis)
	}
}

// ---- 汇总：排序键三规则＋行三要素＋逐 agent 模式 ----

func TestGateCheckSummarySortThreeRules(t *testing.T) {
	e := newQueryEnv(t)
	e.qreg("cc", "s-far", `C:\tmp\s-far.jsonl`, `C:\proj-o0`, 5, 100)
	e.qreg("cc", "s-a2", `C:\tmp\s-a2.jsonl`, `C:\proj-o2`, 10, 100) // 有交接（skeleton）
	e.qreg("cc", "s-ax", `C:\tmp\s-ax.jsonl`, `C:\proj-o0`, 10, 100)
	e.qreg("cc", "s-b2", `C:\tmp\s-b2.jsonl`, `C:\proj-o0`, 10, 100)
	e.qreg("cc", "s-warn", `C:\tmp\s-warn.jsonl`, `C:\proj-o0`, 32, 100)
	e.qreg("cc", "s-block", `C:\tmp\s-block.jsonl`, `C:\proj-o1`, 35, 100)
	e.qreg("codex", "s-cx", `C:\tmp\s-cx.jsonl`, `C:\proj-o0`, 5, 100) // codex 默认 off
	e.st.SaveHandoff("s-block", "cc", `C:\proj-o1`, "T", isoUTC(e.t0), "fresh", "md")
	e.st.SaveHandoff("s-a2", "cc", `C:\proj-o2`, "T", isoUTC(e.t0), "skeleton", "md2")

	rows := gateCheckSummary(t, e, "")
	wantOrder := []string{"s-block", "s-warn", "s-ax", "s-b2", "s-a2", "s-cx", "s-far"}
	// 规则①临近度升序（预计拦截时刻＝last_write+block_s）：已过线（s-block）
	// 最先，其后 s-warn（t0-2）→ 并列组（t0+20）→（t0+25）。
	// 规则②无交接者在前：同刻并列组内 s-a2 字典序本应最先（"s-a2"<"s-ax"），
	// 因有交接被排到 s-ax/s-b2 之后。
	// 规则③并列 sid 字典序：s-ax < s-b2；s-cx < s-far（codex off 行同键参与）。
	for i, row := range rows {
		if row["session_id"] != wantOrder[i] {
			t.Fatalf("第 %d 行 = %v, want %s\n规则: ①临近度升序 ②无交接在前 ③并列 sid 字典序",
				i, row["session_id"], wantOrder[i])
		}
		if !keySetEqual(keys(row), "session_id", "verdict", "block_in_min") {
			t.Fatalf("汇总行应恰三要素, got %v: %v", keys(row), row)
		}
	}
	wantVerdict := map[string]string{"s-block": "block", "s-warn": "warn",
		"s-ax": "allow", "s-b2": "allow", "s-a2": "allow", "s-cx": "allow",
		"s-far": "allow"}
	for _, row := range rows {
		sid := row["session_id"].(string)
		if row["verdict"] != wantVerdict[sid] {
			t.Fatalf("%s verdict = %v, want %v", sid, row["verdict"], wantVerdict[sid])
		}
	}
	// 已拦行 block_in_min=0；未凉 (30-10)/60≈0.3。
	if got := rows[0]["block_in_min"].(float64); got != 0 {
		t.Fatalf("s-block block_in_min = %v, want 0", got)
	}
	if got := rows[2]["block_in_min"].(float64); got != 0.3 {
		t.Fatalf("s-ax block_in_min = %v, want 0.3", got)
	}
}

// ---- 汇总：limit 默认 50 生效＋非法 limit 400＋空台账空数组 ----

func TestGateCheckSummaryLimitAndEmpty(t *testing.T) {
	e := newQueryEnv(t)
	if raw := gateCheckSummary(t, e, ""); len(raw) != 0 {
		t.Fatalf("空台账应空数组, got %v", raw)
	}
	code, raw := getRaw(t, e.port, "/gate_check", e.token)
	if code != 200 || !strings.Contains(string(raw), `"gate_checks":[]`) {
		t.Fatalf("空台账应 {\"gate_checks\":[]}: %d %q", code, raw)
	}

	for i := 0; i < 55; i++ {
		sid := "l-" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		e.qreg("cc", sid, `C:\tmp\`+sid+`.jsonl`, `C:\proj`, float64(i), 100)
	}
	if rows := gateCheckSummary(t, e, ""); len(rows) != 50 {
		t.Fatalf("默认 limit 应 50, got %d", len(rows))
	}
	// 排序后裁尾部：闲置最长（i=54 → l-cc）最先，保留到 i=5（l-af）。
	rows := gateCheckSummary(t, e, "")
	if rows[0]["session_id"] != "l-cc" || rows[49]["session_id"] != "l-af" {
		t.Fatalf("默认裁最凉之外: 首 %v 末 %v", rows[0]["session_id"], rows[49]["session_id"])
	}
	if rows = gateCheckSummary(t, e, "?limit=2"); len(rows) != 2 {
		t.Fatalf("limit=2 应 2 行, got %d", len(rows))
	}
	if code, raw = getRaw(t, e.port, "/gate_check?limit=abc", e.token); code != 400 {
		t.Fatalf("limit 非法应 400, got %d %q", code, raw)
	}
	if code, raw = getRaw(t, e.port, "/gate_check?limit=0", e.token); code != 400 {
		t.Fatalf("limit=0 应 400, got %d %q", code, raw)
	}
}

// ---- 鉴权：无/错 token → 401（与 /stats 同语义） ----

func TestGateCheckRequireBearer(t *testing.T) {
	e := newQueryEnv(t)
	for _, path := range []string{"/gate_check", "/gate_check?session_id=x"} {
		if code, body := getRaw(t, e.port, path, ""); code != 401 ||
			string(body) != `{"error":"unauthorized"}` {
			t.Fatalf("GET %s 无 Bearer = %d %q, want 401", path, code, body)
		}
		if code, _ := getRaw(t, e.port, path, "wrong-token"); code != 401 {
			t.Fatalf("GET %s 错 token = %d, want 401", path, code)
		}
	}
}

// ---- pending 降级路径的精确镜像（分支 6 降级：Blocks≥3 → 下次真提交放行） ----

func TestGateCheckPendingDegradeMirror(t *testing.T) {
	e := newQueryEnv(t)
	e.qreg("cc", "gc-d1", `C:\tmp\gc-d1.jsonl`, `C:\proj-d1`, 35, 100)
	e.qreg("cc", "gc-d2", `C:\tmp\gc-d2.jsonl`, `C:\proj-d2`, 35, 100)
	e.d.Pending.Set([2]string{"cc", "gc-d1"}) // Set 惰性建表，再改 Blocks
	e.d.Pending.Set([2]string{"cc", "gc-d2"})
	e.d.Pending.mu.Lock()
	// 已被真闸门连拦 3 次（Blocks=3）→ 下次真提交 n=4>3 即 Clear＋放行（降级）
	e.d.Pending.t[[2]string{"cc", "gc-d1"}] = PendingRec{SetAt: e.t0, Blocks: 3}
	// 只拦过 2 次 → 下次真提交 n=3 仍 block（未达降级）
	e.d.Pending.t[[2]string{"cc", "gc-d2"}] = PendingRec{SetAt: e.t0, Blocks: 2}
	e.d.Pending.mu.Unlock()

	code, resp := gateCheckSingle(t, e, "gc-d1")
	if code != 200 {
		t.Fatalf("gc-d1 = %d", code)
	}
	basis, _ := resp["basis"].(map[string]any)
	if resp["verdict"] != "allow" || basis["reason"] != "pending-degraded" {
		t.Fatalf("gc-d1 Blocks=3 应预演降级放行, got verdict=%v basis=%v",
			resp["verdict"], resp["basis"])
	}
	code, resp = gateCheckSingle(t, e, "gc-d2")
	if code != 200 {
		t.Fatalf("gc-d2 = %d", code)
	}
	basis, _ = resp["basis"].(map[string]any)
	if resp["verdict"] != "block" || basis["reason"] != "pending" {
		t.Fatalf("gc-d2 Blocks=2 应仍预演 block, got verdict=%v basis=%v",
			resp["verdict"], resp["basis"])
	}
	// 汇总模式同谓词：gc-d1 行 allow、gc-d2 行 block
	rows := gateCheckSummary(t, e, "")
	verdicts := map[string]any{}
	for _, r := range rows {
		verdicts[r["session_id"].(string)] = r["verdict"]
	}
	if verdicts["gc-d1"] != "allow" || verdicts["gc-d2"] != "block" {
		t.Fatalf("汇总降级镜像不符: %v", verdicts)
	}
}

// ---- 零状态写入：调用前后台账/pending/窗口/闸门计数/交接库文件字节不变 ----

func TestGateCheckZeroStateWrite(t *testing.T) {
	e := newQueryEnv(t)
	e.qreg("cc", "gc-z1", `C:\tmp\gc-z1.jsonl`, `C:\proj-z1`, 40, 100)
	e.qreg("cc", "gc-z2", `C:\tmp\gc-z2.jsonl`, `C:\proj-z2`, 5, 100)
	e.qreg("cc", "gc-z3", `C:\tmp\gc-z3.jsonl`, `C:\proj-z3`, 35, 100)
	// gc-z4：凉＋有效交接＋无窗无 pending——驱动写副作用最重的分支 5
	// （真闸门此分支 Pending.Clear＋MarkBlocked＋SavePendingPrompt＋addBlocks）。
	e.qreg("cc", "gc-z4", `C:\tmp\gc-z4.jsonl`, `C:\proj-z4`, 2100, 100)
	e.st.SaveHandoff("gc-z4", "cc", `C:\proj-z4`, "T", isoUTC(e.t0), "fresh", "MD-Z4")
	en := e.st.SaveHandoff("gc-z1", "cc", `C:\proj-z1`, "T", isoUTC(e.t0), "fresh", "MD-Z")
	e.st.SavePendingPrompt("gc-z1", "PROMPT-SENTINEL-Z")
	e.d.Pending.Set([2]string{"cc", "gc-z3"})
	if _, err := e.d.Subagent(map[string]any{"event": "start", "agent": "cc",
		"session_id": "gc-z1"}); err != nil {
		t.Fatal(err)
	}
	e.d.windowsMu.Lock()
	parked := e.t0 - 30
	e.d.windows[winKey{"cc", "gc-z1"}].StopTS = &parked
	e.d.windowsMu.Unlock()

	storeDir := filepath.Dir(en.Path)
	snapFiles := func() map[string]string {
		t.Helper()
		out := map[string]string{}
		ents, err := os.ReadDir(storeDir)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range ents {
			b, err := os.ReadFile(filepath.Join(storeDir, f.Name()))
			if err != nil {
				t.Fatal(err)
			}
			out[f.Name()] = string(b)
		}
		return out
	}
	snapLedger := func() map[string]ledger.SessionState {
		t.Helper()
		out := map[string]ledger.SessionState{}
		e.led.Mu().Lock()
		for _, st := range e.led.AllSessionsLocked() {
			out[st.Agent+"/"+st.SessionID] = *st
		}
		e.led.Mu().Unlock()
		return out
	}
	snapPending := func() map[[2]string]PendingRec {
		t.Helper()
		out := map[[2]string]PendingRec{}
		e.d.Pending.mu.Lock()
		for k, v := range e.d.Pending.t {
			out[k] = v
		}
		e.d.Pending.mu.Unlock()
		return out
	}
	snapWindows := func() map[winKey]waitWindow {
		t.Helper()
		out := map[winKey]waitWindow{}
		e.d.windowsMu.Lock()
		for k, w := range e.d.windows {
			if w != nil {
				out[k] = *w
			}
		}
		e.d.windowsMu.Unlock()
		return out
	}
	snapStats := func() [5]int {
		t.Helper()
		return [5]int{e.d.Stats.Total, e.d.Stats.Bypass, e.d.Stats.Blocks,
			e.d.Stats.Warns, e.d.Stats.SubagentEvents}
	}

	files0, led0 := snapFiles(), snapLedger()
	pend0, win0, stats0 := snapPending(), snapWindows(), snapStats()

	for _, q := range []string{"/gate_check?session_id=gc-z1", "/gate_check?session_id=gc-z3",
		"/gate_check?session_id=gc-z4", "/gate_check", "/gate_check?limit=2"} {
		if code, raw := getRaw(t, e.port, q, e.token); code != 200 {
			t.Fatalf("GET %s = %d %q", q, code, raw)
		}
	}

	if files1 := snapFiles(); !reflect.DeepEqual(files0, files1) {
		t.Fatal("交接库目录文件字节在 /gate_check 前后发生变化（零写入红线）")
	}
	if led1 := snapLedger(); !reflect.DeepEqual(led0, led1) {
		t.Fatal("台账状态在 /gate_check 前后发生变化（零写入红线）")
	}
	if pend1 := snapPending(); !reflect.DeepEqual(pend0, pend1) {
		t.Fatal("pending 在 /gate_check 前后发生变化（零写入红线）")
	}
	if win1 := snapWindows(); !reflect.DeepEqual(win0, win1) {
		t.Fatal("窗口状态在 /gate_check 前后发生变化（零写入红线）")
	}
	if stats1 := snapStats(); stats0 != stats1 {
		t.Fatalf("闸门计数在 /gate_check 前后变化: %v → %v", stats0, stats1)
	}
}

// ---- 红线反向断言：无消息内容、无凭据字段（单会话＋汇总） ----

func TestGateCheckRedlinesNoContentNoCredentials(t *testing.T) {
	e := newQueryEnv(t)
	e.qreg("cc", "gc-r", `C:\tmp\gc-r.jsonl`, `C:\proj-r`, 40, 100)
	en := e.st.SaveHandoff("gc-r", "cc", `C:\proj-r`, "T", isoUTC(e.t0), "fresh",
		"HANDOFF-BODY-SENTINEL-交接正文")
	e.st.SavePendingPrompt("gc-r", "PROMPT-SENTINEL-被拦原话")

	for _, q := range []string{"/gate_check?session_id=gc-r", "/gate_check"} {
		_, raw := getRaw(t, e.port, q, e.token)
		body := string(raw)
		for _, sentinel := range []string{"PROMPT-SENTINEL", "HANDOFF-BODY-SENTINEL", e.token} {
			if strings.Contains(body, sentinel) {
				t.Fatalf("GET %s 泄漏红线内容 %q: %q", q, sentinel, body)
			}
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("GET %s 非 JSON: %q", q, raw)
		}
		var allKeys []string
		walkKeys(v, &allKeys)
		for _, k := range allKeys {
			switch strings.ToLower(k) {
			case "api_key", "apikey", "token", "secret", "authorization", "password":
				t.Fatalf("GET %s 含凭据类字段 %q: %q", q, k, body)
			}
		}
	}
	// 判定依据只给路径要素（id/status/path），不给正文。
	_, resp := gateCheckSingle(t, e, "gc-r")
	basis := resp["basis"].(map[string]any)
	if basis["reason"] != "valid-handoff" || basis["handoff_id"] != en.HandoffID {
		t.Fatalf("block 判定依据 = %v", basis)
	}
	if _, has := basis["handoff_path"]; !has {
		t.Fatalf("block 判定依据应含 handoff_path: %v", basis)
	}
}
