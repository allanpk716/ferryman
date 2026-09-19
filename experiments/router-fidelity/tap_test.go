// router-fidelity/tap_test.go —— 上游捕获器单测。
// 全部流量走 httptest 本地回环，绝不真实外呼；捕获目录用 t.TempDir()，不碰 ~/ferryman。
// 覆盖：原样转发（headers/body 含 auth 零脱敏）、请求捕获落盘、SSE 逐块不缓冲、
// 响应摘要 usage 四列（message_delta 覆盖 message_start）、响应正文零落盘、
// 权限位传参（0o700 目录 / 0o600 文件）、自检端点零捕获。
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- 夹具 ----

func newTestTap(t *testing.T) *tap {
	t.Helper()
	tp, err := newTap(t.TempDir())
	if err != nil {
		t.Fatalf("newTap: %v", err)
	}
	return tp
}

func startUpstream(t *testing.T, h http.Handler) *url.URL {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	return u
}

// serveTap 把 tap 架在 httptest 上，返回可请求的本地地址。
func serveTap(t *testing.T, tp *tap, target *url.URL) string {
	t.Helper()
	srv := httptest.NewServer(tp.handler(target))
	t.Cleanup(srv.Close)
	return srv.URL
}

// readReqCapture 断言恰有一份请求捕获文件并解析返回。
func readReqCapture(t *testing.T, dir string) map[string]any {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*_req*.json"))
	if err != nil {
		t.Fatalf("glob req capture: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("请求捕获文件数 = %d, want 1（%v）", len(files), files)
	}
	return readCaptureFile(t, files[0])
}

// waitForRespCapture 响应摘要在代理拷贝完响应体后异步落盘，轮询等待出现。
func waitForRespCapture(t *testing.T, dir string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		files, err := filepath.Glob(filepath.Join(dir, "*_resp*.json"))
		if err == nil && len(files) > 0 {
			return readCaptureFile(t, files[0])
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("响应摘要捕获文件未在 3s 内出现")
	return nil
}

func readCaptureFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读捕获文件 %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("捕获文件 %s 非合法 JSON: %v\n%s", path, err, data)
	}
	return m
}

// captureFiles 返回目录下全部捕获文件内容（正文零落盘断言用）。
func captureFiles(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatalf("glob captures: %v", err)
	}
	var out []string
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读 %s: %v", f, err)
		}
		out = append(out, string(data))
	}
	return out
}

// ---- 原样转发＋请求捕获 ----

func TestTapForwardsVerbatimHeadersAndBody(t *testing.T) {
	const (
		authz  = "Bearer sk-real-secret-abc123def456"
		apiKey = "sk-raw-key-99990000"
	)
	sentBody := `{"model":"claude-sonnet-5","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`

	var mu sync.Mutex
	var gotHdr http.Header
	var gotBody []byte
	up := startUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotHdr = r.Header.Clone()
		gotBody = b
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"nonce":"resp-nonce-XYZZY-1"}`)
	}))

	tp := newTestTap(t)
	addr := serveTap(t, tp, up)

	req, err := http.NewRequest("POST", addr+"/v1/messages?beta=true", strings.NewReader(sentBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", authz)
	req.Header.Set("X-Api-Key", apiKey)
	req.Header.Set("anthropic-beta", "claude-code-20250219")
	req.Header.Set("content-type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// 上游收到原样 body＋原样 headers（auth 原样透传，零脱敏零改写）
	mu.Lock()
	defer mu.Unlock()
	if string(gotBody) != sentBody {
		t.Errorf("上游收到的 body 被改动：\ngot  %s\nwant %s", gotBody, sentBody)
	}
	if got := gotHdr.Get("Authorization"); got != authz {
		t.Errorf("Authorization = %q, want 原样 %q", got, authz)
	}
	if got := gotHdr.Get("X-Api-Key"); got != apiKey {
		t.Errorf("X-Api-Key = %q, want 原样 %q", got, apiKey)
	}
	if got := gotHdr.Get("Anthropic-Beta"); got != "claude-code-20250219" {
		t.Errorf("Anthropic-Beta = %q, want 原样", got)
	}

	// 捕获文件：含全部头（未脱敏）与解析后 body＋路径/query
	cap := readReqCapture(t, tp.outDir)
	if cap["path"] != "/v1/messages" {
		t.Errorf("capture path = %v, want /v1/messages", cap["path"])
	}
	if cap["query"] != "beta=true" {
		t.Errorf("capture query = %v, want beta=true", cap["query"])
	}
	hdrs, ok := cap["headers"].(map[string]any)
	if !ok {
		t.Fatalf("capture headers 形状不对: %T", cap["headers"])
	}
	if got := hdrs["Authorization"]; got != authz {
		t.Errorf("捕获 Authorization = %v, want 完整原样 %q", got, authz)
	}
	if got := hdrs["X-Api-Key"]; got != apiKey {
		t.Errorf("捕获 X-Api-Key = %v, want 完整原样 %q", got, apiKey)
	}
	if s := fmt.Sprint(hdrs); strings.Contains(s, "redacted") || strings.Contains(s, "…") {
		t.Errorf("捕获出现脱敏痕迹（本 tap 设计为不脱敏）: %s", s)
	}
	bodyMap, ok := cap["body"].(map[string]any)
	if !ok {
		t.Fatalf("capture body 应为解析后 JSON, got %T", cap["body"])
	}
	if bodyMap["model"] != "claude-sonnet-5" {
		t.Errorf("capture body.model = %v, want claude-sonnet-5", bodyMap["model"])
	}
	if msgs, ok := bodyMap["messages"].([]any); !ok || len(msgs) != 1 {
		t.Errorf("capture body.messages = %v, want 1 条", bodyMap["messages"])
	}
}

// ---- SSE 逐块到达（不被缓冲） ----

func TestTapSSEStreamsUnbuffered(t *testing.T) {
	release := make(chan struct{}) // 测试放行后上游才发后续事件
	upstreamDone := make(chan struct{})
	ev1 := `data: {"type":"message_start","message":{"usage":{"input_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":0}}}` + "\n\n"
	ev2 := `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":9,"cache_read_input_tokens":3,"cache_creation_input_tokens":1,"output_tokens":2}}` + "\n\n"

	up := startUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		io.WriteString(w, ev1)
		f.Flush()
		<-release // 阻塞住：客户端收到 ev1 时上游必然还没写 ev2
		io.WriteString(w, ev2)
		f.Flush()
		close(upstreamDone)
	}))

	tp := newTestTap(t)
	addr := serveTap(t, tp, up)
	resp, err := http.Post(addr+"/v1/messages", "application/json", strings.NewReader(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// 逐块读：读到 ev1 全部字节为止（客户端带 5s 兜底超时，缓冲 bug 会立刻显形为超时）
	buf := make([]byte, 512)
	var got []byte
	for !bytes.Contains(got, []byte("message_start")) {
		n, err := resp.Body.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			t.Fatalf("SSE 首块未到达即流终（疑似被缓冲）: %v, got=%q", err, got)
		}
	}
	// ev1 已穿过 tap 到客户端，而上游仍阻塞 ⇒ 逐块转发，零缓冲
	select {
	case <-upstreamDone:
		t.Fatal("上游已写完才收到首块——响应被缓冲")
	default:
	}
	close(release)

	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读余下响应: %v", err)
	}
	if string(append(got, rest...)) != ev1+ev2 {
		t.Errorf("客户端收到的字节与上游写的不一致（保真破坏）:\ngot  %q\nwant %q", append(got, rest...), ev1+ev2)
	}
}

