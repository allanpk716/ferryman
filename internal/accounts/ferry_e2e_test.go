package accounts_test

// ferry_e2e_test.go — 票17 回填：tests/test_accounts.py 的 3 个摆渡记账 e2e
// 占位转绿（test_ferry_completion_books_handoff / test_failed_ferry_books_
// exactly_one_row / test_booking_failure_never_breaks_ferry）。
//
// 装配器（gate_e2e/window_e2e 同款"最小测试装配器"，扩 Worker 通道）：
// 临时目录 Accounts+Store+Worker（恒成功/恒败 ferryFunc 替身，离线）；
// 直驱 Worker.Enqueue——守望达阈值入队腿由票16 守望测试覆盖，摆渡记账行为
// 与链路无关（gate_e2e 先例），行为等价且全确定。
//
// 隐私不变量随行钉死：handoff 行无 content/md 键。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/config"
	"ferryman/internal/daemon"
	"ferryman/internal/ferry"
	"ferryman/internal/store"
)

type ferryHarness struct {
	t           *testing.T
	tmp         string
	accts       *accounts.Accounts
	st          *store.Store
	w           *daemon.Worker
	defaultBook func(item map[string]any, agent, sid string, meta map[string]any, outcome string)
}

func newFerryHarness(t *testing.T, ferryFn daemon.FerryFunc) *ferryHarness {
	t.Helper()
	tmp := t.TempDir()
	accts, err := accounts.New(tmp)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.FerryProvider = "fake"
	providers := map[string]ferry.Provider{"fake": {
		Name: "fake", BaseURL: "http://127.0.0.1:9/v1", Model: "fake"}}
	w := daemon.NewWorker(cfg, st, accts, providers, ferryFn)
	h := &ferryHarness{t: t, tmp: tmp, accts: accts, st: st, w: w,
		defaultBook: w.BookHandoff}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	t.Cleanup(func() { cancel(); w.Stop() })
	return h
}

// writeSession tests/helpers.py::write_session 的最小形（转录路径 + mtime=now）。
func (h *ferryHarness) writeSession(sid, cwd string) string {
	h.t.Helper()
	d := filepath.Join(h.tmp, "projects", "C--proj")
	if err := os.MkdirAll(d, 0o755); err != nil {
		h.t.Fatal(err)
	}
	f := filepath.Join(d, sid+".jsonl")
	j := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			h.t.Fatal(err)
		}
		return string(b)
	}
	nowIso := time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")
	lines := []string{
		j(map[string]any{"type": "user", "timestamp": nowIso, "cwd": cwd,
			"sessionId": sid, "message": map[string]any{"role": "user", "content": "做点活"}}),
		j(map[string]any{"type": "assistant", "timestamp": nowIso,
			"message": map[string]any{"role": "assistant",
				"content": []any{map[string]any{"type": "text", "text": "干完了"}},
				"usage": map[string]any{"input_tokens": 2000,
					"cache_read_input_tokens": 100, "cache_creation_input_tokens": 0,
					"output_tokens": 5}}}),
		j(map[string]any{"type": "ai-title", "aiTitle": "集成测试会话"}),
	}
	if err := os.WriteFile(f, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		h.t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(f, now, now); err != nil {
		h.t.Fatal(err)
	}
	return f
}

// enqueue 守望入队腿的直驱形（item 四键与生产装配一致）。
func (h *ferryHarness) enqueue(sid, transcriptPath, cwd string) bool {
	return h.w.Enqueue(map[string]any{"transcript_path": transcriptPath,
		"agent": "cc", "session_id": sid, "cwd": cwd})
}

// succFerry 恒成功替身（假 md+meta）。
var succFerry = func(path string, pr ferry.Provider, timeoutS float64,
	agent string) (string, map[string]any, error) {
	md := "[Ferryman 交接 · 会话 集成测试会话]\n\n<<<INJECT>>>\n注入层：干完了\n<<</INJECT>>\n\n# 全文\n干完了\n"
	meta := map[string]any{
		"title": "集成测试会话", "mode": "L1", "wall_s": 1.2,
		"covers_until_iso": time.Now().UTC().Format("2006-01-02T15:04:05.000000Z"),
		"model":            "glm-5.3",
		"usage": map[string]any{"prompt_tokens": float64(100),
			"completion_tokens": float64(50)},
	}
	return md, meta, nil
}

// explodingFerry 恒败替身。
var explodingFerry = func(string, ferry.Provider, float64,
	string) (string, map[string]any, error) {
	return "", nil, fmt.Errorf("provider down")
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("waitFor 条件超时")
}

// ---- Python: test_accounts.py::test_ferry_completion_books_handoff ----

