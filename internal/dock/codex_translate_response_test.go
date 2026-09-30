// codex_translate_response_test.go — 票03 表驱动测试：响应向翻译
// （Anthropic → responses 非流式 + usage/stop_reason/错误形态），用例从对照
// 表 §2.1/§2.3/§2.4 逐行生成。
package dock

import (
	"strings"
	"testing"
)

// runResponseTranslate 非流式响应翻译。
func runResponseTranslate(t *testing.T, body string) (map[string]any, error) {
	t.Helper()
	obj := decodeJSONObject([]byte(body))
	if obj == nil {
		t.Fatalf("夹具非法 JSON")
	}
	return anthropicToResponses(obj, newToolContext())
}

// ---- §2.1 非流式响应体 ----

func TestResponseNonStream(t *testing.T) {
	t.Run("文本+工具+thinking 混合内容", func(t *testing.T) {
		out, err := runResponseTranslate(t, `{
			"id":"msg_1","type":"message","role":"assistant","model":"glm-5.3",
			"content":[
				{"type":"text","text":"Hello"},
				{"type":"text","text":"World"},
				{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"Tokyo"}},
				{"type":"thinking","thinking":"hmm","signature":"sig1"}
			],
			"stop_reason":"end_turn",
			"usage":{"input_tokens":4,"output_tokens":2}
		}`)
		if err != nil {
			t.Fatalf("翻译失败: %v", err)
		}
		if out["id"] != "resp_msg_1" {
			t.Fatalf("id = %v（resp_ 前缀补加）", out["id"])
		}
		if out["object"] != "response" || out["created_at"] != 0 {
			t.Fatalf("object/created_at = %v/%v", out["object"], out["created_at"])
		}
		if out["model"] != "glm-5.3" {
			t.Fatalf("model = %v（原样回显）", out["model"])
		}
		output, _ := asSlice(out["output"])
		// 连续 text 块合成一个 message 项（§2.1）→ message + function_call + reasoning = 3。
		if len(output) != 3 {
			t.Fatalf("output 条数 = %d, want 3: %s", len(output), canonicalJSONString(output))
		}
		// 连续 text 块合成一个 message 项，两项文本都在 content 里。
		m0, _ := asMap(output[0])
		if mapStr(m0, "type") != "message" {
			t.Fatalf("output[0] = %v", m0)
		}
		content, _ := asSlice(m0["content"])
		if len(content) != 2 {
			t.Fatalf("message content = %v, want 两个 output_text", content)
		}
		c0, _ := asMap(content[0])
		if mapStr(c0, "type") != "output_text" || mapStr(c0, "text") != "Hello" {
			t.Fatalf("content[0] = %v", c0)
		}
		if list, _ := asSlice(c0["annotations"]); len(list) != 0 {
			t.Fatalf("annotations = %v", c0["annotations"])
		}
		if mapStr(m0, "id") != "resp_msg_1_msg_0" {
			t.Fatalf("message id = %v", m0["id"])
		}
		// tool_use → function_call（arguments 规范化 JSON 字符串）。
		fc, _ := asMap(output[1])
		if mapStr(fc, "type") != "function_call" || mapStr(fc, "call_id") != "toolu_1" ||
			mapStr(fc, "name") != "get_weather" {
			t.Fatalf("function_call = %v", fc)
		}
		if mapStr(fc, "arguments") != `{"city":"Tokyo"}` {
			t.Fatalf("arguments = %v", fc["arguments"])
		}
		if mapStr(fc, "id") != "fc_toolu_1" {
			t.Fatalf("id = %v（fc_ 前缀）", fc["id"])
		}
		// thinking → reasoning 项（summary 可见文本 + 信封）。
		rs, _ := asMap(output[2])
		if mapStr(rs, "type") != "reasoning" {
			t.Fatalf("reasoning = %v", rs)
		}
		summary, _ := asSlice(rs["summary"])
		if len(summary) != 1 {
			t.Fatalf("summary = %v", rs["summary"])
		}
		enc := mapStr(rs, "encrypted_content")
		if !strings.HasPrefix(enc, anthropicThinkingEnvelopePrefix) {
			t.Fatalf("encrypted_content = %q", enc)
		}
		if block, ok := decodeAnthropicThinkingBlock(enc); !ok || mapStr(block, "signature") != "sig1" {
			t.Fatalf("信封回放失败: %v", block)
		}
	})

	t.Run("id 空 → resp_ccswitch；已带 resp_ 前缀不重复加", func(t *testing.T) {
		out, _ := runResponseTranslate(t, `{"content":[{"type":"text","text":"x"}],"stop_reason":"end_turn"}`)
		if out["id"] != "resp_ccswitch" {
			t.Fatalf("id = %v", out["id"])
		}
		out2, _ := runResponseTranslate(t, `{"id":"resp_xyz","content":[{"type":"text","text":"x"}],"stop_reason":"end_turn"}`)
		if out2["id"] != "resp_xyz" {
			t.Fatalf("id = %v", out2["id"])
		}
	})

	t.Run("redacted_thinking → 空 summary 数组", func(t *testing.T) {
		out, _ := runResponseTranslate(t, `{
			"id":"m","content":[{"type":"redacted_thinking","data":"opaque"}],
			"stop_reason":"end_turn"}`)
		output, _ := asSlice(out["output"])
		if len(output) != 1 {
			t.Fatalf("output = %s", canonicalJSONString(output))
		}
		rs, _ := asMap(output[0])
		summary, _ := asSlice(rs["summary"])
		if len(summary) != 0 {
			t.Fatalf("redacted 无可见 summary: %v", rs["summary"])
		}
	})

	t.Run("无签名 thinking 块整块丢弃不回放", func(t *testing.T) {
		out, _ := runResponseTranslate(t, `{
			"id":"m","content":[{"type":"thinking","thinking":"no sig"}],
			"stop_reason":"end_turn"}`)
		output, _ := asSlice(out["output"])
		if len(output) != 0 {
			t.Fatalf("无签名 thinking 应丢弃: %s", canonicalJSONString(output))
		}
	})

	t.Run("错误信封 → 显式错误", func(t *testing.T) {
		_, err := runResponseTranslate(t, `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`)
		if err == nil || !strings.Contains(err.Error(), "overloaded_error") || !strings.Contains(err.Error(), "busy") {
			t.Fatalf("err = %v（上游 type+message 保留）", err)
		}
	})
}

