// rewrite_test.go — 票05：改写五件之纯逻辑件验收钉子。
// 覆盖票面验收四条：模型映射六键（四别名→映射值／值域真名带不带 [1M] 原样
// 透传／未知名落 default／[1m] 小写变体同样剥／中段 [1M] 不剥）、text-only
// 图片降级（固定占位字面量）、顶层形状保真（只动 model/messages，其余键解析
// 后 DeepEqual，含中文/emoji/HTML 字符/大整数精度）、非法 body 错误返回。
//
// 断言口径：改写后重编码会规范化空白与键序（票面接受的等价），故保真一律
// 「解析后 DeepEqual 未动键＋动的键精确断言」，不做全文逐字节比较；解析
// 与实现同用 UseNumber，数字按字面量比较。
package dock

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

// fixtureCfg 票面四别名＋default＋text_only 的样例映射（档名任意，规则与
// 形状为准）。注意 TextOnly 放的是映射后的 GLM 档名——匹配目标是改写后模型。
func fixtureCfg() RewriteConfig {
	return RewriteConfig{
		ModelMap: map[string]string{
			"claude-opus-5":    "glm-5.5",
			"claude-fable-5-1": "glm-5.3",
			"claude-sonnet-5":  "glm-5.3-air",
			"claude-haiku-4-5": "glm-5-air",
		},
		Default:  "glm-4.7-flash",
		TextOnly: []string{"glm-5.3", "glm-5-air"},
	}
}

// rewriteOK 便捷入口：改写意外报错直接 Fatal。
func rewriteOK(t *testing.T, body []byte, cfg RewriteConfig) Rewritten {
	t.Helper()
	out, err := Rewrite(body, cfg)
	if err != nil {
		t.Fatalf("Rewrite 意外报错: %v", err)
	}
	return out
}

// parseWithUseNumber 与实现同口径解析（数字为 json.Number 字面量），
// DeepEqual 才有逐字面量意义。
func parseWithUseNumber(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var v any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	obj, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("解析结果不是 JSON object")
	}
	return obj
}

// msgWithBlocks 构造 content 为块数组的消息。
func msgWithBlocks(blocks ...map[string]any) map[string]any {
	return map[string]any{"role": "user", "content": blocks}
}

func textBlock(s string) map[string]any {
	return map[string]any{"type": "text", "text": s}
}

func imageBlock() map[string]any {
	return map[string]any{
		"type":   "image",
		"source": map[string]any{"type": "base64", "media_type": "image/png", "data": "AAAA"},
	}
}

// TestRewriteModelMappingSixKeys 六键映射逐键：先剥 [1M] 后缀（大小写变体），
// 值域真名原样透传（[1M] 信息丢弃），别名换映射值，未知名落 default，
// 中段 [1M] 不剥。
func TestRewriteModelMappingSixKeys(t *testing.T) {
	cases := []struct {
		name    string
		model   string
		wantOut string
	}{
		{"别名_opus", "claude-opus-5", "glm-5.5"},
		{"别名_fable", "claude-fable-5-1", "glm-5.3"},
		{"别名_sonnet", "claude-sonnet-5", "glm-5.3-air"},
		{"别名_haiku", "claude-haiku-4-5", "glm-5-air"},
		{"真名_值域内原样透传", "glm-5.5", "glm-5.5"},
		{"真名_default本身在值域_透传优先于兜底", "glm-4.7-flash", "glm-4.7-flash"},
		{"真名带[1M]大写_剥后透传", "glm-5.5[1M]", "glm-5.5"},
		{"真名带[1m]小写_剥后透传", "glm-5.5[1m]", "glm-5.5"},
		{"别名带[1M]_剥后映射", "claude-opus-5[1M]", "glm-5.5"},
		{"别名带[1m]_剥后映射", "claude-haiku-4-5[1m]", "glm-5-air"},
		{"未知名落default", "gpt-99-turbo", "glm-4.7-flash"},
		{"中段[1M]不剥_视为未知名落default", "claude-opus[1M]-5", "glm-4.7-flash"},
		{"残缺后缀[1m无闭括号_不剥_落default", "glm-5.5[1m", "glm-4.7-flash"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"model": tc.model, "max_tokens": 16})
			out := rewriteOK(t, body, fixtureCfg())
			if out.ModelIn != tc.model {
				t.Errorf("ModelIn = %q, want 入参原值 %q", out.ModelIn, tc.model)
			}
			if out.ModelOut != tc.wantOut {
				t.Errorf("ModelOut = %q, want %q", out.ModelOut, tc.wantOut)
			}
			if m, _ := parseWithUseNumber(t, out.Body)["model"].(string); m != tc.wantOut {
				t.Errorf("body.model = %q, want %q", m, tc.wantOut)
			}
		})
	}
}