// ---- 响应摘要：usage 四列取自 message_delta；响应正文零落盘 ----

func TestTapResponseSummaryUsageFromDelta(t *testing.T) {
	const marker = "UNIQUE-RESPONSE-BODY-MARKER-7f3a"
	sse := `data: {"type":"message_start","message":{"usage":{"input_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":0}}}` + "\n\n" +
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"` + marker + `"}}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":120,"cache_read_input_tokens":880,"cache_creation_input_tokens":5,"output_tokens":1}}` + "\n\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	up := startUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse)
	}))

	tp := newTestTap(t)
	addr := serveTap(t, tp, up)
	resp, err := http.Post(addr+"/v1/messages", "application/json", strings.NewReader(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	sum := waitForRespCapture(t, tp.outDir)
	if sum["status"] != float64(200) {
		t.Errorf("summary status = %v, want 200", sum["status"])
	}
	if src := sum["usage_source"]; src != "message_delta" {
		t.Errorf("usage_source = %v, want message_delta（Q14：真 usage 在 delta）", src)
	}
	usage, ok := sum["usage"].(map[string]any)
	if !ok {
		t.Fatalf("summary usage 缺失或形状不对: %v", sum["usage"])
	}
	want := map[string]float64{
		"input_tokens":                120,
		"cache_read_input_tokens":     880,
		"cache_creation_input_tokens": 5,
		"output_tokens":               1,
	}
	for k, w := range want {
		if usage[k] != w {
			t.Errorf("usage.%s = %v, want %v", k, usage[k], w)
		}
	}

	// 响应正文零落盘：正文里的独有标记不得出现在任何捕获文件
	for _, c := range captureFiles(t, tp.outDir) {
		if strings.Contains(c, marker) {
			t.Error("响应正文（独有标记）出现在捕获文件——违反“响应正文零落盘”")
		}
	}
	// 对照：请求侧按设计落盘（请求捕获存在）
	readReqCapture(t, tp.outDir)
}

// message_start 带非零值也必须被 message_delta 覆盖（与 beat 同语义）。
func TestTapUsageDeltaOverridesStart(t *testing.T) {
	sse := `data: {"type":"message_start","message":{"usage":{"input_tokens":999,"cache_read_input_tokens":777,"cache_creation_input_tokens":666,"output_tokens":555}}}` + "\n\n" +
		`data: {"type":"message_delta","delta":{},"usage":{"input_tokens":120,"cache_read_input_tokens":880,"cache_creation_input_tokens":5,"output_tokens":1}}` + "\n\n"

	up := startUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse)
	}))
	tp := newTestTap(t)
	addr := serveTap(t, tp, up)
	resp, err := http.Post(addr+"/v1/messages", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	sum := waitForRespCapture(t, tp.outDir)
	usage := sum["usage"].(map[string]any)
	if usage["input_tokens"] != float64(120) || usage["cache_read_input_tokens"] != float64(880) ||
		usage["cache_creation_input_tokens"] != float64(5) || usage["output_tokens"] != float64(1) {
		t.Errorf("usage = %v, want delta 值 120/880/5/1（start 的 999/777/666/555 必须被覆盖）", usage)
	}
}

