// codexlane_test.go — 票03 车道级验收钉子（httptest 假上游 × 渡口全链路）：
// 路径分流/model 改写/GLM 注入/cache 断点/鉴权/原生透传/不支持分支/记账
// （事件+四列，无快照）。
package dock

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/config"
)

// anthropicFakeUpstream Anthropic 方言假上游：记录方法/路径/鉴权头/请求体，
// 可配 SSE / JSON / 错误响应。
type anthropicFakeUpstream struct {
	srv    *httptest.Server
	gotCh  chan upstreamCapture
	respFn func(w http.ResponseWriter, body map[string]any)
}

type upstreamCapture struct {
	method string
	path   string
	auth   string
	accept string
	beta   string
	ver    string
	uagent string
	body   string
}

func newAnthropicFakeUpstream(t *testing.T, respFn func(w http.ResponseWriter, body map[string]any)) *anthropicFakeUpstream {
	t.Helper()
	u := &anthropicFakeUpstream{gotCh: make(chan upstreamCapture, 16), respFn: respFn}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		cap := upstreamCapture{
			method: r.Method, path: r.URL.Path,
			auth:   r.Header.Get("Authorization"),
			accept: r.Header.Get("Accept"),
			beta:   r.Header.Get("Anthropic-Beta"),
			ver:    r.Header.Get("Anthropic-Version"),
			uagent: r.Header.Get("User-Agent"),
			body:   string(b),
		}
		u.gotCh <- cap
		if u.respFn != nil {
			obj := decodeJSONObject(b)
			u.respFn(w, obj)
		}
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *anthropicFakeUpstream) capture(t *testing.T) upstreamCapture {
	t.Helper()
	select {
	case c := <-u.gotCh:
		return c
	default:
		t.Fatal("假上游未收到请求")
		return upstreamCapture{}
	}
}

// codexLaneEntry 造一个 anthropic 方言条目（codex 主模型键可选）。
func codexLaneEntry(baseURL, apiKey string, modelMap map[string]string) *config.DockUpstream {
	return &config.DockUpstream{
		BaseURL:  baseURL,
		APIKey:   apiKey,
		ModelMap: modelMap,
		Dialect:  config.DialectAnthropic,
	}
}

// newCodexLaneDock 建渡口 + httptest 前门。
func newCodexLaneDock(t *testing.T, up *config.DockUpstream, acc *accounts.Accounts) string {
	t.Helper()
	srv, err := NewWithOptions("127.0.0.1:15722", up.BaseURL, Options{Upstream: up, Accounts: acc})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(srv)
	t.Cleanup(front.Close)
	return front.URL
}

// codexRequest 发一个 responses 请求。
func codexRequest(t *testing.T, url string, body string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// ---- 验收 1：路径分流 ----

func TestResponsesLanePathRouting(t *testing.T) {
	up := newAnthropicFakeUpstream(t, func(w http.ResponseWriter, body map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m1","type":"message","role":"assistant","model":"glm-5.3",` +
			`"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",` +
			`"usage":{"input_tokens":1,"output_tokens":1}}`))
	})
	front := newCodexLaneDock(t, codexLaneEntry(up.srv.URL, "sk-lane-key", map[string]string{
		"default": "glm-5.3", "codex": "glm-5.3",
	}), nil)

	// §4.1 变体全表 → 全进车道（上游收到 /v1/messages 翻译产物）。
	variants := []string{
		"/responses", "/v1/responses", "/v1/v1/responses", "/codex/v1/responses",
		"/responses/compact", "/v1/responses/compact", "/v1/v1/responses/compact", "/codex/v1/responses/compact",
	}
	body := `{"model":"glm-5.3","max_output_tokens":100,"stream":false,"input":[{"role":"user","content":"hi"}]}`
	for _, v := range variants {
		resp, _ := codexRequest(t, front+v, body)
		if resp.StatusCode != 200 {
			t.Fatalf("变体 %s = %d", v, resp.StatusCode)
		}
		c := up.capture(t)
		if c.path != "/v1/messages" {
			t.Fatalf("变体 %s → 上游路径 %q, want /v1/messages（端点改写+归一化）", v, c.path)
		}
		if !strings.Contains(c.body, `"messages"`) || strings.Contains(c.body, `"input"`) {
			t.Fatalf("变体 %s 上游体未翻译: %s", v, c.body)
		}
	}
}

