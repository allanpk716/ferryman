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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/daemon"
	"ferryman/internal/ledger"
	"ferryman/internal/notify"
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

// TestGateBlockDispatchTieredAsync 票02：真实 NotifyBlock 通道（Daemon.NotifyBlock
// seam 留 nil → 回落真通道）走 block 事件分派——缺省 toast（D2：人被拦时必在
// 电脑前，手机不发）：Pushover 假端点零请求；配置显式 both 后手机收到一条
//（正向对照，证明通路在而非"碰巧没发"）。Toast 通道关＝不弹真气泡；文案
// 本体由 notify 包测试钉死，此处只验分派计数。
func TestGateBlockDispatchTieredAsync(t *testing.T) {
	var mu sync.Mutex
	var reqs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		reqs++
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()
	old := notify.PushoverURL
	notify.PushoverURL = srv.URL
	t.Cleanup(func() { notify.PushoverURL = old })

	tmp := t.TempDir()
	st, err := store.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	acc, err := accounts.New(tmp)
	if err != nil {
		t.Fatal(err)
	}
	led := ledger.New()
	cfg := config.Default()
	cfg.GateCC = "enforce"
	// Events 缺省（nil → 回落内置缺省表 block=toast）；Toast 关＝不弹真气泡
	// 且不影响手机侧断言（通道开关是通道维度上限）。
	cfg.Notify = config.NotifyCfg{Enabled: true, Pushover: true,
		PushoverToken: "t", PushoverUser: "u", Toast: false}
	d := daemon.NewDaemon(cfg, led, st,
		func(*ledger.SessionState) bool { return true }, acc, 0, nil) // seam 留 nil：回落真通道

	gate := func(sid string) map[string]any {
		t.Helper()
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
		return d.Gate(body)
	}
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return reqs
	}

	if r := gate("notify-tier-1"); r["decision"] != "block" {
		t.Fatalf("应 block: %v", r)
	}
	time.Sleep(400 * time.Millisecond) // 负面断言：给足异步排空窗口
	if n := count(); n != 0 {
		t.Fatalf("缺省 block=toast：手机不应推送, got %d 次", n)
	}

	// 正向对照：显式 both → 手机恰一条（同为缺省静默则本断言抓"通路断"）。
	cfg.Notify.Events = map[string]config.NotifyEventTier{"block": config.NotifyEventBoth}
	if r := gate("notify-tier-2"); r["decision"] != "block" {
		t.Fatalf("应 block: %v", r)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if n := count(); n >= 1 {
			if n != 1 {
				t.Fatalf("显式 both 应恰一条, got %d", n)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("显式 both 未推送（真通道通路断）")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
