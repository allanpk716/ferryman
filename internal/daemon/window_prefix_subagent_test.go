package daemon

// 终局评审修复②的回归测试（夜链 20260920-114721）：票01 起子代理 usage 行随
// 父 sid 入账，windowPrefixLocked 的账本回落（peak_ctx==0 时取"开窗前该会话
// 最后一条 usage 行"）必须只看主会话行——子代理行的 input+cache_read+creation
// 是子代理自己转录的前缀规模，不代表主会话前缀；全为子行时回落 0，不得把
// 未过滤全量当兜底重新引入。

import (
	"testing"

	"ferryman/internal/accounts"
	"ferryman/internal/config"
	"ferryman/internal/ledger"
)

func prefixDaemon(t *testing.T) (*Daemon, *accounts.Accounts) {
	t.Helper()
	accts, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := NewDaemon(config.Default(), ledger.New(), nil,
		func(*ledger.SessionState) bool { return true }, accts, 0, nil)
	return d, accts
}

func recUsage(t *testing.T, a *accounts.Accounts, sid, sub string, ts float64,
	in, cr, cc int64) {
	t.Helper()
	_, err := a.Record("usage", ts, accounts.Fields{
		"agent": "cc", "session_id": sid, "lineage_id": "L", "project": "P",
		"subagent": sub, "model": "m", "title": "t",
		"input_tokens": in, "cache_read_tokens": cr,
		"cache_creation_tokens": cc, "output_tokens": 1, "offset": 0,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWindowPrefixFallbackSkipsSubagentRows(t *testing.T) {
	d, a := prefixDaemon(t)
	sid := "win-pfx-1"
	const opened = 1000.0
	recUsage(t, a, sid, "", 900, 30_000, 50_000, 2_000)   // 主行：82k
	recUsage(t, a, sid, "agent-x", 950, 1_000, 2_000, 0)  // 子行更晚：3k（渗入则取它）
	d.windowsMu.Lock()
	got := d.windowPrefixLocked(0, sid, opened)
	d.windowsMu.Unlock()
	if got != 82_000 {
		t.Errorf("windowPrefixLocked = %d, want 82000（子行不得替换回落值）", got)
	}
}

func TestWindowPrefixFallbackAllSubagentRowsGivesZero(t *testing.T) {
	d, a := prefixDaemon(t)
	sid := "win-pfx-2"
	const opened = 1000.0
	recUsage(t, a, sid, "agent-x", 900, 1_000, 2_000, 0)
	recUsage(t, a, sid, "agent-y", 950, 500, 500, 0)
	d.windowsMu.Lock()
	got := d.windowPrefixLocked(0, sid, opened)
	d.windowsMu.Unlock()
	if got != 0 {
		t.Errorf("windowPrefixLocked = %d, want 0（只剩子行时回落 0，不得回退未过滤全量）", got)
	}
}
