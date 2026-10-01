// codex_translate_request.go — 票03：请求向翻译（responses → Anthropic
// messages，对照表 §1）。构造方式＝白名单：Anthropic 请求体从零拼装，只写
// 下表列出的字段；responses 侧其余顶层字段不进上游体（§1.7：已知专有字段
// 静默丢弃，未知字段名经返回值上报记事件）。
//
// 差异点 D4：不移植参考实现的按模型名 adaptive thinking 启发——三家目标
// （GLM/DeepSeek/Kimi）均非该名单模型，车道永不产出 adaptive/output_config。
package dock

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// anthropicThinkingEnvelopePrefix thinking 桥接信封前缀（差异点 D6：逐字节
// 沿用参考实现——接管存量：cc-switch 时代留下的 codex 会话历史里已带此前缀
// 的 encrypted_content，换前缀＝解码失败＝thinking 连续性断裂）。
const anthropicThinkingEnvelopePrefix = "ccswitch-anthropic-thinking-v1:"

// defaultCodexMaxTokens responses 体缺 max_output_tokens 时的 max_tokens 注入
// 值（Anthropic 必填字段；8192＝保守值，所有当前模型与网关都接受）。
const defaultCodexMaxTokens = 8192

// anthropicThinkingMinBudget Anthropic thinking 预算下限（钳后不足即整轮关）。
const anthropicThinkingMinBudget = 1024

// errCodexInvalidRequest 翻译失败中的请求非法类（400）。
var errCodexInvalidRequest = fmt.Errorf("codex 翻译车道：请求非法")

// effortToThinkingBudget reasoning.effort → thinking 预算（§1.4 预算表；
// 大小写不敏感、trim；未识别值＝0＝不启用 thinking 保持普通采样）。
func effortToThinkingBudget(effort string) int {
	switch strings.TrimSpace(strings.ToLower(effort)) {
	case "minimal", "low":
		return 2048
	case "medium":
		return 8192
	case "high":
		return 16384
	case "xhigh", "max", "ultra":
		return 24576
	}
	return 0
}

// reasoningExplicitlyDisabled effort 显式关闭档（none/off/disabled）。
func reasoningExplicitlyDisabled(effort string) bool {
	switch strings.TrimSpace(strings.ToLower(effort)) {
	case "none", "off", "disabled":
		return true
	}
	return false
}

// encodeAnthropicThinkingBlock 签名 thinking/redacted_thinking 块 → 信封串
// （JSON → base64url 无填充 → 前缀）。无签名/无数据＝不可编码。
func encodeAnthropicThinkingBlock(block map[string]any) (string, bool) {
	switch mapStr(block, "type") {
	case "thinking":
		if mapStr(block, "signature") == "" {
			return "", false
		}
	case "redacted_thinking":
		if mapStr(block, "data") == "" {
			return "", false
		}
	default:
		return "", false
	}
	enc := base64.RawURLEncoding.EncodeToString(encodeCompact(block))
	return anthropicThinkingEnvelopePrefix + enc, true
}

// decodeAnthropicThinkingBlock 信封串 → thinking 块（前缀+可编码性双校验，
// 防外来密文/伪造块回放进工具轮）。
func decodeAnthropicThinkingBlock(s string) (map[string]any, bool) {
	if !strings.HasPrefix(s, anthropicThinkingEnvelopePrefix) {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, anthropicThinkingEnvelopePrefix))
	if err != nil {
		return nil, false
	}
	block := decodeJSONObject(raw)
	if block == nil {
		return nil, false
	}
	if _, ok := encodeAnthropicThinkingBlock(block); !ok {
		return nil, false
	}
	return block, true
}

