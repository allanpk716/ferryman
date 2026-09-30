// codex_cache_inject_test.go — 票03 表驱动测试：cache 断点注入（对照表
// §1.6 第三步，参考实现 cache_injector.rs 用例同源）。
package dock

import (
	"testing"
)

func cacheInjBody() map[string]any {
	return map[string]any{
		"model": "test",
		"tools": []any{
			map[string]any{"name": "tool1"},
			map[string]any{"name": "tool2"},
		},
		"system": []any{
			map[string]any{"type": "text", "text": "sys prompt"},
		},
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "hi"},
			}},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "hello"},
			}},
		},
	}
}

// 三断点：tools 末尾 + system 末尾 + 最新可缓存消息最后非 thinking 块。
func TestCacheInjectThreeBreakpoints(t *testing.T) {
	body := cacheInjBody()
	injectCacheBreakpoints(body)
	tools, _ := asSlice(body["tools"])
	t1, _ := asMap(tools[1])
	if _, has := t1["cache_control"]; !has {
		t.Fatalf("tools 末尾应打锚点: %v", tools)
	}
	cc, _ := asMap(t1["cache_control"])
	if mapStr(cc, "type") != "ephemeral" {
		t.Fatalf("cache_control = %v", cc)
	}
	if _, hasTTL := cc["ttl"]; hasTTL {
		t.Fatalf("TTL 形态＝裸 ephemeral（无显式 ttl 字段）: %v", cc)
	}
	system, _ := asSlice(body["system"])
	s0, _ := asMap(system[0])
	if _, has := s0["cache_control"]; !has {
		t.Fatalf("system 末尾应打锚点: %v", system)
	}
	msgs, _ := asSlice(body["messages"])
	m1, _ := asMap(msgs[1])
	c, _ := asSlice(m1["content"])
	b0, _ := asMap(c[0])
	if _, has := b0["cache_control"]; !has {
		t.Fatalf("最新消息最后非 thinking 块应打锚点: %v", m1)
	}
	if got := countExistingBreakpoints(body); got != 3 {
		t.Fatalf("断点总数 = %d, want 3", got)
	}
}

// 第四注入点：预算有余且 messages≥4 → 倒数第二条 user 消息再打锚点。
func TestCacheInjectFourthPriorUserAnchor(t *testing.T) {
	body := map[string]any{
		"model": "test",
		"tools": []any{map[string]any{"name": "tool1"}},
		"system": []any{
			map[string]any{"type": "text", "text": "sys"},
		},
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "first"},
			}},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "answer"},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "c1", "content": "result"},
			}},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "latest"},
			}},
		},
	}
	injectCacheBreakpoints(body)
	if got := countExistingBreakpoints(body); got != 4 {
		t.Fatalf("断点总数 = %d, want 4（第四注入点）", got)
	}
	msgs, _ := asSlice(body["messages"])
	m0, _ := asMap(msgs[0])
	c0, _ := asSlice(m0["content"])
	b0, _ := asMap(c0[0])
	if _, has := b0["cache_control"]; !has {
		t.Fatalf("倒数第二条 user（首条 user）应打第四锚点: %s", canonicalJSONString(body))
	}
	m3, _ := asMap(msgs[3])
	c3, _ := asSlice(m3["content"])
	b3, _ := asMap(c3[0])
	if _, has := b3["cache_control"]; !has {
		t.Fatalf("最新消息锚点缺失: %s", canonicalJSONString(body))
	}
}

// messages<4 不打第四锚点。
func TestCacheInjectNoFourthWhenShort(t *testing.T) {
	body := cacheInjBody() // messages=2
	injectCacheBreakpoints(body)
	if got := countExistingBreakpoints(body); got != 3 {
		t.Fatalf("断点总数 = %d, want 3（messages<4 不打第四锚点）", got)
	}
}

// thinking 块跳过：锚点打在最后一个非 thinking 块。
func TestCacheInjectSkipsThinkingBlocks(t *testing.T) {
	body := map[string]any{
		"model": "test",
		"messages": []any{
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "thinking", "thinking": "hmm"},
				map[string]any{"type": "text", "text": "result"},
				map[string]any{"type": "redacted_thinking", "data": "xxx"},
			}},
		},
	}
	injectCacheBreakpoints(body)
	msgs, _ := asSlice(body["messages"])
	m0, _ := asMap(msgs[0])
	c, _ := asSlice(m0["content"])
	if _, has := c[0].(map[string]any)["cache_control"]; has {
		t.Fatalf("thinking 块不应打锚点")
	}
	if _, has := c[1].(map[string]any)["cache_control"]; !has {
		t.Fatalf("text 块应打锚点")
	}
	if _, has := c[2].(map[string]any)["cache_control"]; has {
		t.Fatalf("redacted_thinking 块不应打锚点")
	}
}

