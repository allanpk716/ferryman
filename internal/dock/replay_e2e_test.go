// replay_e2e_test.go — 票04：渡口端到端回放（四形夹具 × 两分支假上游）。
//
// 翻译分支（dialect=anthropic）：Anthropic 方言假上游按夹具回放 → 断言出站
// 线协议逐字段符合对照表（§1 请求体/§3 头/§5 model 改写/§1.6 cache 断点）+
// 客户端 responses SSE 逐事件符合 §2.2 事件流总表（含 §2.3 usage/§2.4 状态）。
// 原生透传分支（dialect=openai_responses）：responses 方言假上游按夹具回放 →
// 断言请求逐字节透传（仅 model 改写时重编码、未知字段全达上游）+ 响应逐字
// 节中继。长流形断言全程不截断。
//
// 夹具来自 internal/dock/fixtures（加载强制过零真实密钥扫描）；本文件只组
// 合票03 既有车道，不改其行为。
package dock

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ferryman/internal/config"
	"ferryman/internal/dock/fixtures"
)

// replayTestKeys 合成占位真钥（sk-test-* 纪律；T39：真钥只进出站头）。
const (
	replayTranslateKey = "sk-test-dock-replay-key"
	replayNativeKey    = "sk-test-dock-replay-native"
	replayPlaceholder  = "codex-placeholder-key"
)

// replaySSEFrame 夹具事件 → 线上帧字节（与假上游出帧同源，字节级断言基准）。
func replaySSEFrame(ev fixtures.SSEEvent) []byte {
	var b strings.Builder
	b.WriteString("event: ")
	b.WriteString(ev.Event)
	b.WriteString("\ndata: ")
	b.Write(ev.Data)
	b.WriteString("\n\n")
	return []byte(b.String())
}

// writeReplayReply 假上游按夹具回放脚本应答（逐帧 flush，模真分块流）。
func writeReplayReply(w http.ResponseWriter, reply *fixtures.UpstreamReply) {
	status := reply.Status
	if status == 0 {
		status = http.StatusOK
	}
	if reply.ContentType != "" {
		w.Header().Set("Content-Type", reply.ContentType)
	}
	w.WriteHeader(status)
	fl, _ := w.(http.Flusher)
	if len(reply.SSE) > 0 {
		for _, ev := range reply.SSE {
			_, _ = w.Write(replaySSEFrame(ev))
			if fl != nil {
				fl.Flush()
			}
		}
		return
	}
	_, _ = w.Write([]byte(reply.Body))
	if fl != nil {
		fl.Flush()
	}
}

// replayCapture 假上游收到的请求观测（含指纹头，供剥除断言）。
type replayCapture struct {
	method     string
	path       string
	auth       string
	accept     string
	acceptEnc  string
	beta       string
	ver        string
	uagent     string
	originator string
	sessionID  string
	stainless  string
	openaiBeta string
	body       []byte
}

// replayUpstream 两分支通用假上游：记录观测 + 按夹具脚本回放。
type replayUpstream struct {
	srv *httptest.Server
	got chan replayCapture
}

func newReplayUpstream(t *testing.T, reply *fixtures.UpstreamReply) *replayUpstream {
	t.Helper()
	u := &replayUpstream{got: make(chan replayCapture, 8)}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		u.got <- replayCapture{
			method:     r.Method,
			path:       r.URL.Path,
			auth:       r.Header.Get("Authorization"),
			accept:     r.Header.Get("Accept"),
			acceptEnc:  r.Header.Get("Accept-Encoding"),
			beta:       r.Header.Get("Anthropic-Beta"),
			ver:        r.Header.Get("Anthropic-Version"),
			uagent:     r.Header.Get("User-Agent"),
			originator: r.Header.Get("Originator"),
			sessionID:  r.Header.Get("Session_id"),
			stainless:  r.Header.Get("X-Stainless-Retry-Count"),
			openaiBeta: r.Header.Get("Openai-Beta"),
			body:       b,
		}
		writeReplayReply(w, reply)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *replayUpstream) capture(t *testing.T) replayCapture {
	t.Helper()
	select {
	case c := <-u.got:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("假上游未收到请求")
		return replayCapture{}
	}
}

