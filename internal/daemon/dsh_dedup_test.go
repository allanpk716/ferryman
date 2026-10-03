package daemon

// dsh_dedup_test.go — 票05 跨源 usage 去重钉子（链内修订：票04 评审发现
// 事件直报与 pollDsh 文件守望对同一 assistant/message 双计,通流量前必须消除）。
//
// 策略 (a) 事件接管单源化的验收四件：
//   - 双源同事件只出一行：事件口先接管（turn/start 标记）,同一份事件日志文件
//     里的 assistant/message 由事件口直报入账,文件守望面对该会话让位不重记；
//   - 未接管会话文件守望照常：无事件流量的会话目录照旧采集入账；
//   - daemon 重启不重采：接管表自账本回种（事件行 lineage 恒空可辨识）,
//     重启后文件尾（含接管期的旧内容）不被重放；
//   - 子会话同款：子会话直报（parent_session_id）标记子键,子目录文件面让位。

import (
	"testing"

	"ferryman/internal/accounts"
	"ferryman/internal/config"
	"ferryman/internal/ledger"
)

// 另一个主会话 id/头（未接管对照面用;dshMainID/dshMainHeader 复用 dsh_wiring_test.go）。
const dshAltID = "session-44444444-4444-4444-8444-444444444444"
const dshAltHeader = `{"type":"session","version":4,"id":"session-44444444-4444-4444-8444-444444444444","createdAt":1790905220031,"cwd":"C:\\proj","isSeeded":false,"delegationDepth":0}` + "\n"

// 事件口四列（snake 形,票04 契约）与文件面同一条事件（camel 形,事件日志本体）
// 的同一逻辑事件对：seq=3, 120/30000/5000/80。
const dedupUsageEvent = `{"type":"assistant/message","seq":3,"time":1790905227524,"data":{"message":{"source":{"kind":"model","provider":"x","model":"glm-5.3"}},"usage":{"inputTokens":120,"outputTokens":80,"cacheReadTokens":30000,"cacheWriteTokens":5000}}}` + "\n"

// newDshDedupEnv 票05 去重环境：Daemon（事件口）＋Watcher（文件守望）共享同一
// ledger/accounts——生产装配同构（serve.go:161 NewDaemon 与 :282 NewWatcher
// 同传 led/acc/d,接管表经 w.Daemon 互通）。
func newDshDedupEnv(t *testing.T, root, accDir string) (*Daemon, *Watcher, *accounts.Accounts) {
	t.Helper()
	acc, err := accounts.New(accDir)
	if err != nil {
		t.Fatal(err)
	}
	led := ledger.New()
	noopEnqueue := func(*ledger.SessionState) bool { return true }
	d := NewDaemon(config.Default(), led, nil, noopEnqueue, acc, 0, nil)
	cfgW := config.Default()
	cfgW.Watch.DshSessionsDir = root
	w := NewWatcher(cfgW, led, nil, noopEnqueue, 0, acc, d, nil, nil)
	return d, w, acc
}

// TestDshDedupDualSourceSingleRow 验收主钉：双源同事件只出一行。
// 插件真实时序——会话开轮 turn/start 先到（接管标记落）,其后同一条
// assistant/message 两面都看得见：事件口直报一行,文件面让位。
func TestDshDedupDualSourceSingleRow(t *testing.T) {
	root, accDir := t.TempDir(), t.TempDir()
	writeDshSession(t, root, dshMainID, dshMainHeader, dedupUsageEvent)
	d, w, acc := newDshDedupEnv(t, root, accDir)

	// ① 事件口通流量：turn/start（无 usage,但接管标记即落）。
	if r := d.DshEvent(map[string]any{"session_id": dshMainID, "event": "turn/start",
		"time": 1790905227000.0, "cwd": "C:/proj", "title": "接线"}); r["ok"] != true {
		t.Fatalf("turn/start 应 ok: %v", r)
	}
	// ② 事件口直报同一条 assistant/message（插件面唯一入账行）。
	d.DshEvent(map[string]any{"session_id": dshMainID, "event": "assistant/message",
		"time": 1790905227524.0, "model": "glm-5.3",
		"usage": map[string]any{"input_tokens": 120, "cache_read_tokens": 30000,
			"cache_creation_tokens": 5000, "output_tokens": 80}})
	// ③ 文件守望面看到同一条（文件=同一份事件日志）——接管后让位,零新增。
	w.pollDsh()

	rows := dshUsageRows(t, acc)
	if len(rows) != 1 {
		t.Fatalf("双源同事件 want 1 行, got %d: %+v", len(rows), rows)
	}
	if rows[0]["input_tokens"] != float64(120) || rows[0]["output_tokens"] != float64(80) {
		t.Fatalf("四列不符: %+v", rows[0])
	}
	// 登记面保留：接管会话仍 Touch（真转录路径收敛,观察窗照常）。
	st := w.Ledger.Get("dsh", dshMainID)
	if st == nil {
		t.Fatal("接管会话仍应 Touch 登记（只让位 usage 采集,不让位登记）")
	}

	// ④ 再轮仍单行（幂等）。
	w.pollDsh()
	if rows := dshUsageRows(t, acc); len(rows) != 1 {
		t.Fatalf("重复轮 want 1 行, got %d", len(rows))
	}
}

