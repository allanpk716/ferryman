// server_test.go — 票01：透传保真验收钉子（.xcheck/20260919-195118/exp/
// xf7_reverseproxy_headers 同款断言）：体逐字节相等；后端收到的头集合除
// Host→上游、逐跳头剥离外逐项相同、零新增头；count_tokens 不入快照；
// session_id 提取失败透传不受影响只记计数。
package dock

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// upstreamEcho 捕获型后端：锁存收到的头/Host/体，回 200 固定串。
type upstreamEcho struct {
	mu   sync.Mutex
	hdr  http.Header
	host string
	body []byte
}

func (u *upstreamEcho) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.hdr = r.Header.Clone()
	u.host = r.Host
	u.body = body
	u.mu.Unlock()
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("backend-ok"))
}

func (u *upstreamEcho) snapshot() (http.Header, string, []byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hdr, u.host, u.body
}

// newDockFront 以后端为上游构造渡口 handler，挂 httptest 前端（handler 级测试，
// 不占真实端口）。
func newDockFront(t *testing.T, backendURL string) (*Server, *httptest.Server) {
	t.Helper()
	srv, err := New("127.0.0.1:15722", backendURL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	front := httptest.NewServer(srv)
	t.Cleanup(front.Close)
	return srv, front
}

// ccRequest 模拟 CC 真实发送的全头夹具请求。
func ccRequest(t *testing.T, url string, body []byte) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "client-host.example" // 客户端显式 Host：应被重写为上游
	h := req.Header
	h.Set("Content-Type", "application/json")
	h.Set("Anthropic-Version", "2023-06-01")
	h.Set("Anthropic-Beta", "claude-code-20250219,oauth-2025-04-20")
	h.Set("User-Agent", "claude-cli/2.0.0 (external)")
	h.Set("Accept", "application/json")
	h.Set("Accept-Encoding", "gzip, deflate")
	h.Set("Authorization", "Bearer placeholder-token") // 占位令牌照抄（入站不鉴权）
	h.Set("X-Api-Key", "sk-ant-placeholder")
	h.Set("Connection", "keep-alive")        // 逐跳头：应被剥离
	h.Set("X-Custom-Passthrough", "keep-me") // 自定义头：应保留
	return req
}

func TestPassthroughFidelityBodyAndHeaders(t *testing.T) {
	var up upstreamEcho
	backend := httptest.NewServer(http.HandlerFunc(up.handler))
	defer backend.Close()
	dockSrv, front := newDockFront(t, backend.URL)

	body := []byte(`{"model":"claude-opus-5[1M]","max_tokens":1024,` +
		`"metadata":{"session_id":"sess-fidelity"},` +
		`"messages":[{"role":"user","content":"hello 世界 ✓"}]}`)
	resp, err := http.DefaultClient.Do(ccRequest(t, front.URL+"/v1/messages", body))
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if string(respBody) != "backend-ok" {
		t.Fatalf("响应体 = %q, want backend-ok（响应须回流）", respBody)
	}

	gotHdr, gotHost, gotBody := up.snapshot()
	// 断言一：体逐字节相等
	if !bytes.Equal(gotBody, body) {
		t.Fatalf("体不逐字节相等: got %d 字节, want %d 字节", len(gotBody), len(body))
	}
	// 断言二：Host 重写为上游（客户端显式 Host 不外泄）
	target, _ := url.Parse(backend.URL)
	if gotHost != target.Host {
		t.Fatalf("后端 Host = %q, want 上游 %q", gotHost, target.Host)
	}
	// 断言三：头集合除逐跳（Connection）剥离外逐项相同、零新增头。
	// Content-Length 由 Go 传输层按体长写线（客户端直发后端也会带上），
	// 不算新增——按线上真相纳入期望，同时锁"长度=体长"不被渡口破坏。
	wantHdr := ccRequest(t, front.URL+"/v1/messages", body).Header // 同夹具重建期望
	wantHdr.Del("Connection")
	wantHdr.Set("Content-Length", strconv.Itoa(len(body)))
	if !reflect.DeepEqual(gotHdr, wantHdr) {
		t.Fatalf("后端头集合不等（逐项相同 + 零新增失败）:\ngot:  %v\nwant: %v", gotHdr, wantHdr)
	}
	if gotHdr.Get("Connection") != "" {
		t.Fatal("逐跳头 Connection 未被剥离")
	}
	if gotHdr.Get("X-Forwarded-For") != "" {
		t.Fatal("ReverseProxy 不得自动追加 X-Forwarded-For")
	}

	// 快照侧同请求入账：主快照体逐字节相等，头集为白名单（auth 不入）
	main, ok := dockSrv.Snapshots().Main("sess-fidelity")
	if !ok {
		t.Fatal("快照未捕获 sess-fidelity")
	}
	if !bytes.Equal(main.Body, body) {
		t.Fatalf("快照体与请求体不等: %d vs %d 字节", len(main.Body), len(body))
	}
	if _, has := main.Headers["authorization"]; has {
		t.Fatal("authorization 入了快照（红线：auth 类头一律不入）")
	}
	if main.Headers["anthropic-beta"] != "claude-code-20250219,oauth-2025-04-20" {
		t.Fatalf("快照 anthropic-beta = %q", main.Headers["anthropic-beta"])
	}
}

