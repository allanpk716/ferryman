// codex_translate_response.go — 票03：响应向翻译（Anthropic → responses，
// 对照表 §2 非流式节 + usage/stop_reason 映射 + 错误形态）。
package dock

import (
	"fmt"
	"strings"
)

// mapStopReason Anthropic stop_reason → responses (status, incomplete_details.
// reason)（§2.4 表）。pause_turn 不可达路径按 completed（codex 请求不声明
// Anthropic 服务端工具）。
func mapStopReason(stopReason string) (string, string) {
	switch stopReason {
	case "max_tokens":
		return "incomplete", "max_output_tokens"
	case "refusal":
		// 安全拒答不当正常完成，防 codex 视作空回复。
		return "incomplete", "content_filter"
	case "model_context_window_exceeded":
		return "incomplete", "max_output_tokens"
	default:
		return "completed", ""
	}
}

// buildResponsesUsageFromAnthropic usage 映射（§2.3，双向共用）：
// responses input_tokens = fresh + cache_read + cache_creation（含缓存的总
// 输入）；cached_tokens/cache_write_tokens 为两个缓存子集；total 只计一次；
// 兼容别名 cache_creation_input_tokens 保留一个窗口期。缺失/非对象 → 全 0。
func buildResponsesUsageFromAnthropic(usage any) map[string]any {
	u, ok := asMap(usage)
	if !ok {
		return zeroResponsesUsage()
	}
	fresh := uint64From(u["input_tokens"])
	output := uint64From(u["output_tokens"])
	reasoning := uint64AtPath(u, "output_tokens_details", "thinking_tokens")
	cacheRead := uint64From(u["cache_read_input_tokens"])
	cacheCreation := uint64From(u["cache_creation_input_tokens"])

	inputTokens := fresh + cacheRead + cacheCreation
	result := map[string]any{
		"input_tokens":  inputTokens,
		"output_tokens": output,
		"total_tokens":  inputTokens + output,
		"output_tokens_details": map[string]any{
			"reasoning_tokens": reasoning,
		},
	}
	if cacheRead > 0 || cacheCreation > 0 {
		result["input_tokens_details"] = map[string]any{
			"cached_tokens":      cacheRead,
			"cache_write_tokens": cacheCreation,
		}
	}
	if cacheCreation > 0 {
		result["cache_creation_input_tokens"] = cacheCreation
	}
	return result
}

func zeroResponsesUsage() map[string]any {
	return map[string]any{
		"input_tokens":          uint64(0),
		"output_tokens":         uint64(0),
		"total_tokens":          uint64(0),
		"output_tokens_details": map[string]any{"reasoning_tokens": uint64(0)},
	}
}

func uint64From(v any) uint64 {
	n, _ := asU64(v)
	return n
}

func uint64AtPath(m map[string]any, path ...string) uint64 {
	cur := m
	for i, key := range path {
		if i == len(path)-1 {
			return uint64From(cur[key])
		}
		next, ok := asMap(cur[key])
		if !ok {
			return 0
		}
		cur = next
	}
	return 0
}

// anthropicErrorEnvelope 上游错误信封提取（message + type 逐字保留）。
func anthropicErrorEnvelope(body any) (message, errType string) {
	var src map[string]any
	switch v := body.(type) {
	case map[string]any:
		if e, ok := asMap(v["error"]); ok {
			src = e
		} else {
			src = v
		}
	case string:
		return v, "error"
	default:
		return "Anthropic upstream returned an error envelope", "error"
	}
	if s, ok := asStr(src["message"]); ok && s != "" {
		message = s
	} else {
		message = "Anthropic upstream returned an error envelope"
	}
	errType = mapStr(src, "type")
	if errType == "" {
		errType = "error"
	}
	return message, errType
}