// responsesToAnthropic 翻译主函数（对照表 §1.1-§1.5）。tc＝调用方预建的
// 工具注册表（响应向还原同用同一实例）。返回译文 + 被丢弃的未知顶层字段名
// 列表（§1.7：调用方记 dock 告警事件）。请求非法（空 messages/arguments
// 非 JSON 对象等）返回 error（400 类）。
func responsesToAnthropic(body map[string]any, tc *toolContext) (map[string]any, []string, error) {
	result := map[string]any{}

	// model 原样透传（改写已在翻译前完成，§5.1 链路第 3 步）。
	if m, ok := asStr(body["model"]); ok {
		result["model"] = m
	}

	// instructions + 历史 system/developer 项 → system（§1.1：Anthropic 只认
	// user/assistant 两种 role，系统/开发者消息上提，不降级为 user）。
	var systemParts []string
	if instructions, ok := asStr(body["instructions"]); ok && meaningfulText(instructions) {
		systemParts = append(systemParts, strings.TrimSpace(instructions))
	}
	if items, ok := asSlice(body["input"]); ok {
		for _, item := range items {
			im, isMap := asMap(item)
			if !isMap {
				continue
			}
			if role := mapStr(im, "role"); role == "system" || role == "developer" {
				systemParts = append(systemParts, responsesSystemText(im)...)
			}
		}
	}
	if len(systemParts) > 0 {
		result["system"] = strings.Join(systemParts, "\n\n")
	}

	// input → messages（数组重嵌套 / 纯字符串单条 user，§1.2）。
	var messages []map[string]any
	switch input := body["input"].(type) {
	case []any:
		var err error
		messages, err = convertInputToMessages(input, tc)
		if err != nil {
			return nil, nil, err
		}
	case string:
		if meaningfulText(input) {
			messages = []map[string]any{{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": input},
				},
			}}
		}
	}

	// 消息规范化五规则（§1.5，顺序即执行序）。
	dropIncompleteToolTurns(&messages)
	dropEmptyMessages(&messages)
	ensureLeadingUserMessage(&messages)
	if len(messages) == 0 {
		return nil, nil, fmt.Errorf("%w: messages 翻译后为空（无法转成 Anthropic 请求）", errCodexInvalidRequest)
	}
	trimTrailingAssistantText(messages)
	dropEmptyMessages(&messages)
	if len(messages) == 0 {
		return nil, nil, fmt.Errorf("%w: messages 翻译后为空（无法转成 Anthropic 请求）", errCodexInvalidRequest)
	}
	thinkingHistoryValid := trailingTurnSupportsThinking(messages)
	// []map[string]any → []any（译文统一 any 容器，测试与下游取值同形）。
	msgsAny := make([]any, len(messages))
	for i, m := range messages {
		msgsAny[i] = m
	}
	result["messages"] = msgsAny

	// reasoning.effort → thinking 判定链（§1.4；D4 无 adaptive 分支）。
	reasoningEffort := ""
	if r, ok := asMap(body["reasoning"]); ok {
		reasoningEffort = mapStr(r, "effort")
	}
	maxTokens := uint64(defaultCodexMaxTokens)
	if v, ok := asU64(body["max_output_tokens"]); ok && v > 0 {
		maxTokens = v
	}
	thinkingEnabled := false
	thinkingBudget := effortToThinkingBudget(reasoningEffort)
	explicitlyDisabled := reasoningExplicitlyDisabled(reasoningEffort)
	if thinkingHistoryValid {
		switch {
		case explicitlyDisabled:
			result["thinking"] = map[string]any{"type": "disabled"}
		case thinkingBudget > 0:
			thinkingEnabled = true
			// 预算钳到 ≤ max_tokens/2（给可见回答留一半）；钳后 <1024（Anthropic
			// 下限）整轮关 thinking、不抬 max_tokens（抬可能超模型输出上限 400）。
			ceiling := maxTokens / 2
			if uint64(thinkingBudget) > ceiling {
				thinkingBudget = int(ceiling)
			}
			if thinkingBudget < anthropicThinkingMinBudget {
				thinkingEnabled = false
			}
		}
	}
	result["max_tokens"] = maxTokens
	if thinkingEnabled {
		result["thinking"] = map[string]any{
			"type":          "enabled",
			"budget_tokens": thinkingBudget,
		}
	}

	// thinking 启用期间 temperature/top_p 不透传（Anthropic 拒绝并存形态）。
	if !thinkingEnabled {
		if v, present := body["temperature"]; present {
			result["temperature"] = v
		}
		if v, present := body["top_p"]; present {
			result["top_p"] = v
		}
	}
	if v, present := body["stream"]; present {
		result["stream"] = v
	}

	// tools：注册表折平后转 Anthropic 形（§1.3）；滤空则 tools 键不出现。
	var anthTools []any
	for _, ct := range tc.chatTools {
		if t, ok := chatToolToAnthropicTool(ct); ok {
			anthTools = append(anthTools, t)
		}
	}
	hasTools := len(anthTools) > 0
	if hasTools {
		result["tools"] = anthTools
	}

	// tool_choice：仅过滤后 tools 非空才映射（无 tools 的 tool_choice 必 400
	// 且不可重试）；thinking 与强制 tool_choice 冲突时保留调用方选择、关
	// thinking（并恢复采样参数透传），不静默弱化（§1.3 约束联动）。
	if hasTools {
		if tcChoice, present := body["tool_choice"]; present {
			mapped := mapToolChoiceToAnthropic(tcChoice, tc)
			forced := false
			if t := mapStr(mapped, "type"); t == "any" || t == "tool" {
				forced = true
			}
			if thinkingEnabled && forced {
				result["thinking"] = map[string]any{"type": "disabled"}
				delete(result, "output_config") // 防御性剥除（本车道本不产出）
				if v, present := body["temperature"]; present {
					result["temperature"] = v
				}
				if v, present := body["top_p"]; present {
					result["top_p"] = v
				}
			}
			result["tool_choice"] = mapped
		}
		if ptc, ok := asBool(body["parallel_tool_calls"]); ok && !ptc {
			if _, present := result["tool_choice"]; !present {
				result["tool_choice"] = map[string]any{"type": "auto"}
			}
			if tcc, ok := asMap(result["tool_choice"]); ok {
				tcc["disable_parallel_tool_use"] = true
			}
		}
	}

	return result, droppedUnknownTopLevelFields(body), nil
}