func TestPassthroughCountTokensNotCaptured(t *testing.T) {
	var up upstreamEcho
	backend := httptest.NewServer(http.HandlerFunc(up.handler))
	defer backend.Close()
	dockSrv, front := newDockFront(t, backend.URL)

	body := []byte(`{"metadata":{"session_id":"sess-ct"},"messages":[]}`)
	resp, err := http.DefaultClient.Do(ccRequest(t, front.URL+"/v1/messages/count_tokens", body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	_, _, gotBody := up.snapshot()
	if !bytes.Equal(gotBody, body) {
		t.Fatal("count_tokens 体未保真转发")
	}
	if _, ok := dockSrv.Snapshots().Main("sess-ct"); ok {
		t.Fatal("count_tokens 路径不得入快照")
	}
	if got := dockSrv.Snapshots().Skipped(); got != 0 {
		t.Fatalf("count_tokens 是路径排除不是 session 缺失, Skipped() = %d, want 0", got)
	}
}

func TestPassthroughMissingSessionIDForwardsAndCounts(t *testing.T) {
	var up upstreamEcho
	backend := httptest.NewServer(http.HandlerFunc(up.handler))
	defer backend.Close()
	dockSrv, front := newDockFront(t, backend.URL)

	// 有 metadata 无 session_id + 完全非 JSON：都须透传不受影响、快照跳过记数
	for i, body := range [][]byte{
		[]byte(`{"model":"m","metadata":{}}`),
		[]byte(`totally not json ✗`),
	} {
		resp, err := http.DefaultClient.Do(ccRequest(t, front.URL+"/v1/messages", body))
		if err != nil {
			t.Fatalf("case %d 请求失败: %v", i, err)
		}
		resp.Body.Close()
		_, _, gotBody := up.snapshot()
		if !bytes.Equal(gotBody, body) {
			t.Fatalf("case %d 体未保真转发", i)
		}
	}
	if got := dockSrv.Snapshots().Skipped(); got != 2 {
		t.Fatalf("Skipped() = %d, want 2", got)
	}
}

func TestPassthroughNonMessagesPathStreamsBody(t *testing.T) {
	// 非 messages 路径（GET/其他）：不动体直接进代理
	var up upstreamEcho
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		up.mu.Lock()
		up.hdr, up.host, up.body = r.Header.Clone(), r.Host, append([]byte(nil), b...)
		up.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("get-ok:" + r.Method))
	}))
	defer backend.Close()
	dockSrv, front := newDockFront(t, backend.URL)

	req, _ := http.NewRequest(http.MethodGet, front.URL+"/v1/models", nil)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "get-ok:GET" {
		t.Fatalf("GET 透传失败: %q", b)
	}
	if got := dockSrv.Snapshots().Skipped(); got != 0 {
		t.Fatalf("非 messages 路径不该计 skip, got %d", got)
	}
}

