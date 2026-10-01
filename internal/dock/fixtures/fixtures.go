// Package fixtures — 票04：回放夹具与加载器（对照表 §1/§2 语义的四形合成
// 数据：正常回复/工具调用/长流/错误注入）。
//
// 夹具纪律（spec Testing Decisions/F3）：全部为脱敏或重构的合成数据，鉴权
// 头一律占位；加载任何夹具前强制过"零真实密钥"扫描——任何形如真实密钥的
// 串（sk- 长体、GitHub/AWS/Google/Slack token、Bearer 长令牌）拒绝加载，
// 合成占位只认 sk-test-* 前缀与显式占位串（票面纪律：假钥一律 sk-test-*
// 或占位）。
//
// 每个夹具同时携带两分支假上游的回放数据：
//   - anthropic_upstream：Anthropic 方言假上游（翻译分支）照此回放；
//   - responses_upstream：responses 方言假上游（原生透传分支）照此回放。
package fixtures

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// SSEEvent 单个 SSE 事件：event 名 + 原始 data 字节（RawMessage 保真，
// 回放时逐字节出——不重编码，防键序漂移污染字节级断言）。
type SSEEvent struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// UpstreamReply 假上游回放脚本：状态码 + Content-Type + SSE 事件序（SSE 形）
// 或整体 body（非 SSE 形，如原生分支错误注入的 JSON 错误体）。
type UpstreamReply struct {
	Status      int        `json:"status"`
	ContentType string     `json:"content_type"`
	SSE         []SSEEvent `json:"sse,omitempty"`
	Body        string     `json:"body,omitempty"`
}

// Fixture 四形夹具：codex 侧 responses 请求 + 两分支上游回放数据。
type Fixture struct {
	Name              string         `json:"name"`
	Description       string         `json:"description"`
	CodexRequest      map[string]any `json:"codex_request"`
	AnthropicUpstream *UpstreamReply `json:"anthropic_upstream"`
	ResponsesUpstream *UpstreamReply `json:"responses_upstream"`
}

// Names 四形夹具规范名（顺序即票面顺序）。
var Names = []string{"normal_reply", "tool_call", "long_stream", "error_injection"}

// Dir 夹具目录（本包源码所在目录；测试与重录工具同源取数）。
func Dir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	return filepath.Dir(file)
}

// Load 按名加载夹具（加载即过零真实密钥扫描——违规拒绝加载）。
func Load(name string) (*Fixture, error) {
	path := filepath.Join(Dir(), name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("fixtures: 读夹具 %s: %w", name, err)
	}
	return parse(name, data)
}

