package accounts_test

// 票13 回填：tests/test_accounts.py 的 4 个窗口 e2e（原 accounts_test.go 内的
// t.Skip 占位）转绿。本文件为 Go 外部测试包——daemon → accounts 是生产依赖
// 方向，accounts 的内部测试包引用 daemon 会成环，外部测试包正是该场景的标准解。
//
// 装配器（票面许可的"最小测试装配器"）：临时目录 Accounts+Ledger+Daemon；
// Store/EnqueueFerry 置空——窗口道不经过摆渡与交接库。HTTP Harness 面
// （Python h.sub / h.gate）归票15 httpapi；此处按票面许可直驱 Daemon 导出
// 方法：Subagent = POST /subagent；NoteGatePrompt = gate 步 0 的闭窗钩子
// （server.py:143-151，主会话来讯在 bypass 判定之前闭未停车窗——票14 的
// gate.go 于 bypass 分支调用同一钩子）。

import (
	"testing"

	"ferryman/internal/accounts"
	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/daemon"
	"ferryman/internal/ledger"
)

type windowHarness struct {
	accts *accounts.Accounts
	led   *ledger.Ledger
	d     *daemon.Daemon
}

func newWindowHarness(t *testing.T) *windowHarness {
	t.Helper()
	accts, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	led := ledger.New()
	d := daemon.NewDaemon(config.Default(), led, nil,
		func(*ledger.SessionState) bool { return true }, accts, 0, nil)
	return &windowHarness{accts: accts, led: led, d: d}
}

// sub 对应 Python h.sub({"event", "agent": "cc", "session_id"})。
func (h *windowHarness) sub(t *testing.T, event, sid string) {
	t.Helper()
	if _, err := h.d.Subagent(map[string]any{"event": event, "agent": "cc", "session_id": sid}); err != nil {
		t.Fatalf("sub %s %s: %v", event, sid, err)
	}
}

func (h *windowHarness) windowRows(session string) []map[string]any {
	return h.accts.Read(accounts.ReadOpts{Kind: "window", Session: session})
}

// ---- Python: test_accounts.py::test_window_books_on_subagent_cycle ----

func TestWindowBooksOnSubagentCycle(t *testing.T) {
	h := newWindowHarness(t)
	h.sub(t, "start", "acct3")
	h.sub(t, "start", "acct3") // 嵌套：计数 2
	h.sub(t, "stop", "acct3")
	h.sub(t, "stop", "acct3") // 计数归零 → 闭窗
	rows := h.windowRows("acct3")
	if len(rows) != 1 {
		t.Fatalf("window 行数 = %d, want 1: %v", len(rows), rows)
	}
	e := rows[0]
	if e["session_id"] != "acct3" {
		t.Fatalf("session_id = %v", e["session_id"])
	}
	if e["dur_s"].(float64) < 0 || e["close_reason"] != "subagents_done" {
		t.Fatalf("dur_s/close_reason = %v/%v, want >=0/subagents_done", e["dur_s"], e["close_reason"])
	}
	for _, k := range []string{"opened_ts", "closed_ts", "dur_s", "prefix_tokens"} {
		if _, ok := e[k]; !ok {
			t.Fatalf("窗口行缺字段 %s: %v", k, e)
		}
	}
}

// ---- Python: test_accounts.py::test_window_closes_on_prompt ----

func TestWindowClosesOnPrompt(t *testing.T) {
	h := newWindowHarness(t)
	h.sub(t, "start", "acct4")
	// gate({"prompt": "人回来了"}) 的窗口效应（gate 步 0；decision=allow 的
	// 闸门判定归票14 gate.go，此处钉 daemon 钩子契约）。
	h.d.NoteGatePrompt("cc", "acct4")
	rows := h.windowRows("acct4")
	if len(rows) != 1 || rows[0]["close_reason"] != "prompt" {
		t.Fatalf("window 行 = %v, want 1 条 prompt", rows)
	}
	// 窗已闭：后续 stop 不再产生第二条
	h.sub(t, "stop", "acct4")
	if got := len(h.windowRows("acct4")); got != 1 {
		t.Fatalf("窗已闭后 stop 不得再记账, 行数 = %d", got)
	}
}

// ---- Python: test_accounts.py::test_window_closes_on_bypass_prompt ----

func TestWindowClosesOnBypassPrompt(t *testing.T) {
	// R1：强续/bypass 亦是主会话恢复写入——等待窗口同样闭窗（钩子须在 bypass
	// 分支之前）。gate 层 decision=allow / reason=bypass 断言归票14；本票钉住
	// daemon 钩子：bypass prompt 走同一闭窗道。
	h := newWindowHarness(t)
	h.sub(t, "start", "acct5")
	h.d.NoteGatePrompt("cc", "acct5") // gate("强续 继续") 步 0 的窗口效应
	rows := h.windowRows("acct5")
	if len(rows) != 1 {
		t.Fatalf("window 行数 = %d, want 1", len(rows))
	}
	if rows[0]["session_id"] != "acct5" || rows[0]["close_reason"] != "prompt" {
		t.Fatalf("session_id/close_reason = %v/%v, want acct5/prompt",
			rows[0]["session_id"], rows[0]["close_reason"])
	}
}

// ---- Python: test_accounts.py::test_window_reanchors_after_leak_gap ----

func TestWindowReanchorsAfterLeakGap(t *testing.T) {
	// 模拟 Stop 丢失 + 泄漏超时后再 start：旧窗不沿用（否则 dur_s 虚高跨越
	// 泄漏间隙）。Python 经 h.daemon._windows 直塞泄漏旧窗；Go 外部测试包触
	// 不到私有表，改用 clock.Now 注入把时钟拨过泄漏界——语义同构（旧窗
	// opened_ts 距今超 SUBAGENT_EVENT_LEAK_S）。
	h := newWindowHarness(t)
	t0 := clock.Now()
	orig := clock.Now
	clock.Now = func() float64 { return t0 }
	defer func() { clock.Now = orig }()

	h.sub(t, "start", "acct6") // 窗开于 t0
	clock.Now = func() float64 { return t0 + ledger.SubagentEventLeakS + 60 }
	h.sub(t, "start", "acct6") // 泄漏超限 → 重锚：旧窗未停车 → 静默弃、不记行
	h.sub(t, "stop", "acct6")  // 计数 2→1
	h.sub(t, "stop", "acct6")  // 计数 1→0 → 闭窗

	rows := h.windowRows("acct6")
	if len(rows) != 1 {
		t.Fatalf("旧窗不沿用不记行, 行数 = %d: %v", len(rows), rows)
	}
	if e := rows[len(rows)-1]["dur_s"].(float64); e >= ledger.SubagentEventLeakS {
		t.Fatalf("dur_s = %v, want < SUBAGENT_EVENT_LEAK_S（不跨泄漏间隙虚高）", e)
	}
}
