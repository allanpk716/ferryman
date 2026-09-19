package accounts_test

// 票14 回填：tests/test_accounts.py 的 3 个 gate/restore e2e 占位转绿
// （TestBlockBooksEntry / TestBypassBooksEntry / TestRestoreBooksInject）。
// 本文件为 Go 外部测试包——daemon → accounts 是生产依赖方向（装配器同
// window_e2e_test.go 惯例），扩 Gate/Restore 通道：
//   - d.Gate(body) = POST /gate（票14 gate.go；HTTP 面归票15）；
//   - d.Restore(agent, cwd, sessionID) = GET /restore（同上）。
// 交接产物 Python 版经守望摆渡链生成（wait_for 轮询）；Go 版直构
// Store.SaveHandoff（票面"最小测试装配器"口径）——闸门/归还的记账行为
// 与链路无关，行为等价且全确定。

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/daemon"
	"ferryman/internal/ledger"
	"ferryman/internal/store"
)

type gateHarness struct {
	accts *accounts.Accounts
	led   *ledger.Ledger
	st    *store.Store
	d     *daemon.Daemon
	tmp   string
}

func newGateHarness(t *testing.T) *gateHarness {
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
	led := ledger.New()
	cfg := config.Default()
	cfg.GateCC = "enforce"
	d := daemon.NewDaemon(cfg, led, st,
		func(*ledger.SessionState) bool { return true }, accts, 0, nil)
	return &gateHarness{accts: accts, led: led, st: st, d: d, tmp: tmp}
}

// staleBlockedBody Python _stale_blocked_session：造一个已达拦截阈值、有有效
// 交接的会话（enforce 下必被拦）。
func (h *gateHarness) staleBlockedBody(t *testing.T) map[string]any {
	t.Helper()
	sid := "acct2"
	p := filepath.Join(h.tmp, sid+".jsonl")
	if err := os.WriteFile(p,
		[]byte("{\"type\":\"user\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"hi\"}]}}\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	h.led.TouchFull("cc", sid, p, clock.Now()-2500, 10, "C:/proj", "", 99999, 0)
	covers := time.Now().UTC().Format("2006-01-02T15:04:05Z07:00")
	h.st.SaveHandoff(sid, "cc", "C:/proj", "t", covers, "fresh", "md")
	return map[string]any{"agent": "cc", "session_id": sid,
		"transcript_path": p, "cwd": "C:/proj", "prompt": "继续干活"}
}

// ---- Python: test_accounts.py::test_block_books_entry ----

func TestBlockBooksEntry(t *testing.T) {
	h := newGateHarness(t)
	body := h.staleBlockedBody(t)
	if r := h.d.Gate(body); r["decision"] != "block" {
		t.Fatalf("enforce+有效交接应 block: %v", r)
	}
	rows := h.accts.Read(accounts.ReadOpts{Kind: "block"})
	if len(rows) == 0 {
		t.Fatal("block 行未入账")
	}
	e := rows[len(rows)-1]
	if e["session_id"] != "acct2" {
		t.Fatalf("session_id = %v, want acct2", e["session_id"])
	}
	pt, _ := e["prefix_tokens"].(float64)
	// 票14 Minor 顺手清：阈值引用 config.Default()，不再硬编码 20000
	if !(pt >= float64(config.Default().Thresholds.MinCtxTokens) || pt == 0) { // peak_ctx 尽力而为
		t.Fatalf("prefix_tokens = %v, want ≥MIN_CTX 或 0", e["prefix_tokens"])
	}
	if e["idle_s"].(float64) <= 0 {
		t.Fatalf("idle_s = %v, want > 0", e["idle_s"])
	}
}

// ---- Python: test_accounts.py::test_bypass_books_entry ----

func TestBypassBooksEntry(t *testing.T) {
	h := newGateHarness(t)
	body := h.staleBlockedBody(t)
	body["prompt"] = "强续 无论如何继续"
	if r := h.d.Gate(body); r["decision"] != "allow" {
		t.Fatalf("强续应放行: %v", r)
	}
	rows := h.accts.Read(accounts.ReadOpts{Kind: "bypass"})
	if len(rows) == 0 {
		t.Fatal("bypass 行未入账")
	}
	if rows[len(rows)-1]["session_id"] != "acct2" {
		t.Fatalf("session_id = %v, want acct2", rows[len(rows)-1]["session_id"])
	}
}

// ---- Python: test_accounts.py::test_restore_books_inject ----

func TestRestoreBooksInject(t *testing.T) {
	h := newGateHarness(t)
	body := h.staleBlockedBody(t)
	if r := h.d.Gate(body); r["decision"] != "block" {
		t.Fatalf("应先被拦: %v", r)
	}
	r := h.d.Restore("cc", "C:/proj", "newsid")
	if ctx, _ := r["context"].(string); ctx == "" {
		t.Fatalf("context 不应为空: %v", r)
	}
	injects := h.accts.Read(accounts.ReadOpts{Kind: "inject"})
	if len(injects) == 0 {
		t.Fatal("inject 行未入账")
	}
	e := injects[len(injects)-1]
	if e["session_id"] != "newsid" {
		t.Fatalf("session_id = %v, want newsid", e["session_id"])
	}
	if e["tokens"].(float64) <= 0 || e["handoff_id"] == "" {
		t.Fatalf("tokens/handoff_id = %v/%v", e["tokens"], e["handoff_id"])
	}
	blocks := h.accts.Read(accounts.ReadOpts{Kind: "block"})
	if len(blocks) == 0 {
		t.Fatal("block 行未入账")
	}
	// R9：inject 与 block 同谱系（Q7 因果链）
	if e["lineage_id"] != blocks[len(blocks)-1]["lineage_id"] {
		t.Fatalf("inject lineage = %v, want 与 block 同谱系 %v",
			e["lineage_id"], blocks[len(blocks)-1]["lineage_id"])
	}
}