// TestRewriteImageDegradation 图片降级逐场景：命中与否取决于**映射后**模型
// 是否在 text_only 名单；只换 image 块为固定占位，其余块与纯字符串 content
// 一概不动。
func TestRewriteImageDegradation(t *testing.T) {
	placeholder := map[string]any{"type": "text", "text": "[image omitted: text-only model]"}

	t.Run("text_only模型_混合块只换image", func(t *testing.T) {
		// 请求里是别名 claude-fable-5-1，TextOnly 放的是映射值 glm-5.3——
		// 降级仍触发即钉死「按映射后模型匹配」。
		body, _ := json.Marshal(map[string]any{
			"model":    "claude-fable-5-1",
			"messages": []any{msgWithBlocks(textBlock("看图"), imageBlock(), textBlock("描述它"))},
		})
		out := rewriteOK(t, body, fixtureCfg())
		blocks := parseWithUseNumber(t, out.Body)["messages"].([]any)[0].(map[string]any)["content"].([]any)
		if len(blocks) != 3 {
			t.Fatalf("块数 = %d, want 3（只替换不删减）", len(blocks))
		}
		if !reflect.DeepEqual(blocks[0], textBlock("看图")) {
			t.Errorf("块0 被改动: %v", blocks[0])
		}
		if !reflect.DeepEqual(blocks[1], placeholder) {
			t.Errorf("块1 = %v, want 固定占位 %+v", blocks[1], placeholder)
		}
		if !reflect.DeepEqual(blocks[2], textBlock("描述它")) {
			t.Errorf("块2 被改动: %v", blocks[2])
		}
	})

	t.Run("非text_only模型_image不动", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"model":    "claude-opus-5", // → glm-5.5，不在 TextOnly
			"messages": []any{msgWithBlocks(textBlock("看图"), imageBlock())},
		})
		want := parseWithUseNumber(t, body)["messages"]
		got := parseWithUseNumber(t, rewriteOK(t, body, fixtureCfg()).Body)["messages"]
		if !reflect.DeepEqual(got, want) {
			t.Errorf("非 text_only 模型的 messages 被改动:\n want %v\n got  %v", want, got)
		}
	})

	t.Run("text_only模型_无image块不动", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"model":    "claude-fable-5-1",
			"messages": []any{msgWithBlocks(textBlock("纯文本"), textBlock("两段"))},
		})
		want := parseWithUseNumber(t, body)["messages"]
		got := parseWithUseNumber(t, rewriteOK(t, body, fixtureCfg()).Body)["messages"]
		if !reflect.DeepEqual(got, want) {
			t.Errorf("无 image 块时 messages 被改动:\n want %v\n got  %v", want, got)
		}
	})

	t.Run("content为纯字符串不动", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"model":    "claude-fable-5-1",
			"messages": []any{map[string]any{"role": "user", "content": "描述这张图"}},
		})
		want := parseWithUseNumber(t, body)["messages"]
		got := parseWithUseNumber(t, rewriteOK(t, body, fixtureCfg()).Body)["messages"]
		if !reflect.DeepEqual(got, want) {
			t.Errorf("纯字符串 content 被改动:\n want %v\n got  %v", want, got)
		}
	})

	t.Run("多消息只换含image那条", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"model": "claude-fable-5-1",
			"messages": []any{
				map[string]any{"role": "user", "content": "先说背景"},
				msgWithBlocks(imageBlock(), textBlock("这是截图")),
			},
		})
		gotMsgs := parseWithUseNumber(t, rewriteOK(t, body, fixtureCfg()).Body)["messages"].([]any)
		if c, _ := gotMsgs[0].(map[string]any)["content"].(string); c != "先说背景" {
			t.Errorf("消息0（纯字符串）被改动: %v", gotMsgs[0])
		}
		blocks := gotMsgs[1].(map[string]any)["content"].([]any)
		if !reflect.DeepEqual(blocks[0], placeholder) {
			t.Errorf("消息1 块0 = %v, want 占位", blocks[0])
		}
		if !reflect.DeepEqual(blocks[1], textBlock("这是截图")) {
			t.Errorf("消息1 块1 被改动: %v", blocks[1])
		}
	})

	t.Run("缺messages键_降级路径不报错", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"model": "claude-fable-5-1"})
		out := rewriteOK(t, body, fixtureCfg()) // 不应 panic/报错
		if _, exists := parseWithUseNumber(t, out.Body)["messages"]; exists {
			t.Errorf("改写器凭空造出了 messages 键")
		}
	})
}