// ---- 夹具事件解析件 ----

type replayEvent struct {
	name string
	data map[string]any
}

func replayEventData(t *testing.T, ev fixtures.SSEEvent) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(ev.Data, &m); err != nil {
		t.Fatalf("夹具事件 data 非 JSON object: %v", err)
	}
	return m
}

func parseReplaySSE(t *testing.T, raw []byte) []replayEvent {
	t.Helper()
	var evs []replayEvent
	rest := string(raw)
	for {
		block, r, ok := takeSSEBlock(rest)
		if !ok {
			break
		}
		rest = r
		name, data := parseSSEBlock(block)
		if data == nil {
			continue
		}
		evs = append(evs, replayEvent{name: name, data: data})
	}
	return evs
}

func replayNames(evs []replayEvent) []string {
	names := make([]string, len(evs))
	for i, e := range evs {
		names[i] = e.name
	}
	return names
}

func replayByName(evs []replayEvent, name string) *replayEvent {
	for i := range evs {
		if evs[i].name == name {
			return &evs[i]
		}
	}
	return nil
}

func replayExpectSeq(t *testing.T, got []replayEvent, want []string) {
	t.Helper()
	names := replayNames(got)
	if len(names) != len(want) {
		t.Fatalf("事件数 = %d, want %d\n got: %v\nwant: %v", len(names), len(want), names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("第 %d 事件 = %q, want %q\n got: %v\nwant: %v", i, names[i], want[i], names, want)
		}
	}
}

// replayPost 带夹具客户端头 POST（鉴权占位 + codex 指纹头，供两分支头断言）。
func replayPost(t *testing.T, url string, body []byte, native bool) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+replayPlaceholder)
	req.Header.Set("X-Api-Key", replayPlaceholder)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", "codex-fixture/1.0")
	req.Header.Set("Originator", "codex_cli_rs")
	if native {
		req.Header.Set("Session_id", "replay-native")
	} else {
		req.Header.Set("Session_id", "replay-translate")
		req.Header.Set("X-Stainless-Retry-Count", "1")
		req.Header.Set("Openai-Beta", "responses=experimental")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读响应失败: %v", err)
	}
	resp.Body.Close()
	return resp, raw
}