// /v1/messages 既有车道零变化（同渡口上两车道共存）。
func TestResponsesLaneCoexistsWithMessagesLane(t *testing.T) {
	up := newAnthropicFakeUpstream(t, func(w http.ResponseWriter, body map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m1","type":"message","content":[{"type":"text","text":"cc-ok"}],"stop_reason":"end_turn"}`))
	})
	front := newCodexLaneDock(t, codexLaneEntry(up.srv.URL, "sk-lane-key", map[string]string{
		"default": "glm-5.3", "codex": "glm-5.3",
	}), nil)

	resp, body := codexRequest(t, front+"/v1/messages",
		`{"model":"claude-x","metadata":{"session_id":"s1"},"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 || !strings.Contains(body, "cc-ok") {
		t.Fatalf("/v1/messages 既有车道被破坏: %d %s", resp.StatusCode, body)
	}
	c := up.capture(t)
	if c.path != "/v1/messages" || c.auth != "Bearer sk-lane-key" {
		t.Fatalf("既有车道出站异常: %+v", c)
	}
	if !strings.Contains(c.body, `"model":"glm-5.3"`) {
		t.Fatalf("既有车道 CC 档位改写失效: %s", c.body)
	}
}

// D1：GET /responses/{id} 不实现——不进车道（走渡口既有通用行为）。
func TestResponsesLaneGETNotRouted(t *testing.T) {
	up := newAnthropicFakeUpstream(t, func(w http.ResponseWriter, body map[string]any) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"type":"not_found_error","message":"no such route"}}`))
	})
	front := newCodexLaneDock(t, codexLaneEntry(up.srv.URL, "sk-lane-key", map[string]string{
		"default": "glm-5.3",
	}), nil)
	resp, err := http.Get(front + "/responses/resp_abc")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /responses/{id} = %d, want 上游 404 透传（诚实信号）", resp.StatusCode)
	}
	// 请求原样到达上游（未翻译——GET 不进车道）。
	c := up.capture(t)
	if c.path != "/responses/resp_abc" {
		t.Fatalf("GET 路径 = %q, want 原样", c.path)
	}
}

// ---- 验收 3：model 改写 ----