// TestRewriteTopLevelFidelity 保真钉死：除 model/messages（降级时）外，其余
// 顶层键解析后逐键 DeepEqual——中文/emoji/HTML 字符/嵌套结构/大整数精度全过。
func TestRewriteTopLevelFidelity(t *testing.T) {
	raw := []byte(`{
		"model": "claude-opus-5",
		"max_tokens": 8192,
		"big_int": 9007199254740993,
		"stream": true,
		"thinking": {"type": "enabled", "budget_tokens": 4096},
		"metadata": {"session_id": "sess-中文🌲-001", "user_id": "u_01"},
		"system": [{"type": "text", "text": "你是<Coder> & 助手🚀"}],
		"tools": [{"name": "bash", "input_schema": {"type": "object", "properties": {}}}],
		"output_config": {"format": "text"},
		"context_management": {"edits": []},
		"messages": [{"role": "user", "content": [{"type": "text", "text": "你好<世界>&🌲"}]}]
	}`)
	out := rewriteOK(t, raw, fixtureCfg()) // → glm-5.5，不在 TextOnly，messages 不该被碰
	want := parseWithUseNumber(t, raw)
	got := parseWithUseNumber(t, out.Body)

	if len(got) != len(want) {
		t.Fatalf("顶层键数 = %d, want %d（不许新增/丢失键）", len(got), len(want))
	}
	for k, wantV := range want {
		if k == "model" {
			if m, _ := got["model"].(string); m != "glm-5.5" {
				t.Errorf("model = %v, want glm-5.5", got["model"])
			}
			continue
		}
		if !reflect.DeepEqual(wantV, got[k]) {
			t.Errorf("顶层键 %q 被改动:\n want %v\n got  %v", k, wantV, got[k])
		}
	}

	// 大整数精度钉子：float64 解析路径会损坏 2^53 以上整数，UseNumber 才保真。
	if n, _ := got["big_int"].(json.Number); n.String() != "9007199254740993" {
		t.Errorf("big_int = %v, want 字面量 9007199254740993（精度丢失）", got["big_int"])
	}
	// 中文/emoji 原样、<>& 不转义（SetEscapeHTML(false) 的字节级证据）。
	if !bytes.Contains(out.Body, []byte("你是<Coder> & 助手🚀")) {
		t.Errorf("system 文本含转义/乱码: %s", out.Body)
	}
}

// TestRewriteFidelityWithImageDegradation 降级路径的保真：messages 里只有
// image 块被换，metadata.session_id 等其余键不受影响。
func TestRewriteFidelityWithImageDegradation(t *testing.T) {
	raw := []byte(`{
		"model": "claude-fable-5-1",
		"max_tokens": 4096,
		"metadata": {"session_id": "sess-会话🌲-002", "user_id": "u_02"},
		"system": "系统提示<含>&符号",
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "看这张图🌲"},
				{"type": "image", "source": {"type": "base64", "data": "AAAA"}}
			]}
		]
	}`)
	out := rewriteOK(t, raw, fixtureCfg())
	want := parseWithUseNumber(t, raw)
	got := parseWithUseNumber(t, out.Body)

	if len(got) != len(want) {
		t.Fatalf("顶层键数 = %d, want %d", len(got), len(want))
	}
	for k, wantV := range want {
		switch k {
		case "model":
			if m, _ := got["model"].(string); m != "glm-5.3" {
				t.Errorf("model = %v, want glm-5.3", got["model"])
			}
		case "messages":
			blocks := got["messages"].([]any)[0].(map[string]any)["content"].([]any)
			if !reflect.DeepEqual(blocks[0], textBlock("看这张图🌲")) {
				t.Errorf("text 块被改动: %v", blocks[0])
			}
			if !reflect.DeepEqual(blocks[1], map[string]any{"type": "text", "text": "[image omitted: text-only model]"}) {
				t.Errorf("image 块未换为固定占位: %v", blocks[1])
			}
		default:
			if !reflect.DeepEqual(wantV, got[k]) {
				t.Errorf("顶层键 %q 受降级牵连被改动:\n want %v\n got  %v", k, wantV, got[k])
			}
		}
	}
	// metadata.session_id 显式再钉一次（票面点名）。
	md, _ := got["metadata"].(map[string]any)
	if sid, _ := md["session_id"].(string); sid != "sess-会话🌲-002" {
		t.Errorf("metadata.session_id = %q, want 原值", sid)
	}
}

// TestRewriteIllegalBody 非法 body（非 JSON／非 object／缺 model／model 非字
// 符串）→ 错误返回；错误处理归接线方（票06），本件只保证报错。
func TestRewriteIllegalBody(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"非JSON文本", "this is not json"},
		{"空体", ""},
		{"JSON数组", `[{"model": "x"}]`},
		{"JSON字符串", `"hello"`},
		{"JSON数字", "42"},
		{"JSON null", "null"},
		{"缺model字段", `{"max_tokens": 10}`},
		{"model非字符串", `{"model": 42}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Rewrite([]byte(tc.body), fixtureCfg()); err == nil {
				t.Fatalf("want 错误, got nil")
			}
		})
	}
}

// TestRewriteDegenerateConfigNeverFabricatesModel 防御钉子：model_map/default
// 未配置（票06 校验前的退化输入）时宁可不映射，绝不凭空造 model="" 打向上游。
func TestRewriteDegenerateConfigNeverFabricatesModel(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"model": "whatever-v9", "max_tokens": 1})
	out := rewriteOK(t, body, RewriteConfig{})
	if out.ModelOut != "whatever-v9" {
		t.Errorf("空配置下 ModelOut = %q, want 原样 %q", out.ModelOut, "whatever-v9")
	}
}
