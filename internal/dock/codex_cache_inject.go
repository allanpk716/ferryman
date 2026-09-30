// codex_cache_inject.go — 票03：cache 断点注入（对照表 §1.6 第三步，恒开）。
//
// 移植参考实现 cache_injector.rs（差异点：参考有总开关，本仓对照表裁定恒开
// ——codex 请求不带 Anthropic cache_control，不注入则每轮工具循环全价重付
// system/tools/历史；三家目标的 Anthropic 方言端点均支持标准 cache_control）。
// 预算 4 断点，注入顺序：tools 末尾 → system 末尾（字符串 system 先数组化）
// → 最新一条可缓存消息的最后一个非 thinking 块 → 预算有余且 messages≥4 时
// 倒数第二条 user 消息再打一锚点（best-effort，应对长工具循环中稳定前缀落
// 在最新断点 20 块回看窗之外）。TTL 形态＝注入裸 {"type":"ephemeral"}（5m
// 为端点默认语义）。既有断点 >4 个则不动交上游裁决。
package dock

// cacheBreakpointBudget Anthropic cache_control 断点预算。
const cacheBreakpointBudget = 4

// injectCacheBreakpoints 就地注入 cache_control 断点（幂等：已有断点的块
// 不再打）。
func injectCacheBreakpoints(body map[string]any) {
	existing := countExistingBreakpoints(body)
	budget := cacheBreakpointBudget - existing
	if existing > cacheBreakpointBudget {
		// 既有标记归调用方所有：不静默删改，交上游裁决（记日志一次即可，
		// 车道高频调用不刷屏——超限是配置/客户端形态问题，非逐请求事件）。
		budget = 0
	}
	if budget <= 0 {
		return
	}

	// (a) tools 末尾。
	if budget > 0 {
		if tools, ok := asSlice(body["tools"]); ok && len(tools) > 0 {
			if last, ok := asMap(tools[len(tools)-1]); ok {
				if _, has := last["cache_control"]; !has {
					last["cache_control"] = ephemeralCacheControl()
					budget--
				}
			}
		}
	}

	// (b) system 末尾（字符串先数组化）。
	if budget > 0 {
		if text, ok := asStr(body["system"]); ok {
			body["system"] = []any{
				map[string]any{"type": "text", "text": text},
			}
		}
		if system, ok := asSlice(body["system"]); ok && len(system) > 0 {
			if last, ok := asMap(system[len(system)-1]); ok {
				if _, has := last["cache_control"]; !has {
					last["cache_control"] = ephemeralCacheControl()
					budget--
				}
			}
		}
	}

	// (c) 最新一条可缓存消息的最后一个非 thinking 块 + (d) 倒数第二条 user
	// 锚点（预算有余且 messages≥4，best-effort 居 4 断点预算内）。
	if budget > 0 {
		messages, ok := asSlice(body["messages"])
		if ok {
			for i := len(messages) - 1; i >= 0; i-- {
				m, isMap := asMap(messages[i])
				if !isMap {
					continue
				}
				if injectMessageBreakpoint(m) {
					budget--
					break
				}
			}
			if budget > 0 && len(messages) >= 4 {
				userCount := 0
				for i := len(messages) - 1; i >= 0; i-- {
					m, isMap := asMap(messages[i])
					if !isMap || mapStr(m, "role") != "user" {
						continue
					}
					userCount++
					if userCount == 2 {
						injectMessageBreakpoint(m) // best-effort：不打满不强制
						break
					}
				}
			}
		}
	}
}

// injectMessageBreakpoint 消息内最后一个非 thinking 块打锚点；已有锚点/无
// 可打块返回 false。
func injectMessageBreakpoint(message map[string]any) bool {
	content, ok := asSlice(message["content"])
	if !ok {
		return false
	}
	for i := len(content) - 1; i >= 0; i-- {
		block, isMap := asMap(content[i])
		if !isMap {
			continue
		}
		switch mapStr(block, "type") {
		case "thinking", "redacted_thinking":
			continue
		}
		if _, has := block["cache_control"]; has {
			return false
		}
		block["cache_control"] = ephemeralCacheControl()
		return true
	}
	return false
}

// ephemeralCacheControl 裸 ephemeral（5m 端点默认语义，无显式 ttl 字段）。
func ephemeralCacheControl() map[string]any {
	return map[string]any{"type": "ephemeral"}
}

// countExistingBreakpoints 既有断点计数（tools/system/messages 全扫）。
func countExistingBreakpoints(body map[string]any) int {
	count := 0
	if tools, ok := asSlice(body["tools"]); ok {
		for _, t := range tools {
			if tm, isMap := asMap(t); isMap {
				if _, has := tm["cache_control"]; has {
					count++
				}
			}
		}
	}
	if system, ok := asSlice(body["system"]); ok {
		for _, b := range system {
			if bm, isMap := asMap(b); isMap {
				if _, has := bm["cache_control"]; has {
					count++
				}
			}
		}
	}
	if messages, ok := asSlice(body["messages"]); ok {
		for _, m := range messages {
			mm, isMap := asMap(m)
			if !isMap {
				continue
			}
			if content, ok := asSlice(mm["content"]); ok {
				for _, b := range content {
					if bm, isMap := asMap(b); isMap {
						if _, has := bm["cache_control"]; has {
							count++
						}
					}
				}
			}
		}
	}
	return count
}