// anthropicToResponses 非流式：Anthropic message JSON → responses response
// JSON（§2.1 表）。错误信封 → error 返回（调用方按 §2.4 错误体处置）。
func anthropicToResponses(body map[string]any, tc *toolContext) (map[string]any, error) {
	if t, _ := asStr(body["type"]); t == "error" {
		message, errType := anthropicErrorEnvelope(body)
		return nil, fmt.Errorf("Anthropic upstream %s: %s", errType, message)
	}
	if _, present := body["error"]; present {
		message, errType := anthropicErrorEnvelope(body)
		return nil, fmt.Errorf("Anthropic upstream %s: %s", errType, message)
	}

	id := mapStr(body, "id")
	responseID := "resp_ccswitch"
	if id != "" {
		if strings.HasPrefix(id, "resp_") {
			responseID = id
		} else {
			responseID = "resp_" + id
		}
	}

	var output []any
	var textParts []any

	flushText := func() {
		if len(textParts) > 0 {
			idx := len(output)
			output = append(output, map[string]any{
				"id":      fmt.Sprintf("%s_msg_%d", responseID, idx),
				"type":    "message",
				"status":  "completed",
				"role":    "assistant",
				"content": textParts,
			})
			textParts = nil
		}
	}

	if blocks, ok := asSlice(body["content"]); ok {
		for _, blockAny := range blocks {
			block, ok := asMap(blockAny)
			if !ok {
				continue
			}
			switch mapStr(block, "type") {
			case "text":
				if t, ok := asStr(block["text"]); ok {
					textParts = append(textParts, map[string]any{
						"type": "output_text", "text": t, "annotations": []any{},
					})
				}
			case "tool_use":
				flushText()
				callID := mapStr(block, "id")
				name := mapStr(block, "name")
				input, _ := block["input"]
				if input == nil {
					input = map[string]any{}
				}
				input = sanitizeReadToolInput(name, input)
				itemID := tc.responseToolCallItemID(callID, name)
				output = append(output, tc.responseToolCallItem(
					itemID, "completed", callID, name, canonicalJSONString(input)))
			case "thinking", "redacted_thinking":
				flushText()
				item, ok := responsesReasoningItem(
					fmt.Sprintf("rs_%s_%d", responseID, len(output)), block)
				if ok {
					output = append(output, item)
				}
				// 无签名 thinking 块整块丢弃不回放（§2.1）。
			}
		}
	}
	flushText()

	status, incompleteReason := mapStopReason(mapStr(body, "stop_reason"))
	result := map[string]any{
		"id":         responseID,
		"object":     "response",
		"created_at": 0,
		"status":     status,
		"model":      mapStr(body, "model"),
		"output":     output,
		"usage":      buildResponsesUsageFromAnthropic(body["usage"]),
	}
	if incompleteReason != "" {
		result["incomplete_details"] = map[string]any{"reason": incompleteReason}
	}
	return result, nil
}

// responsesReasoningItem thinking 块 → responses reasoning 项（summary 可见
// 文本；encrypted_content＝§1.2 信封）。不可编码（无签名）＝ok false。
func responsesReasoningItem(itemID string, block map[string]any) (map[string]any, bool) {
	enc, ok := encodeAnthropicThinkingBlock(block)
	if !ok {
		return nil, false
	}
	summary := []any{}
	if t, ok := asStr(block["thinking"]); ok && t != "" {
		summary = append(summary, map[string]any{"type": "summary_text", "text": t})
	}
	return map[string]any{
		"id":                itemID,
		"type":              "reasoning",
		"summary":           summary,
		"encrypted_content": enc,
	}, true
}

// normalizeUpstreamErrorBody 上游错误体规整（§2.4 错误表行 1）：统一为
// {"error":{message,type,code,param}}（codex 客户端只识别这个形状）；非 JSON
// 错误体按文本包进 message（截 1024 字节，UTF-8 边界安全）。
func normalizeUpstreamErrorBody(body []byte) map[string]any {
	parsed, ok := decodeJSONValue(body)
	if !ok {
		return responsesErrorShape(lossyTruncate(body), "upstream_error", nil, nil)
	}
	if s, isStr := parsed.(string); isStr {
		return responsesErrorShape(s, "upstream_error", nil, nil)
	}
	var src map[string]any
	if e, hasErr := asMap(parsed); hasErr {
		if inner, ok := asMap(e["error"]); ok {
			src = inner
		} else {
			src = e
		}
	}
	if src == nil {
		// 顶层非对象（数组/标量）：整体序列化进 message 方便排查。
		return responsesErrorShape(canonicalJSONString(parsed), "upstream_error", nil, nil)
	}
	message := firstNonEmpty(mapStr(src, "message"), mapStr(src, "detail"),
		mapStr(src, "status_msg"), mapStrAtPath(src, "base_resp", "status_msg"))
	if message == "" {
		message = canonicalJSONString(src)
	}
	errType := mapStr(src, "type")
	if errType == "" {
		errType = "upstream_error"
	}
	var code any
	if c, present := src["code"]; present {
		code = c
	} else if c := anyAtPath(src, "base_resp", "status_code"); c != nil {
		code = c
	}
	var param any
	if p, present := src["param"]; present {
		param = p
	}
	return responsesErrorShape(message, errType, code, param)
}

func mapStrAtPath(m map[string]any, path ...string) string {
	cur := m
	for i, key := range path {
		if i == len(path)-1 {
			return mapStr(cur, key)
		}
		next, ok := asMap(cur[key])
		if !ok {
			return ""
		}
		cur = next
	}
	return ""
}

func anyAtPath(m map[string]any, path ...string) any {
	cur := m
	for i, key := range path {
		if i == len(path)-1 {
			return cur[key]
		}
		next, ok := asMap(cur[key])
		if !ok {
			return nil
		}
		cur = next
	}
	return nil
}

// responsesErrorShape codex 认识的 responses 错误体。
func responsesErrorShape(message, errType string, code, param any) map[string]any {
	return map[string]any{"error": map[string]any{
		"message": message, "type": errType, "code": code, "param": param,
	}}
}

// lossyTruncate 非 JSON 错误体文本化截断（1024 字节，字符边界安全）。
func lossyTruncate(body []byte) string {
	const max = 1024
	s := string(body)
	if len(s) <= max {
		return s
	}
	end := max
	for end > 0 && !utf8RuneStart(s[end]) {
		end--
	}
	return s[:end] + "…(truncated)"
}