// fixtureRequestBytes 夹具请求体定档字节（原生分支逐字节断言的基准）。
func fixtureRequestBytes(t *testing.T, fx *fixtures.Fixture) []byte {
	t.Helper()
	b, err := json.Marshal(fx.CodexRequest)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// assertTranslateUpstreamWire 翻译分支出站线协议断言（对照表 §1/§3/§5）。
func assertTranslateUpstreamWire(t *testing.T, c replayCapture, fx *fixtures.Fixture, wantModel string) {
	t.Helper()
	if c.path != "/v1/messages" {
		t.Fatalf("上游路径 = %q, want /v1/messages（§4.3 端点改写）", c.path)
	}
	if c.auth != "Bearer "+replayTranslateKey {
		t.Fatalf("Authorization = %q, want 渡口真钥 Bearer 注入（§3.2/T39）", c.auth)
	}
	if c.accept != "application/json" {
		t.Fatalf("Accept = %q, want 强制 application/json（§3.3）", c.accept)
	}
	if c.acceptEnc != "identity" {
		t.Fatalf("Accept-Encoding = %q, want 强制 identity（§3.3）", c.acceptEnc)
	}
	if c.ver != "2023-06-01" {
		t.Fatalf("Anthropic-Version = %q, want 2023-06-01（§3.3）", c.ver)
	}
	if c.beta != "" {
		t.Fatalf("Anthropic-Beta = %q, want 空（普通翻译路径不发 beta）", c.beta)
	}
	// codex/OpenAI 指纹头永不转发（§3.3）。
	if c.originator != "" || c.sessionID != "" || c.stainless != "" || c.openaiBeta != "" {
		t.Fatalf("指纹头泄漏: originator=%q session_id=%q stainless=%q openai-beta=%q",
			c.originator, c.sessionID, c.stainless, c.openaiBeta)
	}
	if c.uagent != "codex-fixture/1.0" {
		t.Fatalf("User-Agent = %q, want 透传（§3.3）", c.uagent)
	}

	body := decodeJSONObject(c.body)
	if body == nil {
		t.Fatalf("上游体非 JSON object: %s", c.body)
	}
	// §1.1 白名单构造：responses 专有顶层字段不进上游体。
	for _, k := range []string{"store", "include", "service_tier", "metadata",
		"prompt_cache_key", "parallel_tool_calls"} {
		if _, has := body[k]; has {
			t.Fatalf("白名单外字段 %s 出现在上游体（§1.7）: %s", k, c.body)
		}
	}
	if _, has := body["input"]; has {
		t.Fatalf("responses 原生键 input 残留（应为 messages）: %s", c.body)
	}
	if _, has := body["messages"]; !has {
		t.Fatalf("上游体缺 messages: %s", c.body)
	}
	if v, _ := asBool(body["stream"]); !v {
		t.Fatalf("stream 应原样透传 true: %s", c.body)
	}
	if mapStr(body, "model") != wantModel {
		t.Fatalf("出站 model = %q, want %q（§5.3 改写）", mapStr(body, "model"), wantModel)
	}
	if mt := uint64From(body["max_tokens"]); mt != uint64From(fx.CodexRequest["max_output_tokens"]) {
		t.Fatalf("max_tokens = %d, want max_output_tokens 映射值 %d（§1.1）",
			mt, uint64From(fx.CodexRequest["max_output_tokens"]))
	}
}

// assertTranslateClientWire 翻译分支客户端 responses SSE 断言（§2.2/§2.3/§2.4）。
func assertTranslateClientWire(t *testing.T, resp *http.Response, raw []byte, fx *fixtures.Fixture) {
	t.Helper()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", cc)
	}
	evs := parseReplaySSE(t, raw)
	switch fx.Name {
	case "normal_reply":
		assertNormalReplyClient(t, evs)
	case "tool_call":
		assertToolCallClient(t, evs)
	case "long_stream":
		assertLongStreamClient(t, evs, fx)
	case "error_injection":
		assertErrorInjectionClient(t, evs)
	default:
		t.Fatalf("未知的夹具形 %q", fx.Name)
	}
}

// ---- 各形客户端断言 ----

func assertNormalReplyClient(t *testing.T, evs []replayEvent) {
	t.Helper()
	replayExpectSeq(t, evs, []string{
		"response.created", "response.in_progress",
		"response.output_item.added", "response.content_part.added",
		"response.output_text.delta", "response.output_text.delta",
		"response.output_text.done", "response.content_part.done",
		"response.output_item.done", "response.completed",
	})
	// 项 id 形（§2.2：message = {resp_id}_msg_{n}）。
	item := asMapT(t, evs[2].data["item"])
	if mapStr(item, "id") != "resp_msg_fx_norm_msg_0" {
		t.Fatalf("message 项 id = %q, want resp_msg_fx_norm_msg_0", mapStr(item, "id"))
	}
	full := "SSE 是服务器向客户端单向持续推流的 HTTP 协议。"
	if d := mapStr(evs[4].data, "delta"); d != "SSE 是服务器" {
		t.Fatalf("第 1 delta = %q", d)
	}
	if d := mapStr(evs[5].data, "delta"); d != "向客户端单向持续推流的 HTTP 协议。" {
		t.Fatalf("第 2 delta = %q", d)
	}
	if txt := mapStr(evs[6].data, "text"); txt != full {
		t.Fatalf("output_text.done = %q, want 全文拼接", txt)
	}
	comp := evs[9]
	r := asMapT(t, comp.data["response"])
	if mapStr(r, "id") != "resp_msg_fx_norm" || mapStr(r, "status") != "completed" {
		t.Fatalf("终态 id/status = %q/%q", mapStr(r, "id"), mapStr(r, "status"))
	}
	assertResponsesUsage(t, asMapT(t, r["usage"]), map[string]uint64{
		"input_tokens": 170, "output_tokens": 25, "total_tokens": 195,
		"cached_tokens": 40, "cache_write_tokens": 10, "cache_creation_input_tokens": 10,
	})
}