// knownResponsesTopLevel 消费键 + 已知 responses 专有丢弃键（§1.1/§1.7）。
var knownResponsesTopLevel = map[string]bool{
	// 消费键（翻译有承载位）
	"model": true, "instructions": true, "input": true, "max_output_tokens": true,
	"reasoning": true, "temperature": true, "top_p": true, "stream": true,
	"tools": true, "tool_choice": true, "parallel_tool_calls": true,
	// 已知专有丢弃键（Anthropic 方言无承载位，静默丢弃不记事件）
	"store": true, "include": true, "service_tier": true, "metadata": true,
	"prompt_cache_key": true, "text": true,
}

// droppedUnknownTopLevelFields 未知顶层字段名全集（形状漂移观察的事件源，
// §1.7 差异点 D2）。已知 responses 专有字段（store/include 等）不算未知。
func droppedUnknownTopLevelFields(body map[string]any) []string {
	var dropped []string
	for k := range body {
		if !knownResponsesTopLevel[k] {
			dropped = append(dropped, k)
		}
	}
	sortStrings(dropped)
	return dropped
}

// responsesSystemText system/developer 历史项的文本提取（字符串或
// input_text/output_text/text parts）。
func responsesSystemText(item map[string]any) []string {
	var out []string
	switch content := item["content"].(type) {
	case string:
		if meaningfulText(content) {
			out = append(out, strings.TrimSpace(content))
		}
	case []any:
		for _, part := range content {
			pm, ok := asMap(part)
			if !ok {
				continue
			}
			switch mapStr(pm, "type") {
			case "input_text", "output_text", "text":
				if t, ok := asStr(pm["text"]); ok && meaningfulText(t) {
					out = append(out, strings.TrimSpace(t))
				}
			}
		}
	}
	return out
}

