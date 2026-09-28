// errorshape_test.go — 票02：渡口自产错误形状的验收钉子（错误契约热修 2/3）。
//   - 拨号失败：上游不可达 → 502＋Anthropic 错误体＋Retry-After（替换 Go 默认
//     空体 502）；
//   - 首包闸门：仅流式记账请求——上游 200 头后静默（注入短超时）→ 504＋错误
//     体＋Retry-After；正常流首字节无额外延迟；非流式慢响应不被闸；
//   - 上游非 200 透传锁定：429（retry-after 头＋JSON 体）状态码/体/头三者
//     原样到达（响应保真，既有行为钉死）。
// 全部 httptest 假上游或死端口，零真实外呼；时间参数经包级 var 注入短值。
package dock

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ferryman/internal/accounts"
)

// deadPort 取一个确定无人监听的回环端口（拨号失败测试的上游地址）。
func deadPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// wantAnthropicErrorBody 断言客户端实收体是 Anthropic 标准错误形状
// （{"type":"error","error":{"type":"api_error","message":人话}}）。
func wantAnthropicErrorBody(t *testing.T, body []byte) {
	t.Helper()
	var ev struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		t.Fatalf("错误体非法 JSON: %s", body)
	}
	if ev.Type != "error" || ev.Error.Type != "api_error" {
		t.Fatalf("错误体形状 = %s/%s, want error/api_error: %s", ev.Type, ev.Error.Type, body)
	}
	if ev.Error.Message == "" {
		t.Fatalf("错误体缺一句人话原因: %s", body)
	}
}

// streamBody 流式 messages 请求体夹具。
func streamBody(session string) []byte {
	return []byte(`{"model":"claude-opus-5","stream":true,"max_tokens":16,` +
		`"metadata":{"session_id":"` + session + `"},"messages":[]}`)
}

// newRecordingFront 记账模式渡口（acc 注入即 record 路径）＋ httptest 前端。
func newRecordingFront(t *testing.T, upstream string) (*Server, *accounts.Accounts, string) {
	t.Helper()
	acc, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewWithOptions("127.0.0.1:15722", upstream, Options{Accounts: acc})
	if err != nil {
		t.Fatal(err)
	}
	return srv, acc, frontOf(t, srv)
}

// TestDialFailureReturnsAnthropic502 上游不可达：客户端收 502＋标准错误体＋
// Retry-After（记账行状态以客户端实收 502 为准）。
func TestDialFailureReturnsAnthropic502(t *testing.T) {
	upAddr := deadPort(t)
	_, acc, front := newRecordingFront(t, "http://"+upAddr)

	resp, err := http.DefaultClient.Do(ccRequest(t, front+"/v1/messages", streamBody("dial-fail")))
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("状态码 = %d, want 502（替换 Go 默认空体 502 的形状）", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("Retry-After 头缺失")
	}
	body, _ := io.ReadAll(resp.Body)
	wantAnthropicErrorBody(t, body)

	rows := waitDockRows(t, acc, 1)
	if rows[0]["status"].(float64) != 502 {
		t.Fatalf("行 status = %v, want 502（以客户端实收为准）", rows[0]["status"])
	}
}

// TestDialFailurePassthroughModeAlsoShaped 纯透传（无记账）同样吃标准形状：
// 自产错误不属于上游字节，透传保真不管辖（契约：渡口自产错误一律标准形状）。
func TestDialFailurePassthroughModeAlsoShaped(t *testing.T) {
	srv, err := New("127.0.0.1:15722", "http://"+deadPort(t))
	if err != nil {
		t.Fatal(err)
	}
	front := frontOf(t, srv)

	resp, err := http.DefaultClient.Do(ccRequest(t, front+"/v1/messages", streamBody("dial-pt")))
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("状态码 = %d, want 502", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("Retry-After 头缺失")
	}
	body, _ := io.ReadAll(resp.Body)
	wantAnthropicErrorBody(t, body)
}

// TestFirstByteGateSilentUpstreamTimesOut 桩回 200 头后永不吐体（注入短闸门
// 超时）→ 60 秒语义路径收 504＋错误体＋Retry-After；行状态记 504。
func TestFirstByteGateSilentUpstreamTimesOut(t *testing.T) {
	old := firstByteGateTimeout
	firstByteGateTimeout = 150 * time.Millisecond
	defer func() { firstByteGateTimeout = old }()

	block := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-block // 头已到、体永不到
	}))
	defer backend.Close()
	defer close(block)

	_, acc, front := newRecordingFront(t, backend.URL)

	resp, err := http.DefaultClient.Do(ccRequest(t, front+"/v1/messages", streamBody("gate-silent")))
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("状态码 = %d, want 504（首包静默判失败）", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("Retry-After 头缺失")
	}
	body, _ := io.ReadAll(resp.Body)
	wantAnthropicErrorBody(t, body)

	rows := waitDockRows(t, acc, 1)
	if rows[0]["status"].(float64) != 504 {
		t.Fatalf("行 status = %v, want 504（闸门失败以客户端实收为准）", rows[0]["status"])
	}
}