// TestDshDedupSparesUnfederatedSessions 未接管会话文件守望照常：
// 无事件流量的会话（插件未挂/未接管）文件面零改动。
func TestDshDedupSparesUnfederatedSessions(t *testing.T) {
	root, accDir := t.TempDir(), t.TempDir()
	writeDshSession(t, root, dshMainID, dshMainHeader, dedupUsageEvent)
	writeDshSession(t, root, dshAltID, dshAltHeader,
		`{"type":"assistant/message","seq":1,"time":1790905300000,"data":{"message":{"source":{"model":"glm-5.3"}},"usage":{"inputTokens":10,"outputTokens":5}}}`+"\n")
	d, w, acc := newDshDedupEnv(t, root, accDir)
	_ = d

	w.pollDsh()
	rows := dshUsageRows(t, acc)
	if len(rows) != 2 {
		t.Fatalf("未接管会话 want 2 行（文件面照常）, got %d: %+v", len(rows), rows)
	}
	for _, sid := range []string{dshMainID, dshAltID} {
		if w.Ledger.Get("dsh", sid) == nil {
			t.Errorf("%s 未登记", sid)
		}
	}
}

// TestDshDedupSurvivesRestart daemon 重启不重采：接管表自账本回种。
// 重启后新 Watcher 的文件断点停在接管前（事件行不入断点表）,若无接管表
// 回种会整段重放——本钉保证不重放。
func TestDshDedupSurvivesRestart(t *testing.T) {
	root, accDir := t.TempDir(), t.TempDir()
	writeDshSession(t, root, dshMainID, dshMainHeader, dedupUsageEvent)
	d1, w1, _ := newDshDedupEnv(t, root, accDir)

	// 接管 + 事件直报一行（文件面零行——文件断点表无该会话条目）。
	d1.DshEvent(map[string]any{"session_id": dshMainID, "event": "turn/start", "time": 1790905227000.0})
	d1.DshEvent(map[string]any{"session_id": dshMainID, "event": "assistant/message",
		"time": 1790905227524.0, "usage": map[string]any{"input_tokens": 120, "output_tokens": 80}})
	w1.pollDsh()

	// daemon 重启：同账本重建 Daemon（接管表回种）＋Watcher（断点从文件行恢复=0）。
	d2, w2, acc2 := newDshDedupEnv(t, root, accDir)
	w2.pollDsh()
	rows := dshUsageRows(t, acc2)
	if len(rows) != 1 {
		t.Fatalf("重启后 want 1 行（接管表回种,文件尾不重放）, got %d: %+v", len(rows), rows)
	}
	if w2.Ledger.Get("dsh", dshMainID) == nil {
		t.Error("重启后 Touch 照常")
	}
	_ = d2
}

// TestDshDedupChildSession 子会话同款：子会话直报（parent_session_id 随父入账）
// 标记子键,子目录文件面让位——随父入账不双计;重启后同款不重放。
func TestDshDedupChildSession(t *testing.T) {
	root, accDir := t.TempDir(), t.TempDir()
	writeDshSession(t, root, dshChildID, dshChildHeader,
		`{"type":"assistant/message","seq":1,"time":1790905300000,"data":{"message":{"source":{"model":"glm-5.3"}},"usage":{"inputTokens":500,"outputTokens":50,"cacheReadTokens":8000}}}`+"\n")
	d, w, acc := newDshDedupEnv(t, root, accDir)

	// 子会话直报：随父入账（session_id=父,subagent=子）＋子键接管标记。
	if r := d.DshEvent(map[string]any{"session_id": dshChildID, "parent_session_id": dshMainID,
		"event": "assistant/message", "time": 1790905300000.0,
		"usage": map[string]any{"input_tokens": 500, "cache_read_tokens": 8000,
			"output_tokens": 50}}); r["ok"] != true {
		t.Fatalf("子会话直报应 ok: %v", r)
	}
	w.pollDsh()
	rows := dshUsageRows(t, acc)
	if len(rows) != 1 {
		t.Fatalf("子会话双源 want 1 行, got %d: %+v", len(rows), rows)
	}
	if rows[0]["session_id"] != dshMainID || rows[0]["subagent"] != dshChildID {
		t.Fatalf("随父入账不符: %+v", rows[0])
	}

	// 重启后子目录同样不重放。
	_, w2, acc2 := newDshDedupEnv(t, root, accDir)
	w2.pollDsh()
	if rows := dshUsageRows(t, acc2); len(rows) != 1 {
		t.Fatalf("重启后子会话 want 1 行, got %d: %+v", len(rows), rows)
	}
}