func TestResponsesLaneModelRewrite(t *testing.T) {
	up := newAnthropicFakeUpstream(t, func(w http.ResponseWriter, body map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m1","type":"message","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
	})
	entry := codexLaneEntry(up.srv.URL, "k", map[string]string{
		"default": "glm-5.3", "codex": "glm-5.3-codex", "opus": "glm-5.5",
	})
	front := newCodexLaneDock(t, entry, nil)
	srvUp := entry

	t.Run("已知真名原样透传（目录保真）", func(t *testing.T) {
		codexRequest(t, front+"/responses",
			`{"model":"glm-5.5","max_output_tokens":10,"input":[{"role":"user","content":"hi"}]}`)
		c := up.capture(t)
		if !strings.Contains(c.body, `"model":"glm-5.5"`) {
			t.Fatalf("已知真名应透传: %s", c.body)
		}
	})

	t.Run("未知名改写为 codex 主模型键", func(t *testing.T) {
		codexRequest(t, front+"/responses",
			`{"model":"gpt-7","max_output_tokens":10,"input":[{"role":"user","content":"hi"}]}`)
		c := up.capture(t)
		if !strings.Contains(c.body, `"model":"glm-5.3-codex"`) {
			t.Fatalf("未知名应改写 codex 键: %s", c.body)
		}
	})

	t.Run("主模型键未配置 → 透传原文 + 告警事件", func(t *testing.T) {
		srv, err := NewWithOptions("127.0.0.1:15722", up.srv.URL, Options{Upstream: func() *config.DockUpstream {
			e := codexLaneEntry(up.srv.URL, "k", map[string]string{"default": "glm-5.3"})
			return e
		}()})
		if err != nil {
			t.Fatal(err)
		}
		f := httptest.NewServer(srv)
		defer f.Close()
		codexRequest(t, f.URL+"/responses",
			`{"model":"mystery-model","max_output_tokens":10,"input":[{"role":"user","content":"hi"}]}`)
		c := up.capture(t)
		if !strings.Contains(c.body, `"model":"mystery-model"`) {
			t.Fatalf("未配 codex 键应透传原文（不静默替换）: %s", c.body)
		}
		evs := srv.codexEvents.snapshot()
		found := false
		for _, ev := range evs {
			if strings.Contains(ev, "mystery-model") && strings.Contains(ev, "未知模型名") {
				found = true
			}
		}
		if !found {
			t.Fatalf("未知模型告警事件缺失: %v", evs)
		}
	})

	_ = srvUp
}

// ---- 验收 4/5：GLM thinking:disabled + [1M] + cache 断点 ----

func TestResponsesLaneGLMThinkingDisabled(t *testing.T) {
	up := newAnthropicFakeUpstream(t, func(w http.ResponseWriter, body map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m1","type":"message","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
	})
	// GLM 域 + glm 模型名（双信号任一命中）。
	entry := codexLaneEntry("https://open.bigmodel.cn/api/anthropic", "k", map[string]string{
		"default": "glm-5.3", "codex": "glm-5.3",
	})
	entry.BaseURL = up.srv.URL // 指向假上游（模型名信号判定 GLM）
	front := newCodexLaneDock(t, entry, nil)

	// effort=high 本会推导 enabled；GLM 目标强制 disabled（幂等覆盖）。
	codexRequest(t, front+"/responses",
		`{"model":"glm-5.3","max_output_tokens":40000,"reasoning":{"effort":"high"},`+
			`"input":[{"role":"user","content":"hi"}]}`)
	c := up.capture(t)
	if !strings.Contains(c.body, `"thinking":{"type":"disabled"}`) {
		t.Fatalf("GLM 目标应强制 thinking:disabled: %s", c.body)
	}

	// 非 GLM 条目不注入。
	entry2 := codexLaneEntry(up.srv.URL, "k", map[string]string{"default": "deepseek-v4", "codex": "deepseek-v4"})
	front2 := newCodexLaneDock(t, entry2, nil)
	codexRequest(t, front2+"/responses",
		`{"model":"deepseek-v4","max_output_tokens":40000,"reasoning":{"effort":"high"},`+
			`"input":[{"role":"user","content":"hi"}]}`)
	c2 := up.capture(t)
	if !strings.Contains(c2.body, `"type":"enabled"`) {
		t.Fatalf("非 GLM 目标保留 thinking 推导: %s", c2.body)
	}
}

