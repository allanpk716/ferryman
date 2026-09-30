// codex_translate_request_test.go — 票03 表驱动测试：请求向翻译
// （responses → Anthropic），用例从对照表 §1 逐行生成。
package dock

import (
	"strings"
	"testing"
)

// runTranslate 翻译一个 responses JSON 文本，返回译文 map（失败即 Fatal）。
func runTranslate(t *testing.T, body string) (map[string]any, []string) {
	t.Helper()
	obj := decodeJSONObject([]byte(body))
	if obj == nil {
		t.Fatalf("测试夹具非法 JSON: %s", body)
	}
	out, dropped, err := responsesToAnthropic(obj, buildToolContext(obj))
	if err != nil {
		t.Fatalf("翻译失败: %v", err)
	}
	return out, dropped
}

// laneMessages 译文的 messages 提取（[]any → []map）。
func laneMessages(t *testing.T, out map[string]any) []map[string]any {
	t.Helper()
	arr, ok := asSlice(out["messages"])
	if !ok {
		t.Fatalf("messages 缺失: %v", out["messages"])
	}
	msgs := make([]map[string]any, 0, len(arr))
	for _, m := range arr {
		mm, ok := asMap(m)
		if !ok {
			t.Fatalf("message 非 object: %v", m)
		}
		msgs = append(msgs, mm)
	}
	return msgs
}

// blocksOf 消息 content 块提取。
func blocksOf(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	arr, ok := asSlice(m["content"])
	if !ok {
		t.Fatalf("content 缺失: %v", m["content"])
	}
	blocks := make([]map[string]any, 0, len(arr))
	for _, b := range arr {
		bm, ok := asMap(b)
		if !ok {
			t.Fatalf("块非 object: %v", b)
		}
		blocks = append(blocks, bm)
	}
	return blocks
}

// ---- §1.1 顶层字段总表 ----