// convertInputToMessages input 数组 → Anthropic messages 重嵌套（§1.2 表）。
func convertInputToMessages(items []any, tc *toolContext) ([]map[string]any, error) {
	messages := []map[string]any{}
	for _, itemAny := range items {
		item, ok := asMap(itemAny)
		if !ok {
			continue
		}
		itemType := mapStr(item, "type")

		// status:"incomplete" 的历史工具调用整项丢弃（§1.2：截断轮残迹，
		// 留下必 400）。
		if itemType == "function_call" || itemType == "custom_tool_call" || itemType == "tool_search_call" {
			if mapStr(item, "status") == "incomplete" {
				continue
			}
		}

		switch itemType {
		case "function_call":
			callID := firstNonEmpty(mapStr(item, "call_id"), mapStr(item, "id"))
			name := mapStr(item, "name")
			upstreamName := tc.chatNameForFunction(name, mapStr(item, "namespace"))
			argsStr := mapStr(item, "arguments")
			var input any
			if strings.TrimSpace(argsStr) == "" {
				input = map[string]any{}
			} else {
				v, ok := decodeJSONValue([]byte(argsStr))
				if !ok {
					return nil, fmt.Errorf("%w: function_call %q 的 arguments 不是合法 JSON",
						errCodexInvalidRequest, name)
				}
				if _, isObj := asMap(v); !isObj {
					return nil, fmt.Errorf("%w: function_call %q 的 arguments 必须是 JSON 对象",
						errCodexInvalidRequest, name)
				}
				input = v
			}
			input = sanitizeReadToolInput(name, input)
			pushBlock(&messages, "assistant", map[string]any{
				"type": "tool_use", "id": callID, "name": upstreamName, "input": input,
			})
		case "custom_tool_call":
			callID := firstNonEmpty(mapStr(item, "call_id"), mapStr(item, "id"))
			name := mapStr(item, "name")
			input, present := item["input"]
			if !present {
				input = ""
			}
			pushBlock(&messages, "assistant", map[string]any{
				"type": "tool_use", "id": callID, "name": name,
				"input": map[string]any{"input": input},
			})
		case "tool_search_call":
			callID := firstNonEmpty(mapStr(item, "call_id"), mapStr(item, "id"))
			input, ok := asMap(item["arguments"])
			if !ok {
				input = map[string]any{}
			}
			pushBlock(&messages, "assistant", map[string]any{
				"type": "tool_use", "id": callID, "name": toolSearchProxyName, "input": input,
			})
		case "function_call_output", "custom_tool_call_output", "tool_search_output":
			callID := mapStr(item, "call_id")
			content, isError := toolResultContentFromItem(item)
			block := map[string]any{
				"type": "tool_result", "tool_use_id": callID, "content": content,
			}
			if isError {
				block["is_error"] = true
			}
			pushToolResultBlock(&messages, block)
		case "input_text":
			// 顶层裸项＝本轮 prompt 的简化形态（无 role）。
			if t, ok := asStr(item["text"]); ok && meaningfulText(t) {
				pushBlock(&messages, "user", map[string]any{"type": "text", "text": t})
			}
		case "input_image":
			if block, ok := imageBlockFromInputImage(item); ok {
				pushBlock(&messages, "user", block)
			}
		case "reasoning":
			// 仅识别本桥自有信封；解不开（外来密文/无签名）整项丢弃。
			if enc := mapStr(item, "encrypted_content"); enc != "" {
				if block, ok := decodeAnthropicThinkingBlock(enc); ok {
					pushAssistantThinkingBlock(&messages, block)
				}
			}
		default:
			// message 项或带 role 的项。
			role := mapStr(item, "role")
			if role == "" {
				role = "user"
			}
			if role == "system" || role == "developer" {
				continue // 已上提 system
			}
			anthRole := "user"
			if role == "assistant" {
				anthRole = "assistant"
			}
			switch content := item["content"].(type) {
			case string:
				if meaningfulText(content) {
					pushBlock(&messages, anthRole, map[string]any{"type": "text", "text": content})
				}
			case []any:
				for _, partAny := range content {
					part, ok := asMap(partAny)
					if !ok {
						continue
					}
					switch mapStr(part, "type") {
					case "input_text", "output_text":
						if t, ok := asStr(part["text"]); ok && meaningfulText(t) {
							pushBlock(&messages, anthRole, map[string]any{"type": "text", "text": t})
						}
					case "refusal":
						if t, ok := asStr(part["refusal"]); ok && meaningfulText(t) {
							pushBlock(&messages, anthRole, map[string]any{"type": "text", "text": t})
						}
					case "input_image":
						if block, ok := imageBlockFromInputImage(part); ok {
							pushBlock(&messages, anthRole, block)
						}
					case "input_file":
						if block, ok := documentBlockFromFile(part); ok {
							pushBlock(&messages, anthRole, block)
						}
					}
					// 其余未知 part 类型静默丢弃（§1.2 表末行）。
				}
			}
			// 其余未知 type 且无 role 的项静默丢弃（match 兜底臂）。
		}
	}
	return messages, nil
}

// sanitizeReadToolInput Read 工具剥 pages:""（§1.2：codex 发空 pages 会被
// Anthropic 网关拒收；其余工具不动）。
func sanitizeReadToolInput(name string, input any) any {
	if name != "Read" {
		return input
	}
	obj, ok := asMap(input)
	if !ok {
		return input
	}
	if pages, isStr := obj["pages"].(string); isStr && pages == "" {
		out := make(map[string]any, len(obj))
		for k, v := range obj {
			out[k] = v
		}
		delete(out, "pages")
		return out
	}
	return input
}