func TestResponsesLaneOneMStripAndCacheBreakpoints(t *testing.T) {
	up := newAnthropicFakeUpstream(t, func(w http.ResponseWriter, body map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m1","type":"message","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
	})
	front := newCodexLaneDock(t, codexLaneEntry(up.srv.URL, "k", map[string]string{
		"default": "glm-5.3", "codex": "glm-5.3",
	}), nil)

	// [1M] 模型名尾标：译文剥后缀 + 出站注入 context-1m beta（§1.6/§5.5）。
	codexRequest(t, front+"/responses",
		`{"model":"glm-5.3[1M]","max_output_tokens":10,"input":[{"role":"user","content":"hi"}]}`)
	c := up.capture(t)
	if !strings.Contains(c.body, `"model":"glm-5.3"`) || strings.Contains(c.body, "[1M]") {
		t.Fatalf("[1M] 剥离失败: %s", c.body)
	}
	if c.beta != "context-1m-2025-08-07" {
		t.Fatalf("Anthropic-Beta = %q, want context-1m 旗标", c.beta)
	}

	// cache 断点四步注入进上游体：tools 末尾 + system 末尾 + 最新消息 + 倒数
	// 第二条 user（翻译后 messages≥4 时）。相邻同 role 项会合并，用交替序。
	codexRequest(t, front+"/responses",
		`{"model":"glm-5.3","max_output_tokens":10,"instructions":"sys text",`+
			`"tools":[{"type":"function","name":"t","parameters":{"type":"object"}}],`+
			`"input":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},`+
			`{"role":"user","content":"c"},{"role":"assistant","content":"d"},`+
			`{"role":"user","content":"e"}]}`)
	c2 := up.capture(t)
	if got := strings.Count(c2.body, `"cache_control"`); got != 4 {
		t.Fatalf("cache 断点数 = %d, want 4（tools+system+最新消息+倒数第二条 user）:\n%s", got, c2.body)
	}
	if !strings.Contains(c2.body, `{"type":"ephemeral"}`) {
		t.Fatalf("TTL 形态＝裸 ephemeral:\n%s", c2.body)
	}
	// 字符串 system 已数组化。
	if !strings.Contains(c2.body, `"system":[{"cache_control":`) && !strings.Contains(c2.body, `"system":[{"text":"sys text"`) {
		t.Fatalf("system 数组化失败:\n%s", c2.body)
	}
}

// ---- 验收 6/鉴权 ----

