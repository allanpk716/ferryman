// serve_drain_source_test.go — 票03（spec 错误契约热修 1/5 的 daemon 侧）验收钉子：
//   - 排水接线：daemon 关停先 dockSrv.Shutdown（上限取 [dock].drain_timeout_s）
//     再 Close 兜底——在途流按住时不得提前返回；到期必在上限内返回（不悬挂）
//     且端口硬收；dockSrv 为 nil 不空引用。真实监听 + Start 生命周期（与
//     serveConfig 生产接线同形，dock 包票02 测试同款假上游）。
//   - 关停来源（票03 扩票路径后三源皆真源头）：/shutdown 端点源、托盘退出
//     预记源（NoteShutdownSource → Done 汇聚点消费）与「无源直取消不误记」
//     （旧排除法兜底已废）各经真 serveConfig e2e 断言日志形态；Ctrl+C 源在
//     信号观察者层单测（真信号注入在 Windows 测试进程不可靠，观察者逻辑以
//     自有通道驱动——装配侧仅 signal.Notify 注册一行，不在单测覆盖内）。托盘
//     GUI 点击链（cmd/ferryman）不可单测，托盘源仅人工冒烟可验。
package daemon

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/dock"
)

// ---- 排水接线（shutdownDockGracefully） ----

// newDrainDock 起一个真实监听的记账模式渡口（Start 生命周期，serveConfig 同形）
// ＋「首帧即发、按住等释放」的假上游。返回（渡口、渡口端口）。hold 由调用方
// 关闭以放行在途流（或 defer 关闭兜底）。
func newDrainDock(t *testing.T, hold <-chan struct{}) (*dock.Server, int) {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\"}\n\n")
		w.(http.Flusher).Flush()
		<-hold
	}))
	t.Cleanup(backend.Close)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // 让渡给渡口绑定（dock 包测试同款）

	acc, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := dock.NewWithOptions(fmt.Sprintf("127.0.0.1:%d", port), backend.URL,
		dock.Options{Accounts: acc})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("渡口 Start: %v", err)
	}
	return srv, port
}

// drainClientResult 在途流式请求的收割形状。
type drainClientResult struct {
	status int
	body   []byte
	err    error
}

// drainStreamIn 后台经渡口发流式 messages 请求并收全响应（与排水关停并行）。
func drainStreamIn(t *testing.T, port int) <-chan drainClientResult {
	t.Helper()
	body := []byte(`{"model":"claude-opus-5","stream":true,"max_tokens":16,` +
		`"metadata":{"session_id":"drain-wiring"},"messages":[]}`)
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/v1/messages", port), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "claude-cli/2.0.0 (external)")
	req.Header.Set("Anthropic-Beta", "claude-code-20250219")
	done := make(chan drainClientResult, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- drainClientResult{err: err}
			return
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		done <- drainClientResult{status: resp.StatusCode, body: b}
	}()
	return done
}

// drainWaitPortClosed 轮询等渡口端口不再监听（Close 兜底已收的落盘证据）。
func drainWaitPortClosed(t *testing.T, port int) {
	t.Helper()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, derr := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if derr != nil {
			return
		}
		c.Close()
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("排水关停后渡口端口仍在监听（Close 兜底未收）")
}

// TestShutdownDockGracefullyWaitsForInFlight 在途流未完时排水关停不得提前返回
// （上限时钟在走）；在途自然跑完后返回，客户端收完整响应、无注入错误事件，
// Close 兜底随后收口（端口不再监听）。
func TestShutdownDockGracefullyWaitsForInFlight(t *testing.T) {
	hold := make(chan struct{})
	srv, port := newDrainDock(t, hold)
	done := drainStreamIn(t, port)

	time.Sleep(150 * time.Millisecond) // 流已建立（首帧过闸、在途在册）

	returned := make(chan struct{})
	go func() { shutdownDockGracefully(srv, 5); close(returned) }()

	select {
	case <-returned:
		t.Fatal("在途流未完排水关停提前返回（上限应为 5s）")
	case <-time.After(300 * time.Millisecond):
	}

	close(hold) // 上游放行 → 在途自然跑完
	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("在途跑完后排水关停未返回")
	}
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("客户端收尾失败: %v", r.err)
		}
		if !strings.Contains(string(r.body), "message_start") {
			t.Fatalf("在途首帧丢失: %q", r.body)
		}
		if strings.Contains(string(r.body), "event: error") {
			t.Fatalf("自然结束不应注入错误事件: %q", r.body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("3s 内客户端未收完整响应")
	}
	drainWaitPortClosed(t, port)
}