// toolResultContentFromItem function_call_output 的 output 形态分解
// （§1.2 表下清单）。返回 (content, is_error)。
func toolResultContentFromItem(item map[string]any) (any, bool) {
	output, present := item["output"]
	if !present {
		// output 缺失：整项规范化 JSON 兜底（不丢 call_id 配对）。
		return canonicalJSONString(item), false
	}
	switch v := output.(type) {
	case string:
		if content, isError, ok := alternateImageToolResult(v); ok {
			return content, isError
		}
		return v, false
	case []any:
		var content []any
		isError := false
		for _, partAny := range v {
			part, ok := asMap(partAny)
			if !ok {
				content = append(content, map[string]any{
					"type": "text", "text": canonicalJSONString(partAny),
				})
				continue
			}
			switch mapStr(part, "type") {
			case "input_text", "output_text":
				if t, ok := asStr(part["text"]); ok {
					if t == toolResultErrorMarker {
						isError = true
					} else {
						content = append(content, map[string]any{"type": "text", "text": t})
					}
				}
			case "input_image":
				if img, ok := imageBlockFromInputImage(part); ok {
					content = append(content, img)
				} else {
					content = append(content, map[string]any{
						"type": "text", "text": canonicalJSONString(part),
					})
				}
			case "input_file":
				if doc, ok := documentBlockFromFile(part); ok {
					content = append(content, doc)
				} else {
					content = append(content, map[string]any{
						"type": "text", "text": canonicalJSONString(part),
					})
				}
			default:
				if altContent, altErr, ok := alternateImageToolResult(part); ok {
					isError = isError || altErr
					if blocks, isArr := altContent.([]any); isArr {
						content = append(content, blocks...)
					} else if s, isStr := altContent.(string); isStr {
						content = append(content, map[string]any{"type": "text", "text": s})
					} else {
						content = append(content, map[string]any{
							"type": "text", "text": canonicalJSONString(altContent),
						})
					}
				} else {
					content = append(content, map[string]any{
						"type": "text", "text": canonicalJSONString(part),
					})
				}
			}
		}
		return content, isError
	default:
		if content, isError, ok := alternateImageToolResult(output); ok {
			return content, isError
		}
		return canonicalJSONString(output), false
	}
}

// alternateImageToolResult 嵌媒体非原生形态剥离（JSON 字符串里的 MCP image
// 块 / Chat image_url / 整串 data URL）：命中 → 剥出图片块 + 剩余文本；
// 未命中 → ok=false 原样走默认分解。
func alternateImageToolResult(v any) (any, bool, bool) {
	replacementBlock := map[string]any{"type": "input_text", "text": toolResultMediaAttachedMarker}
	cleaned, replaced, mediaParts := stripAndClampMediaFromToolValue(
		v, replacementBlock, toolResultMediaAttachedMarker, 0)
	if replaced == 0 {
		return nil, false, false
	}
	var content []any
	isError := false
	appendValue := func(val any) {
		switch cur := val.(type) {
		case string:
			if cur == toolResultErrorMarker {
				isError = true
			} else if cur != "" {
				content = append(content, map[string]any{"type": "text", "text": cur})
			}
		case []any:
			for _, p := range cur {
				pm, ok := asMap(p)
				if !ok {
					content = append(content, map[string]any{
						"type": "text", "text": canonicalJSONString(p),
					})
					continue
				}
				switch mapStr(pm, "type") {
				case "input_text", "output_text", "text":
					if t, ok := asStr(pm["text"]); ok {
						if t == toolResultErrorMarker {
							isError = true
						} else {
							content = append(content, map[string]any{"type": "text", "text": t})
						}
					}
				default:
					content = append(content, map[string]any{
						"type": "text", "text": canonicalJSONString(pm),
					})
				}
			}
		case map[string]any:
			if t := mapStr(cur, "type"); t == "input_text" || t == "output_text" || t == "text" {
				if txt, ok := asStr(cur["text"]); ok {
					if txt == toolResultErrorMarker {
						isError = true
					} else {
						content = append(content, map[string]any{"type": "text", "text": txt})
					}
				}
			} else {
				content = append(content, map[string]any{
					"type": "text", "text": canonicalJSONString(cur),
				})
			}
		default:
			content = append(content, map[string]any{
				"type": "text", "text": canonicalJSONString(val),
			})
		}
	}
	appendValue(cleaned)
	for _, mp := range mediaParts {
		if img, ok := imageBlockFromInputImage(mp); ok {
			content = append(content, img)
		}
	}
	return content, isError, true
}

// imageBlockFromInputImage input_image → Anthropic image 块（data: URL 拆
// base64 source；http(s) 用 url source；其余丢弃）。
func imageBlockFromInputImage(part map[string]any) (map[string]any, bool) {
	var u string
	switch iu := part["image_url"].(type) {
	case string:
		u = iu
	case map[string]any:
		u = mapStr(iu, "url")
	default:
		return nil, false
	}
	lower := strings.ToLower(u)
	switch {
	case strings.HasPrefix(lower, "data:"):
		rest := u[5:]
		comma := strings.IndexByte(rest, ',')
		if comma < 0 {
			return nil, false
		}
		meta := rest[:comma]
		mediaType := meta
		if i := strings.IndexByte(meta, ';'); i >= 0 {
			mediaType = meta[:i]
		}
		if mediaType == "" {
			mediaType = "image/png"
		}
		return map[string]any{
			"type": "image",
			"source": map[string]any{
				"type": "base64", "media_type": mediaType, "data": rest[comma+1:],
			},
		}, true
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		return map[string]any{
			"type":   "image",
			"source": map[string]any{"type": "url", "url": u},
		}, true
	}
	return nil, false
}