func assertToolCallClient(t *testing.T, evs []replayEvent) {
	t.Helper()
	replayExpectSeq(t, evs, []string{
		"response.created", "response.in_progress",
		"response.output_item.added", "response.content_part.added",
		"response.output_text.delta",
		"response.output_text.done", "response.content_part.done",
		"response.output_item.done",
		// 工具块：added → 增量×2 → 收口（§2.2 事件序模板 tool 段）。
		"response.output_item.added",
		"response.function_call_arguments.delta", "response.function_call_arguments.delta",
		"response.function_call_arguments.done", "response.output_item.done",
		"response.completed",
	})
	// function_call 项形（§2.2：fc_{call_id}，call_id 原样）。
	fcAdded := asMapT(t, evs[8].data["item"])
	if mapStr(fcAdded, "id") != "fc_toolu_fx_read_1" || mapStr(fcAdded, "type") != "function_call" ||
		mapStr(fcAdded, "call_id") != "toolu_fx_read_1" || mapStr(fcAdded, "name") != "read_file" {
		t.Fatalf("function_call added 项形 = %v", fcAdded)
	}
	if args := mapStr(evs[11].data, "arguments"); args != `{"path":"src/main.go"}` {
		t.Fatalf("arguments.done = %q, want 规范化 JSON", args)
	}
	fcDone := asMapT(t, evs[12].data["item"])
	if mapStr(fcDone, "status") != "completed" || mapStr(fcDone, "arguments") != `{"path":"src/main.go"}` {
		t.Fatalf("function_call done 项 = %v", fcDone)
	}
	comp := evs[13]
	r := asMapT(t, comp.data["response"])
	if mapStr(r, "id") != "resp_msg_fx_tool" || mapStr(r, "status") != "completed" {
		t.Fatalf("终态 id/status = %q/%q（stop_reason tool_use → completed，§2.4）",
			mapStr(r, "id"), mapStr(r, "status"))
	}
	output := asSliceT(t, r["output"])
	if len(output) != 2 {
		t.Fatalf("终态 output 数 = %d, want 2（文本项 + 工具项）", len(output))
	}
	if mapStr(asMapT(t, output[0]), "type") != "message" || mapStr(asMapT(t, output[1]), "type") != "function_call" {
		t.Fatalf("终态 output 顺序/类型 = %v", replayOutputTypes(output))
	}
	assertResponsesUsage(t, asMapT(t, r["usage"]), map[string]uint64{
		"input_tokens": 88, "output_tokens": 40, "total_tokens": 128,
	})
}

func assertLongStreamClient(t *testing.T, evs []replayEvent, fx *fixtures.Fixture) {
	t.Helper()
	// 全程不截断：增量逐片到位、全文一致、终态 completed 收尾。
	deltas := 0
	var sb strings.Builder
	for _, ev := range evs {
		if ev.name != "response.output_text.delta" {
			continue
		}
		deltas++
		sb.WriteString(mapStr(ev.data, "delta"))
	}
	wantChunks := 0
	var wantFull strings.Builder
	for _, ev := range fx.AnthropicUpstream.SSE {
		data := replayEventData(t, ev)
		if mapStr(data, "type") == "content_block_delta" {
			wantChunks++
			wantFull.WriteString(mapStr(asMapT(t, data["delta"]), "text"))
		}
	}
	if deltas != wantChunks {
		t.Fatalf("增量数 = %d, want %d（长流截断红线）", deltas, wantChunks)
	}
	if sb.String() != wantFull.String() {
		t.Fatalf("全文拼接不一致：got %d 字符, want %d 字符", sb.Len(), wantFull.Len())
	}
	if replayByName(evs, "response.failed") != nil {
		t.Fatalf("长流出现 response.failed（误报截断）")
	}
	comp := replayByName(evs, "response.completed")
	if comp == nil {
		t.Fatalf("长流缺 response.completed 终态: %v", replayNames(evs))
	}
	r := asMapT(t, comp.data["response"])
	if mapStr(r, "status") != "completed" {
		t.Fatalf("长流终态 status = %q, want completed", mapStr(r, "status"))
	}
	// 尾事件必须是 completed（终态兜底不抢跑）。
	if names := replayNames(evs); names[len(names)-1] != "response.completed" {
		t.Fatalf("末事件 = %q, want response.completed", names[len(names)-1])
	}
	assertResponsesUsage(t, asMapT(t, r["usage"]), map[string]uint64{
		"input_tokens": 600, "output_tokens": 300, "total_tokens": 900, "cached_tokens": 100,
	})
}

