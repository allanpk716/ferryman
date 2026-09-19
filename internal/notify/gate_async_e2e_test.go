package notify_test

// 票14 回填：tests/test_notify.py::test_gate_block_fires_notification_async
// 1:1——gate block → 异步 notify_block（Python 线程语义 → Go goroutine），
// 文案带交接路径且路径是交接文件（不含 session id）。
//
// 本文件为 Go 外部测试包：daemon → notify 是生产依赖方向，内部测试包引用
// daemon 会成环（accounts/window_e2e_test.go 同惯例）。Python 版以
// monkeypatch notify_mod.notify_block 注入录制器；Go 版同位注入
// Daemon.NotifyBlock seam（gate.go：nil 回落真通道）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/daemon"
	"ferryman/internal/ledger"
	"ferryman/internal/store"
)

func TestGateBlockFiresNotificationAsync(t *testing.T) {
	tmp := t.TempDir()
	st, err := store.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	acc, err := accounts.New(tmp) // 记账尽力而为，不影响决策
	if err != nil {
		t.Fatal(err)
	}
	led := ledger.New()
	cfg := config.Default()
	cfg.GateCC = "enforce"
	// Python h.cfg.notify = NotifyCfg(enabled=True, pushover_token="t",
	// pushover_user="u")；toast 关（不出网不弹窗，通道本体由 notify 包测试钉住）。
	cfg.Notify = config.NotifyCfg{Enabled: true, Pushover: true,
		PushoverToken: "t", PushoverUser: "u", Toast: false}
	d := daemon.NewDaemon(cfg, led, st,
		func(*ledger.SessionState) bool { return true }, acc, 0, nil)

	fired := make(chan [5]string, 4)
	d.NotifyBlock = func(handoffPath, agent, sessionID, project, sessionTitle string, _ *config.Config) {
		fired <- [5]string{handoffPath, agent, sessionID, project, sessionTitle}
	} // monkeypatch notify_block 同位（票08 加宽：project/sessionTitle）

	// 造一个已达拦截阈值、有有效交接的会话（enforce 下必被拦）。
	sid := "notify-0001"
	proj := filepath.Join(tmp, "proj")
	p := filepath.Join(tmp, sid+".jsonl")
	if err := os.WriteFile(p,
		[]byte("{\"type\":\"user\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"hi\"}]}}\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	led.TouchFull("cc", sid, p, clock.Now()-2500, 10, proj, "", 99999, 0)
	covers := time.Now().UTC().Format("2006-01-02T15:04:05Z07:00")
	st.SaveHandoff(sid, "cc", proj, "t", covers, "fresh", "md")

	body := map[string]any{"agent": "cc", "session_id": sid,
		"transcript_path": p, "cwd": proj, "prompt": "继续"}
	if r := d.Gate(body); r["decision"] != "block" {
		t.Fatalf("应 block: %v", r)
	}
	select { // 异步线程已触发（wait_for fired 同义：goroutine 到达即收）
	case call := <-fired:
		handoffPath, agent, sess := call[0], call[1], call[2]
		if strings.Contains(handoffPath, sid) { // 路径是交接文件
			t.Fatalf("handoff_path 不得含 session id: %s", handoffPath)
		}
		if agent != "cc" || sess != sid {
			t.Fatalf("agent/session_id = %s/%s, want cc/%s", agent, sess, sid)
		}
		if projGot := call[3]; projGot != proj { // 票08：项目名随 seam 传到（标题降级链入参）
			t.Fatalf("project = %q, want %q", projGot, proj)
		}
		if !strings.HasPrefix(handoffPath, filepath.Join(tmp, "data", "handoffs")) {
			t.Fatalf("handoff_path 应指向 handoffs 目录: %s", handoffPath)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("异步通知未触发（goroutine 未在超时内到达）")
	}
}