func TestResponsesLaneAuthAndHeaders(t *testing.T) {
	up := newAnthropicFakeUpstream(t, func(w http.ResponseWriter, body map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m1","type":"message","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
	})
	front := newCodexLaneDock(t, codexLaneEntry(up.srv.URL, "sk-real-lane-key", map[string]string{
		"default": "glm-5.3", "codex": "glm-5.3",
	}), nil)

	req, _ := http.NewRequest("POST", front+"/responses",
		strings.NewReader(`{"model":"glm-5.3","max_output_tokens":10,"input":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer codex-placeholder-key")
	req.Header.Set("X-Api-Key", "codex-placeholder-key")
	req.Header.Set("Originator", "codex_cli_rs")
	req.Header.Set("Session_id", "sess-1")
	req.Header.Set("Openai-Beta", "responses=experimental")
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.Header.Set("X-Stainless-Retry-Count", "2")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", "codex/0.5")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	c := up.capture(t)
	if c.auth != "Bearer sk-real-lane-key" {
		t.Fatalf("Authorization = %q, want 真钥注入（T39：真钥只进出站头）", c.auth)
	}
	if c.accept != "application/json" {
		t.Fatalf("Accept = %q, want 强制 application/json", c.accept)
	}
	if c.ver != "2023-06-01" {
		t.Fatalf("Anthropic-Version = %q, want 缺省补 2023-06-01", c.ver)
	}
	if c.uagent != "codex/0.5" {
		t.Fatalf("User-Agent = %q, want 透传", c.uagent)
	}
	if c.beta != "" {
		t.Fatalf("普通翻译路径不发任何 beta: %q", c.beta)
	}
}

// ---- 验收 7：不支持分支（F11）----

func TestResponsesLaneUnsupportedImmediate(t *testing.T) {
	entry := codexLaneEntry("https://anywhere.test", "k", map[string]string{
		"default": "x", "codex": "x",
	})
	entry.Codex = config.CodexUnsupported
	front := newCodexLaneDock(t, entry, nil)

	start := time.Now()
	resp, body := codexRequest(t, front+"/responses",
		`{"model":"x","max_output_tokens":10,"input":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("状态 = %d, want 503（即刻显式错误）", resp.StatusCode)
	}
	if !strings.Contains(body, "unsupported") || !strings.Contains(body, "codex") {
		t.Fatalf("错误体应带清晰原因: %s", body)
	}
	errObj := decodeJSONObject([]byte(body))
	if errObj == nil {
		t.Fatalf("错误体非 JSON object: %s", body)
	}
	e, ok := asMap(errObj["error"])
	if !ok || mapStr(e, "message") == "" {
		t.Fatalf("错误体形状 = %s（responses error 形）", body)
	}
	// 即刻返回：不挂起（<2s 远小于任何超时）。
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("耗时 %v，疑似挂起", d)
	}
}

// ---- 验收 8：记账（事件+四列，无快照）----

func TestResponsesLaneAccounting(t *testing.T) {
	up := newAnthropicFakeUpstream(t, func(w http.ResponseWriter, body map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m1","type":"message","content":[{"type":"text","text":"ok"}],` +
			`"stop_reason":"end_turn","usage":{"input_tokens":100,"cache_read_input_tokens":30,` +
			`"cache_creation_input_tokens":20,"output_tokens":50}}`))
	})
	acc, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	entry := codexLaneEntry(up.srv.URL, "k", map[string]string{"default": "glm-5.3", "codex": "glm-5.3"})
	srv, err := NewWithOptions("127.0.0.1:15722", up.srv.URL, Options{Upstream: entry, Accounts: acc})
	if err != nil {
		t.Fatal(err)
	}
	f2 := httptest.NewServer(srv)
	defer f2.Close()

	req, _ := http.NewRequest("POST", f2.URL+"/responses",
		strings.NewReader(`{"model":"gpt-x","max_output_tokens":10,"input":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Session_id", "codex-sess-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	rows := waitDockRows(t, acc, 1)
	row := rows[0]
	if row["agent"] != "codex" {
		t.Fatalf("agent = %v, want codex（lane 标注）", row["agent"])
	}
	if row["mode"] != "codex_translate" {
		t.Fatalf("mode = %v, want codex_translate", row["mode"])
	}
	if row["session_id"] != "codex-sess-1" {
		t.Fatalf("session_id = %v（codex session_id 头）", row["session_id"])
	}
	if row["model_in"] != "gpt-x" || row["model_out"] != "glm-5.3" {
		t.Fatalf("model_in/out = %v/%v", row["model_in"], row["model_out"])
	}
	if row["input_tokens"] != float64(100) || row["cache_read_tokens"] != float64(30) ||
		row["cache_creation_tokens"] != float64(20) || row["output_tokens"] != float64(50) {
		t.Fatalf("四列 = %v %v %v %v", row["input_tokens"], row["cache_read_tokens"],
			row["cache_creation_tokens"], row["output_tokens"])
	}
	if row["status"] != float64(200) {
		t.Fatalf("status = %v", row["status"])
	}
	// 无快照：codex 车道不进快照库（Main/Last 均无 + 无跳过计数即未触 Capture）。
	if _, ok := srv.Snapshots().Main("codex-sess-1"); ok {
		t.Fatalf("codex 车道不应入快照库")
	}
	if _, ok := srv.Snapshots().Last("codex-sess-1"); ok {
		t.Fatalf("codex 车道不应入快照库（Last）")
	}
}

// ---- 上游错误形态 ----

func TestResponsesLaneUpstreamError(t *testing.T) {
	up := newAnthropicFakeUpstream(t, func(w http.ResponseWriter, body map[string]any) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`))
	})
	front := newCodexLaneDock(t, codexLaneEntry(up.srv.URL, "k", map[string]string{
		"default": "glm-5.3", "codex": "glm-5.3",
	}), nil)

	resp, body := codexRequest(t, front+"/responses",
		`{"model":"glm-5.3","max_output_tokens":10,"input":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("状态 = %d, want 原状态码透传", resp.StatusCode)
	}
	e := decodeJSONObject([]byte(body))
	errObj, _ := asMap(e["error"])
	if mapStr(errObj, "message") != "bad key" || mapStr(errObj, "type") != "authentication_error" {
		t.Fatalf("错误体规整失败: %s", body)
	}
	if _, has := errObj["param"]; !has {
		t.Fatalf("param 字段缺失（responses 错误形状）: %s", body)
	}
}

// ---- 流式端到端 ----

func TestResponsesLaneStreamingE2E(t *testing.T) {
	up := newAnthropicFakeUpstream(t, func(w http.ResponseWriter, body map[string]any) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher := w.(http.Flusher)
		events := []string{
			`{"type":"message_start","message":{"id":"msg_s","model":"glm-5.3","usage":{"input_tokens":9}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"streaming"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
			`{"type":"message_stop"}`,
		}
		for _, ev := range events {
			_, _ = w.Write([]byte("event: x\ndata: " + ev + "\n\n"))
			flusher.Flush()
		}
	})
	front := newCodexLaneDock(t, codexLaneEntry(up.srv.URL, "k", map[string]string{
		"default": "glm-5.3", "codex": "glm-5.3",
	}), nil)

	resp, err := http.Post(front+"/responses", "application/json",
		strings.NewReader(`{"model":"glm-5.3","max_output_tokens":10,"stream":true,`+
			`"input":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control = %q", cc)
	}
	b, _ := io.ReadAll(resp.Body)
	s := string(b)
	for _, want := range []string{
		"event: response.created",
		"event: response.output_text.delta",
		`"delta":"streaming"`,
		"event: response.completed",
		`"status":"completed"`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("缺 %q:\n%s", want, s)
		}
	}
	// 上游收到翻译产物且 stream:true 驱动。
	c := up.capture(t)
	if !strings.Contains(c.body, `"stream":true`) {
		t.Fatalf("stream 透传失败: %s", c.body)
	}
}

// 非流式端到端：上游 JSON → responses JSON。
func TestResponsesLaneNonStreamE2E(t *testing.T) {
	up := newAnthropicFakeUpstream(t, func(w http.ResponseWriter, body map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_n","type":"message","role":"assistant","model":"glm-5.3",` +
			`"content":[{"type":"text","text":"plain"}],"stop_reason":"end_turn",` +
			`"usage":{"input_tokens":3,"output_tokens":2}}`))
	})
	front := newCodexLaneDock(t, codexLaneEntry(up.srv.URL, "k", map[string]string{
		"default": "glm-5.3", "codex": "glm-5.3",
	}), nil)

	resp, body := codexRequest(t, front+"/responses",
		`{"model":"glm-5.3","max_output_tokens":10,"stream":false,"input":[{"role":"user","content":"hi"}]}`)
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want 重建 application/json", ct)
	}
	obj := decodeJSONObject([]byte(body))
	if obj == nil || mapStr(obj, "id") != "resp_msg_n" || mapStr(obj, "object") != "response" {
		t.Fatalf("responses 形状失败: %s", body)
	}
	usage, _ := asMap(obj["usage"])
	if v, _ := asU64(usage["total_tokens"]); v != 5 {
		t.Fatalf("usage = %v", usage)
	}
}