// 工具循环形态：最新断点打在最新 tool_result（user）而非更旧 assistant。
func TestCacheInjectLatestToolResult(t *testing.T) {
	body := map[string]any{
		"messages": []any{
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "call_1", "name": "Read", "input": map[string]any{}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "call_1", "content": "done"},
			}},
		},
	}
	injectCacheBreakpoints(body)
	msgs, _ := asSlice(body["messages"])
	m0, _ := asMap(msgs[0])
	c0, _ := asSlice(m0["content"])
	if _, has := c0[0].(map[string]any)["cache_control"]; has {
		t.Fatalf("旧 assistant 不应打锚点（有更新消息）")
	}
	m1, _ := asMap(msgs[1])
	c1, _ := asSlice(m1["content"])
	if _, has := c1[0].(map[string]any)["cache_control"]; !has {
		t.Fatalf("最新 tool_result 应打锚点")
	}
}

// 既有断点计入预算；>4 个既有断点原样保留（调用方所有，交上游裁决）。
func TestCacheInjectExistingBreakpoints(t *testing.T) {
	t.Run("既有 2 个 → 只补 2 个", func(t *testing.T) {
		body := map[string]any{
			"model": "test",
			"tools": []any{
				map[string]any{"name": "t1", "cache_control": map[string]any{"type": "ephemeral"}},
				map[string]any{"name": "t2", "cache_control": map[string]any{"type": "ephemeral"}},
			},
			"system": []any{map[string]any{"type": "text", "text": "sys"}},
			"messages": []any{
				map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "text", "text": "ok"},
				}},
			},
		}
		injectCacheBreakpoints(body)
		if got := countExistingBreakpoints(body); got != 4 {
			t.Fatalf("断点总数 = %d, want 4（预算 4-2=2 补齐）", got)
		}
	})

	t.Run("既有 5 个 → 不动不删", func(t *testing.T) {
		body := map[string]any{
			"model": "test",
			"tools": []any{
				map[string]any{"name": "t1", "cache_control": map[string]any{"type": "ephemeral"}},
				map[string]any{"name": "t2", "cache_control": map[string]any{"type": "ephemeral"}},
			},
			"system": []any{
				map[string]any{"type": "text", "text": "s1", "cache_control": map[string]any{"type": "ephemeral"}},
				map[string]any{"type": "text", "text": "s2", "cache_control": map[string]any{"type": "ephemeral"}},
			},
			"messages": []any{
				map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "text", "text": "m1", "cache_control": map[string]any{"type": "ephemeral"}},
					map[string]any{"type": "text", "text": "m2"},
				}},
			},
		}
		injectCacheBreakpoints(body)
		if got := countExistingBreakpoints(body); got != 5 {
			t.Fatalf("断点总数 = %d, want 5（>4 既有原样保留）", got)
		}
		msgs, _ := asSlice(body["messages"])
		m0, _ := asMap(msgs[0])
		c, _ := asSlice(m0["content"])
		if _, has := c[1].(map[string]any)["cache_control"]; has {
			t.Fatalf("超预算不应再打新锚点")
		}
	})
}

// 字符串 system 先数组化再打锚点（§1.6：字符串 system 先数组化）。
func TestCacheInjectStringSystemToArray(t *testing.T) {
	body := map[string]any{
		"model":  "test",
		"system": "You are a helpful assistant",
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "hi"},
			}},
		},
	}
	injectCacheBreakpoints(body)
	system, ok := asSlice(body["system"])
	if !ok || len(system) != 1 {
		t.Fatalf("system 应数组化: %v", body["system"])
	}
	s0, _ := asMap(system[0])
	if mapStr(s0, "type") != "text" || mapStr(s0, "text") != "You are a helpful assistant" {
		t.Fatalf("system[0] = %v", s0)
	}
	if _, has := s0["cache_control"]; !has {
		t.Fatalf("数组化 system 末尾应打锚点: %v", s0)
	}
}

// 重复注入：预算制（与参考实现同义）——二次调用时既有锚点计入预算，扫描
// 向更旧消息补锚，总数不超 4。
func TestCacheInjectBudgetOnSecondCall(t *testing.T) {
	body := cacheInjBody()
	injectCacheBreakpoints(body)
	if got := countExistingBreakpoints(body); got != 3 {
		t.Fatalf("首次断点总数 = %d, want 3", got)
	}
	injectCacheBreakpoints(body)
	if got := countExistingBreakpoints(body); got != 4 {
		t.Fatalf("二次断点总数 = %d, want 4（预算 1 补在更旧消息，不超预算）", got)
	}
	injectCacheBreakpoints(body)
	if got := countExistingBreakpoints(body); got != 4 {
		t.Fatalf("三次断点总数 = %d, want 4（预算尽不再加）", got)
	}
}