func assertErrorInjectionClient(t *testing.T, evs []replayEvent) {
	t.Helper()
	replayExpectSeq(t, evs, []string{
		"response.created", "response.in_progress",
		"response.output_item.added", "response.content_part.added",
		"response.output_text.delta",
		"response.output_text.done", "response.content_part.done",
		"response.output_item.done",
		"response.failed",
	})
	failed := evs[len(evs)-1]
	r := asMapT(t, failed.data["response"])
	if mapStr(r, "id") != "resp_msg_fx_err" || mapStr(r, "status") != "failed" {
		t.Fatalf("failed id/status = %q/%q", mapStr(r, "id"), mapStr(r, "status"))
	}
	errObj := asMapT(t, r["error"])
	if mapStr(errObj, "message") != "上游过载（夹具注入）" || mapStr(errObj, "type") != "overloaded_error" {
		t.Fatalf("error message/type 应逐字保留: %v", errObj)
	}
	// 已完成的部分 output 随 failed 带回。
	output := asSliceT(t, r["output"])
	if len(output) != 1 || mapStr(asMapT(t, output[0]), "type") != "message" {
		t.Fatalf("部分 output = %v", replayOutputTypes(output))
	}
	if replayByName(evs, "response.completed") != nil {
		t.Fatalf("failed 终态之后不得再发 completed")
	}
}

// assertResponsesUsage responses usage 形断言（§2.3）。
func assertResponsesUsage(t *testing.T, usage map[string]any, want map[string]uint64) {
	t.Helper()
	if usage == nil {
		t.Fatal("缺 usage")
	}
	for key, w := range want {
		var got uint64
		switch key {
		case "cached_tokens":
			got = uint64AtPath(usage, "input_tokens_details", "cached_tokens")
		case "cache_write_tokens":
			got = uint64AtPath(usage, "input_tokens_details", "cache_write_tokens")
		default:
			got = uint64From(usage[key])
		}
		if got != w {
			t.Fatalf("usage.%s = %d, want %d（usage=%v）", key, got, w, usage)
		}
	}
	if w := want["cached_tokens"] + want["cache_write_tokens"]; w > 0 {
		if _, has := asMapT(t, usage["input_tokens_details"])["cached_tokens"]; !has {
			t.Fatalf("缓存子集 >0 时缺 input_tokens_details: %v", usage)
		}
	}
}

func replayOutputTypes(output []any) []string {
	types := make([]string, 0, len(output))
	for _, it := range output {
		m, _ := it.(map[string]any)
		types = append(types, mapStr(m, "type"))
	}
	return types
}

func asMapT(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := asMap(v)
	if !ok {
		t.Fatalf("期望 JSON object, got %T (%v)", v, v)
	}
	return m
}

func asSliceT(t *testing.T, v any) []any {
	t.Helper()
	s, ok := asSlice(v)
	if !ok {
		t.Fatalf("期望 JSON array, got %T (%v)", v, v)
	}
	return s
}

// ---- 翻译分支：四形全链路回放 ----

