package daemon

// notify_wiring_test.go — 票08：真实调用点接线（watcher.qwatchAlert →
// notify.NotifyAlert）。钉死：推送标题走 BuildTitle 降级链（项目名＋台账
// 会话标题，不再含裸 session id）；正文＝事件名＋原消息＋尾部 sid 小字。
// Pushover 走 httptest 假端点收包（不出网、不弹 toast）。

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"ferryman/internal/config"
	"ferryman/internal/ledger"
	"ferryman/internal/notify"
)

func TestQwatchAlertWiresBuildTitleAndSidTail(t *testing.T) {
	got := make(chan url.Values, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("表单解析失败: %v", err)
			return
		}
		got <- r.PostForm
		w.WriteHeader(200)
	}))
	defer srv.Close()
	old := notify.PushoverURL
	notify.PushoverURL = srv.URL
	t.Cleanup(func() { notify.PushoverURL = old })

	cfg := mergeCfg()
	cfg.Notify = config.NotifyCfg{Enabled: true, Pushover: true,
		PushoverToken: "tok", PushoverUser: "usr", Toast: false}
	led := ledger.New()
	_, w := newWaitDaemonAndWatcher(cfg, led, nil, nil)
	st, _ := bareSession(t, led, t.TempDir(), "3f9a2c1e-8b7d-4a6f")
	led.Mu().Lock() // Title/Cwd 可变字段：锁内写入（共享引用纪律）
	st.Cwd = "C:/WorkSpace/agent/Ferryman"
	st.Title = "心跳保真实验"
	led.Mu().Unlock()

	w.qwatchAlert(st, "等待窗心跳熔断", "1 跳 MISS：停本窗剩余跳") // 真实调用点

	select {
	case form := <-got:
		title, body := form.Get("title"), form.Get("message")
		if title != "Ferryman｜Ferryman：心跳保真实验" {
			t.Fatalf("title = %q, want 降级链构造标题", title)
		}
		if strings.Contains(title, "3f9a2c1e") {
			t.Fatal("标题不得含裸 session id")
		}
		if !strings.HasPrefix(body, "等待窗心跳熔断：1 跳 MISS：停本窗剩余跳") {
			t.Fatalf("正文应以事件名＋原消息开头: %q", body)
		}
		// sid 小字截 8 rune（与控制台日志 runeCap8 同口径）。
		if !strings.HasSuffix(body, "\n(sid=3f9a2c1e)") {
			t.Fatalf("正文尾部缺 sid 小字: %q", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("通知未到达假端点（异步 goroutine 未在超时内发送）")
	}
}

func TestQwatchAlertDisabledStaysSilent(t *testing.T) {
	// enabled=false 全静默（旁路纪律回归）：不发、不炸。
	cfg := mergeCfg()
	cfg.Notify = config.NotifyCfg{Enabled: false}
	led := ledger.New()
	_, w := newWaitDaemonAndWatcher(cfg, led, nil, nil)
	st, _ := bareSession(t, led, t.TempDir(), "wl-notify-2")
	w.qwatchAlert(st, "等待窗心跳熔断", "不应外发") // 不 panic 即可
}
