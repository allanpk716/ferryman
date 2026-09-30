// codexjson.go — 票03：codex 翻译车道共用的 JSON 工具件。
//
// 口径与 rewrite.go 同源：UseNumber 解析（数字按字面量保留）＋
// SetEscapeHTML(false) 重编码（CC/codex 请求体常含代码片段，不做 <>& 多余
// 转义）。Go 的 encoding/json 对 map 键排序输出（语言规范保证）＝对照表
// §1.6「键序规范化」的免费实现；canonicalJSONString 再叠加无空白紧凑形，
// 与参考实现 json_canonical.rs 的 canonical_json_string 语义一致（对象键
// 排序、数组保序、字符串标准转义）。
package dock

import (
	"bytes"
	"encoding/json"
	"strings"
)

// decodeJSONObject UseNumber 解析顶层 JSON object；非 object/非法 JSON 返回
// nil（调用方按分支处置，不在此报错）。
func decodeJSONObject(body []byte) map[string]any {
	var root any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return nil
	}
	m, _ := root.(map[string]any)
	return m
}

// decodeJSONValue UseNumber 解析任意 JSON 值（顶层可为数组/标量）。
func decodeJSONValue(body []byte) (any, bool) {
	var root any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return nil, false
	}
	return root, true
}

// encodeCompact 紧凑编码（无 HTML 转义、键排序、无尾随换行）。
func encodeCompact(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil // map/slice/string/number 组成的值不会失败；兜底 nil 由调用方防御
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// canonicalJSONString 规范化 JSON 文本（对照表 §1.2：未知 part 以规范化 JSON
// 文本块保底；工具 arguments 收口键序规范）。Go map 键序天然确定（排序），
// 与参考实现 canonical_json_string 等价。
func canonicalJSONString(v any) string {
	return string(encodeCompact(v))
}

// canonicalizeJSONStringIfParseable 字符串能解析成 JSON 就规范化回写，否则
// 原样返回（工具 arguments 流式拼接的收口路径）。
func canonicalizeJSONStringIfParseable(s string) string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return s
	}
	v, ok := decodeJSONValue([]byte(trimmed))
	if !ok {
		return s
	}
	return canonicalJSONString(v)
}

// canonicalizeToolArgumentsStr 工具 arguments 字符串规范化：空/纯空白 → "{}"
// （无参调用必须序列化成 "{}"，严格上游对 "" 报 400），其余规范化键序。
func canonicalizeToolArgumentsStr(s string) string {
	if strings.TrimSpace(s) == "" {
		return "{}"
	}
	return canonicalizeJSONStringIfParseable(s)
}

// ---- 弱类型取值件（翻译层输入是外来 JSON，形状不可信，一律尽力取值） ----

func asMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

func asSlice(v any) ([]any, bool) {
	s, ok := v.([]any)
	return s, ok
}

func asStr(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

func asBool(v any) (bool, bool) {
	b, ok := v.(bool)
	return b, ok
}

// asU64 数字取值（json.Number 整数形或原生 Go 整数/可整浮点——译文内部
// 既有解码值也有实现自产的 uint64/int）。
func asU64(v any) (uint64, bool) {
	switch n := v.(type) {
	case json.Number:
		u, err := n.Int64()
		if err != nil || u < 0 {
			return 0, false
		}
		return uint64(u), true
	case uint64:
		return n, true
	case uint:
		return uint64(n), true
	case int:
		if n < 0 {
			return 0, false
		}
		return uint64(n), true
	case int64:
		if n < 0 {
			return 0, false
		}
		return uint64(n), true
	case float64:
		if n < 0 || n != float64(uint64(n)) {
			return 0, false
		}
		return uint64(n), true
	}
	return 0, false
}

// mapStr m[key] 字符串取值（缺失/非字符串=空串）。
func mapStr(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

// meaningfulText 非空白文本判定（Anthropic 对空 text 块 400，§1.2）。
func meaningfulText(s string) bool {
	return strings.TrimSpace(s) != ""
}

// stripOneMSuffix 剥模型名尾缀 [1m]（大小写不敏感，只剥后缀）——与
// rewrite.go mapModel 内联实现同语义，抽出共用。
func stripOneMSuffix(s string) string {
	const suffix = "[1m]"
	if len(s) >= len(suffix) && strings.EqualFold(s[len(s)-len(suffix):], suffix) {
		return s[:len(s)-len(suffix)]
	}
	return s
}

// hasOneMSuffix 模型名是否带 [1m] 尾标。
func hasOneMSuffix(s string) bool {
	const suffix = "[1m]"
	return len(s) >= len(suffix) && strings.EqualFold(s[len(s)-len(suffix):], suffix)
}