func TestReplayTranslateLaneFourShapes(t *testing.T) {
	cases := []struct {
		fixture   string
		modelMap  map[string]string
		wantModel string // 出站 model（改写后）
		inPath    string
	}{
		{
			fixture:   "normal_reply",
			modelMap:  map[string]string{"default": "deepseek-v4", "codex": "deepseek-v4-chat"},
			wantModel: "deepseek-v4", // 已知真名透传（目录保真 §5.3）
			inPath:    "/v1/responses",
		},
		{
			fixture:   "tool_call",
			modelMap:  map[string]string{"default": "kimi-k3", "codex": "kimi-k3-coder"},
			wantModel: "kimi-k3-coder", // 未知名 → codex 主模型键
			inPath:    "/responses",
		},
		{
			fixture:   "long_stream",
			modelMap:  map[string]string{"default": "deepseek-v4", "codex": "deepseek-v4-chat"},
			wantModel: "deepseek-v4",
			inPath:    "/responses",
		},
		{
			fixture:   "error_injection",
			modelMap:  map[string]string{"default": "deepseek-v4", "codex": "deepseek-v4-chat"},
			wantModel: "deepseek-v4",
			inPath:    "/responses",
		},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			fx, err := fixtures.Load(tc.fixture)
			if err != nil {
				t.Fatal(err)
			}
			up := newReplayUpstream(t, fx.AnthropicUpstream)
			front := newCodexLaneDock(t, codexLaneEntry(up.srv.URL, replayTranslateKey, tc.modelMap), nil)

			resp, raw := replayPost(t, front+tc.inPath, fixtureRequestBytes(t, fx), false)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("状态 = %d, body=%s", resp.StatusCode, raw)
			}

			assertTranslateUpstreamWire(t, up.capture(t), fx, tc.wantModel)
			assertTranslateClientWire(t, resp, raw, fx)
		})
	}
}

