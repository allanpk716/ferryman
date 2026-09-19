package notify

// 规格：tests/test_notify.py 全部 7 例 1:1（T25：拦截通知旁路纪律）。
// 真发送全部走 httptest 假端点 + mock 命令执行（不弹真 toast、不出网）：
//   - Pushover：Python monkeypatch urlopen → Go 换包级 PushoverURL 指 httptest；
//   - Toast：Python monkeypatch subprocess.run → Go 换包级 runToast var
//     （票面指定的 seam，同位）；argv/timeout 断言转直测 toastCmd/常量；
//   - test_gate_block_fires_notification_async 为 gate 集成用例（Python Harness
//     起全链 daemon），归票 14/21 装配——按 accounts_test.go 惯例落 t.Skip 占位。

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ferryman/internal/config"
)

// pushStub httptest 假端点：记录请求，回固定 status；并换 PushoverURL（还原入
// t.Cleanup，monkeypatch.setattr 同位）。
type pushStub struct {
	srv    *httptest.Server
	mu     sync.Mutex
	reqs   int
	method string
	body   string
}

func startPushStub(t *testing.T, status int) *pushStub {
	t.Helper()
	ps := &pushStub{}
	ps.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ps.mu.Lock()
		ps.reqs++
		ps.method = r.Method
		ps.body = string(b)
		ps.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(ps.srv.Close)
	old := PushoverURL
	PushoverURL = ps.srv.URL
	t.Cleanup(func() { PushoverURL = old })
	return ps
}

func (ps *pushStub) count() int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.reqs
}

func (ps *pushStub) form(t *testing.T) url.Values {
	t.Helper()
	ps.mu.Lock()
	defer ps.mu.Unlock()
	v, err := url.ParseQuery(ps.body)
	if err != nil {
		t.Fatalf("表单体解析失败: %v", err)
	}
	return v
}

func (ps *pushStub) reset() {
	ps.mu.Lock()
	ps.reqs = 0
	ps.body = ""
	ps.mu.Unlock()
}

// mockToast 替换 runToast seam（记录脚本入参，还原入 t.Cleanup）；
// fn=nil 表示执行成功。返回收到的脚本切片。
func mockToast(t *testing.T, fn func(ps string) error) *[]string {
	t.Helper()
	calls := &[]string{}
	old := runToast
	runToast = func(ps string) error {
		*calls = append(*calls, ps)
		if fn != nil {
			return fn(ps)
		}
		return nil
	}
	t.Cleanup(func() { runToast = old })
	return calls
}

// ---------- 通道：Pushover ----------

func TestPushoverPostsCredentialsAndMessage(t *testing.T) {
	ps := startPushStub(t, 200)
	if !SendPushover("标题", "消息体", "tok", "usr") {
		t.Fatal("200 应回 true")
	}
	if ps.method != http.MethodPost {
		t.Fatalf("method = %s, want POST", ps.method)
	}
	form := ps.form(t) // 表单体是百分号编码，解码后断言
	if form.Get("token") != "tok" || form.Get("user") != "usr" {
		t.Fatalf("凭据缺失: %v", form)
	}
	if form.Get("title") != "标题" || form.Get("message") != "消息体" {
		t.Fatalf("标题/消息缺失: %v", form)
	}
	if form.Get("priority") != "1" {
		t.Fatalf("priority = %q, want 1", form.Get("priority"))
	}
	// Python 断言 timeout 非 None——Go 侧为包内常量，钉死 4s。
	if pushoverTimeout != 4*time.Second {
		t.Fatalf("pushoverTimeout = %v, want 4s", pushoverTimeout)
	}
}

func TestPushoverSwallowsAllFailures(t *testing.T) {
	// 已关闭的假端点 ≡ Python monkeypatch urlopen 抛 OSError（网络层故障）
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	dead := srv.URL
	srv.Close()
	old := PushoverURL
	PushoverURL = dead
	t.Cleanup(func() { PushoverURL = old })
	if SendPushover("t", "m", "tok", "usr") {
		t.Fatal("任何故障一律吞掉返回 false")
	}
}

// ---------- 通道：Windows Toast ----------

func TestToastInvokesPowershellWithEscapedXML(t *testing.T) {
	calls := mockToast(t, nil)
	if !SendToast("Ferryman 拦截", "交接: C:/x<&>y.md") {
		t.Fatal("应回 true")
	}
	if len(*calls) != 1 {
		t.Fatalf("调用次数 = %d, want 1", len(*calls))
	}
	script := (*calls)[0]
	if !strings.Contains(script, "Ferryman 拦截") {
		t.Fatal("脚本应含标题")
	}
	if !strings.Contains(script, "&amp;") || !strings.Contains(script, "&lt;") {
		t.Fatal("脚本应含 XML 转义（& <）")
	}
	// Python 另断言 args[0]=="powershell" 且 timeout 非 None——执行层被 mock 后
	// 这两项转直测（不真执行）：
	if toastTimeout != 5*time.Second {
		t.Fatalf("toastTimeout = %v, want 5s", toastTimeout)
	}
	cmd := toastCmd("X")
	want := []string{"powershell", "-NoProfile", "-Command", "X"}
	if !slices.Equal(cmd.Args, want) {
		t.Fatalf("argv = %v, want %v", cmd.Args, want)
	}
}