// documentBlockFromFile input_file → Anthropic document 块（file_url http(s)
// 为 url source；file_data data: 为 base64 source；filename → title）。
func documentBlockFromFile(part map[string]any) (map[string]any, bool) {
	filename := mapStr(part, "filename")
	var block map[string]any
	if fu := mapStr(part, "file_url"); strings.HasPrefix(fu, "http://") || strings.HasPrefix(fu, "https://") {
		block = map[string]any{
			"type":   "document",
			"source": map[string]any{"type": "url", "url": fu},
		}
	} else {
		fd := mapStr(part, "file_data")
		if !strings.HasPrefix(strings.ToLower(fd), "data:") || fd == "" {
			return nil, false
		}
		rest := strings.TrimPrefix(strings.TrimPrefix(fd, "data:"), "")
		comma := strings.IndexByte(rest, ',')
		if comma < 0 || rest[comma+1:] == "" {
			return nil, false
		}
		meta := rest[:comma]
		mediaType := meta
		if i := strings.IndexByte(meta, ';'); i >= 0 {
			mediaType = meta[:i]
		}
		if mediaType == "" {
			mediaType = "application/pdf"
		}
		block = map[string]any{
			"type": "document",
			"source": map[string]any{
				"type": "base64", "media_type": mediaType, "data": rest[comma+1:],
			},
		}
	}
	if filename != "" {
		block["title"] = filename
	}
	return block, true
}

// chatToolToAnthropicTool 注册表 chat 形工具 → Anthropic 工具（§1.3 表：
// name/input_schema=parameters/description?/strict?；空 parameters 兜底）。
func chatToolToAnthropicTool(chatTool map[string]any) (map[string]any, bool) {
	fn, ok := asMap(chatTool["function"])
	if !ok {
		return nil, false
	}
	name := strings.TrimSpace(mapStr(fn, "name"))
	if name == "" {
		return nil, false
	}
	inputSchema := normalizeFunctionParameters(fn["parameters"])
	tool := map[string]any{"name": name, "input_schema": inputSchema}
	if d, ok := asStr(fn["description"]); ok {
		tool["description"] = d
	}
	if strict, ok := asBool(fn["strict"]); ok {
		tool["strict"] = strict
	}
	return tool, true
}

// mapToolChoiceToAnthropic tool_choice 映射（§1.3 表：required→any、auto→
// auto、none→none、function/custom/tool_search→tool（折平名），其余降级
// auto 防 OpenAI 私有形状直传 400）。
func mapToolChoiceToAnthropic(toolChoice any, tc *toolContext) map[string]any {
	switch v := toolChoice.(type) {
	case string:
		switch v {
		case "required":
			return map[string]any{"type": "any"}
		case "auto":
			return map[string]any{"type": "auto"}
		case "none":
			return map[string]any{"type": "none"}
		}
		return map[string]any{"type": "auto"}
	case map[string]any:
		switch mapStr(v, "type") {
		case "function":
			upstreamName := tc.chatNameForFunction(mapStr(v, "name"), mapStr(v, "namespace"))
			return map[string]any{"type": "tool", "name": upstreamName}
		case "custom":
			return map[string]any{"type": "tool", "name": mapStr(v, "name")}
		case "tool_search":
			return map[string]any{"type": "tool", "name": toolSearchProxyName}
		}
		return map[string]any{"type": "auto"}
	}
	return map[string]any{"type": "auto"}
}

// ---- 消息块装配（§1.2 通用机制） ----

// pushBlock 同 role 相邻块合并进同一条 message，否则新开。
func pushBlock(messages *[]map[string]any, role string, block map[string]any) {
	if len(*messages) > 0 {
		last := (*messages)[len(*messages)-1]
		if mapStr(last, "role") == role {
			if content, ok := last["content"].([]any); ok {
				last["content"] = append(content, block)
				return
			}
		}
	}
	*messages = append(*messages, map[string]any{"role": role, "content": []any{block}})
}

