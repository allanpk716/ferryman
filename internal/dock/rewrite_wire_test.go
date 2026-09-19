// rewrite_wire_test.go — 票06：改写接线＋守卫回退＋dock 科目＋漂移接线的
// server 级验收钉子。复用 server_test.go 的 upstreamEcho/ccRequest 夹具
// （同包）。
package dock

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/config"
)

// rewriteDockCfg 放行形态的 [dock] 节（上游由调用方给）。
func rewriteDockCfg(upstream string) *config.DockCfg {
	return &config.DockCfg{
		Listen:          "127.0.0.1:15722",
		UpstreamBaseURL: upstream,
		RewriteEnabled:  true,
		APIKey:          "sk-real-key",
		ModelMap: map[string]string{
			"claude-opus-5":   "glm-5.5",
			"claude-sonnet-5": "glm-5.3-air",
			"default":         "glm-4.7-flash",
		},
		TextOnly: []string{"glm-5.3-air"},
	}
}

func frontOf(t *testing.T, srv *Server) string {
	t.Helper()
	front := httptest.NewServer(srv)
	t.Cleanup(front.Close)
	return front.URL
}

// waitDockRows 轮询等 dock 行落盘。recordRow 在代理返回后才执行，而客户端
// 可能先收到完整响应（FlushInterval:-1 逐块冲刷）——测试须与该收尾竞速，
// 短轮询等齐再断言（实现侧顺序无问题，纯测试时序协调）。
func waitDockRows(t *testing.T, acc *accounts.Accounts, want int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		rows := acc.Read(accounts.ReadOpts{Kind: "dock"})
		if len(rows) >= want {
			return rows
		}
		if time.Now().After(deadline) {
			t.Fatalf("dock 行未落盘: want %d, got %d", want, len(rows))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRewriteHappyPathRewritesBodyAndHeaders(t *testing.T) {
	var up upstreamEcho
	backend := httptest.NewServer(http.HandlerFunc(up.handler))
	defer backend.Close()

	dcfg := rewriteDockCfg(backend.URL)
	srv, err := NewWithOptions(dcfg.Listen, dcfg.UpstreamBaseURL, Options{Dock: dcfg})
	if err != nil {
		t.Fatal(err)
	}
	front := frontOf(t, srv)

	original := []byte(`{"model":"claude-opus-5[1M]","max_tokens":1024,` +
		`"metadata":{"session_id":"sess-rw"},` +
		`"messages":[{"role":"user","content":[` +
		`{"type":"text","text":"看图"},` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}`)
	resp, err := http.DefaultClient.Do(ccRequest(t, front+"/v1/messages", original))
	if err != nil {
		t.Fatal(err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(respBody) != "backend-ok" {
		t.Fatalf("响应回流 = %q, want backend-ok", respBody)
	}

	// 期望改写体＝同一改写器的输出（改写五件细节已由 rewrite_test.go 钉死，
	// 这里钉"接线确实用了它"）。
	want, err := Rewrite(original, RewriteConfig{
		ModelMap: map[string]string{"claude-opus-5": "glm-5.5", "claude-sonnet-5": "glm-5.3-air"},
		Default:  "glm-4.7-flash",
		TextOnly: []string{"glm-5.3-air"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, gotBody := up.snapshot()
	if !bytes.Equal(gotBody, want.Body) {
		t.Fatalf("上游收到的体与改写器输出不符:\ngot:  %s\nwant: %s", gotBody, want.Body)
	}

	gotHdr, gotHost, _ := up.snapshot()
	if got := gotHdr.Get("Authorization"); got != "Bearer sk-real-key" {
		t.Fatalf("出站 Authorization = %q, want 真钥 Bearer", got)
	}
	if gotHdr.Get("X-Api-Key") != "" {
		t.Fatal("x-api-key 未删")
	}
	for _, k := range []string{"X-Forwarded-For", "X-Real-Ip", "Traceparent", "Cdn-Loop"} {
		if gotHdr.Get(k) != "" {
			t.Fatalf("%s 未删", k)
		}
	}
	beta := gotHdr.Get("Anthropic-Beta")
	if !strings.Contains(beta, "claude-code-20250219") || !strings.Contains(beta, "oauth-2025-04-20") {
		t.Fatalf("beta 重建不符（含必需标记＋保留客户端标记）: %q", beta)
	}
	if gotHdr.Get("User-Agent") != "claude-cli/2.0.0 (external)" {
		t.Fatal("UA 须保留")
	}
	if gotHdr.Get("Anthropic-Version") != "2023-06-01" {
		t.Fatal("anthropic-version 须透传")
	}
	target, _ := url.Parse(backend.URL)
	if gotHost != target.Host {
		t.Fatalf("Host = %q, want 上游 %q", gotHost, target.Host)
	}

	// 快照存改写前 CC 原始体（beat 重放原始请求经渡口再走同一改写的不变式）
	main, ok := srv.Snapshots().Main("sess-rw")
	if !ok {
		t.Fatal("快照未捕获 sess-rw")
	}
	if !bytes.Equal(main.Body, original) {
		t.Fatalf("快照须存改写前原始体: %s", main.Body)
	}
	if !strings.Contains(string(main.Body), "claude-opus-5[1M]") {
		t.Fatalf("快照体模型名应是原始值: %s", main.Body)
	}
}

func TestRewriteGuardBlocksLocalRelayAtServer(t *testing.T) {
	// 三种回环形态在构造期全部拒绝改写（rewriteOn=false）；大模型上游放行。
	table := []struct {
		upstream string
		wantOn   bool
	}{
		{"http://127.0.0.1:15721", false},
		{"http://localhost:15721", false},
		{"http://[::1]:15722", false},
		{"https://open.bigmodel.cn/api/paas/v4", true},
	}
	for _, tc := range table {
		srv, err := NewWithOptions("127.0.0.1:15722", tc.upstream,
			Options{Dock: rewriteDockCfg(tc.upstream)})
		if err != nil {
			t.Fatalf("%s: %v", tc.upstream, err)
		}
		if srv.rewriteOn != tc.wantOn {
			t.Fatalf("%s: rewriteOn = %v, want %v", tc.upstream, srv.rewriteOn, tc.wantOn)
		}
	}
}

func TestRewriteMissingDefaultFallsBackPassthrough(t *testing.T) {
	var up upstreamEcho
	backend := httptest.NewServer(http.HandlerFunc(up.handler))
	defer backend.Close()
	acc, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dcfg := rewriteDockCfg(backend.URL)
	dcfg.ModelMap = map[string]string{"claude-opus-5": "glm-5.5"} // 无 default 键
	srv, err := NewWithOptions(dcfg.Listen, dcfg.UpstreamBaseURL,
		Options{Dock: dcfg, Accounts: acc})
	if err != nil {
		t.Fatal(err)
	}
	front := frontOf(t, srv)

	body := []byte(`{"model":"claude-opus-5","max_tokens":8,"metadata":{"session_id":"s1"},"messages":[]}`)
	resp, err := http.DefaultClient.Do(ccRequest(t, front+"/v1/messages", body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if srv.rewriteOn {
		t.Fatal("缺 default 须退回纯透传")
	}
	_, _, gotBody := up.snapshot()
	if !bytes.Equal(gotBody, body) {
		t.Fatal("守卫拒绝后须逐字节透传")
	}
	gotHdr, _, _ := up.snapshot()
	// 头零处理＝票01 保真语义：入站认证照抄、无真钥替换
	if gotHdr.Get("X-Api-Key") != "sk-ant-placeholder" ||
		gotHdr.Get("Authorization") != "Bearer placeholder-token" {
		t.Fatalf("退回透传后头须零处理: %v", gotHdr)
	}
	// 行仍要记（透传模式，前后模型名同值）
	rows := waitDockRows(t, acc, 1)
	if rows[0]["mode"] != "passthrough" {
		t.Fatalf("mode = %v", rows[0]["mode"])
	}
	if rows[0]["model_in"] != rows[0]["model_out"] || rows[0]["model_in"] != "claude-opus-5" {
		t.Fatalf("透传行模型名: %v/%v", rows[0]["model_in"], rows[0]["model_out"])
	}
}

func TestRewriteCountTokensModelOnly(t *testing.T) {
	var up upstreamEcho
	backend := httptest.NewServer(http.HandlerFunc(up.handler))
	defer backend.Close()
	acc, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dcfg := rewriteDockCfg(backend.URL)
	srv, err := NewWithOptions(dcfg.Listen, dcfg.UpstreamBaseURL,
		Options{Dock: dcfg, Accounts: acc})
	if err != nil {
		t.Fatal(err)
	}
	front := frontOf(t, srv)

	body := []byte(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":[` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"QQ=="}}]}]}`)
	resp, err := http.DefaultClient.Do(ccRequest(t, front+"/v1/messages/count_tokens", body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	_, _, gotBody := up.snapshot()
	var parsed map[string]any
	if err := json.Unmarshal(gotBody, &parsed); err != nil {
		t.Fatalf("count_tokens 体非法: %s", gotBody)
	}
	if parsed["model"] != "glm-5.3-air" {
		t.Fatalf("count_tokens 模型须同映射: %v", parsed["model"])
	}
	// 其余键不动：image 块原样保留（count_tokens 不做图片降级）
	c0 := parsed["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if c0[0].(map[string]any)["type"] != "image" {
		t.Fatalf("count_tokens 除 model 外键须不动（不降级图片）: %v", c0)
	}
	rows := waitDockRows(t, acc, 1)
	if rows[0]["mode"] != "rewrite" || rows[0]["model_out"] != "glm-5.3-air" {
		t.Fatalf("count_tokens 行: %v", rows[0])
	}

	// 无 model 的 count_tokens：原样透传
	body2 := []byte(`{"messages":[]}`)
	resp, err = http.DefaultClient.Do(ccRequest(t, front+"/v1/messages/count_tokens", body2))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	_, _, gotBody = up.snapshot()
	if !bytes.Equal(gotBody, body2) {
		t.Fatal("无 model 的 count_tokens 须原样透传")
	}
}

// TestRewriteRecordsUsageRow 改写模式解析上游 SSE usage（message_delta 真值
// 覆盖 message_start 的 0，Q14 语义）落 dock 科目行。
func TestRewriteRecordsUsageRow(t *testing.T) {
	var up upstreamEcho
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		up.mu.Lock()
		up.body = b
		up.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: message_start\n"+
			`data: {"type":"message_start","message":{"usage":{"input_tokens":0,`+
			`"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":0}}}`+"\n\n")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, "event: message_delta\n"+
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":`+
			`{"input_tokens":120,"cache_read_input_tokens":880,"cache_creation_input_tokens":5,"output_tokens":1}}`+"\n\n")
		w.(http.Flusher).Flush()
	}))
	defer backend.Close()
	acc, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dcfg := rewriteDockCfg(backend.URL)
	srv, err := NewWithOptions(dcfg.Listen, dcfg.UpstreamBaseURL,
		Options{Dock: dcfg, Accounts: acc})
	if err != nil {
		t.Fatal(err)
	}
	front := frontOf(t, srv)

	body := []byte(`{"model":"claude-opus-5","stream":true,"max_tokens":16,` +
		`"metadata":{"session_id":"sess-acc"},"messages":[]}`)
	resp, err := http.DefaultClient.Do(ccRequest(t, front+"/v1/messages", body))
	if err != nil {
		t.Fatal(err)
	}
	all, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(all), "message_delta") {
		t.Fatalf("SSE 未回流: %s", all)
	}

	rows := waitDockRows(t, acc, 1)
	row := rows[0]
	if row["mode"] != "rewrite" {
		t.Fatalf("mode = %v", row["mode"])
	}
	if row["model_in"] != "claude-opus-5" || row["model_out"] != "glm-5.5" {
		t.Fatalf("模型名: %v/%v", row["model_in"], row["model_out"])
	}
	for k, want := range map[string]float64{
		"input_tokens": 120, "cache_read_tokens": 880, "cache_creation_tokens": 5, "output_tokens": 1,
	} {
		if got := row[k].(float64); got != want {
			t.Fatalf("%s = %v, want %v（message_delta 真值）", k, row[k], want)
		}
	}
	if row["status"].(float64) != 200 {
		t.Fatalf("status = %v", row["status"])
	}
	if row["session_id"] != "sess-acc" {
		t.Fatalf("session = %v", row["session_id"])
	}
	if row["latency_s"].(float64) < 0 {
		t.Fatal("latency_s 应非负")
	}
}