// TestShutdownDockGracefullyDeadlineBoundedAndHardCloses 到期未完：排水关停必
// 在上限内返回（不悬挂）、客户端收 Server 内部合成的流内错误事件（daemon 不
// 重复造收尾），Close 兜底硬收端口。
func TestShutdownDockGracefullyDeadlineBoundedAndHardCloses(t *testing.T) {
	hold := make(chan struct{})
	defer close(hold) // 兜底放行（防上游 handler 泄漏；排水收尾应先行）
	srv, port := newDrainDock(t, hold)
	done := drainStreamIn(t, port)

	time.Sleep(150 * time.Millisecond) // 流已建立

	started := time.Now()
	returned := make(chan struct{})
	go func() { shutdownDockGracefully(srv, 0.25); close(returned) }()
	select {
	case <-returned:
	case <-time.After(6 * time.Second):
		t.Fatal("排水到期关停未在上限内返回（悬挂）")
	}
	if el := time.Since(started); el < 200*time.Millisecond {
		t.Fatalf("排水关停过早返回（%v）——上限时钟未生效", el)
	}
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("客户端收尾失败: %v", r.err)
		}
		if !strings.Contains(string(r.body), "event: error") {
			t.Fatalf("到期收尾未见流内错误事件（收尾应走 Server 内部）: %q", r.body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("3s 内连接未收尾")
	}
	drainWaitPortClosed(t, port)
}

// TestShutdownDockGracefullyNilServerNoPanic 渡口未启用（dockSrv nil，含
// cfg.Dock == nil 与构造降级两形态）关停路径不空引用、即返。
func TestShutdownDockGracefullyNilServerNoPanic(t *testing.T) {
	done := make(chan struct{})
	go func() { shutdownDockGracefully(nil, 180); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("nil 渡口排水关停未即返")
	}
}

// ---- 关停来源落日志 ----

// serveForSourceLog 起真 serveConfig（无 [dock]，密闭配置），等守护就绪并
// 返回（取消函数、退出码通道、token）。
func serveForSourceLog(t *testing.T) (cancel context.CancelFunc, codeCh chan int, port int, token string) {
	t.Helper()
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "data")
	port = freePort(t)
	cfg := serveTestCfg(t, port, dataDir)
	ctx, cancel := context.WithCancel(context.Background())
	codeCh = make(chan int, 1)
	go func() { codeCh <- serveConfig(cfg, ctx, "dev") }()
	waitForCond(t, 10*time.Second, func() bool {
		select {
		case code := <-codeCh:
			t.Fatalf("serveConfig 提前退出: %d", code)
		default:
		}
		b, err := os.ReadFile(filepath.Join(dataDir, "daemon.token"))
		if err != nil {
			return false
		}
		token = strings.TrimSpace(string(b))
		return token != "" && AlreadyRunning(port, token)
	})
	return cancel, codeCh, port, token
}