// TestFirstByteGateHealthyStreamNoExtraDelay 正常流：闸门只 withhold 到首字节，
// 不引入额外缓冲延迟（首行到达须远早于桩的按住时长）。
func TestFirstByteGateHealthyStreamNoExtraDelay(t *testing.T) {
	const hold = 800 * time.Millisecond
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: message_start\n"+
			`data: {"type":"message_start","frame":1}`+"\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(hold)
		_, _ = io.WriteString(w, "event: message_stop\n"+
			`data: {"type":"message_stop"}`+"\n\n")
		w.(http.Flusher).Flush()
	}))
	defer backend.Close()

	_, _, front := newRecordingFront(t, backend.URL)

	start := time.Now()
	resp, err := http.DefaultClient.Do(ccRequest(t, front+"/v1/messages", streamBody("gate-live")))
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	rd := bufio.NewReader(resp.Body)
	line, err := rd.ReadString('\n')
	if err != nil {
		t.Fatalf("读 SSE 首行失败: %v", err)
	}
	elapsed := time.Since(start)
	if !strings.Contains(line, "message_start") {
		t.Fatalf("SSE 首行 = %q, want message_start", line)
	}
	if elapsed >= hold/2 {
		t.Fatalf("首字节 %v 才到达（≥ hold/2=%v）——闸门引入了额外缓冲/扣留", elapsed, hold/2)
	}
	rest, _ := io.ReadAll(rd)
	if !strings.Contains(string(rest), "message_stop") {
		t.Fatalf("流尾丢失（闸门≠截断）: %q", rest)
	}
}

// TestFirstByteGateSkipsNonStream 非流式不闸：慢响应的响应头即时到达，
// 不等体完成。
func TestFirstByteGateSkipsNonStream(t *testing.T) {
	const hold = 700 * time.Millisecond
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(hold) // 慢生成是合法行为，不得误杀
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer backend.Close()

	_, _, front := newRecordingFront(t, backend.URL)

	start := time.Now()
	resp, err := http.DefaultClient.Do(ccRequest(t, front+"/v1/messages",
		[]byte(`{"model":"claude-opus-5","metadata":{"session_id":"gate-nostream"},"messages":[]}`)))
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if elapsed := time.Since(start); elapsed >= hold/2 {
		t.Fatalf("非流式响应头 %v 才到达（≥ hold/2=%v）——非流式被闸了", elapsed, hold/2)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"ok":true}` {
		t.Fatalf("响应体 = %q", body)
	}
}

// TestUpstreamNon200PassthroughFidelityLocked 上游 429（retry-after 头＋JSON
// 体）：客户端实收状态码/体/头三者一致——响应保真红线的行为锁定测试。
func TestUpstreamNon200PassthroughFidelityLocked(t *testing.T) {
	const wantBody = `{"type":"error","error":{"type":"rate_limit_error","message":"Number of requests too high"}}`
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "27")
		w.Header().Set("X-Upstream-Extra", "keep-me")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(wantBody))
	}))
	defer backend.Close()

	acc, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewWithOptions("127.0.0.1:15722", backend.URL,
		Options{Upstream: rewriteDockUpstream(backend.URL), Accounts: acc})
	if err != nil {
		t.Fatal(err)
	}
	front := frontOf(t, srv)

	resp, err := http.DefaultClient.Do(ccRequest(t, front+"/v1/messages", streamBody("up-429")))
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("状态码 = %d, want 429（零改写透传）", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != wantBody {
		t.Fatalf("错误体被改写:\ngot:  %s\nwant: %s", body, wantBody)
	}
	if got := resp.Header.Get("Retry-After"); got != "27" {
		t.Fatalf("Retry-After = %q, want 27（上游头原样到达）", got)
	}
	if got := resp.Header.Get("X-Upstream-Extra"); got != "keep-me" {
		t.Fatalf("X-Upstream-Extra = %q, want keep-me", got)
	}
	// 行状态记上游真值 429（非 200 也照记）
	rows := waitDockRows(t, acc, 1)
	if rows[0]["status"].(float64) != 429 {
		t.Fatalf("行 status = %v, want 429", rows[0]["status"])
	}
}
