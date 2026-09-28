// shutdown_test.go — 票02：优雅排水（错误契约热修 1）的验收钉子。
//   - 在途流进行中触发 Shutdown（短排水上限注入）→ 在途跑完、客户端收完整
//     响应、Shutdown 返回 nil；
//   - 到期未完 → 客户端收到 Anthropic 流内错误事件（event: error）后连接
//     收尾；行带 truncated；
//   - 到期·首字节未写出（闸门扣住 / 响应头未回）→ 504＋Anthropic 错误体；
//   - 真实监听（Start）路径：到期注入交付后硬收，端口不再监听。
// 时间参数经 ctx／包级 var 注入短值，测试不等真实 60s/180s。
package dock

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ferryman/internal/accounts"
)

// sseBackend 首帧即发、按住等释放、释放后补发尾帧的假上游（hold=nil 跳过
// 按住直接收尾）。
func sseBackend(t *testing.T, firstEvent string, hold <-chan struct{}, tailEvent string) *httptest.Server {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, firstEvent)
		w.(http.Flusher).Flush()
		if hold != nil {
			<-hold
		}
		if tailEvent != "" {
			_, _ = io.WriteString(w, tailEvent)
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(backend.Close)
	return backend
}

// clientResult 异步客户端请求的收割形状。
type clientResult struct {
	status int
	body   []byte
	err    error
}