// pushToolResultBlock tool_result 块永远排在所在 user 消息里任何 text/image
// 块之前（Anthropic 顺序要求）。
func pushToolResultBlock(messages *[]map[string]any, block map[string]any) {
	if len(*messages) > 0 {
		last := (*messages)[len(*messages)-1]
		if mapStr(last, "role") == "user" {
			if content, ok := last["content"].([]any); ok {
				insertAt := len(content)
				for i, item := range content {
					if im, ok := asMap(item); ok && mapStr(im, "type") != "tool_result" {
						insertAt = i
						break
					}
				}
				newContent := make([]any, 0, len(content)+1)
				newContent = append(newContent, content[:insertAt]...)
				newContent = append(newContent, block)
				newContent = append(newContent, content[insertAt:]...)
				last["content"] = newContent
				return
			}
		}
	}
	*messages = append(*messages, map[string]any{"role": "user", "content": []any{block}})
}

// pushAssistantThinkingBlock thinking 块插在 assistant 消息头部 thinking
// 块序列之后。
func pushAssistantThinkingBlock(messages *[]map[string]any, block map[string]any) {
	if len(*messages) > 0 {
		last := (*messages)[len(*messages)-1]
		if mapStr(last, "role") == "assistant" {
			if content, ok := last["content"].([]any); ok {
				idx := 0
				for idx < len(content) {
					im, isMap := asMap(content[idx])
					if !isMap {
						break
					}
					t := mapStr(im, "type")
					if t != "thinking" && t != "redacted_thinking" {
						break
					}
					idx++
				}
				newContent := make([]any, 0, len(content)+1)
				newContent = append(newContent, content[:idx]...)
				newContent = append(newContent, block)
				newContent = append(newContent, content[idx:]...)
				last["content"] = newContent
				return
			}
		}
	}
	pushBlock(messages, "assistant", block)
}

// ---- 消息规范化（§1.5 五规则） ----

// dropIncompleteToolTurns 压缩/续接历史里不成对的 assistant tool_use ↔ user
// tool_result 整轮剔除；不成对 user 消息里残留的孤儿 tool_result 块剔除。
func dropIncompleteToolTurns(messages *[]map[string]any) {
	original := *messages
	sanitized := make([]map[string]any, 0, len(original))
	i := 0
	for i < len(original) {
		msg := original[i]
		isAssistant := mapStr(msg, "role") == "assistant"
		var toolUseIDs []string
		if isAssistant {
			toolUseIDs = messageBlockIDs(msg, "tool_use", "id")
		}
		if len(toolUseIDs) > 0 {
			var pairedUser map[string]any
			if i+1 < len(original) && mapStr(original[i+1], "role") == "user" {
				pairedUser = original[i+1]
			}
			var toolResultIDs []string
			if pairedUser != nil {
				toolResultIDs = messageBlockIDs(pairedUser, "tool_result", "tool_use_id")
			}
			complete := allNonEmpty(toolUseIDs) && allNonEmpty(toolResultIDs) &&
				uniqueCount(toolUseIDs) == len(toolUseIDs) &&
				uniqueCount(toolResultIDs) == len(toolResultIDs) &&
				sameSet(toolUseIDs, toolResultIDs)
			if complete {
				sanitized = append(sanitized, msg)
				sanitized = append(sanitized, pairedUser)
			} else if pairedUser != nil {
				user := copyMessageWithoutToolResults(pairedUser)
				if messageHasContent(user) {
					sanitized = append(sanitized, user)
				}
			}
			if pairedUser != nil {
				i += 2
			} else {
				i++
			}
			continue
		}
		out := msg
		if mapStr(msg, "role") == "user" {
			// 未被完整工具对消耗的 user 消息不得残留 tool_result 块。
			out = copyMessageWithoutToolResults(msg)
		}
		if messageHasContent(out) {
			sanitized = append(sanitized, out)
		}
		i++
	}
	*messages = sanitized
}

// messageBlockIDs 消息 content 里指定块类型的 id 字段全集。
func messageBlockIDs(msg map[string]any, blockType, idField string) []string {
	content, ok := asSlice(msg["content"])
	if !ok {
		return nil
	}
	var ids []string
	for _, item := range content {
		im, ok := asMap(item)
		if !ok || mapStr(im, "type") != blockType {
			continue
		}
		s, _ := asStr(im[idField])
		ids = append(ids, s)
	}
	return ids
}

// copyMessageWithoutToolResults 剔除 tool_result 块的消息浅拷贝（原消息不
// 改——上游消息可能是共享引用）。
func copyMessageWithoutToolResults(msg map[string]any) map[string]any {
	content, ok := asSlice(msg["content"])
	if !ok {
		return msg
	}
	kept := make([]any, 0, len(content))
	for _, item := range content {
		if im, isMap := asMap(item); isMap && mapStr(im, "type") == "tool_result" {
			continue
		}
		kept = append(kept, item)
	}
	out := make(map[string]any, len(msg))
	for k, v := range msg {
		out[k] = v
	}
	out["content"] = kept
	return out
}