// waitSourceExit 断言 serveConfig 在时限内以 0 退出（优雅停走完）。
func waitSourceExit(t *testing.T, codeCh chan int) {
	t.Helper()
	select {
	case code := <-codeCh:
		if code != 0 {
			t.Fatalf("serveConfig 退出码 = %d, want 0（优雅停）", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveConfig 未在时限内优雅停")
	}
}

// TestShutdownSourceLogEndpointE2E /shutdown 端点源：经真端点触发关停 →
// 日志恰有端点源一行（来源可区分、首源锁存不重复）。
func TestShutdownSourceLogEndpointE2E(t *testing.T) {
	cancel, codeCh, port, token := serveForSourceLog(t)
	defer cancel()
	col := startStdoutCapture(t)
	if code, got := postRaw(t, port, "/shutdown", token, nil); code != http.StatusOK {
		t.Fatalf("POST /shutdown = %d %q, want 200", code, got)
	}
	waitSourceExit(t, codeCh)
	col.waitContains(t, "关停来源: shutdown端点", 5*time.Second)
	out := col.snapshot()
	if strings.Contains(out, "关停来源: 托盘退出") || strings.Contains(out, "关停来源: Ctrl+C中断") {
		t.Fatalf("来源日志不唯一（应仅端点源一行）:\n%q", out)
	}
}

// TestShutdownSourceLogTrayNoteE2E 托盘退出预记源（票03 扩票路径，daemon 侧
// 消费机制）：生产中 cmd/ferryman 托盘「退出」点击处预记（GUI 链不可单测，
// 此处模拟同形调用）后上层 stop() 直取消 → Done 汇聚点消费预记，日志恰有
// 托盘源一行且首源锁存不重复。
func TestShutdownSourceLogTrayNoteE2E(t *testing.T) {
	t.Cleanup(func() { externalShutdownSource.Store("") }) // 预记不外泄他测
	cancel, codeCh, _, _ := serveForSourceLog(t)
	NoteShutdownSource("托盘退出") // 模拟 cmd/ferryman 点击处预记（先于 stop()）
	col := startStdoutCapture(t)
	cancel() // 上层 stop() → ctx 取消（与生产 stop() 同形）
	waitSourceExit(t, codeCh)
	col.waitContains(t, "关停来源: 托盘退出", 5*time.Second)
	out := col.snapshot()
	if strings.Contains(out, "关停来源: shutdown端点") || strings.Contains(out, "关停来源: Ctrl+C中断") {
		t.Fatalf("来源日志不唯一（应仅托盘源一行）:\n%q", out)
	}
}

// TestShutdownSourceLogNoSourceNoMislog 无源直取消不误记（旧排除法兜底已废）：
// 无信号、无端点、无托盘预记，上层直 cancel → 来源日志一行都没有——托盘源
// 由真源头自报，daemon 侧不再从「端点与信号皆未现」倒推归因。
func TestShutdownSourceLogNoSourceNoMislog(t *testing.T) {
	externalShutdownSource.Store("") // 防同进程他测残留预记（次序无关）
	cancel, codeCh, _, _ := serveForSourceLog(t)
	col := startStdoutCapture(t)
	cancel() // 上层直取消（无信号、无端点、无预记）
	waitSourceExit(t, codeCh)
	time.Sleep(150 * time.Millisecond) // 留暴露窗：误记若有，此刻应已现形
	if out := col.snapshot(); strings.Contains(out, "关停来源: ") {
		t.Fatalf("无源直取消不应记任何来源日志:\n%q", out)
	}
}

// stubSourceLog 关停来源日志缝换桩（收形到带锁切片；t.Cleanup 还原）。
func stubSourceLog(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var got []string
	old := logShutdownSource
	logShutdownSource = func(src string) {
		mu.Lock()
		got = append(got, src)
		mu.Unlock()
	}
	t.Cleanup(func() { logShutdownSource = old })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

// TestWatchInterruptOnceSignalSource Ctrl+C 源观察者：信号到达即记一行；
// ctx 结束（无信号）不记——早退路径零误报。
func TestWatchInterruptOnceSignalSource(t *testing.T) {
	snap := stubSourceLog(t)

	ch := make(chan os.Signal, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go watchInterruptOnce(ctx, ch, (&sourceMarker{}).mark)
	ch <- syscall.SIGINT // 观察者逻辑单测：自有通道驱动，不打真信号
	waitForCond(t, 2*time.Second, func() bool { return len(snap()) == 1 })
	if got := snap(); got[0] != "Ctrl+C中断" {
		t.Fatalf("信号源日志 = %v, want [Ctrl+C中断]", got)
	}
	cancel()

	// ctx 结束不记
	snap2Got := stubSourceLog(t)
	ctx2, cancel2 := context.WithCancel(context.Background())
	go watchInterruptOnce(ctx2, make(chan os.Signal, 1), (&sourceMarker{}).mark)
	cancel2()
	time.Sleep(100 * time.Millisecond)
	if got := snap2Got(); len(got) != 0 {
		t.Fatalf("ctx 结束不应记来源: %v", got)
	}
}

// TestSourceMarkerFirstWins 首源锁存：一次关停只记首个到源，后到者静默。
func TestSourceMarkerFirstWins(t *testing.T) {
	snap := stubSourceLog(t)
	m := &sourceMarker{}
	m.mark("shutdown端点")
	m.mark("Ctrl+C中断")
	m.mark("托盘退出")
	if got := snap(); len(got) != 1 || got[0] != "shutdown端点" {
		t.Fatalf("首源锁存失效: %v", got)
	}
}