// doStreamIn 后台发流式请求并收全响应（与 Shutdown 并行）。
func doStreamIn(t *testing.T, url string) <-chan clientResult {
	t.Helper()
	done := make(chan clientResult, 1)
	go func() {
		resp, err := http.DefaultClient.Do(ccRequest(t, url, streamBody("drain")))
		if err != nil {
			done <- clientResult{err: err}
			return
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		done <- clientResult{status: resp.StatusCode, body: b}
	}()
	return done
}

// TestShutdownWaitsForInFlightStream 在途流未完时 Shutdown 不得提前返回；
// 流自然跑完后客户端收完整响应、Shutdown 返回 nil、行不带 truncated。
func TestShutdownWaitsForInFlightStream(t *testing.T) {
	release := make(chan struct{})
	backend := sseBackend(t, "event: message_start\ndata: {\"type\":\"message_start\"}\n\n", release,
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	srv, acc, front := newRecordingFront(t, backend.URL)
	done := doStreamIn(t, front+"/v1/messages")

	time.Sleep(150 * time.Millisecond) // 等首帧确实在途（handler 已进入 record 路径并注册）

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shDone := make(chan error, 1)
	go func() { shDone <- srv.Shutdown(ctx) }()

	select {
	case err := <-shDone:
		t.Fatalf("在途流未完 Shutdown 提前返回: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	close(release)
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("客户端收尾失败: %v", r.err)
		}
		if !strings.Contains(string(r.body), "message_start") ||
			!strings.Contains(string(r.body), "message_stop") {
			t.Fatalf("在途流未跑完（客户端未收完整响应）: %q", r.body)
		}
		if strings.Contains(string(r.body), "event: error") {
			t.Fatalf("自然结束不应注入错误事件: %q", r.body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("3s 内客户端未收完整响应")
	}
	select {
	case err := <-shDone:
		if err != nil {
			t.Fatalf("自然结束 Shutdown 应返回 nil: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown 未随在途结束返回")
	}

	rows := waitDockRows(t, acc, 1)
	if _, ok := rows[0]["truncated"]; ok {
		t.Fatalf("完整流不应带 truncated: %v", rows[0])
	}
}

// TestShutdownDeadlineInjectsStreamErrorEvent 到期未完：流已建立的在途请求
// 收到合成 Anthropic 流内错误事件后连接收尾；行带 truncated。
func TestShutdownDeadlineInjectsStreamErrorEvent(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	backend := sseBackend(t, "event: message_start\ndata: {\"type\":\"message_start\"}\n\n", block, "")

	srv, acc, front := newRecordingFront(t, backend.URL)
	done := doStreamIn(t, front+"/v1/messages")

	time.Sleep(150 * time.Millisecond) // 流已建立（首帧已发）

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if err := srv.Shutdown(ctx); err == nil {
		t.Fatal("排水到期 Shutdown 应返回非 nil（ctx 错误）")
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("客户端收尾失败: %v", r.err)
		}
		body := string(r.body)
		if !strings.Contains(body, "message_start") {
			t.Fatalf("在途首帧丢失: %q", body)
		}
		if !strings.Contains(body, "event: error") {
			t.Fatalf("未见流内错误事件（裸断连＝契约违例）: %q", body)
		}
		if !strings.Contains(body, `"type":"error"`) || !strings.Contains(body, `"api_error"`) {
			t.Fatalf("错误事件负载非 Anthropic 标准形状: %q", body)
		}
		if strings.Contains(body, "message_stop") {
			t.Fatalf("按住中的桩不该吐出 message_stop: %q", body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("3s 内连接未收尾")
	}

	rows := waitDockRows(t, acc, 1)
	if rows[0]["truncated"] != true {
		t.Fatalf("排水掐流行应带 truncated: %v", rows[0])
	}
}

// TestShutdownDeadlineGateBlockedReturns504 到期·首字节未写出（闸门扣住中）：
// 客户端收 504＋Anthropic 错误体＋Retry-After；行状态 504。
func TestShutdownDeadlineGateBlockedReturns504(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	backend := sseBackend(t, "", block, "") // 只有 200 响应头，体一字不到（闸门扣住）

	srv, acc, front := newRecordingFront(t, backend.URL)
	done := doStreamIn(t, front+"/v1/messages")

	time.Sleep(150 * time.Millisecond) // 确保闸门已扣住（响应头已从上游到）

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if err := srv.Shutdown(ctx); err == nil {
		t.Fatal("排水到期 Shutdown 应返回非 nil")
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("客户端收尾失败: %v", r.err)
		}
		if r.status != http.StatusGatewayTimeout {
			t.Fatalf("状态码 = %d, want 504（首字节未写出走 504 路径）", r.status)
		}
		wantAnthropicErrorBody(t, r.body)
	case <-time.After(3 * time.Second):
		t.Fatal("3s 内客户端未收 504")
	}

	rows := waitDockRows(t, acc, 1)
	if rows[0]["status"].(float64) != 504 {
		t.Fatalf("行 status = %v, want 504", rows[0]["status"])
	}
}

// TestShutdownDeadlineHeadersPendingReturns504 到期·首字节未写出（上游响应头
// 都没回）：出站被取消 → ErrorHandler 回 504＋Anthropic 错误体。
func TestShutdownDeadlineHeadersPendingReturns504(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block // 连响应头都按住不发
	}))
	t.Cleanup(backend.Close)

	srv, _, front := newRecordingFront(t, backend.URL)
	done := doStreamIn(t, front+"/v1/messages")

	time.Sleep(150 * time.Millisecond) // 确保出站已挂起在等响应头

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if err := srv.Shutdown(ctx); err == nil {
		t.Fatal("排水到期 Shutdown 应返回非 nil")
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("客户端收尾失败: %v", r.err)
		}
		if r.status != http.StatusGatewayTimeout {
			t.Fatalf("状态码 = %d, want 504", r.status)
		}
		wantAnthropicErrorBody(t, r.body)
	case <-time.After(3 * time.Second):
		t.Fatal("3s 内客户端未收 504")
	}
}

// TestShutdownFixedLengthBodyEndsWithoutInjection 定长体（上游以 JSON 应答
// stream 请求且声明 Content-Length）到期收尾＝EOF 截断，不注入 SSE 字节
// （注入会超出声明长度造成协议违例）。
func TestShutdownFixedLengthBodyEndsWithoutInjection(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "27") // 声明 27 字节
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":`) // 只发一部分，其余按住
		w.(http.Flusher).Flush()
		<-block
	}))
	t.Cleanup(backend.Close)

	srv, acc, front := newRecordingFront(t, backend.URL)
	done := doStreamIn(t, front+"/v1/messages")

	time.Sleep(150 * time.Millisecond) // 首字节已放行（定长体在册）

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if err := srv.Shutdown(ctx); err == nil {
		t.Fatal("排水到期 Shutdown 应返回非 nil")
	}

	select {
	case r := <-done:
		// 短体（unexpected EOF）或已收部分：都没有注入的 SSE 字节
		if strings.Contains(string(r.body), "event: error") {
			t.Fatalf("定长体不得注入 SSE 字节（超出 Content-Length）: %q", r.body)
		}
		if !strings.Contains(string(r.body), `{"ok":`) {
			t.Fatalf("已放行首段丢失: %q (err=%v)", r.body, r.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("3s 内连接未收尾")
	}

	// JSON 短体无 SSE 事件可见：不带 truncated
	rows := waitDockRows(t, acc, 1)
	if _, ok := rows[0]["truncated"]; ok {
		t.Fatalf("非 SSE 定长体不应带 truncated: %v", rows[0])
	}
}

// TestShutdownRealServerHardClosesAfterDeadline 真实监听路径（Start）：到期
// 注入错误事件并硬收——客户端收全（首帧＋错误事件）后连接关停，端口不再监听。
func TestShutdownRealServerHardClosesAfterDeadline(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	backend := sseBackend(t, "event: message_start\ndata: {\"type\":\"message_start\"}\n\n", block, "")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // 让渡给渡口绑定

	// 记账模式（注册表与闸门都在 record 路径上）——真实监听 + Start 生命周期
	acc, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewWithOptions(fmt.Sprintf("127.0.0.1:%d", port), backend.URL, Options{Accounts: acc})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Close()

	addr := fmt.Sprintf("http://127.0.0.1:%d/v1/messages", port)
	done := doStreamIn(t, addr)

	time.Sleep(150 * time.Millisecond) // 流已建立

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if err := srv.Shutdown(ctx); err == nil {
		t.Fatal("排水到期 Shutdown 应返回非 nil")
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("客户端收尾失败: %v", r.err)
		}
		body := string(r.body)
		if !strings.Contains(body, "message_start") {
			t.Fatalf("在途首帧丢失: %q", body)
		}
		if !strings.Contains(body, "event: error") {
			t.Fatalf("未见注入的流内错误事件: %q", body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("3s 内连接未收尾")
	}

	// 硬收后端口不再监听
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, derr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if derr != nil {
			return // 关停成功
		}
		c.Close()
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("Shutdown 硬收后渡口端口仍在监听")
}