func TestToastSwallowsFailures(t *testing.T) {
	mockToast(t, func(_ string) error {
		return context.DeadlineExceeded // ≡ Python TimeoutExpired
	})
	if SendToast("t", "m") {
		t.Fatal("超时/故障一律吞掉返回 false")
	}
}

// ---------- 组装：notify_block ----------

func TestNotifyBlockIncludesHandoffPathAndRespectsFlags(t *testing.T) {
	ps := startPushStub(t, 200)
	toastCalls := mockToast(t, nil)

	cfg := config.Default()
	cfg.Notify = config.NotifyCfg{Enabled: true, Pushover: true,
		PushoverToken: "t", PushoverUser: "u", Toast: true}
	NotifyBlock("C:/handoffs/h1.md", "cc", "s123", "", "", cfg)

	if len(*toastCalls) != 1 || !strings.Contains((*toastCalls)[0], "h1.md") {
		t.Fatalf("toast 应带交接路径，got %v", *toastCalls)
	}
	if ps.count() != 1 {
		t.Fatalf("pushover 应已发送，got %d 次", ps.count())
	}
	form := ps.form(t)
	if form.Get("title") != "Ferryman 拦截" {
		t.Fatalf("title = %q, want Ferryman 拦截", form.Get("title"))
	}
	// 票08 文案逐字：sid 移正文尾部小字（标题/正文主体不再内嵌裸 sid）。
	want := "会话闲置被拦，交接已生成：\nC:/handoffs/h1.md\n新会话发任意字即可取回上下文。\n(sid=s123)"
	if form.Get("message") != want {
		t.Fatalf("message = %q, want %q", form.Get("message"), want)
	}
	if !strings.HasSuffix(form.Get("message"), "(sid=s123)") {
		t.Fatal("正文尾部应含 sid 小字")
	}

	// 票08 协调者补线：项目名＋会话标题齐备时标题走降级链（用户痛点 4 的
	// 主场景——拦截通知可分辨哪个项目哪个会话）；project/title 皆空回落旧标题。
	ps.reset()
	NotifyBlock("C:/handoffs/h3.md", "cc", "s456", "proj-x", "心跳保真实验", cfg)
	form = ps.form(t)
	if form.Get("title") != "Ferryman｜proj-x：心跳保真实验" {
		t.Fatalf("title = %q, want Ferryman｜proj-x：心跳保真实验", form.Get("title"))
	}
	if !strings.HasSuffix(form.Get("message"), "(sid=s456)") {
		t.Fatal("正文尾部应含 sid 小字（带项目标题形态）")
	}

	cfg.Notify.Enabled = false // 总开关关 → 全静默
	ps.reset()
	*toastCalls = nil
	NotifyBlock("C:/h2.md", "cc", "s", "", "", cfg)
	if ps.count() != 0 || len(*toastCalls) != 0 {
		t.Fatalf("enabled=false 应全静默：push=%d toast=%d", ps.count(), len(*toastCalls))
	}
}

func TestNotifyBlockMissingPushoverCredentialsSkipsPush(t *testing.T) {
	t.Setenv("PUSHOVER_TOKEN", "") // monkeypatch.delenv 同效：回落取不到凭据
	t.Setenv("PUSHOVER_USER", "")
	ps := startPushStub(t, 200)
	toastCalls := mockToast(t, nil)
	cfg := config.Default()
	cfg.Notify = config.NotifyCfg{Enabled: true, Pushover: true,
		PushoverToken: "", PushoverUser: "", Toast: true}
	NotifyBlock("C:/h.md", "cc", "s", "", "", cfg)
	if len(*toastCalls) != 1 {
		t.Fatalf("toast 应已发送，got %d", len(*toastCalls))
	}
	if n := ps.count(); n != 0 { // 缺凭据 → 只走 toast
		t.Fatalf("pushover 不应发送，got %d 次", n)
	}
}

// ---------- 集成：gate block → 异步通知 ----------
//
// 票14 回填注：test_gate_block_fires_notification_async 已转绿——daemon→notify
// 生产依赖方向不可被内部测试包引用（成环），落在本目录外部测试包
// gate_async_e2e_test.go（Daemon.NotifyBlock seam + 最小装配器）。