// messageHasContent content 数组非空（非数组形视为有内容）。
func messageHasContent(msg map[string]any) bool {
	content, ok := asSlice(msg["content"])
	if !ok {
		return true
	}
	return len(content) > 0
}

func allNonEmpty(ids []string) bool {
	for _, id := range ids {
		if id == "" {
			return false
		}
	}
	return true
}

func uniqueCount(ids []string) int {
	seen := map[string]int{}
	for _, id := range ids {
		seen[id]++
	}
	n := 0
	for _, c := range seen {
		if c == 1 {
			n++
		}
	}
	return n
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := map[string]int{}
	for _, id := range a {
		set[id]++
	}
	for _, id := range b {
		set[id]--
		if set[id] < 0 {
			return false
		}
	}
	for _, c := range set {
		if c != 0 {
			return false
		}
	}
	return true
}

// ensureLeadingUserMessage 首条必须是 user：否则头部插合成 user 消息
// "(continuing the conversation)"（§1.5 规则 3）。
func ensureLeadingUserMessage(messages *[]map[string]any) {
	if len(*messages) == 0 || mapStr((*messages)[0], "role") == "user" {
		return
	}
	synth := map[string]any{
		"role": "user",
		"content": []any{
			map[string]any{"type": "text", "text": "(continuing the conversation)"},
		},
	}
	*messages = append([]map[string]any{synth}, *messages...)
}

// dropEmptyMessages 内容全被过滤后 content 空的消息剔除（§1.5 规则 2）。
func dropEmptyMessages(messages *[]map[string]any) {
	kept := (*messages)[:0]
	for _, msg := range *messages {
		if messageHasContent(msg) {
			kept = append(kept, msg)
		}
	}
	*messages = kept
}

// trimTrailingAssistantText 末条 assistant 的尾随空白文本 trim/剔除
// （Anthropic 拒绝 prefill 尾空白；§1.5 规则 5）。
func trimTrailingAssistantText(messages []map[string]any) {
	if len(messages) == 0 {
		return
	}
	last := messages[len(messages)-1]
	if mapStr(last, "role") != "assistant" {
		return
	}
	content, ok := asSlice(last["content"])
	if !ok || len(content) == 0 {
		return
	}
	lastBlock, ok := asMap(content[len(content)-1])
	if !ok || mapStr(lastBlock, "type") != "text" {
		return
	}
	text, ok := asStr(lastBlock["text"])
	if !ok {
		return
	}
	trimmed := strings.TrimRight(text, " \t\n\r\v\f")
	if trimmed == "" {
		last["content"] = content[:len(content)-1]
	} else if trimmed != text {
		lastBlock["text"] = trimmed
	}
}

// trailingTurnSupportsThinking 工具史闸门（§1.4 判定链 1）：末轮是
// tool_result 续轮时，仅当紧邻前一条 assistant 轮含签名 thinking/
// redacted_thinking 块且 tool_use id 集合与 tool_result id 完全配对才允许
// thinking；闸门不过＝本轮 thinking 关。
func trailingTurnSupportsThinking(messages []map[string]any) bool {
	if len(messages) == 0 {
		return false
	}
	last := messages[len(messages)-1]
	if mapStr(last, "role") != "user" {
		return false
	}
	content, ok := asSlice(last["content"])
	if !ok {
		return true
	}
	var toolResultIDs []string
	for _, item := range content {
		im, ok := asMap(item)
		if !ok || mapStr(im, "type") != "tool_result" {
			continue
		}
		id := mapStr(im, "tool_use_id")
		if id == "" {
			return false
		}
		toolResultIDs = append(toolResultIDs, id)
	}
	if len(toolResultIDs) == 0 {
		return true
	}
	if len(messages) < 2 {
		return false
	}
	paired := messages[len(messages)-2]
	if mapStr(paired, "role") != "assistant" {
		return false
	}
	blocks, ok := asSlice(paired["content"])
	if !ok {
		return false
	}
	hasSignedThinking := false
	toolUseSet := map[string]bool{}
	for _, item := range blocks {
		bm, ok := asMap(item)
		if !ok {
			continue
		}
		switch mapStr(bm, "type") {
		case "thinking", "redacted_thinking":
			hasSignedThinking = true
		case "tool_use":
			if id := mapStr(bm, "id"); id != "" {
				toolUseSet[id] = true
			}
		}
	}
	if !hasSignedThinking {
		return false
	}
	for _, id := range toolResultIDs {
		if !toolUseSet[id] {
			return false
		}
	}
	return true
}

// sortStrings 排序（事件字段列表确定性用）。
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