func TestPassthroughSSEStream(t *testing.T) {
	// FlushInterval:-1 必须在位：SSE 分块写＋逐块 Flush，前端完整收齐不坏
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, "event: chunk%d\ndata: {\"i\":%d}\n\n", i, i)
			w.(http.Flusher).Flush()
			time.Sleep(20 * time.Millisecond)
		}
	}))
	defer backend.Close()
	_, front := newDockFront(t, backend.URL)

	req, _ := http.NewRequest(http.MethodPost, front.URL+"/v1/messages",
		strings.NewReader(`{"stream":true,"metadata":{"session_id":"sse"}}`))
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	all, _ := io.ReadAll(resp.Body)
	for i := 0; i < 3; i++ {
		want := fmt.Sprintf("event: chunk%d", i)
		if !strings.Contains(string(all), want) {
			t.Fatalf("SSE 块缺失: %q 不含 %q（流被缓冲/截断）", all, want)
		}
	}
}

func TestNewValidatesArgs(t *testing.T) {
	if _, err := New("", "http://127.0.0.1:15721"); err == nil {
		t.Fatal("listen 为空应报错")
	}
	if _, err := New("127.0.0.1:15722", "://bad url"); err == nil {
		t.Fatal("坏上游地址应报错")
	}
	if _, err := New("127.0.0.1:15722", "no-scheme-host"); err == nil {
		t.Fatal("缺 scheme/host 的上游地址应报错")
	}
	if _, err := New("127.0.0.1:15722", "http://127.0.0.1:15721"); err != nil {
		t.Fatalf("合法参数不应报错: %v", err)
	}
}

func TestStartBindConflictReturnsError(t *testing.T) {
	// 端口被占 → Start 返回错误（daemon 侧据此只告警不拖垮主服务）
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	srv, err := New(fmt.Sprintf("127.0.0.1:%d", port), "http://127.0.0.1:15721")
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err == nil {
		_ = srv.Close()
		t.Fatal("端口被占时 Start 应返回错误")
	}
}

