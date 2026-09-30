// codex_translate_stream_test.go — 票03 表驱动测试：流式翻译
// （Anthropic SSE → responses SSE），用例从对照表 §2.2 事件序模板与 §2.5
// 异形态兜底逐行生成。
package dock

import (
	"strings"
	"testing"
)

// runStream 把一整段上游 SSE 喂进状态机并收尾，返回全部产出事件文本。
func runStream(t *testing.T, input string) string {
	t.Helper()
	return runStreamChunks(t, newToolContext(), input)
}

// runStreamChunks 按任意切块喂（模拟传输分块），验证跨块安全。
func runStreamChunks(t *testing.T, tc *toolContext, input string) string {
	t.Helper()
	st := newStreamTranslator(tc)
	var out []byte
	// 按 7 字节切块：必命中 UTF-8/SSE 帧跨块边界。
	for i := 0; i < len(input); i += 7 {
		end := i + 7
		if end > len(input) {
			end = len(input)
		}
		ev, failed := st.feed([]byte(input[i:end]))
		out = append(out, ev...)
		if failed {
			return string(out)
		}
	}
	out = append(out, st.finish()...)
	return string(out)
}

// eventNames 按序提取产出事件名。
func eventNames(s string) []string {
	var names []string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "event: ") {
			names = append(names, strings.TrimPrefix(line, "event: "))
		}
	}
	return names
}

func assertEventSequence(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("事件序 = %v\n        want %v\n产出:\n%s", got, want, dumps(t, got))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("事件序第 %d 位 = %q, want %q\n全序 = %v", i, got[i], want[i], got)
		}
	}
}

func dumps(t *testing.T, names []string) string {
	t.Helper()
	return strings.Join(names, "\n")
}