func utf8RuneStart(b byte) bool {
	return b&0xC0 != 0x80
}

// aggregateAnthropicSSE SSE 体聚合回单条 Anthropic message JSON（§2.4 表
// 倒数第二行：非流式请求但上游回了 SSE 体——含无 Content-Type 标记形态；
// 容忍末事件缺尾随空行的截断流）。
func aggregateAnthropicSSE(body []byte) (map[string]any, error) {
	var message map[string]any
	blocks := map[int]map[string]any{}
	jsonAccum := map[int]*strings.Builder{}
	stopReason := ""
	deltaUsage := map[string]any{}
	sawStop := false

	process := func(block string) error {
		eventName, data := parseSSEBlock(block)
		if data == nil {
			return nil
		}
		dataType := mapStr(data, "type")
		if dataType == "" {
			dataType = eventName
		}
		switch dataType {
		case "message_start":
			if msg, ok := asMap(data["message"]); ok {
				message = msg
			}
		case "content_block_start":
			if idx, ok := asU64(data["index"]); ok {
				i := int(idx)
				var blockVal map[string]any
				if cb, isMap := asMap(data["content_block"]); isMap {
					blockVal = cb
				} else {
					blockVal = map[string]any{"type": "text"} // 非对象头的形状恢复
				}
				blocks[i] = blockVal
				if _, has := jsonAccum[i]; !has {
					jsonAccum[i] = &strings.Builder{}
				}
			}
		case "content_block_delta":
			if idx, ok := asU64(data["index"]); ok {
				i := int(idx)
				b := blocks[i]
				if b == nil {
					b = map[string]any{}
					blocks[i] = b
				}
				acc, has := jsonAccum[i]
				if !has {
					acc = &strings.Builder{}
					jsonAccum[i] = acc
				}
				delta, _ := asMap(data["delta"])
				switch mapStr(delta, "type") {
				case "text_delta":
					appendStrField(b, "text", mapStr(delta, "text"))
				case "thinking_delta":
					appendStrField(b, "thinking", mapStr(delta, "thinking"))
				case "signature_delta":
					if s := mapStr(delta, "signature"); s != "" {
						b["signature"] = s
					}
				case "input_json_delta":
					acc.WriteString(mapStr(delta, "partial_json"))
				}
			}
		case "content_block_stop":
			if idx, ok := asU64(data["index"]); ok {
				i := int(idx)
				if acc := jsonAccum[i]; acc != nil && strings.TrimSpace(acc.String()) != "" {
					parsed, ok := decodeJSONValue([]byte(acc.String()))
					if !ok {
						parsed = map[string]any{}
					}
					if b := blocks[i]; b != nil {
						b["input"] = parsed
					}
				}
			}
		case "message_delta":
			if d, ok := asMap(data["delta"]); ok {
				if r := mapStr(d, "stop_reason"); r != "" {
					stopReason = r
				}
			}
			if u, ok := asMap(data["usage"]); ok {
				for k, v := range u {
					if v == nil {
						continue
					}
					deltaUsage[k] = v
				}
			}
		case "message_stop":
			sawStop = true
		case "error":
			msg, _ := anthropicErrorEnvelope(data)
			return fmt.Errorf("anthropic SSE error event: %s", msg)
		}
		return nil
	}

	buffer := string(body)
	for {
		block, rest, ok := takeSSEBlock(buffer)
		if !ok {
			break
		}
		if err := process(block); err != nil {
			return nil, err
		}
		buffer = rest
	}
	if strings.TrimSpace(buffer) != "" {
		if err := process(buffer); err != nil {
			return nil, err
		}
	}

	if message == nil {
		return nil, fmt.Errorf("anthropic SSE aggregation: missing message_start event")
	}
	if !sawStop && stopReason == "" {
		if len(blocks) == 0 {
			return nil, fmt.Errorf("anthropic SSE aggregation: stream ended before message_stop")
		}
		// 保留部分内容但让截断可见（不当正常 completed 回给 codex）。
		stopReason = "max_tokens"
	}
	maxIdx := -1
	for i := range blocks {
		if i > maxIdx {
			maxIdx = i
		}
	}
	content := make([]any, 0, len(blocks))
	for i := 0; i <= maxIdx; i++ {
		if b, has := blocks[i]; has {
			content = append(content, b)
		}
	}
	message["content"] = content
	if stopReason != "" {
		message["stop_reason"] = stopReason
	}
	if len(deltaUsage) > 0 {
		usage, ok := asMap(message["usage"])
		if !ok {
			usage = map[string]any{}
		}
		for k, v := range deltaUsage {
			// 0 值不覆盖已有正值（message_start 已带的真值不被 delta 的 0 冲掉）。
			if n, isNum := asU64(v); isNum && n == 0 {
				if existing, has := usage[k]; has {
					if e, isNum := asU64(existing); isNum && e > 0 {
						continue
					}
				}
			}
			usage[k] = v
		}
		message["usage"] = usage
	}
	return message, nil
}

// appendStrField 字段累加（创建缺省）。
func appendStrField(block map[string]any, field, text string) {
	existing, _ := block[field].(string)
	block[field] = existing + text
}