func TestStartServeAndCloseLifecycle(t *testing.T) {
	var up upstreamEcho
	backend := httptest.NewServer(http.HandlerFunc(up.handler))
	defer backend.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // 让渡给渡口绑定

	srv, err := New(fmt.Sprintf("127.0.0.1:%d", port), backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	body := []byte(`{"metadata":{"session_id":"life"},"messages":[]}`)
	resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/v1/messages", port),
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("真实 TCP 经渡口请求失败: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(respBody) != "backend-ok" {
		t.Fatalf("响应 = %q, want backend-ok", respBody)
	}
	if _, _, got := up.snapshot(); !bytes.Equal(got, body) {
		t.Fatal("真实 TCP 路径体不保真")
	}

	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, derr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if derr != nil {
			return // 关停成功：端口不再接受连接
		}
		c.Close()
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("Close 后渡口端口仍在监听")
}

// ---- 票01 W1：/stats 渡口统计（dock_inflight / last_request_ts）验收钉子 ----
//
// 口径：只统计 15722 代理面上真实 CC 流量（记账路径进出＝在途注册表同事件）；
// 心跳自产重放（x-ferryman-replay 标记头）与管理口 15700 流量一律不计入。
// 断言经 Snapshots().ProxyStats() 取数——与 daemon /stats 完全同路径
// （serveConfig: d.DockSnap = ds.Snapshots() 只读转交），不另开测试后门。

// waitForInflight 轮询等统计在途数到位。注册发生在 handler 进 record 分支时，
// 与客户端发出请求存在竞速——固定 sleep 换带死线轮询，防慢机 flake。
func waitForInflight(t *testing.T, srv *Server, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		n, _ := srv.Snapshots().ProxyStats()
		if n == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("统计在途数未到位: want %d, got %d", want, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestProxyStatsInflightAndLastTS 在途数与实际并发一致、完成后回落；
// last_request_ts 记最后一个请求的完成时刻（秒级）；启动零值与"未完成不记时"
// 同钉（零请求视为静默成立的判定归消费方，这里只钉零值如实暴露）。
func TestProxyStatsInflightAndLastTS(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	// 唯一关闭点：任何断言路径 Fatalf 也放行在途流——否则 t.Cleanup 的
	// httptest Close 会等按住中的连接，测试进程悬挂（比失败更糟）。
	kick := func() { once.Do(func() { close(release) }) }
	defer kick()
	backend := sseBackend(t, "event: message_start\ndata: {\"type\":\"message_start\"}\n\n", release,
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	srv, acc, front := newRecordingFront(t, backend.URL)
	testStart := time.Now().Unix()

	if n, ts := srv.Snapshots().ProxyStats(); n != 0 || ts != 0 {
		t.Fatalf("刚启动应 0/0（零请求如实暴露）: got %d/%d", n, ts)
	}

	d1 := doStreamIn(t, front+"/v1/messages")
	waitForInflight(t, srv, 1)
	d2 := doStreamIn(t, front+"/v1/messages")
	waitForInflight(t, srv, 2) // dock_inflight 与实际并发数一致
	if _, ts := srv.Snapshots().ProxyStats(); ts != 0 {
		t.Fatalf("无请求完成前 last_request_ts 应为 0: got %d", ts)
	}

	kick()
	for _, ch := range []<-chan clientResult{d1, d2} {
		select {
		case r := <-ch:
			if r.err != nil {
				t.Fatalf("客户端收尾失败: %v", r.err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("3s 内请求未收尾")
		}
	}
	waitDockRows(t, acc, 2)    // 落行在出册前——行到即完成收尾在即
	waitForInflight(t, srv, 0) // 完成后回落

	n, ts := srv.Snapshots().ProxyStats()
	if n != 0 {
		t.Fatalf("完成后在途应回落 0: got %d", n)
	}
	if ts < testStart || ts > time.Now().Unix() {
		t.Fatalf("last_request_ts = %d, want 完成时刻（秒级，[%d, %d]）", ts, testStart, time.Now().Unix())
	}
}

// TestProxyStatsReplayNotCounted 心跳自产重放（x-ferryman-replay 标记头）与真实
// 流量走同一 handler、同一在途注册表（排水语义不变），但统计不计入：在途不涨、
// 完成不记时——升级静默门只看真实 CC 流量，本程序自产心跳不该把门按住。
func TestProxyStatsReplayNotCounted(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	kick := func() { once.Do(func() { close(release) }) } // 唯一关闭点（同上：Fatalf 路径不悬挂）
	defer kick()
	backend := sseBackend(t, "event: message_start\ndata: {\"type\":\"message_start\"}\n\n", release,
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	srv, acc, front := newRecordingFront(t, backend.URL)

	done := make(chan clientResult, 1)
	go func() {
		req := ccRequest(t, front+"/v1/messages", streamBody("hb"))
		req.Header.Set(HeaderFerrymanReplay, "1") // HttpBeatSender 同款自产标记
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- clientResult{err: err}
			return
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		done <- clientResult{status: resp.StatusCode, body: b}
	}()

	// 在途按住中：注册表里有它（排水照管），统计必须没有——在途数保持 0 且
	// 不记完成时刻。这是比"完成后补看"更强的口径断言。
	waitForInflight(t, srv, 0)
	if _, ts := srv.Snapshots().ProxyStats(); ts != 0 {
		t.Fatalf("在途重放不得记完成时刻: got %d", ts)
	}

	kick() // 放行：重放照记 dock 科目行（既有语义不动）
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("重放请求失败: %v", r.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("3s 内重放未收尾")
	}
	waitDockRows(t, acc, 1)
	waitForInflight(t, srv, 0)
	if n, ts := srv.Snapshots().ProxyStats(); n != 0 || ts != 0 {
		t.Fatalf("重放不计入统计: got %d/%d, want 0/0", n, ts)
	}
}

// TestSessionHeaderFallbackWireAttribution 票01 头回落端到端（真 ServeHTTP 缝）：
//   - 仅头归因（claude-cli 2.1.273 真实形状：体 metadata 只有 user_id）——
//     快照与 dock 科目行都按头归因，Skipped 不涨；
//   - 头体冲突——以体为准（快照与记账行都归到体 ID）；
//   - 头格式坏＝缺失——跳过计数、不伪造、不入库。
//
// 快照捕获与记账归因两个消费点走同一 ExtractSessionID（一处实现），本测试
// 经同一请求同缝验证两处。
func TestSessionHeaderFallbackWireAttribution(t *testing.T) {
	var up upstreamEcho
	backend := httptest.NewServer(http.HandlerFunc(up.handler))
	defer backend.Close()
	srv, acc, front := newRecordingFront(t, backend.URL)

	// 阶段一：仅头归因
	body := []byte(`{"model":"claude-opus-5","max_tokens":16,"stream":true,` +
		`"metadata":{"user_id":"u"},"messages":[]}`)
	req := ccRequest(t, front+"/v1/messages", body)
	req.Header.Set(HeaderClaudeCodeSessionID, uuidFixture)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("阶段一请求失败: %v", err)
	}
	resp.Body.Close()
	main, ok := srv.Snapshots().Main(uuidFixture)
	if !ok {
		t.Fatal("仅头归因失败：快照未按头入库")
	}
	if !bytes.Equal(main.Body, body) {
		t.Fatal("快照体与请求体不等")
	}
	rows := waitDockRows(t, acc, 1)
	if rows[0]["session_id"] != uuidFixture {
		t.Fatalf("阶段一记账行 session_id = %v, want %v（头回落归因）",
			rows[0]["session_id"], uuidFixture)
	}
	if got := srv.Snapshots().Skipped(); got != 0 {
		t.Fatalf("头回落成功不得记跳过, Skipped() = %d", got)
	}

	// 阶段二：头体冲突——以体为准
	body2 := streamBody("body-wins-sid")
	req2 := ccRequest(t, front+"/v1/messages", body2)
	req2.Header.Set(HeaderClaudeCodeSessionID, "123e4567-e89b-12d3-a456-426614174001")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("阶段二请求失败: %v", err)
	}
	resp2.Body.Close()
	if _, ok := srv.Snapshots().Main("body-wins-sid"); !ok {
		t.Fatal("冲突未以体为准：快照未归到体 ID")
	}
	rows = waitDockRows(t, acc, 2)
	if rows[1]["session_id"] != "body-wins-sid" {
		t.Fatalf("阶段二记账行 session_id = %v, want body-wins-sid（以体为准）",
			rows[1]["session_id"])
	}

	// 阶段三：头格式坏＝缺失——跳过计数、不伪造
	before := srv.Snapshots().Skipped()
	body3 := []byte(`{"model":"m","max_tokens":16,"stream":true,"messages":[]}`)
	req3 := ccRequest(t, front+"/v1/messages", body3)
	req3.Header.Set(HeaderClaudeCodeSessionID, "not-a-uuid")
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatalf("阶段三请求失败: %v", err)
	}
	resp3.Body.Close()
	if got := srv.Snapshots().Skipped(); got != before+1 {
		t.Fatalf("坏头应记跳过, Skipped() = %d, want %d", got, before+1)
	}
	if _, ok := srv.Snapshots().Main("not-a-uuid"); ok {
		t.Fatal("坏头值不得入库（绝不伪造会话 ID）")
	}
}