// 流式请求 + 上游显式 JSON Content-Type → 合成 SSE。
func TestResponsesLaneStreamRequestJSONResponse(t *testing.T) {
	up := newAnthropicFakeUpstream(t, func(w http.ResponseWriter, body map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_j","type":"message","role":"assistant","model":"glm-5.3",` +
			`"content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn",` +
			`"usage":{"input_tokens":4,"output_tokens":2}}`))
	})
	front := newCodexLaneDock(t, codexLaneEntry(up.srv.URL, "k", map[string]string{
		"default": "glm-5.3", "codex": "glm-5.3",
	}), nil)

	resp, err := http.Post(front+"/responses", "application/json",
		strings.NewReader(`{"model":"glm-5.3","max_output_tokens":10,"stream":true,`+
			`"input":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream（合成 SSE）", ct)
	}
	b, _ := io.ReadAll(resp.Body)
	s := string(b)
	for _, want := range []string{
		"event: response.created", "event: response.output_text.delta", `"delta":"Hello"`,
		"event: response.completed",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("缺 %q:\n%s", want, s)
		}
	}
}

// ---- 原生透传分支（dialect=openai_responses）----

func TestResponsesLaneNativePassthrough(t *testing.T) {
	var nativeGot chan upstreamCapture = make(chan upstreamCapture, 4)
	var nativeBodyRaw chan []byte = make(chan []byte, 4)
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		nativeBodyRaw <- b
		nativeGot <- upstreamCapture{
			method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"),
			accept: r.Header.Get("Accept"), uagent: r.Header.Get("User-Agent"),
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\n" +
			`data: {"type":"response.created","response":{"id":"resp_native"}}` + "\n\n" +
			"event: response.completed\n" +
			`data: {"type":"response.completed","response":{"id":"resp_native","status":"completed","usage":{"input_tokens":150,"input_tokens_details":{"cached_tokens":50,"cache_write_tokens":25},"output_tokens":10,"total_tokens":160}}}` + "\n\n"))
	}))
	t.Cleanup(native.Close)

	entry := &config.DockUpstream{
		BaseURL:  native.URL,
		APIKey:   "sk-native-key",
		ModelMap: map[string]string{"default": "kimi-k3", "codex": "kimi-k3-turbo"},
		Dialect:  config.DialectOpenAIResponses,
	}
	acc, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	front := newCodexLaneDock(t, entry, acc)

	// 未知字段（vendor_future_field）逐字节达上游；已知 model 不改时体逐字节不动。
	body := `{"model":"kimi-k3","max_output_tokens":100,"stream":true,"vendor_future_field":{"x":[1,2]},"input":"hello"}`
	req, _ := http.NewRequest("POST", front+"/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer placeholder")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", "codex/0.5")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)

	select {
	case raw := <-nativeBodyRaw:
		if string(raw) != body {
			t.Fatalf("原生分支体被改动（已知 model 须逐字节透传）:\n got %s\nwant %s", raw, body)
		}
	default:
		t.Fatal("原生上游未收到请求")
	}
	select {
	case c := <-nativeGot:
		if c.path != "/v1/responses" {
			t.Fatalf("原生路径 = %q, want /v1/responses（变体归一 /responses + 纯 origin 补 /v1）", c.path)
		}
		if c.auth != "Bearer sk-native-key" {
			t.Fatalf("原生鉴权 = %q", c.auth)
		}
		if c.accept != "text/event-stream" {
			t.Fatalf("原生 Accept 应照抄: %q", c.accept)
		}
	default:
	}
	if !strings.Contains(string(b), "event: response.completed") {
		t.Fatalf("原生响应中继失败: %s", b)
	}
	// 记账：native 模式 + responses usage 反推四列（150-50-25=75/50/25/10）。
	rows := waitDockRows(t, acc, 1)
	row := rows[0]
	if row["mode"] != "codex_native" || row["agent"] != "codex" {
		t.Fatalf("mode/agent = %v/%v", row["mode"], row["agent"])
	}
	if row["input_tokens"] != float64(75) || row["cache_read_tokens"] != float64(50) ||
		row["cache_creation_tokens"] != float64(25) || row["output_tokens"] != float64(10) {
		t.Fatalf("四列反推 = %v %v %v %v", row["input_tokens"], row["cache_read_tokens"],
			row["cache_creation_tokens"], row["output_tokens"])
	}
}

// 原生分支未知名改写为 codex 键（体重新编码，未知字段仍全达上游）。
func TestResponsesLaneNativeModelRewrite(t *testing.T) {
	nativeGot := make(chan upstreamCapture, 4)
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		nativeGot <- upstreamCapture{path: r.URL.Path, auth: r.Header.Get("Authorization"), body: string(b)}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(native.Close)

	entry := &config.DockUpstream{
		BaseURL:  native.URL,
		APIKey:   "k",
		ModelMap: map[string]string{"default": "kimi-k3", "codex": "kimi-k3-codex"},
		Dialect:  config.DialectOpenAIResponses,
	}
	front := newCodexLaneDock(t, entry, nil)

	_, _ = codexRequest(t, front+"/responses",
		`{"model":"unknown-model","max_output_tokens":10,"vendor_field":true,"input":"hi"}`)
	select {
	case c := <-nativeGot:
		if !strings.Contains(c.body, `"model":"kimi-k3-codex"`) {
			t.Fatalf("原生分支 model 改写失败: %s", c.body)
		}
		if !strings.Contains(c.body, "vendor_field") {
			t.Fatalf("原生分支未知字段应达上游: %s", c.body)
		}
	default:
		t.Fatal("原生上游未收到请求")
	}
}

// 非法请求体 → 400 invalid_request_error（不挂起）。
func TestResponsesLaneInvalidBody(t *testing.T) {
	up := newAnthropicFakeUpstream(t, nil)
	front := newCodexLaneDock(t, codexLaneEntry(up.srv.URL, "k", map[string]string{
		"default": "glm-5.3", "codex": "glm-5.3",
	}), nil)
	resp, body := codexRequest(t, front+"/responses", `not-json`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("状态 = %d, want 400", resp.StatusCode)
	}
	if !strings.Contains(body, "invalid_request_error") {
		t.Fatalf("错误体: %s", body)
	}
}

// 出站连不上 → 502 proxy_error（渡口自产错误形状）。
func TestResponsesLaneUpstreamUnreachable(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	front := newCodexLaneDock(t, codexLaneEntry(deadURL, "k", map[string]string{
		"default": "glm-5.3", "codex": "glm-5.3",
	}), nil)
	resp, body := codexRequest(t, front+"/responses",
		`{"model":"glm-5.3","max_output_tokens":10,"input":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("状态 = %d, want 502", resp.StatusCode)
	}
	if !strings.Contains(body, "proxy_error") {
		t.Fatalf("错误体: %s", body)
	}
	// 真钥永不入错误文本（T39）。
	if strings.Contains(body, "\"k\"") {
		t.Fatalf("真钥泄漏进错误体: %s", body)
	}
}

// thinking 信封跨请求闭环：请求向编码 ↔ 响应向解码（§1.2 闭环钉子）。
func TestResponsesLaneThinkingEnvelopeWire(t *testing.T) {
	enc := anthropicThinkingEnvelopePrefix + base64.RawURLEncoding.EncodeToString(
		[]byte(`{"signature":"sig_roundtrip","thinking":"deep","type":"thinking"}`))
	up := newAnthropicFakeUpstream(t, func(w http.ResponseWriter, body map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m1","type":"message","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
	})
	front := newCodexLaneDock(t, codexLaneEntry(up.srv.URL, "k", map[string]string{
		"default": "glm-5.3", "codex": "glm-5.3",
	}), nil)
	_, _ = codexRequest(t, front+"/responses",
		`{"model":"glm-5.3","max_output_tokens":100,"input":[`+
			`{"type":"reasoning","encrypted_content":"`+enc+`"},`+
			`{"role":"user","content":"go on"}]}`)
	c := up.capture(t)
	if !strings.Contains(c.body, `"type":"thinking"`) || !strings.Contains(c.body, "sig_roundtrip") {
		t.Fatalf("信封回放进上游体失败: %s", c.body)
	}
}
