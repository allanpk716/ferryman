package mcp

// tools_e2e_test.go — 票04 验收钉子：五件工具对临时 daemon（FERRYMAN_CONFIG
// 指向临时 config、临时端口）各一发成功，参数（limit/过滤/scope/key）透传正确，
// daemon 响应 JSON 原样透传；全程响应不回显 token。

import (
	"strings"
	"testing"
)

// TestFiveToolsAgainstTempDaemon 全链：工具调用 → daemon 端点 → 响应透传。
func TestFiveToolsAgainstTempDaemon(t *testing.T) {
	e := newEnv(t, true, true)
	p := startPipes(t, e.cfg)
	noToken := func(where, text string) {
		t.Helper()
		if strings.Contains(text, e.token) {
			t.Fatalf("%s 泄漏 token: %s", where, text)
		}
	}

	// 1) sessions：agent 过滤＋limit 透传（cx-1 是全局最新，agent=cc 应滤掉）。
	text, isErr, _ := p.callTool("sessions", map[string]any{"agent": "cc", "limit": 1})
	if isErr {
		t.Fatalf("sessions 工具报错: %s", text)
	}
	noToken("sessions", text)
	var sr struct {
		Sessions []map[string]any `json:"sessions"`
	}
	mustJSON(t, text, &sr)
	if len(sr.Sessions) != 1 || sr.Sessions[0]["session_id"] != "sd-1" ||
		sr.Sessions[0]["agent"] != "cc" {
		t.Fatalf("sessions agent=cc&limit=1 应只回最新 cc 会话 sd-1: %v", sr.Sessions)
	}

	// 2) session_detail：session_id 透传＋四列会话总账（主转录＋子代理加总）。
	text, isErr, _ = p.callTool("session_detail", map[string]any{"session_id": "sd-1"})
	if isErr {
		t.Fatalf("session_detail 工具报错: %s", text)
	}
	noToken("session_detail", text)
	var dr struct {
		Ledger     map[string]any `json:"ledger"`
		UsageTotal map[string]any `json:"usage_total"`
		Windows    struct {
			Wait   map[string]any `json:"wait"`
			QWatch map[string]any `json:"qwatch"`
		} `json:"windows"`
	}
	mustJSON(t, text, &dr)
	if dr.Ledger["session_id"] != "sd-1" || dr.Ledger["agent"] != "cc" {
		t.Fatalf("session_detail ledger 字段: %v", dr.Ledger)
	}
	if dr.UsageTotal["input_tokens"] != 1300.0 {
		t.Fatalf("usage_total.input_tokens = %v, want 1300（主转录＋子代理加总）",
			dr.UsageTotal["input_tokens"])
	}
	if dr.Windows.Wait["open"] != true {
		t.Fatalf("windows.wait.open 应为 true（夹具开了等待窗）: %v", dr.Windows.Wait)
	}

	// 3) gate_check：session_id 透传（单会话判定；默认 observe＋未进窗 → allow）。
	text, isErr, _ = p.callTool("gate_check", map[string]any{"session_id": "sd-1"})
	if isErr {
		t.Fatalf("gate_check 工具报错: %s", text)
	}
	noToken("gate_check", text)
	var gr map[string]any
	mustJSON(t, text, &gr)
	if gr["session_id"] != "sd-1" || gr["verdict"] != "allow" {
		t.Fatalf("gate_check 单会话判定: %v", gr)
	}

	// 4) cost_report：scope/key 透传（/report 响应回显 scope/key＋按项目过滤）。
	text, isErr, _ = p.callTool("cost_report", map[string]any{"scope": "project", "key": "C:/proj"})
	if isErr {
		t.Fatalf("cost_report 工具报错: %s", text)
	}
	noToken("cost_report", text)
	var cr struct {
		Scope  string         `json:"scope"`
		Key    string         `json:"key"`
		Tokens map[string]any `json:"tokens"`
	}
	mustJSON(t, text, &cr)
	if cr.Scope != "project" || cr.Key != "C:/proj" {
		t.Fatalf("cost_report scope/key 透传: %v/%v", cr.Scope, cr.Key)
	}
	if cr.Tokens["input_tokens"] != 1300.0 {
		t.Fatalf("cost_report tokens.input_tokens = %v, want 1300", cr.Tokens["input_tokens"])
	}

	// 5) heartbeat_status：无参；在飞等待窗（夹具 subagent start 开的那枚）。
	text, isErr, _ = p.callTool("heartbeat_status", nil)
	if isErr {
		t.Fatalf("heartbeat_status 工具报错: %s", text)
	}
	noToken("heartbeat_status", text)
	var br struct {
		Windows []map[string]any `json:"windows"`
	}
	mustJSON(t, text, &br)
	if len(br.Windows) != 1 || br.Windows[0]["kind"] != "wait" ||
		br.Windows[0]["session_id"] != "sd-1" {
		t.Fatalf("heartbeat_status 在飞窗: %v", br.Windows)
	}
	if _, ok := br.Windows[0]["telemetry"].(map[string]any); !ok {
		t.Fatalf("heartbeat_status 逐窗遥测缺失: %v", br.Windows[0])
	}
}

// TestSessionsLimitPassthroughToSummary gate_check 汇总模式 limit 透传：
// 无 session_id＝汇总（逐会话一行），limit=2 只回前两行。
func TestGateCheckSummaryLimitPassthrough(t *testing.T) {
	e := newEnv(t, true, true)
	p := startPipes(t, e.cfg)
	text, isErr, _ := p.callTool("gate_check", map[string]any{"limit": 2})
	if isErr {
		t.Fatalf("gate_check 汇总报错: %s", text)
	}
	var gr struct {
		GateChecks []map[string]any `json:"gate_checks"`
	}
	mustJSON(t, text, &gr)
	if len(gr.GateChecks) != 2 {
		t.Fatalf("gate_check 汇总 limit=2 应 2 行, got %d: %v", len(gr.GateChecks), gr.GateChecks)
	}
}
