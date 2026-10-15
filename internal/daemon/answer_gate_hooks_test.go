package daemon

// answer_gate_hooks_test.go — #36 作答闸门钩子真跑测试（接缝 B，票面约定）：
// 真 powershell 子进程执行 hooks/ferryman-gate.ps1 的 PostToolUse 分支，stdin
// 喂 2026-10-15 实测形态的钩子输入（hook_event_name=PostToolUse、tool_name=
// AskUserQuestion、tool_response.answers＝问题→所选标签映射——notes/
// events.log）。契约：被拦 → stdout 纯 JSON {"continue":false,"stopReason":…}
// 退出 0；放行/守护不可达/401/任何故障 → 零输出；FERRYMAN_DISABLE 短路；
// PostToolUse 的其他工具零输出（matcher 已限 AskUserQuestion，脚本内再验防
// 手工装错）；UserPromptSubmit 分支既有用例见 test_hooks_test.go（零改动）。

import (
	"os"
	"strings"
	"testing"
	"time"

	"ferryman/internal/store"
)

// auqPostBody 实测形态的 PostToolUse(AskUserQuestion) 钩子输入（events.log
// 08:25:56.371 行：answers 为 问题→所选标签 映射；multiSelect 一问并作数组
// 值形态防漏——钩子侧并入一行）。
func auqPostBody(sid, path, cwd string) map[string]any {
	return map[string]any{
		"session_id":      sid,
		"transcript_path": path,
		"cwd":             cwd,
		"hook_event_name": "PostToolUse",
		"tool_name":       "AskUserQuestion",
		"tool_use_id":     "toolu_016F8WSMD6TXerSGRxuDZMPp",
		"tool_input": map[string]any{"questions": []any{
			map[string]any{"question": "午饭吃面还是吃饭?", "header": "午饭",
				"multiSelect": false, "options": []any{
					map[string]any{"label": "面", "description": "午饭吃面"},
					map[string]any{"label": "饭", "description": "午饭吃米饭"}}}}},
		"tool_response": map[string]any{
			"questions": []any{map[string]any{"question": "午饭吃面还是吃饭?", "header": "午饭",
				"options": []any{map[string]any{"label": "面", "description": "午饭吃面"},
					map[string]any{"label": "饭", "description": "午饭吃米饭"}},
				"multiSelect": false}},
			"answers":     map[string]any{"午饭吃面还是吃饭?": "面", "要不要加个蛋?": []any{"加", "不加"}},
			"annotations": map[string]any{},
		},
	}
}

// TestT36AnswerGateHookBlocksIdle 凉会话＋有效交接＋enforce：作答被拦 → exit 0
// ＋stdout 纯 JSON（continue=false＋stopReason＝拦截文案）；答案以"问题→所选
// 答案"落库为待续 prompt（含多选并一行的数组值形态）。
func TestT36AnswerGateHookBlocksIdle(t *testing.T) {
	h := newIntegHarness(t, succFerry)
	proj := h.tmp + "\\proj"
	sid := "auq-0001"
	f := writeIntegSession(t, h.projects, sid, proj, 2000)
	waitForCond(t, 15*time.Second, func() bool {
		return h.handoffInStore("cc", proj, func(e *store.Entry) bool { return e.SessionID == sid })
	})
	code, out := gateBlockPoll(t, "ferryman-gate.ps1", auqPostBody(sid, f, proj), liveEnv(t, h))
	if code != 0 { // block 走 stdout JSON 通道，exit 仍 0
		t.Fatalf("exit=%d, want 0", code)
	}
	payload := parseHookJSON(t, out)
	if payload["continue"] != false {
		t.Fatalf("continue = %v, want false", payload["continue"])
	}
	rsn, _ := payload["stopReason"].(string)
	if !strings.Contains(rsn, "[Ferryman 作答闸门]") {
		t.Fatalf("stopReason 缺拦截标记: %q", rsn)
	}
	if !strings.Contains(rsn, "你的选择已保存") || !strings.Contains(rsn, "交接文档") {
		t.Fatalf("stopReason 缺选择保管/交接指引: %q", rsn)
	}
	if strings.Contains(rsn, "原话") {
		t.Fatalf("作答拦截文案不得带「原话」: %q", rsn)
	}
	// 答案格式化契约：问题→所选答案（多问以；相连；数组值并入一行）。
	p, ok := h.st.LatestPendingFor("cc", proj)
	if !ok {
		t.Fatal("作答被拦后应落待续 prompt（锚）")
	}
	want := "午饭吃面还是吃饭?→面；要不要加个蛋?→加、不加"
	if p.Prompt != want {
		t.Fatalf("待续 prompt = %q, want %q", p.Prompt, want)
	}
}

// TestT36AnswerGateHookAllowZeroOutput 放行（台账 miss——daemon 没见过的会话）
// → 零输出零拦截。
func TestT36AnswerGateHookAllowZeroOutput(t *testing.T) {
	h := newIntegHarness(t, succFerry)
	code, out := runPS(t, "ferryman-gate.ps1",
		auqPostBody("ghost-answer", "C:/ghost.jsonl", "C:/x"), liveEnv(t, h))
	assertSilentOK(t, "allow", code, out)
}

// TestT36AnswerGateHookIgnoresOtherPostToolUseTools PostToolUse 的非
// AskUserQuestion 工具（matcher 缺失/装错时的防御）：零输出，不问闸门。
func TestT36AnswerGateHookIgnoresOtherPostToolUseTools(t *testing.T) {
	h := newIntegHarness(t, succFerry)
	body := auqPostBody("s", "C:/x.jsonl", "C:/x")
	body["tool_name"] = "Bash"
	code, out := runPS(t, "ferryman-gate.ps1", body, liveEnv(t, h))
	assertSilentOK(t, "非 AskUserQuestion", code, out)
}

// TestT36AnswerGateHookFailopenOn401 令牌错（401）→ 零输出放行。
func TestT36AnswerGateHookFailopenOn401(t *testing.T) {
	h := newIntegHarness(t, succFerry)
	badToken := h.tmp + "\\bad.token"
	if err := os.WriteFile(badToken, []byte("wrong-token"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := liveEnv(t, h)
	env["FERRYMAN_TOKEN_FILE"] = badToken
	code, out := runPS(t, "ferryman-gate.ps1",
		auqPostBody("s", "C:/x.jsonl", "C:/x"), env)
	assertSilentOK(t, "401", code, out)
}

// TestT36AnswerGateHookFailopenDaemonDown 守护不在场 → 零输出放行。
func TestT36AnswerGateHookFailopenDaemonDown(t *testing.T) {
	code, out := runPS(t, "ferryman-gate.ps1",
		auqPostBody("s", "C:/x.jsonl", "C:/x"), downEnv(t))
	assertSilentOK(t, "守护关", code, out)
}

// TestT36AnswerGateHookEnvDisableShortCircuits FERRYMAN_DISABLE=1 → 首行短路，
// 不碰网络零输出。
func TestT36AnswerGateHookEnvDisableShortCircuits(t *testing.T) {
	env := downEnv(t)
	env["FERRYMAN_DISABLE"] = "1"
	t0 := time.Now()
	code, out := runPS(t, "ferryman-gate.ps1",
		auqPostBody("s", "C:/x.jsonl", "C:/x"), env)
	assertSilentOK(t, "DISABLE", code, out)
	if el := time.Since(t0); el >= 5*time.Second {
		t.Fatalf("DISABLE 短路耗时 %v, want < 5s", el)
	}
}