// §2.2 事件序模板：一次带 thinking + 文本 + 工具调用的完整流。
func TestStreamFullTemplate(t *testing.T) {
	input := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","model":"glm-5.3","usage":{"input_tokens":12,"output_tokens":0}}}` + "\n\n" +
		// thinking 块
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		// 文本块
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hello"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":1}` + "\n\n" +
		// 工具块
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Tokyo\"}"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":2}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	got := runStream(t, input)
	want := []string{
		"response.created", "response.in_progress",
		// [reasoning]
		"response.output_item.added", "response.reasoning_summary_part.added",
		"response.reasoning_summary_text.delta",
		"response.reasoning_summary_text.done", "response.reasoning_summary_part.done",
		"response.output_item.done",
		// [text]
		"response.output_item.added", "response.content_part.added",
		"response.output_text.delta",
		"response.output_text.done", "response.content_part.done", "response.output_item.done",
		// [tool]
		"response.output_item.added",
		"response.function_call_arguments.delta",
		"response.function_call_arguments.done", "response.output_item.done",
		// 终态
		"response.completed",
	}
	assertEventSequence(t, eventNames(got), want)

	// ID/usage/字段断言。
	if !strings.Contains(got, `"id":"resp_msg_1"`) {
		t.Fatalf("response id 缺失（resp_+上游 id）:\n%s", got)
	}
	if !strings.Contains(got, `"call_id":"toolu_1"`) {
		t.Fatalf("call_id 原样透传失败:\n%s", got)
	}
	if !strings.Contains(got, `"delta":"Hello"`) || !strings.Contains(got, `"delta":"hmm"`) {
		t.Fatalf("文本/thinking 增量缺失:\n%s", got)
	}
	if !strings.Contains(got, `"arguments":"{\"city\":\"Tokyo\"}"`) {
		t.Fatalf("工具参数收口失败:\n%s", got)
	}
	if !strings.Contains(got, `"input_tokens":12`) || !strings.Contains(got, `"output_tokens":7`) {
		t.Fatalf("usage 合并失败（start+delta）:\n%s", got)
	}
	if !strings.Contains(got, `"status":"completed"`) {
		t.Fatalf("终态 completed 缺失:\n%s", got)
	}
	if !strings.Contains(got, anthropicThinkingEnvelopePrefix) {
		t.Fatalf("thinking 信封缺失:\n%s", got)
	}
	// 完成态 reasoning 项无 status 字段（§2.2 表注）。
	if strings.Contains(got, `"type":"reasoning","status"`) {
		t.Fatalf("完成态 reasoning 项不应带 status:\n%s", got)
	}
}

// output_index 按到达顺序单调分配（非上游 index 原值）。
func TestStreamOutputIndexArrivalOrder(t *testing.T) {
	input := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"m"}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":5,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":5}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	got := runStream(t, input)
	if !strings.Contains(got, `"output_index":0`) {
		t.Fatalf("上游 index=5 应映射为 output_index=0:\n%s", got)
	}
	if strings.Contains(got, `"output_index":5`) {
		t.Fatalf("不应透传上游 index:\n%s", got)
	}
}

// 有些网关把完整 input 放 content_block_start、不发 input_json_delta——兜底。
func TestStreamToolInputOnlyInStart(t *testing.T) {
	input := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"m"}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_i","name":"get_weather","input":{"city":"Tokyo"}}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	got := runStream(t, input)
	if !strings.Contains(got, "Tokyo") {
		t.Fatalf("start 兜底参数缺失:\n%s", got)
	}
	if !strings.Contains(got, "event: response.function_call_arguments.done") {
		t.Fatalf("arguments.done 缺失:\n%s", got)
	}
}

// Read 工具：增量不发（消毒留收口），收口剥 pages 空串。
func TestStreamReadToolSanitize(t *testing.T) {
	input := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"m"}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"r1","name":"Read"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"file_path\":\"/tmp/x\",\"pages\":\"\"}"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	got := runStream(t, input)
	if strings.Contains(got, "event: response.function_call_arguments.delta") {
		t.Fatalf("Read 工具不应发增量:\n%s", got)
	}
	if !strings.Contains(got, "/tmp/x") {
		t.Fatalf("file_path 应保留:\n%s", got)
	}
	if strings.Contains(got, "pages") {
		t.Fatalf("pages 空串应剥:\n%s", got)
	}
}

// namespace 工具流式还原（对照表 §2.2 工具块收口）。
func TestStreamNamespaceRestore(t *testing.T) {
	obj := decodeJSONObject([]byte(`{"tools":[{"type":"namespace","name":"mcp_files",` +
		`"tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}]}`))
	tc := buildToolContext(obj)
	input := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"m"}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_1","name":"mcp_files__read"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	got := runStreamChunks(t, tc, input)
	if !strings.Contains(got, `"namespace":"mcp_files"`) || !strings.Contains(got, `"name":"read"`) {
		t.Fatalf("namespace 形状还原失败:\n%s", got)
	}
}

// ---- §2.2 错误事件与终态唯一性 ----

func TestStreamErrorEvents(t *testing.T) {
	t.Run("SSE error 事件 → response.failed（message/type 逐字保留）", func(t *testing.T) {
		input := "event: message_start\n" +
			`data: {"type":"message_start","message":{"id":"m"}}` + "\n\n" +
			"event: error\n" +
			`data: {"type":"error","error":{"type":"overloaded_error","message":"boom"}}` + "\n\n"
		got := runStream(t, input)
		if !strings.Contains(got, "event: response.failed") || !strings.Contains(got, "boom") ||
			!strings.Contains(got, "overloaded_error") {
			t.Fatalf("failed 形状失败:\n%s", got)
		}
	})

	t.Run("message_stop 之后的 error 不二发终态", func(t *testing.T) {
		input := "event: message_start\n" +
			`data: {"type":"message_start","message":{"id":"m"}}` + "\n\n" +
			"event: message_stop\n" +
			`data: {"type":"message_stop"}` + "\n\n" +
			"event: error\n" +
			`data: {"type":"error","error":{"message":"late"}}` + "\n\n"
		got := runStream(t, input)
		if n := strings.Count(got, "event: response.completed"); n != 1 {
			t.Fatalf("completed 出现 %d 次, want 1", n)
		}
		if strings.Contains(got, "event: response.failed") {
			t.Fatalf("终态后不应再发 failed:\n%s", got)
		}
	})

	t.Run("ping 忽略", func(t *testing.T) {
		input := "event: ping\n" +
			`data: {"type":"ping"}` + "\n\n" +
			"event: message_start\n" +
			`data: {"type":"message_start","message":{"id":"m"}}` + "\n\n" +
			"event: message_stop\n" +
			`data: {"type":"message_stop"}` + "\n\n"
		got := runStream(t, input)
		for _, ev := range eventNames(got) {
			if ev == "ping" {
				t.Fatalf("ping 不应产出事件:\n%s", got)
			}
		}
	})
}

// ---- §2.5 异形态兜底 ----

func TestStreamMalformedInputs(t *testing.T) {
	t.Run("流式请求但上游回整体 JSON（无 SSE 标记）→ 合成生命周期", func(t *testing.T) {
		body := `{"id":"msg_json","type":"message","role":"assistant","model":"glm",` +
			`"content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn",` +
			`"usage":{"input_tokens":4,"output_tokens":2}}`
		got := runStream(t, body)
		for _, want := range []string{
			"event: response.created",
			"event: response.output_text.delta",
			`"delta":"Hello"`,
			"event: response.completed",
			`"status":"completed"`,
		} {
			if !strings.Contains(got, want) {
				t.Fatalf("缺 %q:\n%s", want, got)
			}
		}
		if strings.Contains(got, "stream_truncated") {
			t.Fatalf("JSON 回合不当截断报:\n%s", got)
		}
	})

	t.Run("流式请求但上游回 JSON 错误信封 → response.failed", func(t *testing.T) {
		body := `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`
		got := runStream(t, body)
		for _, want := range []string{
			"event: response.failed", "overloaded_error", "busy",
		} {
			if !strings.Contains(got, want) {
				t.Fatalf("缺 %q:\n%s", want, got)
			}
		}
		if strings.Contains(got, "stream_truncated") {
			t.Fatalf("错误信封不当截断报:\n%s", got)
		}
	})

	t.Run("顶层非对象 JSON → response.failed(invalid_response)", func(t *testing.T) {
		got := runStream(t, `[1,2,3]`)
		if !strings.Contains(got, "event: response.failed") || !strings.Contains(got, "invalid_response") {
			t.Fatalf("非对象体应 invalid_response:\n%s", got)
		}
	})

	t.Run("流截断但有部分产出 → completed(incomplete, max_output_tokens)", func(t *testing.T) {
		input := "event: message_start\n" +
			`data: {"type":"message_start","message":{"id":"m"}}` + "\n\n" +
			"event: content_block_start\n" +
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
			"event: content_block_delta\n" +
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}` + "\n\n"
		got := runStream(t, input)
		if !strings.Contains(got, `"delta":"partial"`) {
			t.Fatalf("部分产出应保留:\n%s", got)
		}
		if !strings.Contains(got, "event: response.completed") {
			t.Fatalf("应发 completed:\n%s", got)
		}
		if !strings.Contains(got, `"status":"incomplete"`) || !strings.Contains(got, `"reason":"max_output_tokens"`) {
			t.Fatalf("应标 incomplete+max_output_tokens:\n%s", got)
		}
	})

	t.Run("流截断的工具项标 incomplete 且不发 arguments.done", func(t *testing.T) {
		input := "event: message_start\n" +
			`data: {"type":"message_start","message":{"id":"m"}}` + "\n\n" +
			"event: content_block_start\n" +
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"exec"}}` + "\n\n" +
			"event: content_block_delta\n" +
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":"}}` + "\n\n"
		got := runStream(t, input)
		if strings.Contains(got, "event: response.function_call_arguments.done") {
			t.Fatalf("截断工具项不发 done 增量:\n%s", got)
		}
		if !strings.Contains(got, `"status":"incomplete"`) {
			t.Fatalf("截断工具项标 incomplete:\n%s", got)
		}
	})

	t.Run("零产出截断 → response.failed(stream_truncated)", func(t *testing.T) {
		input := "event: message_start\n" +
			`data: {"type":"message_start","message":{"id":"m"}}` + "\n\n"
		got := runStream(t, input)
		if !strings.Contains(got, "event: response.failed") || !strings.Contains(got, "stream_truncated") {
			t.Fatalf("零产出应 failed:\n%s", got)
		}
		if strings.Contains(got, "event: response.completed") {
			t.Fatalf("零产出不应 completed:\n%s", got)
		}
	})

	t.Run("message_delta 已到、message_stop 没到 → 正常 finalize", func(t *testing.T) {
		input := "event: message_start\n" +
			`data: {"type":"message_start","message":{"id":"m"}}` + "\n\n" +
			"event: content_block_start\n" +
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
			"event: content_block_delta\n" +
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}` + "\n\n" +
			"event: content_block_stop\n" +
			`data: {"type":"content_block_stop","index":0}` + "\n\n" +
			"event: message_delta\n" +
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}` + "\n\n"
		got := runStream(t, input)
		if !strings.Contains(got, "event: response.completed") || !strings.Contains(got, `"status":"completed"`) {
			t.Fatalf("语义完整应正常 finalize:\n%s", got)
		}
	})

	t.Run("末事件缺尾随空行（截断流常见）→ 残余缓冲按最后事件处理", func(t *testing.T) {
		input := "event: message_start\n" +
			`data: {"type":"message_start","message":{"id":"m"}}` + "\n\n" +
			"event: content_block_start\n" +
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
			"event: content_block_delta\n" +
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"tail"}}`
		got := runStream(t, input)
		if !strings.Contains(got, `"delta":"tail"`) {
			t.Fatalf("末事件应处理:\n%s", got)
		}
		if !strings.Contains(got, `"status":"incomplete"`) {
			t.Fatalf("无终态信号标 incomplete:\n%s", got)
		}
	})
}