// 非 SSE 响应：只落元数据，无 usage；正文内容零落盘。
func TestTapNonSSEResponseSummary(t *testing.T) {
	up := startUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"nonce":"plain-json-nonce-XYZZY-2"}`)
	}))
	tp := newTestTap(t)
	addr := serveTap(t, tp, up)
	resp, err := http.Post(addr+"/v1/messages", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	sum := waitForRespCapture(t, tp.outDir)
	if src := sum["usage_source"]; src != "none" {
		t.Errorf("usage_source = %v, want none（非 SSE 无 usage）", src)
	}
	if _, has := sum["usage"]; has {
		t.Errorf("非 SSE 响应不应有 usage 字段（避免全 0 假值冒充真值）: %v", sum["usage"])
	}
	for _, c := range captureFiles(t, tp.outDir) {
		if strings.Contains(c, "plain-json-nonce-XYZZY-2") {
			t.Error("非 SSE 响应正文落盘——违反“响应正文零落盘”")
		}
	}
}

// 压缩响应：不解析 usage（正文保真透传，不解压不重写），来源如实标注。
func TestTapCompressedResponseSkipsUsage(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	io.WriteString(gz, `data: {"type":"message_delta","delta":{},"usage":{"input_tokens":7,"output_tokens":2}}`+"\n\n")
	gz.Close()

	up := startUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(buf.Bytes())
	}))
	tp := newTestTap(t)
	addr := serveTap(t, tp, up)
	resp, err := http.Post(addr+"/v1/messages", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	sum := waitForRespCapture(t, tp.outDir)
	if src := sum["usage_source"]; src != "skipped_compressed" {
		t.Errorf("usage_source = %v, want skipped_compressed", src)
	}
	if _, has := sum["usage"]; has {
		t.Errorf("压缩流不应产出 usage: %v", sum["usage"])
	}
}

// ---- 自检端点：零捕获 ----

func TestTapPingEndpointNoCapture(t *testing.T) {
	up := startUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("ping 不应触达上游")
	}))
	tp := newTestTap(t)
	addr := serveTap(t, tp, up)
	resp, err := http.Get(addr + "/__capture/ping")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "ok") {
		t.Fatalf("ping = %d %q, want 200 ok", resp.StatusCode, body)
	}
	if files, _ := filepath.Glob(filepath.Join(tp.outDir, "*.json")); len(files) != 0 {
		t.Errorf("ping 产生了捕获文件: %v", files)
	}
}

// ---- 权限位传参（Windows 下权限位不生效，验收条款＝断言 os.FileMode 传参） ----

func TestTapPermsPassedToOS(t *testing.T) {
	var mu sync.Mutex
	var mkdirPerms, filePerms []os.FileMode
	origMkdir, origWrite := osMkdirAll, osWriteFile
	osMkdirAll = func(path string, perm os.FileMode) error {
		mu.Lock()
		mkdirPerms = append(mkdirPerms, perm)
		mu.Unlock()
		return origMkdir(path, perm)
	}
	osWriteFile = func(path string, data []byte, perm os.FileMode) error {
		mu.Lock()
		filePerms = append(filePerms, perm)
		mu.Unlock()
		return origWrite(path, data, perm)
	}
	t.Cleanup(func() { osMkdirAll, osWriteFile = origMkdir, origWrite })

	up := startUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	}))
	tp, err := newTap(t.TempDir()) // 构造即建目录（0o700）
	if err != nil {
		t.Fatal(err)
	}
	addr := serveTap(t, tp, up)
	resp, err := http.Post(addr+"/v1/messages", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	waitForRespCapture(t, tp.outDir)

	mu.Lock()
	defer mu.Unlock()
	if len(mkdirPerms) == 0 {
		t.Error("osMkdirAll 未被调用（捕获目录未建）")
	}
	for _, p := range mkdirPerms {
		if p != 0o700 {
			t.Errorf("目录权限传参 = %o, want 700", p)
		}
	}
	if len(filePerms) == 0 {
		t.Error("osWriteFile 未被调用（捕获未落盘）")
	}
	for _, p := range filePerms {
		if p != 0o600 {
			t.Errorf("文件权限传参 = %o, want 600", p)
		}
	}
}

// ---- 默认输出目录 ----

func TestDefaultOutDir(t *testing.T) {
	d, err := defaultOutDir()
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.ToSlash(d); !strings.HasSuffix(got, "ferryman/router-fidelity") {
		t.Errorf("defaultOutDir = %s, want 后缀 ferryman/router-fidelity", got)
	}
}