func TestFerryCompletionBooksHandoff(t *testing.T) {
	// 端到端：合成会话达总结阈值 → fake 摆渡 → 账本出现 handoff 流水。
	h := newFerryHarness(t, succFerry)
	f := h.writeSession("acct1", "C:/proj")
	if !h.enqueue("acct1", f, "C:/proj") {
		t.Fatal("入队失败")
	}
	waitFor(t, 10*time.Second, func() bool {
		return len(h.accts.Read(accounts.ReadOpts{Kind: "handoff"})) > 0
	})
	e := h.accts.Read(accounts.ReadOpts{Kind: "handoff"})[0]
	if e["session_id"] != "acct1" {
		t.Fatalf("session_id = %v, want acct1", e["session_id"])
	}
	if lin, _ := e["lineage_id"].(string); !strings.HasSuffix(lin, "acct1.jsonl") {
		t.Fatalf("lineage_id = %v, want 归一化 transcript 路径尾缀 acct1.jsonl", e["lineage_id"])
	}
	if e["provider"] != "fake" {
		t.Fatalf("provider = %v, want fake", e["provider"])
	}
	switch e["outcome"] { // outcome 三选一
	case "fresh", "skeleton", "failed":
	default:
		t.Fatalf("outcome = %v, want fresh|skeleton|failed", e["outcome"])
	}
	for _, banned := range []string{"content", "md"} { // 隐私不变量：无正文
		if _, exists := e[banned]; exists {
			t.Fatalf("隐私不变量：行不得含 %s 键: %v", banned, e)
		}
	}
}

// ---- Python: test_accounts.py::test_failed_ferry_books_exactly_one_row ----

func TestFailedFerryBooksExactlyOneRow(t *testing.T) {
	// 终审：失败摆渡只记一行 outcome=failed——骨架产物不另记行（append-only
	// 从零起账，双行无法事后修复）。
	h := newFerryHarness(t, explodingFerry)
	sid := "acct-fail1"
	f := h.writeSession(sid, "C:/proj")
	h.enqueue(sid, f, "C:/proj")
	waitFor(t, 10*time.Second, func() bool { // 骨架降级未发生则失败
		for _, e := range h.st.RestoreCandidates("cc", "C:/proj") {
			if e.SessionID == sid && e.Status == "skeleton" {
				return true
			}
		}
		return false
	})
	waitFor(t, 10*time.Second, func() bool { // 失败行未入账则失败
		return len(h.accts.Read(accounts.ReadOpts{Kind: "handoff", Session: sid})) > 0
	})
	time.Sleep(500 * time.Millisecond) // 留出潜在第二行落盘的窗口（RED 期双行必现）
	rows := h.accts.Read(accounts.ReadOpts{Kind: "handoff", Session: sid})
	if len(rows) != 1 {
		t.Fatalf("失败摆渡应只记一行，实记 %d 行", len(rows))
	}
	if rows[0]["outcome"] != "failed" {
		t.Fatalf("outcome = %v, want failed", rows[0]["outcome"])
	}
}

// ---- Python: test_accounts.py::test_booking_failure_never_breaks_ferry ----

func TestBookingFailureNeverBreaksFerry(t *testing.T) {
	// 记账抛异常（坏价格表）时摆渡照常产出交接——骨架兜底不变量优先。
	// Python monkeypatch FerryWorker._book_handoff 的 Go 形 = 换 Worker 的
	// BookHandoff 缝为炸点；工人线程未死的 Go 形 = 炸点后继续完成下一单
	// （并发 1 串行队列：下一单落地即工人存活的直接证明）。
	h := newFerryHarness(t, succFerry)
	h.w.BookHandoff = func(map[string]any, string, string, map[string]any, string) {
		panic("坏 [prices.*] TOML")
	}
	f := h.writeSession("acct-boom", "C:/proj")
	h.enqueue("acct-boom", f, "C:/proj")
	waitFor(t, 10*time.Second, func() bool { // 摆渡产物存在（fresh 或 skeleton）
		return len(h.st.RestoreCandidates("cc", "C:/proj")) > 0
	})
	// worker 线程未死（Go 形）：恢复缺省记账后下一单照常走完整链
	h.w.BookHandoff = h.defaultBook
	f2 := h.writeSession("acct-boom2", "C:/proj")
	h.enqueue("acct-boom2", f2, "C:/proj")
	waitFor(t, 10*time.Second, func() bool {
		for _, e := range h.st.RestoreCandidates("cc", "C:/proj") {
			if e.SessionID == "acct-boom2" {
				return true
			}
		}
		return false
	})
	// 记账在保存之后落盘（先 save 后 book）——等行到位再断言
	waitFor(t, 10*time.Second, func() bool {
		return len(h.accts.Read(accounts.ReadOpts{Kind: "handoff", Session: "acct-boom2"})) == 1
	})
	rows := h.accts.Read(accounts.ReadOpts{Kind: "handoff", Session: "acct-boom2"})
	if rows[0]["outcome"] != "fresh" {
		t.Fatalf("恢复记账后下一单应完整入账 fresh: %v", rows)
	}
}