// TestReplayTranslateLaneUpstreamBodyShapes 出站体逐形深检（§1 映射的夹具级
// 钉子：system 上提/工具史重嵌套/tool_choice 联动/cache 断点落位）。
func TestReplayTranslateLaneUpstreamBodyShapes(t *testing.T) {
	t.Run("normal_reply：instructions 上提 system + cache 断点", func(t *testing.T) {
		fx, err := fixtures.Load("normal_reply")
		if err != nil {
			t.Fatal(err)
		}
		up := newReplayUpstream(t, fx.AnthropicUpstream)
		front := newCodexLaneDock(t, codexLaneEntry(up.srv.URL, replayTranslateKey,
			map[string]string{"default": "deepseek-v4", "codex": "deepseek-v4-chat"}), nil)
		replayPost(t, front+"/responses", fixtureRequestBytes(t, fx), false)
		body := decodeJSONObject(up.capture(t).body)

		system := asSliceT(t, body["system"])
		sysLast := asMapT(t, system[len(system)-1])
		if mapStr(sysLast, "text") != "你是代码助手，回答保持一句话。" {
			t.Fatalf("instructions 应上提为 system: %v", sysLast)
		}
		if _, has := sysLast["cache_control"]; !has {
			t.Fatalf("system 末块缺 cache_control 断点（§1.6）")
		}
		msgs := asSliceT(t, body["messages"])
		if len(msgs) != 1 {
			t.Fatalf("messages 数 = %d, want 1", len(msgs))
		}
		m0 := asMapT(t, msgs[0])
		if mapStr(m0, "role") != "user" {
			t.Fatalf("首消息 role = %q（§1.5 首条必须 user）", mapStr(m0, "role"))
		}
		blk := asMapT(t, asSliceT(t, m0["content"])[0])
		if mapStr(blk, "type") != "text" || mapStr(blk, "text") != "用一句话解释什么是 SSE 流。" {
			t.Fatalf("input_text 重嵌套失败: %v", blk)
		}
		if got := strings.Count(string(encodeCompact(body)), `"cache_control"`); got != 2 {
			t.Fatalf("cache 断点数 = %d, want 2（system+最新消息；无 tools）", got)
		}
	})

	t.Run("tool_call：工具史重嵌套 + tool_choice 联动 + 断点落位", func(t *testing.T) {
		fx, err := fixtures.Load("tool_call")
		if err != nil {
			t.Fatal(err)
		}
		up := newReplayUpstream(t, fx.AnthropicUpstream)
		front := newCodexLaneDock(t, codexLaneEntry(up.srv.URL, replayTranslateKey,
			map[string]string{"default": "kimi-k3", "codex": "kimi-k3-coder"}), nil)
		replayPost(t, front+"/responses", fixtureRequestBytes(t, fx), false)
		body := decodeJSONObject(up.capture(t).body)

		tools := asSliceT(t, body["tools"])
		if len(tools) != 2 {
			t.Fatalf("tools 数 = %d, want 2（§1.3 转上游形）", len(tools))
		}
		tool0 := asMapT(t, tools[0])
		if mapStr(tool0, "name") != "list_files" || mapStr(tool0, "description") != "列出目录内容" {
			t.Fatalf("tools[0] = %v", tool0)
		}
		schema := asMapT(t, tool0["input_schema"])
		if _, has := asMapT(t, schema["properties"])["path"]; !has {
			t.Fatalf("input_schema.properties 应承自 parameters: %v", schema)
		}
		choice := asMapT(t, body["tool_choice"])
		if mapStr(choice, "type") != "auto" {
			t.Fatalf("tool_choice.type = %v（\"auto\" → {type:auto}，§1.3）", choice)
		}
		if v, _ := asBool(choice["disable_parallel_tool_use"]); !v {
			t.Fatalf("parallel_tool_calls:false 应落 tool_choice.disable_parallel_tool_use（§1.1）")
		}

		msgs := asSliceT(t, body["messages"])
		if len(msgs) != 3 {
			t.Fatalf("messages 数 = %d, want 3（相邻 user 合并）", len(msgs))
		}
		m1 := asMapT(t, msgs[1])
		if mapStr(m1, "role") != "assistant" {
			t.Fatalf("第 2 消息 role = %q", mapStr(m1, "role"))
		}
		tu := asMapT(t, asSliceT(t, m1["content"])[0])
		if mapStr(tu, "type") != "tool_use" || mapStr(tu, "id") != "call_fx_ls_1" ||
			mapStr(tu, "name") != "list_files" || mapStr(asMapT(t, tu["input"]), "path") != "." {
			t.Fatalf("function_call → tool_use 重嵌套失败: %v", tu)
		}
		m2 := asMapT(t, msgs[2])
		blocks := asSliceT(t, m2["content"])
		tr := asMapT(t, blocks[0])
		if mapStr(tr, "type") != "tool_result" || mapStr(tr, "tool_use_id") != "call_fx_ls_1" ||
			mapStr(tr, "content") != "main.go\ngo.mod" {
			t.Fatalf("function_call_output → tool_result 失败: %v", tr)
		}
		if mapStr(asMapT(t, blocks[1]), "type") != "tool_result" &&
			mapStr(asMapT(t, blocks[1]), "text") != "再读一下 main.go。" {
			t.Fatalf("合并 user 消息块序异常: %v", blocks)
		}
		// 断点：tools 末 + system 末 + 最新消息末块（messages=3 < 4 无第 4 锚）。
		if got := strings.Count(string(encodeCompact(body)), `"cache_control"`); got != 3 {
			t.Fatalf("cache 断点数 = %d, want 3", got)
		}
	})
}

// ---- 原生透传分支：四形全链路回放 ----