// ---- §2.3 usage 映射 ----

func TestResponsesUsageMapping(t *testing.T) {
	t.Run("缺失/非对象 → 全 0 形状", func(t *testing.T) {
		u := buildResponsesUsageFromAnthropic(nil)
		if canonicalJSONString(u) != `{"input_tokens":0,"output_tokens":0,`+
			`"output_tokens_details":{"reasoning_tokens":0},"total_tokens":0}` {
			t.Fatalf("zero usage = %s", canonicalJSONString(u))
		}
	})

	t.Run("input_tokens = fresh + cache_read + cache_creation；子集入 details", func(t *testing.T) {
		u := buildResponsesUsageFromAnthropic(map[string]any{
			"input_tokens":                100,
			"cache_read_input_tokens":     30,
			"cache_creation_input_tokens": 20,
			"output_tokens":               50,
			"output_tokens_details":       map[string]any{"thinking_tokens": 7},
		})
		if v, _ := asU64(u["input_tokens"]); v != 150 {
			t.Fatalf("input_tokens = %v, want 150", u["input_tokens"])
		}
		if v, _ := asU64(u["total_tokens"]); v != 200 {
			t.Fatalf("total_tokens = %v, want 200（只计一次）", u["total_tokens"])
		}
		details, ok := asMap(u["input_tokens_details"])
		if !ok {
			t.Fatalf("input_tokens_details 缺失（缓存子集 >0 时须出现）")
		}
		if v, _ := asU64(details["cached_tokens"]); v != 30 {
			t.Fatalf("cached_tokens = %v", details["cached_tokens"])
		}
		if v, _ := asU64(details["cache_write_tokens"]); v != 20 {
			t.Fatalf("cache_write_tokens = %v", details["cache_write_tokens"])
		}
		if v, _ := asU64(u["cache_creation_input_tokens"]); v != 20 {
			t.Fatalf("顶层兼容别名 = %v", u["cache_creation_input_tokens"])
		}
		od, _ := asMap(u["output_tokens_details"])
		if v, _ := asU64(od["reasoning_tokens"]); v != 7 {
			t.Fatalf("reasoning_tokens = %v", od["reasoning_tokens"])
		}
	})

	t.Run("无缓存子集 → 不出现 input_tokens_details", func(t *testing.T) {
		u := buildResponsesUsageFromAnthropic(map[string]any{"input_tokens": 5, "output_tokens": 1})
		if _, has := u["input_tokens_details"]; has {
			t.Fatalf("input_tokens_details 不应出现: %v", u)
		}
	})
}

// ---- §2.4 stop_reason → status 映射 ----