// UTF-8 跨块安全：多字节字符被切块边界劈开不丢字符。
func TestStreamUTF8AcrossChunks(t *testing.T) {
	input := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"m"}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你好世界"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	// runStreamChunks 已按 7 字节劈块（中文三字节必被劈）。
	got := runStream(t, input)
	if !strings.Contains(got, `"delta":"你好世界"`) {
		t.Fatalf("UTF-8 跨块拼接丢字:\n%s", got)
	}
	if !strings.Contains(got, `"text":"你好世界"`) {
		t.Fatalf("收口文本丢字:\n%s", got)
	}
}

// 帧格式钉死：event:/data: 两行 + 空行。
func TestStreamFrameFormat(t *testing.T) {
	frame := sseFrame("response.created", map[string]any{"a": 1})
	if string(frame) != "event: response.created\ndata: {\"a\":1}\n\n" {
		t.Fatalf("帧格式 = %q", frame)
	}
}

// max_tokens → incomplete（§2.4 表）。
func TestStreamMaxTokensIncomplete(t *testing.T) {
	input := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"m"}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	got := runStream(t, input)
	if !strings.Contains(got, `"status":"incomplete"`) || !strings.Contains(got, `"reason":"max_output_tokens"`) {
		t.Fatalf("max_tokens 应 incomplete:\n%s", got)
	}
}

// 自定义工具：增量不发、收口换 custom_tool_call_input.done。
func TestStreamCustomToolEvents(t *testing.T) {
	obj := decodeJSONObject([]byte(`{"tools":[{"type":"custom","name":"apply_patch"}]}`))
	tc := buildToolContext(obj)
	input := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"m"}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"cp1","name":"apply_patch"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"input\":\"patch text\"}"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	got := runStreamChunks(t, tc, input)
	if strings.Contains(got, "event: response.function_call_arguments.delta") {
		t.Fatalf("自定义工具不发 function 增量:\n%s", got)
	}
	if !strings.Contains(got, "event: response.custom_tool_call_input.done") {
		t.Fatalf("自定义工具收口事件缺失:\n%s", got)
	}
	if !strings.Contains(got, `"type":"custom_tool_call"`) || !strings.Contains(got, "patch text") {
		t.Fatalf("custom_tool_call 项形状失败:\n%s", got)
	}
}