func TestReplayNativeLaneFourShapes(t *testing.T) {
	cases := []struct {
		fixture    string
		modelMap   map[string]string
		inPath     string
		wantModel  string // 非空＝model 改写（体重编码）；空＝已知名逐字节透传
		wantStatus int
	}{
		{
			fixture:    "normal_reply",
			modelMap:   map[string]string{"default": "deepseek-v4", "codex": "deepseek-v4-x"},
			inPath:     "/v1/responses",
			wantStatus: http.StatusOK,
		},
		{
			fixture:    "tool_call",
			modelMap:   map[string]string{"default": "kimi-k3", "codex": "kimi-k3-turbo"},
			inPath:     "/codex/v1/responses", // 变体归一（§4.1）
			wantModel:  "kimi-k3-turbo",
			wantStatus: http.StatusOK,
		},
		{
			fixture:    "long_stream",
			modelMap:   map[string]string{"default": "deepseek-v4", "codex": "deepseek-v4-x"},
			inPath:     "/responses",
			wantStatus: http.StatusOK,
		},
		{
			fixture:    "error_injection",
			modelMap:   map[string]string{"default": "deepseek-v4", "codex": "deepseek-v4-x"},
			inPath:     "/responses",
			wantStatus: http.StatusTooManyRequests,
		},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			fx, err := fixtures.Load(tc.fixture)
			if err != nil {
				t.Fatal(err)
			}
			up := newReplayUpstream(t, fx.ResponsesUpstream)
			entry := &config.DockUpstream{
				BaseURL:  up.srv.URL,
				APIKey:   replayNativeKey,
				ModelMap: tc.modelMap,
				Dialect:  config.DialectOpenAIResponses,
			}
			front := newCodexLaneDock(t, entry, nil)

			sent := fixtureRequestBytes(t, fx)
			resp, raw := replayPost(t, front+tc.inPath, sent, true)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("状态 = %d, want %d, body=%s", resp.StatusCode, tc.wantStatus, raw)
			}

			// 出站断言：路径归一 + 鉴权注入 + codex 指纹照抄（原生上游本客户端正常头）。
			c := up.capture(t)
			if c.path != "/v1/responses" {
				t.Fatalf("原生上游路径 = %q, want /v1/responses（变体归一+纯 origin 补 /v1）", c.path)
			}
			if c.auth != "Bearer "+replayNativeKey {
				t.Fatalf("原生鉴权 = %q, want 渡口真钥注入", c.auth)
			}
			if c.accept != "text/event-stream" {
				t.Fatalf("原生 Accept = %q, want 照抄（不强制 application/json）", c.accept)
			}
			if c.originator != "codex_cli_rs" {
				t.Fatalf("原生 Originator = %q, want 照抄", c.originator)
			}

			isSSE := len(fx.ResponsesUpstream.SSE) > 0
			if tc.wantModel == "" {
				// 已知名：请求体逐字节透传（D2「透传不丢弃」完整落地）。
				if !bytes.Equal(c.body, sent) {
					t.Fatalf("原生分支体被改动:\n got %s\nwant %s", c.body, sent)
				}
			} else {
				// 未知名：model 改写（体重编码），其余字段全达上游。
				body := decodeJSONObject(c.body)
				if body == nil {
					t.Fatalf("原生上游体非 JSON: %s", c.body)
				}
				if mapStr(body, "model") != tc.wantModel {
					t.Fatalf("原生改写 model = %q, want %q", mapStr(body, "model"), tc.wantModel)
				}
				for _, k := range []string{"tools", "input", "parallel_tool_calls",
					"tool_choice", "store", "prompt_cache_key", "instructions"} {
					if _, has := body[k]; !has {
						t.Fatalf("改写后字段 %s 丢失（透传不丢弃）: %s", k, c.body)
					}
				}
			}

			// 客户端断言：SSE 形逐字节中继；错误形原状态码 + 原体。
			if isSSE {
				var want bytes.Buffer
				for _, ev := range fx.ResponsesUpstream.SSE {
					want.Write(replaySSEFrame(ev))
				}
				if !bytes.Equal(raw, want.Bytes()) {
					t.Fatalf("原生中继字节漂移:\n got %d 字节\nwant %d 字节", len(raw), want.Len())
				}
				if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
					t.Fatalf("原生 CT = %q", ct)
				}
				if tc.fixture == "long_stream" {
					evs := parseReplaySSE(t, raw)
					n := 0
					for _, ev := range evs {
						if ev.name == "response.output_text.delta" {
							n++
						}
					}
					if n != len(fx.ResponsesUpstream.SSE)-4 { // created/added/done/completed 之外全是 delta
						t.Fatalf("原生长流增量数 = %d（截断红线）", n)
					}
					if replayByName(evs, "response.completed") == nil {
						t.Fatalf("原生长流缺 completed 终态")
					}
				}
			} else {
				if string(raw) != fx.ResponsesUpstream.Body {
					t.Fatalf("原生错误体被改动:\n got %s\nwant %s", raw, fx.ResponsesUpstream.Body)
				}
				if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
					t.Fatalf("原生错误 CT = %q", ct)
				}
			}
		})
	}
}