func TestStopReasonMapping(t *testing.T) {
	cases := []struct {
		stop   string
		status string
		reason string
	}{
		{"max_tokens", "incomplete", "max_output_tokens"},
		{"refusal", "incomplete", "content_filter"},
		{"model_context_window_exceeded", "incomplete", "max_output_tokens"},
		{"end_turn", "completed", ""},
		{"tool_use", "completed", ""},
		{"pause_turn", "completed", ""},
		{"", "completed", ""},
	}
	for _, c := range cases {
		status, reason := mapStopReason(c.stop)
		if status != c.status || reason != c.reason {
			t.Fatalf("mapStopReason(%q) = (%q,%q), want (%q,%q)",
				c.stop, status, reason, c.status, c.reason)
		}
	}
	// incomplete_details 只在 incomplete 时出现。
	out, _ := runResponseTranslate(t, `{"id":"m","content":[],"stop_reason":"max_tokens"}`)
	if _, has := out["incomplete_details"]; !has {
		t.Fatalf("incomplete_details 缺失: %v", out)
	}
	id, _ := asMap(out["incomplete_details"])
	if mapStr(id, "reason") != "max_output_tokens" {
		t.Fatalf("reason = %v", id["reason"])
	}
}

// ---- §2.4 错误体规整 ----

func TestNormalizeUpstreamErrorBody(t *testing.T) {
	t.Run("Anthropic 错误信封 → responses 错误形状", func(t *testing.T) {
		e := normalizeUpstreamErrorBody([]byte(`{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`))
		err, _ := asMap(e["error"])
		if mapStr(err, "message") != "bad key" || mapStr(err, "type") != "authentication_error" {
			t.Fatalf("error = %v", err)
		}
		if _, has := err["code"]; !has || err["code"] != nil {
			t.Fatalf("code 应为 null: %v", err)
		}
	})

	t.Run("非 JSON 错误体按文本包进 message 并截 1024", func(t *testing.T) {
		long := strings.Repeat("x", 3000)
		e := normalizeUpstreamErrorBody([]byte("Unauthorized: " + long))
		err, _ := asMap(e["error"])
		msg := mapStr(err, "message")
		if len(msg) > 1100 {
			t.Fatalf("message 长度 = %d（应截断至 ~1024）", len(msg))
		}
		if !strings.Contains(msg, "(truncated)") {
			t.Fatalf("截断标记缺失: %q…", msg[:50])
		}
	})

	t.Run("裸字符串错误体", func(t *testing.T) {
		e := normalizeUpstreamErrorBody([]byte(`"plain error"`))
		err, _ := asMap(e["error"])
		if mapStr(err, "message") != "plain error" {
			t.Fatalf("error = %v", err)
		}
	})
}

// ---- §2.4 表行 6：非流式请求但上游回 SSE 体 → 聚合 ----

func TestAggregateAnthropicSSE(t *testing.T) {
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_a","model":"glm","usage":{"input_tokens":10}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n"

	msg, err := aggregateAnthropicSSE([]byte(sse))
	if err != nil {
		t.Fatalf("聚合失败: %v", err)
	}
	if mapStr(msg, "id") != "msg_a" || mapStr(msg, "stop_reason") != "end_turn" {
		t.Fatalf("msg = %s", canonicalJSONString(msg))
	}
	content, _ := asSlice(msg["content"])
	if len(content) != 1 {
		t.Fatalf("content = %v", content)
	}
	b0, _ := asMap(content[0])
	if mapStr(b0, "text") != "Hi" {
		t.Fatalf("text = %v", b0["text"])
	}
	usage, _ := asMap(msg["usage"])
	if v, _ := asU64(usage["output_tokens"]); v != 3 {
		t.Fatalf("delta usage 合并失败: %v", usage)
	}
	// 聚合产物可直接进非流式翻译。
	resp, err := anthropicToResponses(msg, newToolContext())
	if err != nil || mapStr(resp, "id") != "resp_msg_a" {
		t.Fatalf("翻译聚合体失败: %v %v", resp, err)
	}

	// 截断流（有产出无 stop）→ stop_reason=max_tokens 强制（聚合器语义）。
	truncated := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_t"}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}` + "\n\n"
	msg2, err := aggregateAnthropicSSE([]byte(truncated))
	if err != nil {
		t.Fatalf("截断聚合失败: %v", err)
	}
	if mapStr(msg2, "stop_reason") != "max_tokens" {
		t.Fatalf("截断聚合 stop_reason = %v, want max_tokens", msg2["stop_reason"])
	}

	// 流内 error 事件 → 聚合报错（调用方按错误体处置）。
	errSSE := "event: error\n" +
		`data: {"type":"error","error":{"message":"boom"}}` + "\n\n"
	if _, err := aggregateAnthropicSSE([]byte(errSSE)); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}