func TestRequestTopLevelFields(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		check func(t *testing.T, out map[string]any)
	}{
		{
			name: "model 原样透传",
			in:   `{"model":"glm-5.3","max_output_tokens":100,"input":[{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				if out["model"] != "glm-5.3" {
					t.Fatalf("model = %v", out["model"])
				}
			},
		},
		{
			name: "instructions → system（trim 后非空才收）",
			in:   `{"model":"m","instructions":"You are helpful.","max_output_tokens":100,"input":[{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				if out["system"] != "You are helpful." {
					t.Fatalf("system = %v", out["system"])
				}
			},
		},
		{
			name: "instructions 纯空白不产生 system",
			in:   `{"model":"m","instructions":"   ","max_output_tokens":100,"input":[{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				if _, has := out["system"]; has {
					t.Fatalf("system 不应出现: %v", out["system"])
				}
			},
		},
		{
			name: "system/developer 历史上提并与 instructions 以 \\n\\n 连接",
			in: `{"model":"m","instructions":"base","max_output_tokens":100,"input":[` +
				`{"role":"system","content":"system history"},` +
				`{"role":"developer","content":[{"type":"input_text","text":"developer history"}]},` +
				`{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				want := "base\n\nsystem history\n\ndeveloper history"
				if out["system"] != want {
					t.Fatalf("system = %q, want %q", out["system"], want)
				}
				msgs, _ := asSlice(out["messages"])
				if len(msgs) != 1 {
					t.Fatalf("messages 条数 = %d, want 1（system/developer 不降级为 user）", len(msgs))
				}
			},
		},
		{
			name: "input 纯字符串 → 单条 user text",
			in:   `{"model":"m","max_output_tokens":100,"input":"hello"}`,
			check: func(t *testing.T, out map[string]any) {
				msgs, _ := asSlice(out["messages"])
				if len(msgs) != 1 {
					t.Fatalf("messages 条数 = %d", len(msgs))
				}
				m, _ := asMap(msgs[0])
				if mapStr(m, "role") != "user" {
					t.Fatalf("role = %v", m["role"])
				}
				content, _ := asSlice(m["content"])
				b0, _ := asMap(content[0])
				if mapStr(b0, "type") != "text" || mapStr(b0, "text") != "hello" {
					t.Fatalf("content[0] = %v", content[0])
				}
			},
		},
		{
			name: "max_output_tokens 存在且 >0 才用",
			in:   `{"model":"m","max_output_tokens":512,"input":[{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				if v, _ := asU64(out["max_tokens"]); v != 512 {
					t.Fatalf("max_tokens = %v", out["max_tokens"])
				}
			},
		},
		{
			name: "max_output_tokens 缺失注入默认 8192",
			in:   `{"model":"m","input":[{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				if v, _ := asU64(out["max_tokens"]); v != 8192 {
					t.Fatalf("max_tokens = %v, want 8192", out["max_tokens"])
				}
			},
		},
		{
			name: "temperature/top_p 在 thinking 关时透传",
			in:   `{"model":"m","max_output_tokens":100,"temperature":0.7,"top_p":0.9,"input":[{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				if _, has := out["temperature"]; !has {
					t.Fatalf("temperature 应透传: %v", out)
				}
				if _, has := out["top_p"]; !has {
					t.Fatalf("top_p 应透传: %v", out)
				}
			},
		},
		{
			name: "stream 原样透传",
			in:   `{"model":"m","max_output_tokens":100,"stream":true,"input":[{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				if b, _ := asBool(out["stream"]); !b {
					t.Fatalf("stream = %v", out["stream"])
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, _ := runTranslate(t, tc.in)
			tc.check(t, out)
		})
	}
}

// §1.1 白名单钉死：已知 responses 专有字段不进上游体且不算未知（不记事件）；
// 未知字段也不进上游体但记入 dropped 列表（§1.7/D2）。
func TestRequestWhitelistDrops(t *testing.T) {
	body := `{"model":"m","max_output_tokens":100,` +
		`"store":false,"include":["x"],"service_tier":"auto","metadata":{"a":1},` +
		`"prompt_cache_key":"k","text":{"format":{"type":"text"}},` +
		`"future_codex_field":123,` +
		`"input":[{"role":"user","content":"hi"}]}`
	out, dropped := runTranslate(t, body)
	for _, k := range []string{"store", "include", "service_tier", "metadata", "prompt_cache_key", "text"} {
		if _, has := out[k]; has {
			t.Fatalf("已知专有字段 %q 不应进上游体", k)
		}
	}
	if _, has := out["future_codex_field"]; has {
		t.Fatalf("未知字段不应进上游体")
	}
	if len(dropped) != 1 || dropped[0] != "future_codex_field" {
		t.Fatalf("dropped = %v, want [future_codex_field]（已知专有字段不算未知）", dropped)
	}
}

// 无未知字段时 dropped 为空。
func TestRequestNoUnknownNoDrop(t *testing.T) {
	_, dropped := runTranslate(t, `{"model":"m","max_output_tokens":100,"store":true,"input":[{"role":"user","content":"hi"}]}`)
	if len(dropped) != 0 {
		t.Fatalf("dropped = %v, want 空", dropped)
	}
}

// 翻译后为空 → 显式 InvalidRequest（§1.5 规则 4）。
func TestRequestEmptyMessagesError(t *testing.T) {
	obj := decodeJSONObject([]byte(`{"model":"m","max_output_tokens":100,"input":[]}`))
	_, _, err := responsesToAnthropic(obj, buildToolContext(obj))
	if err == nil || !strings.Contains(err.Error(), "messages 翻译后为空") {
		t.Fatalf("err = %v, want 空消息错误", err)
	}
}

// ---- §1.2 input 项重嵌套 ----

// leadingUser 工具项夹具的统一前导（避免合成 user 消息移动索引）。
const leadingUser = `{"role":"user","content":"prompt"},`

func TestRequestInputItems(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		check func(t *testing.T, out map[string]any)
	}{
		{
			name: "function_call → assistant tool_use（空 arguments → {}）",
			in: `{"model":"m","max_output_tokens":100,"input":[` + leadingUser +
				`{"type":"function_call","call_id":"c1","name":"get_weather","arguments":""},` +
				`{"type":"function_call_output","call_id":"c1","output":"sunny"},` +
				`{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				msgs := laneMessages(t, out)
				if len(msgs) != 3 {
					t.Fatalf("messages 条数 = %d, want 3: %s", len(msgs), canonicalJSONString(out))
				}
				m1 := msgs[1]
				if mapStr(m1, "role") != "assistant" {
					t.Fatalf("第二条 role = %v", m1["role"])
				}
				b0 := blocksOf(t, m1)[0]
				if mapStr(b0, "type") != "tool_use" || mapStr(b0, "id") != "c1" ||
					mapStr(b0, "name") != "get_weather" {
					t.Fatalf("tool_use 块 = %v", b0)
				}
				if in, _ := asMap(b0["input"]); len(in) != 0 {
					t.Fatalf("input = %v, want {}", b0["input"])
				}
			},
		},
		{
			name: "function_call call_id 缺省回退 id",
			in: `{"model":"m","max_output_tokens":100,"input":[` + leadingUser +
				`{"type":"function_call","id":"fb1","name":"t","arguments":"{}"},` +
				`{"type":"function_call_output","call_id":"fb1","output":"r"},` +
				`{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				msgs := laneMessages(t, out)
				b0 := blocksOf(t, msgs[1])[0]
				if mapStr(b0, "id") != "fb1" {
					t.Fatalf("id = %v, want fb1（id 回退）", b0["id"])
				}
			},
		},
		{
			name: "function_call arguments 非 JSON 对象 → 整请求报错",
			in: `{"model":"m","max_output_tokens":100,"input":[` + leadingUser +
				`{"type":"function_call","call_id":"c1","name":"t","arguments":"[1,2]"},` +
				`{"type":"function_call_output","call_id":"c1","output":"r"},` +
				`{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				t.Fatal("不应到达")
			},
		},
		{
			name: "status incomplete 的历史工具调用整项丢弃",
			in: `{"model":"m","max_output_tokens":100,"input":[` + leadingUser +
				`{"type":"function_call","call_id":"c9","name":"t","arguments":"{}","status":"incomplete"},` +
				`{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				s := canonicalJSONString(out)
				if strings.Contains(s, "c9") || strings.Contains(s, "tool_use") {
					t.Fatalf("incomplete 调用应丢弃: %s", s)
				}
			},
		},
		{
			name: "Read 工具剥 pages 空串",
			in: `{"model":"m","max_output_tokens":100,"input":[` + leadingUser +
				`{"type":"function_call","call_id":"c1","name":"Read","arguments":"{\"file_path\":\"/tmp/x\",\"pages\":\"\"}"},` +
				`{"type":"function_call_output","call_id":"c1","output":"r"},` +
				`{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				s := canonicalJSONString(out)
				if strings.Contains(s, "pages") {
					t.Fatalf("pages 空串应剥: %s", s)
				}
				if !strings.Contains(s, "file_path") {
					t.Fatalf("file_path 应保留: %s", s)
				}
			},
		},
		{
			name: "custom_tool_call → input 原样包单键对象",
			in: `{"model":"m","max_output_tokens":100,"input":[` + leadingUser +
				`{"type":"custom_tool_call","call_id":"cc1","name":"apply_patch","input":"*** patch ***"},` +
				`{"type":"custom_tool_call_output","call_id":"cc1","output":"ok"},` +
				`{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				msgs := laneMessages(t, out)
				b0 := blocksOf(t, msgs[1])[0]
				in, _ := asMap(b0["input"])
				if in["input"] != "*** patch ***" {
					t.Fatalf("input = %v", b0["input"])
				}
			},
		},
		{
			name: "tool_search_call → 代理工具 tool_search",
			in: `{"model":"m","max_output_tokens":100,"input":[` + leadingUser +
				`{"type":"tool_search_call","call_id":"ts1","arguments":{"query":"files"}},` +
				`{"type":"tool_search_output","call_id":"ts1","output":"found"},` +
				`{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				msgs := laneMessages(t, out)
				b0 := blocksOf(t, msgs[1])[0]
				if mapStr(b0, "name") != "tool_search" {
					t.Fatalf("name = %v", b0["name"])
				}
			},
		},
		{
			name: "function_call_output 字符串 output → tool_result content 字符串形态",
			in: `{"model":"m","max_output_tokens":100,"input":[` + leadingUser +
				`{"type":"function_call","call_id":"c1","name":"t","arguments":"{}"},` +
				`{"type":"function_call_output","call_id":"c1","output":"plain text"},` +
				`{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				msgs := laneMessages(t, out)
				b0 := blocksOf(t, msgs[2])[0]
				if mapStr(b0, "type") != "tool_result" || mapStr(b0, "content") != "plain text" {
					t.Fatalf("tool_result = %v", b0)
				}
			},
		},
		{
			name: "tool_result 错误标记 → is_error:true 且不进 content",
			in: `{"model":"m","max_output_tokens":100,"input":[` + leadingUser +
				`{"type":"function_call","call_id":"c1","name":"t","arguments":"{}"},` +
				`{"type":"function_call_output","call_id":"c1","output":[{"type":"output_text","text":"` + toolResultErrorMarker + `"}]},` +
				`{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				msgs := laneMessages(t, out)
				b0 := blocksOf(t, msgs[2])[0]
				if b0["is_error"] != true {
					t.Fatalf("is_error = %v", b0["is_error"])
				}
				if c, _ := asSlice(b0["content"]); len(c) != 0 {
					t.Fatalf("错误标记不应进 content: %v", b0["content"])
				}
			},
		},
		{
			name: "output 缺失 → 整项规范化 JSON 兜底（不丢配对）",
			in: `{"model":"m","max_output_tokens":100,"input":[` + leadingUser +
				`{"type":"function_call","call_id":"c1","name":"t","arguments":"{}"},` +
				`{"type":"function_call_output","call_id":"c1"},` +
				`{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				msgs := laneMessages(t, out)
				b0 := blocksOf(t, msgs[2])[0]
				if mapStr(b0, "type") != "tool_result" || mapStr(b0, "tool_use_id") != "c1" {
					t.Fatalf("tool_result = %v", b0)
				}
				if s, _ := asStr(b0["content"]); s == "" {
					t.Fatalf("content 应为规范化 JSON 兜底: %v", b0["content"])
				}
			},
		},
		{
			name: "顶层裸 input_text 简化形态",
			in: `{"model":"m","max_output_tokens":100,"input":[` +
				`{"type":"input_text","text":"just asking"}]}`,
			check: func(t *testing.T, out map[string]any) {
				msgs := laneMessages(t, out)
				if mapStr(msgs[0], "role") != "user" {
					t.Fatalf("role = %v", msgs[0]["role"])
				}
			},
		},
		{
			name: "refusal part 取 refusal 文本为 text 块",
			in: `{"model":"m","max_output_tokens":100,"input":[` + leadingUser +
				`{"role":"assistant","content":[{"type":"refusal","refusal":"I cannot"}]},` +
				`{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				msgs := laneMessages(t, out)
				b0 := blocksOf(t, msgs[1])[0]
				if mapStr(b0, "text") != "I cannot" {
					t.Fatalf("text = %v", b0["text"])
				}
			},
		},
		{
			name: "未知 part 类型丢弃；空白文本丢弃",
			in: `{"model":"m","max_output_tokens":100,"input":[` +
				`{"role":"user","content":[{"type":"future_part","x":1},{"type":"input_text","text":"  "},{"type":"input_text","text":"real"}]}]}`,
			check: func(t *testing.T, out map[string]any) {
				msgs := laneMessages(t, out)
				blocks := blocksOf(t, msgs[0])
				if len(blocks) != 1 {
					t.Fatalf("content = %v, want 只剩 real", blocks)
				}
			},
		},
		{
			name: "input_image data: URL → base64 source；http URL → url source",
			in: `{"model":"m","max_output_tokens":100,"input":[` +
				`{"role":"user","content":[` +
				`{"type":"input_image","image_url":"data:image/png;base64,QUJD"},` +
				`{"type":"input_image","image_url":{"url":"https://x.test/i.png"}}]}]}`,
			check: func(t *testing.T, out map[string]any) {
				blocks := blocksOf(t, laneMessages(t, out)[0])
				src, _ := asMap(blocks[0]["source"])
				if mapStr(src, "type") != "base64" || mapStr(src, "media_type") != "image/png" || mapStr(src, "data") != "QUJD" {
					t.Fatalf("base64 source = %v", src)
				}
				src1, _ := asMap(blocks[1]["source"])
				if mapStr(src1, "type") != "url" || mapStr(src1, "url") != "https://x.test/i.png" {
					t.Fatalf("url source = %v", src1)
				}
			},
		},
		{
			name: "input_file → document 块（filename → title）",
			in: `{"model":"m","max_output_tokens":100,"input":[` +
				`{"role":"user","content":[{"type":"input_file","file_data":"data:application/pdf;base64,QUJD","filename":"doc.pdf"}]}]}`,
			check: func(t *testing.T, out map[string]any) {
				b0 := blocksOf(t, laneMessages(t, out)[0])[0]
				if mapStr(b0, "type") != "document" || mapStr(b0, "title") != "doc.pdf" {
					t.Fatalf("document = %v", b0)
				}
			},
		},
		{
			name: "同 role 相邻项合并进同一条 message",
			in: `{"model":"m","max_output_tokens":100,"input":[` +
				`{"role":"user","content":"a"},{"role":"user","content":"b"}]}`,
			check: func(t *testing.T, out map[string]any) {
				if msgs := laneMessages(t, out); len(msgs) != 1 {
					t.Fatalf("messages = %d 条, want 1（相邻合并）", len(msgs))
				}
			},
		},
		{
			name: "tool_result 块排在所在 user 消息内 text 块之前",
			in: `{"model":"m","max_output_tokens":100,"input":[` + leadingUser +
				`{"type":"function_call","call_id":"c1","name":"t","arguments":"{}"},` +
				`{"type":"function_call_output","call_id":"c1","output":"r"},` +
				`{"role":"user","content":"after"}]}`,
			check: func(t *testing.T, out map[string]any) {
				msgs := laneMessages(t, out)
				blocks := blocksOf(t, msgs[2])
				if mapStr(blocks[0], "type") != "tool_result" {
					t.Fatalf("首块 = %v, want tool_result 在 text 前", blocks[0])
				}
			},
		},
		{
			name: "未知 type 且无 role 的项静默丢弃",
			in: `{"model":"m","max_output_tokens":100,"input":[` +
				`{"type":"future_item","x":1},{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				s := canonicalJSONString(out)
				if strings.Contains(s, "future_item") {
					t.Fatalf("未知项应丢弃: %s", s)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "function_call arguments 非 JSON 对象 → 整请求报错" {
				obj := decodeJSONObject([]byte(tc.in))
				_, _, err := responsesToAnthropic(obj, buildToolContext(obj))
				if err == nil || !strings.Contains(err.Error(), "JSON 对象") {
					t.Fatalf("err = %v, want arguments 非对象错误", err)
				}
				return
			}
			out, _ := runTranslate(t, tc.in)
			tc.check(t, out)
		})
	}
}

// thinking 桥接信封（§1.2 闭环 + D6 逐字节沿用前缀）。
func TestThinkingEnvelopeRoundtrip(t *testing.T) {
	if anthropicThinkingEnvelopePrefix != "ccswitch-anthropic-thinking-v1:" {
		t.Fatalf("前缀 %q 与参考实现不一致（D6：逐字节沿用）", anthropicThinkingEnvelopePrefix)
	}
	block := map[string]any{
		"type": "thinking", "thinking": "hmm", "signature": "sig_abc",
	}
	enc, ok := encodeAnthropicThinkingBlock(block)
	if !ok || !strings.HasPrefix(enc, anthropicThinkingEnvelopePrefix) {
		t.Fatalf("encode = %q ok=%v", enc, ok)
	}
	got, ok := decodeAnthropicThinkingBlock(enc)
	if !ok {
		t.Fatalf("decode 失败")
	}
	if mapStr(got, "signature") != "sig_abc" || mapStr(got, "thinking") != "hmm" {
		t.Fatalf("decode block = %v", got)
	}
	// 无签名 thinking / 无数据 redacted 不可编码。
	if _, ok := encodeAnthropicThinkingBlock(map[string]any{"type": "thinking", "thinking": "x"}); ok {
		t.Fatalf("无签名 thinking 不应可编码")
	}
	if _, ok := encodeAnthropicThinkingBlock(map[string]any{"type": "redacted_thinking", "data": ""}); ok {
		t.Fatalf("空 data redacted 不应可编码")
	}
	// 外来密文（无前缀/解不开）→ 整项丢弃（请求向）。
	if _, ok := decodeAnthropicThinkingBlock("other-vendor:AAAA"); ok {
		t.Fatalf("外来密文不应解码")
	}
}

// reasoning 项回放：本桥信封 → assistant thinking 块（插头部 thinking 序列后）。
func TestReasoningItemReplay(t *testing.T) {
	enc, _ := encodeAnthropicThinkingBlock(map[string]any{
		"type": "thinking", "thinking": "prev", "signature": "sig1",
	})
	body := `{"model":"m","max_output_tokens":100,"input":[` +
		`{"role":"assistant","content":[{"type":"output_text","text":"answer"}]},` +
		`{"type":"reasoning","encrypted_content":"` + enc + `"},` +
		`{"role":"user","content":"next"}]}`
	out, _ := runTranslate(t, body)
	msgs := laneMessages(t, out)
	if len(msgs) < 2 || mapStr(msgs[1], "role") != "assistant" {
		t.Fatalf("messages = %s", canonicalJSONString(out))
	}
	b0 := blocksOf(t, msgs[1])[0]
	if mapStr(b0, "type") != "thinking" {
		t.Fatalf("thinking 应插 assistant 消息头部: %v", msgs[1]["content"])
	}
	// 解不开的 reasoning 项丢弃。
	badBody := `{"model":"m","max_output_tokens":100,"input":[` +
		`{"type":"reasoning","encrypted_content":"garbage"},` +
		`{"role":"user","content":"hi"}]}`
	out2, _ := runTranslate(t, badBody)
	msgs2, _ := asSlice(out2["messages"])
	m2, _ := asMap(msgs2[0])
	if mapStr(m2, "role") != "user" {
		t.Fatalf("解不开的 reasoning 丢弃后首条应为 user: %v", msgs2)
	}
}

// ---- §1.3 tools 与 tool_choice ----

func TestRequestToolsMapping(t *testing.T) {
	body := `{"model":"m","max_output_tokens":100,"input":[{"role":"user","content":"hi"}],` +
		`"tools":[` +
		`{"type":"function","name":"get_weather","description":"d","parameters":{"type":"object","properties":{"city":{"type":"string"}}}},` +
		`{"type":"function","name":"no_params"},` +
		`{"type":"custom","name":"apply_patch"},` +
		`{"type":"tool_search"},` +
		`{"type":"namespace","name":"mcp_files","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]},` +
		`{"type":"web_search"}` +
		`]}`
	out, _ := runTranslate(t, body)
	tools, ok := asSlice(out["tools"])
	if !ok {
		t.Fatalf("tools 缺失: %v", out)
	}
	// function + function(兜底 schema) + custom + tool_search + namespace 折平子项 = 5；
	// web_search 托管工具丢弃。
	if len(tools) != 5 {
		t.Fatalf("tools 条数 = %d, want 5: %s", len(tools), canonicalJSONString(tools))
	}
	t0, _ := asMap(tools[0])
	if mapStr(t0, "name") != "get_weather" {
		t.Fatalf("t0 = %v", t0)
	}
	schema, _ := asMap(t0["input_schema"])
	if st, _ := schema["type"].(string); st != "object" {
		t.Fatalf("input_schema.type = %v", schema["type"])
	}
	if _, has := t0["parameters"]; has {
		t.Fatalf("parameters 键不应出现（改 input_schema）")
	}
	if mapStr(t0, "description") != "d" {
		t.Fatalf("description = %v", t0["description"])
	}
	t1, _ := asMap(tools[1])
	schema1, _ := asMap(t1["input_schema"])
	if len(schema1) == 0 {
		t.Fatalf("缺 parameters 应回退空 object schema: %v", t1)
	}
	// custom 工具包代理 function。
	t2, _ := asMap(tools[2])
	if mapStr(t2, "name") != "apply_patch" {
		t.Fatalf("custom 工具名 = %v", t2["name"])
	}
	// tool_search 固定名。
	t3, _ := asMap(tools[3])
	if mapStr(t3, "name") != "tool_search" {
		t.Fatalf("tool_search = %v", t3["name"])
	}
	// namespace 折平：mcp_files__read。
	t4, _ := asMap(tools[4])
	if mapStr(t4, "name") != "mcp_files__read" {
		t.Fatalf("namespace 折平名 = %v", t4["name"])
	}
}

// 工具全被滤空 → tools 键不出现且 tool_choice 一并不发送（§1.1/§1.3）。
func TestRequestAllToolsFilteredDropsToolChoice(t *testing.T) {
	body := `{"model":"m","max_output_tokens":100,"input":[{"role":"user","content":"hi"}],` +
		`"tools":[{"type":"web_search"}],"tool_choice":"required"}`
	out, _ := runTranslate(t, body)
	if _, has := out["tools"]; has {
		t.Fatalf("tools 不应出现")
	}
	if _, has := out["tool_choice"]; has {
		t.Fatalf("无 tools 的 tool_choice 不应发送（Anthropic 400 且不可重试）")
	}
}

// tool_choice 映射表（§1.3）。
func TestToolChoiceMapping(t *testing.T) {
	tc := newToolContext()
	cases := []struct {
		in   string
		want string
	}{
		{`"required"`, `{"type":"any"}`},
		{`"auto"`, `{"type":"auto"}`},
		{`"none"`, `{"type":"none"}`},
		{`"weird_string"`, `{"type":"auto"}`},
		{`{"type":"function","name":"f"}`, `{"name":"f","type":"tool"}`},
		{`{"type":"custom","name":"c"}`, `{"name":"c","type":"tool"}`},
		{`{"type":"tool_search"}`, `{"name":"tool_search","type":"tool"}`},
		{`{"type":"allowed_tools","tools":["x"]}`, `{"type":"auto"}`},
		{`12345`, `{"type":"auto"}`},
	}
	for _, c := range cases {
		v, ok := decodeJSONValue([]byte(c.in))
		if !ok {
			t.Fatalf("夹具非法: %s", c.in)
		}
		got := mapToolChoiceToAnthropic(v, tc)
		if canonicalJSONString(got) != c.want {
			t.Fatalf("mapToolChoice(%s) = %s, want %s", c.in, canonicalJSONString(got), c.want)
		}
	}
}

// namespace tool_choice 折平到上游名。
func TestToolChoiceNamespaceFlatten(t *testing.T) {
	obj := decodeJSONObject([]byte(`{"model":"m","input":[],"tools":[` +
		`{"type":"namespace","name":"ns1","tools":[{"type":"function","name":"act"}]}]}`))
	tc := buildToolContext(obj)
	got := mapToolChoiceToAnthropic(map[string]any{
		"type": "function", "name": "act", "namespace": "ns1",
	}, tc)
	if mapStr(got, "name") != "ns1__act" {
		t.Fatalf("name = %v, want ns1__act", got["name"])
	}
}

// parallel_tool_calls:false → disable_parallel_tool_use（无 tool_choice 先补 auto）。
func TestParallelToolCallsDisable(t *testing.T) {
	body := `{"model":"m","max_output_tokens":100,"input":[{"role":"user","content":"hi"}],` +
		`"tools":[{"type":"function","name":"t","parameters":{"type":"object"}}],` +
		`"parallel_tool_calls":false}`
	out, _ := runTranslate(t, body)
	tch, ok := asMap(out["tool_choice"])
	if !ok || mapStr(tch, "type") != "auto" || tch["disable_parallel_tool_use"] != true {
		t.Fatalf("tool_choice = %v", out["tool_choice"])
	}
	// true / 缺失不产生任何字段。
	body2 := `{"model":"m","max_output_tokens":100,"input":[{"role":"user","content":"hi"}],` +
		`"tools":[{"type":"function","name":"t","parameters":{"type":"object"}}],` +
		`"parallel_tool_calls":true}`
	out2, _ := runTranslate(t, body2)
	if _, has := out2["tool_choice"]; has {
		t.Fatalf("parallel_tool_calls:true 不应产生 tool_choice: %v", out2["tool_choice"])
	}
}

// ---- §1.4 reasoning.effort → thinking ----

func TestEffortToThinkingBudget(t *testing.T) {
	cases := map[string]int{
		"minimal": 2048, "low": 2048, "LOW": 2048, " medium ": 8192,
		"high": 16384, "xhigh": 24576, "max": 24576, "ultra": 24576,
		"bogus": 0, "": 0,
	}
	for effort, want := range cases {
		if got := effortToThinkingBudget(effort); got != want {
			t.Fatalf("effort %q → %d, want %d", effort, got, want)
		}
	}
}

func TestRequestThinking(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		check func(t *testing.T, out map[string]any)
	}{
		{
			name: "effort=high → enabled 16384",
			in: `{"model":"m","max_output_tokens":40000,"reasoning":{"effort":"high"},` +
				`"input":[{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				th, _ := asMap(out["thinking"])
				if mapStr(th, "type") != "enabled" {
					t.Fatalf("thinking = %v", th)
				}
				if v, _ := asU64(th["budget_tokens"]); v != 16384 {
					t.Fatalf("budget = %v", th["budget_tokens"])
				}
				if _, has := out["temperature"]; has {
					t.Fatalf("thinking 启用时 temperature 不透传")
				}
			},
		},
		{
			name: "预算钳到 max_tokens/2",
			in: `{"model":"m","max_output_tokens":20000,"reasoning":{"effort":"xhigh"},` +
				`"input":[{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				th, _ := asMap(out["thinking"])
				if v, _ := asU64(th["budget_tokens"]); v != 10000 {
					t.Fatalf("budget = %v, want 10000（=20000/2）", th["budget_tokens"])
				}
			},
		},
		{
			name: "钳后 <1024 → 整轮关 thinking（恢复采样透传）",
			in: `{"model":"m","max_output_tokens":1500,"reasoning":{"effort":"low"},` +
				`"temperature":0.5,"input":[{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				if _, has := out["thinking"]; has {
					t.Fatalf("钳后 750<1024 应关 thinking: %v", out["thinking"])
				}
				if _, has := out["temperature"]; !has {
					t.Fatalf("关 thinking 恢复 temperature 透传")
				}
			},
		},
		{
			name: "effort=none → thinking disabled",
			in: `{"model":"m","max_output_tokens":100,"reasoning":{"effort":"none"},` +
				`"input":[{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				th, _ := asMap(out["thinking"])
				if mapStr(th, "type") != "disabled" {
					t.Fatalf("thinking = %v", th)
				}
			},
		},
		{
			name: "未识别 effort → 不启用 thinking 保持采样",
			in: `{"model":"m","max_output_tokens":100,"reasoning":{"effort":"bogus"},` +
				`"temperature":0.3,"input":[{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				if _, has := out["thinking"]; has {
					t.Fatalf("未识别 effort 不应设 thinking: %v", out["thinking"])
				}
				if _, has := out["temperature"]; !has {
					t.Fatalf("普通采样应透传 temperature")
				}
			},
		},
		{
			name: "thinking+强制 tool_choice 冲突 → 保留选择关 thinking",
			in: `{"model":"m","max_output_tokens":40000,"reasoning":{"effort":"high"},` +
				`"temperature":0.4,"tool_choice":"required",` +
				`"tools":[{"type":"function","name":"t","parameters":{"type":"object"}}],` +
				`"input":[{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				th, _ := asMap(out["thinking"])
				if mapStr(th, "type") != "disabled" {
					t.Fatalf("thinking = %v, want disabled（保留调用方 tool_choice）", th)
				}
				tch, _ := asMap(out["tool_choice"])
				if mapStr(tch, "type") != "any" {
					t.Fatalf("tool_choice = %v, want any 保留", tch)
				}
				if _, has := out["temperature"]; !has {
					t.Fatalf("关 thinking 恢复 temperature")
				}
			},
		},
		{
			name: "工具史闸门不过（tool_result 续轮无签名 thinking）→ thinking 关",
			in: func() string {
				return `{"model":"m","max_output_tokens":40000,"reasoning":{"effort":"high"},` +
					`"input":[` +
					`{"type":"function_call","call_id":"c1","name":"t","arguments":"{}"},` +
					`{"type":"function_call_output","call_id":"c1","output":"r"},` +
					`{"role":"user","content":"continue"}]}`
			}(),
			check: func(t *testing.T, out map[string]any) {
				if _, has := out["thinking"]; has {
					t.Fatalf("闸门不过不应设 thinking: %v", out["thinking"])
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, _ := runTranslate(t, tc.in)
			tc.check(t, out)
		})
	}
}

// 工具史闸门通过形态：前一条 assistant 轮带签名 thinking 块且 id 配对。
func TestRequestToolHistoryGatePasses(t *testing.T) {
	enc, _ := encodeAnthropicThinkingBlock(map[string]any{
		"type": "thinking", "thinking": "thought", "signature": "sig",
	})
	body := `{"model":"m","max_output_tokens":40000,"reasoning":{"effort":"high"},` +
		`"input":[` +
		`{"type":"reasoning","encrypted_content":"` + enc + `"},` +
		`{"type":"function_call","call_id":"c1","name":"t","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"c1","output":"r"}]}`
	out, _ := runTranslate(t, body)
	th, ok := asMap(out["thinking"])
	if !ok || mapStr(th, "type") != "enabled" {
		t.Fatalf("thinking = %v, want enabled（闸门通过）", out["thinking"])
	}
}

// ---- §1.5 消息规范化 ----

func TestRequestNormalization(t *testing.T) {
	t.Run("首条非 user → 插合成 user", func(t *testing.T) {
		out, _ := runTranslate(t, `{"model":"m","max_output_tokens":100,"input":[`+
			`{"role":"assistant","content":"answer"},`+
			`{"role":"user","content":"next"}]}`)
		msgs, _ := asSlice(out["messages"])
		m0, _ := asMap(msgs[0])
		if mapStr(m0, "role") != "user" {
			t.Fatalf("首条 role = %v", m0["role"])
		}
		content, _ := asSlice(m0["content"])
		b0, _ := asMap(content[0])
		if mapStr(b0, "text") != "(continuing the conversation)" {
			t.Fatalf("合成文本 = %v", b0["text"])
		}
	})

	t.Run("不成对工具轮整轮剔除", func(t *testing.T) {
		out, _ := runTranslate(t, `{"model":"m","max_output_tokens":100,"input":[`+
			`{"role":"user","content":"go"},`+
			`{"type":"function_call","call_id":"c1","name":"t","arguments":"{}"},`+
			`{"role":"user","content":"next turn"}]}`)
		s := canonicalJSONString(out)
		if strings.Contains(s, "tool_use") {
			t.Fatalf("不成对 tool_use 应整轮剔除: %s", s)
		}
	})

	t.Run("孤儿 tool_result 块剔除", func(t *testing.T) {
		out, _ := runTranslate(t, `{"model":"m","max_output_tokens":100,"input":[`+
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"gone","content":"x"}]},`+
			`{"role":"assistant","content":"answer"},`+
			`{"role":"user","content":"next"}]}`)
		s := canonicalJSONString(out)
		if strings.Contains(s, "tool_result") {
			t.Fatalf("孤儿 tool_result 应剔除: %s", s)
		}
	})

	t.Run("末条 assistant 尾随空白 trim/剔除", func(t *testing.T) {
		// assistant 轮的 tool_use 需要配对 tool_result，否则整轮剔除——用完整对测。
		out, _ := runTranslate(t, `{"model":"m","max_output_tokens":100,"input":[`+
			`{"role":"user","content":"go"},`+
			`{"type":"function_call","call_id":"c1","name":"t","arguments":"{}"},`+
			`{"type":"function_call_output","call_id":"c1","output":"r"},`+
			`{"role":"user","content":"next"},`+
			`{"role":"assistant","content":[{"type":"text","text":"   "}]}]}`)
		s := canonicalJSONString(out)
		if strings.Contains(s, "\"text\":\"   \"") {
			t.Fatalf("尾随空白文本应剔除: %s", s)
		}
	})
}

// §1.2 媒体剥离：嵌图片非原生形态剥出图片块 + 剩余文本。
func TestToolResultMediaStripping(t *testing.T) {
	// MCP/Anthropic image 块嵌在工具输出对象里（顶层即是媒体块形态）。
	body := map[string]any{
		"type":    "function_call_output",
		"call_id": "c1",
		"output": map[string]any{
			"type": "image",
			"source": map[string]any{
				"type": "base64", "media_type": "image/png",
				"data": strings.Repeat("QUJD", 3000),
			},
		},
	}
	content, isError := toolResultContentFromItem(body)
	if isError {
		t.Fatalf("不应 is_error")
	}
	blocks, ok := asSlice(content)
	if !ok {
		t.Fatalf("content = %v", content)
	}
	var hasImage, hasMarker bool
	for _, b := range blocks {
		bm, _ := asMap(b)
		if mapStr(bm, "type") == "image" {
			hasImage = true
		}
		if mapStr(bm, "type") == "text" {
			if s, _ := asStr(bm["text"]); strings.Contains(s, toolResultMediaAttachedMarker) {
				hasMarker = true
			}
		}
	}
	if !hasImage {
		t.Fatalf("应剥出图片块: %s", canonicalJSONString(blocks)[:min(200, len(canonicalJSONString(blocks)))])
	}
	if !hasMarker {
		t.Fatalf("替换标记文本缺失: %s", toolResultMediaAttachedMarker)
	}
}

// 整串 data URL 工具输出（≥8KB）→ 剥为标记文本 + 图片块。
func TestWholeDataURLToolOutput(t *testing.T) {
	big := "data:image/png;base64," + strings.Repeat("iVBORw0KGgo", 1000)
	body := map[string]any{
		"type": "function_call_output", "call_id": "c1", "output": big,
	}
	content, isError := toolResultContentFromItem(body)
	if isError {
		t.Fatalf("不应 is_error")
	}
	s := canonicalJSONString(content)
	if !strings.Contains(s, "image") {
		t.Fatalf("应含图片块: %s", s[:min(200, len(s))])
	}
	if !strings.Contains(s, toolResultMediaAttachedMarker) {
		t.Fatalf("应含媒体标记: %s", toolResultMediaAttachedMarker)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// input 内声明的工具也进注册表（additional_tools 载体，Codex 0.154+）。
func TestInputDeclaredToolsCollected(t *testing.T) {
	body := `{"model":"m","max_output_tokens":100,"input":[` +
		`{"role":"user","content":"hi"},` +
		`{"type":"additional_tools","tools":[{"type":"function","name":"dyn_tool","parameters":{"type":"object"}}]}],` +
		`"tools":[]}`
	out, _ := runTranslate(t, body)
	tools, ok := asSlice(out["tools"])
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %v", out["tools"])
	}
	t0, _ := asMap(tools[0])
	if mapStr(t0, "name") != "dyn_tool" {
		t.Fatalf("input 声明工具未入注册表: %v", tools)
	}
}
