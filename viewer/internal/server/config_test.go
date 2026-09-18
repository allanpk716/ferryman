// config_test.go——/api/config：脱敏、拍平、兜底三件事不许回归。
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cfgBody 解码 /api/config 响应的宽松结构（字段按需取）。
type cfgBody struct {
	OK       bool         `json:"ok"`
	Found    bool         `json:"found"`
	Path     string       `json:"path"`
	Note     string       `json:"note"`
	Sections []cfgSection `json:"sections"`
}

func getCfg(t *testing.T, s *Server) cfgBody {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var b cfgBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return b
}

func TestConfigRedactsSecretLookingKeys(t *testing.T) {
	// 数据根/accounts 布局 + 密钥样例：api_key 必须脱敏，普通键照常展示。
	root := t.TempDir()
	acc := filepath.Join(root, "accounts")
	if err := os.MkdirAll(acc, 0o755); err != nil {
		t.Fatal(err)
	}
	toml := `mode = "enforce"
[thresholds]
summarize_s = 900
block_s = 2100
min_ctx_tokens = 100000
[provider.glm]
api_key = "sk-real-secret-should-never-leak"
token = "tok-bare-secret"
base_url = "https://open.bigmodel.cn/api/paas/v4"
`
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	b := getCfg(t, New(acc))

	if !b.OK || !b.Found {
		t.Fatalf("ok=%v found=%v note=%s", b.OK, b.Found, b.Note)
	}
	if b.Path != filepath.Join(root, "config.toml") {
		t.Fatalf("path = %s", b.Path)
	}
	var sawAPIKey, sawBaseURL, sawBlock, sawMinCtx bool
	for _, sec := range b.Sections {
		for _, r := range sec.Rows {
			switch {
			case r.Key == "api_key" || r.Key == "token":
				sawAPIKey = true
				if strings.Contains(r.Value, "should-never-leak") || strings.Contains(r.Value, "tok-bare") || !r.Redact {
					t.Fatalf("密钥泄漏或未标记: %+v", r)
				}
			case r.Key == "base_url":
				sawBaseURL = true
				if r.Value != "https://open.bigmodel.cn/api/paas/v4" {
					t.Fatalf("base_url 被误脱敏: %s", r.Value)
				}
			case r.Key == "block_s":
				sawBlock = r.Value == "2100"
			case r.Key == "min_ctx_tokens":
				// 数量词不是密钥：末段 tokens（复数）不得脱敏
				sawMinCtx = true
				if r.Redact || r.Value != "100000" {
					t.Fatalf("min_ctx_tokens 被误脱敏: %+v", r)
				}
			}
		}
	}
	if !sawAPIKey || !sawBaseURL || !sawBlock || !sawMinCtx {
		t.Fatalf("缺行: apikey=%v base_url=%v block=%v min_ctx=%v", sawAPIKey, sawBaseURL, sawBlock, sawMinCtx)
	}
}

func TestConfigNotFoundHonest(t *testing.T) {
	// 没有配置文件：200 + found=false + 说明，不编造段。
	b := getCfg(t, New(t.TempDir()))
	if b.OK || b.Found {
		t.Fatalf("应为 found=false: %+v", b)
	}
	if b.Note == "" || len(b.Sections) != 0 {
		t.Fatalf("note=%q sections=%d", b.Note, len(b.Sections))
	}
}

func TestConfigParseErrorHonest(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("not [ valid toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := getCfg(t, New(dir))
	if b.OK || !b.Found || !strings.Contains(b.Note, "解析失败") {
		t.Fatalf("应如实报解析失败: %+v", b)
	}
}

func TestConfigNestedTableFlattened(t *testing.T) {
	// 嵌套表拍平：[prices.glm] → 段 "prices.glm"，不丢不叠。
	dir := t.TempDir()
	toml := "[prices.glm]\np_in = 6.9\np_out = 24\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	b := getCfg(t, New(dir))
	if len(b.Sections) != 1 || b.Sections[0].Name != "prices.glm" || len(b.Sections[0].Rows) != 2 {
		t.Fatalf("拍平结果不对: %+v", b.Sections)
	}
}