// LoadAll 四形全量加载（顺序同 Names）。
func LoadAll() ([]*Fixture, error) {
	var out []*Fixture
	for _, n := range Names {
		f, err := Load(n)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// LoadDir 扫描加载目录下全部 .json 夹具（每文件先过零真实密钥扫描）；
// 供扫描负例（临时目录注检）与重录工具脱敏门禁复核复用。
func LoadDir(dir string) ([]*Fixture, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []*Fixture
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		if err := ScanForRealKeys(data); err != nil {
			return nil, fmt.Errorf("fixtures: %s: %w", e.Name(), err)
		}
		f, err := parse(e.Name(), data)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// parse 单夹具解析：先扫密钥（违规即拒），再归一 SSE data、验结构完整性。
func parse(name string, data []byte) (*Fixture, error) {
	if err := ScanForRealKeys(data); err != nil {
		return nil, fmt.Errorf("fixtures: %s: %w", name, err)
	}
	var f Fixture
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("fixtures: %s: %w", name, err)
	}
	if f.Name == "" {
		f.Name = strings.TrimSuffix(name, ".json")
	}
	if f.CodexRequest == nil || f.AnthropicUpstream == nil || f.ResponsesUpstream == nil {
		return nil, fmt.Errorf("fixtures: %s: 缺 codex_request/anthropic_upstream/responses_upstream 之一", f.Name)
	}
	for _, pair := range []struct {
		label string
		up    *UpstreamReply
	}{{"anthropic_upstream", f.AnthropicUpstream}, {"responses_upstream", f.ResponsesUpstream}} {
		for i, ev := range pair.up.SSE {
			inner, err := decodeSSEData(ev.Data)
			if err != nil {
				return nil, fmt.Errorf("fixtures: %s: %s 第 %d 个事件 data: %w", f.Name, pair.label, i, err)
			}
			pair.up.SSE[i].Data = inner
		}
	}
	return &f, nil
}

// decodeSSEData 事件 data 归一：夹具文件里 data 写成 JSON 字符串（内容＝
// 事件 JSON 文本，SSE 单行友好）时解码为内层字节；已是内嵌 JSON 形则原样。
// 加载后 SSEEvent.Data 恒为事件 JSON 字节，回放/断言同源。
func decodeSSEData(raw json.RawMessage) (json.RawMessage, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if !json.Valid([]byte(s)) {
			return nil, fmt.Errorf("事件 JSON 文本无效")
		}
		return json.RawMessage(s), nil
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("事件 data 既非 JSON 字符串也非合法 JSON")
	}
	return raw, nil
}

// ---- 零真实密钥扫描（F3） ----

// keyRule 一类真形密钥规则：正则认形状；匹配串以 allowed 前缀开头＝显式
// 合成占位，放行。规则对夹具目录全文件生效。
type keyRule struct {
	name     string
	re       *regexp.Regexp
	allowed  []string
	bearerOf string // 非空＝匹配串先剥该前缀（如 "Bearer"）再做占位判定
}

var keyRules = []keyRule{
	{
		name:    "sk/pk/rk 密钥形",
		re:      regexp.MustCompile(`\b(?:sk|pk|rk)-[A-Za-z0-9][A-Za-z0-9_-]{15,}`),
		allowed: []string{"sk-test-", "codex-placeholder", "fixture-placeholder", "placeholder"},
	},
	{
		name: "GitHub token 形",
		re:   regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs)_[A-Za-z0-9]{20,}`),
	},
	{
		name: "AWS AccessKey 形",
		re:   regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	},
	{
		name: "Google API key 形",
		re:   regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{30,}`),
	},
	{
		name: "Slack token 形",
		re:   regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}`),
	},
	{
		name:     "Bearer 长令牌形",
		re:       regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9+/_=.-]{24,}`),
		bearerOf: "Bearer",
		allowed:  []string{"sk-test-", "codex-placeholder", "fixture-placeholder", "placeholder"},
	},
}

// ScanForRealKeys 零真实密钥扫描：命中任一真形规则且非合成占位即拒。
// 错误信息只露匹配串首段（脱敏预览），不复述全串。
func ScanForRealKeys(data []byte) error {
	var violations []string
	text := string(data)
	for _, r := range keyRules {
		for _, loc := range r.re.FindAllStringIndex(text, -1) {
			m := text[loc[0]:loc[1]]
			if r.isAllowedPlaceholder(m) {
				continue
			}
			violations = append(violations, fmt.Sprintf("%s: %s…（%d 字符）", r.name, preview(m), len(m)))
		}
	}
	if len(violations) > 0 {
		return fmt.Errorf("夹具疑似含真实形密钥（合成占位只认 sk-test-* 前缀与显式占位串）: %s",
			strings.Join(violations, "; "))
	}
	return nil
}

// isAllowedPlaceholder 匹配串是否显式合成占位（Bearer 形先剥动词再判）。
func (r keyRule) isAllowedPlaceholder(m string) bool {
	token := m
	if r.bearerOf != "" {
		rest, ok := stripTokenVerb(m, r.bearerOf)
		if !ok {
			return false
		}
		token = rest
	}
	for _, p := range r.allowed {
		if strings.HasPrefix(token, p) {
			return true
		}
	}
	return false
}

// stripTokenVerb 大小写不敏感剥动词前缀（"Bearer xxx" → "xxx"）。
func stripTokenVerb(m, verb string) (string, bool) {
	if len(m) < len(verb) {
		return "", false
	}
	if !strings.EqualFold(m[:len(verb)], verb) {
		return "", false
	}
	return strings.TrimLeft(m[len(verb):], " \t"), true
}

// preview 脱敏预览：只露首 8 字符。
func preview(m string) string {
	r := []rune(m)
	if len(r) > 8 {
		r = r[:8]
	}
	return string(r)
}