// TestPassthroughWithAccountsRecordsRow 透传模式（无 [dock] 改写）也记行：
// 前后模型名同值；响应不解析（保真优先）→ token 尽力而为记 0（票面许可条款）。
func TestPassthroughWithAccountsRecordsRow(t *testing.T) {
	var up upstreamEcho
	backend := httptest.NewServer(http.HandlerFunc(up.handler))
	defer backend.Close()
	acc, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewWithOptions("127.0.0.1:15722", backend.URL, Options{Accounts: acc})
	if err != nil {
		t.Fatal(err)
	}
	front := frontOf(t, srv)

	body := []byte(`{"model":"claude-opus-5","max_tokens":8,"metadata":{"session_id":"s2"},"messages":[]}`)
	resp, err := http.DefaultClient.Do(ccRequest(t, front+"/v1/messages", body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	rows := waitDockRows(t, acc, 1)
	row := rows[0]
	if row["mode"] != "passthrough" || row["model_in"] != "claude-opus-5" || row["model_out"] != "claude-opus-5" {
		t.Fatalf("透传行: %v", row)
	}
	if row["status"].(float64) != 200 {
		t.Fatalf("status = %v", row["status"])
	}
	if row["input_tokens"].(float64) != 0 {
		t.Fatalf("透传不解析响应, token 应记 0: %v", row["input_tokens"])
	}
}

func TestRewriteBadBodyFallsBackAndRecordsRow(t *testing.T) {
	var up upstreamEcho
	backend := httptest.NewServer(http.HandlerFunc(up.handler))
	defer backend.Close()
	acc, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dcfg := rewriteDockCfg(backend.URL)
	srv, err := NewWithOptions(dcfg.Listen, dcfg.UpstreamBaseURL,
		Options{Dock: dcfg, Accounts: acc})
	if err != nil {
		t.Fatal(err)
	}
	front := frontOf(t, srv)

	body := []byte(`totally not json ✗`)
	resp, err := http.DefaultClient.Do(ccRequest(t, front+"/v1/messages", body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	_, _, gotBody := up.snapshot()
	if !bytes.Equal(gotBody, body) {
		t.Fatal("改写失败须原体透传（应答交上游校验）")
	}
	rows := waitDockRows(t, acc, 1)
	if rows[0]["mode"] != "rewrite" || rows[0]["model_in"] != "" {
		t.Fatalf("失败行: %v", rows[0])
	}
}

func TestDriftAlertsWiredThroughOptions(t *testing.T) {
	var up upstreamEcho
	backend := httptest.NewServer(http.HandlerFunc(up.handler))
	defer backend.Close()
	var mu sync.Mutex
	var alerts []string
	dcfg := rewriteDockCfg(backend.URL)
	srv, err := NewWithOptions(dcfg.Listen, dcfg.UpstreamBaseURL, Options{
		Dock: dcfg,
		Alert: func(title, message string) {
			mu.Lock()
			alerts = append(alerts, title+"|"+message)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	front := frontOf(t, srv)

	send := func(beta string) {
		req := ccRequest(t, front+"/v1/messages",
			[]byte(`{"model":"claude-opus-5","metadata":{"session_id":"s3"},"messages":[]}`))
		req.Header.Set("Anthropic-Beta", beta)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	send("beta-first") // 首见：登记不告警
	mu.Lock()
	n := len(alerts)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("首见不应告警: %v", alerts)
	}
	send("beta-second") // 新标记：告警一次且点名
	mu.Lock()
	defer mu.Unlock()
	if len(alerts) != 1 || !strings.Contains(alerts[0], "beta-second") {
		t.Fatalf("应告警一次且点名: %v", alerts)
	}
}
