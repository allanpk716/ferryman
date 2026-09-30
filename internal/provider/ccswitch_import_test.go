// ccswitch_import_test.go — 票06：`provider import-ccswitch` 的库读取与映射
// 验收钉子。
//
// 验收口径：
//   - 全程只触 testdata 假库（modernc.org/sqlite 现建 schema+假密钥 sk-test-*），
//     绝不读真 ~/.cc-switch（路径全参数化）；
//   - claude 类 → dialect=anthropic；模型位从 ANTHROPIC_MODEL/…_OPUS/…_SONNET/
//     …_HAIKU（haiku 缺则 SMALL_FAST 兜）映射；密钥取 AUTH_TOKEN（API_KEY 兜）；
//   - codex 类按端点线协议推 dialect（anthropic 端点→需翻译；/v1 或
//     wire_api=responses→openai_responses；推不出→跳过并报因）；
//   - 范围外 app_type 跳过并报告；settings_config 非 JSON 跳过；
//   - 非本地端点缺 default 模型位 → 跳过（写进表会让守护拒启）。
package provider

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"ferryman/internal/config"
)

// writeFakeDB 现建假 cc-switch 库（列名与真库 providers 表同形，只取映射所需
// 子集；密钥全是 sk-test-* 假钥）。rows 元素 = {name, appType, settingsConfig}。
func writeFakeDB(t *testing.T, rows [][3]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cc-switch.db")
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE providers (
		id TEXT NOT NULL,
		app_type TEXT NOT NULL,
		name TEXT NOT NULL,
		settings_config TEXT NOT NULL,
		website_url TEXT,
		category TEXT,
		is_current BOOLEAN NOT NULL DEFAULT 0,
		PRIMARY KEY (id, app_type)
	)`); err != nil {
		t.Fatal(err)
	}
	for i, r := range rows {
		if _, err := db.Exec(
			`INSERT INTO providers (id, app_type, name, settings_config) VALUES (?, ?, ?, ?)`,
			fmt.Sprintf("id-%d", i), r[1], r[0], r[2]); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// claudeCfg claude 类 settings_config JSON 底稿。
func claudeCfg(baseURL, token string, env map[string]string) string {
	m := map[string]any{"env": map[string]any{}}
	e := m["env"].(map[string]any)
	if baseURL != "" {
		e["ANTHROPIC_BASE_URL"] = baseURL
	}
	if token != "" {
		e["ANTHROPIC_AUTH_TOKEN"] = token
	}
	for k, v := range env {
		e[k] = v
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// codexCfg codex 类 settings_config JSON 底稿（config 列 = config.toml 文本）。
func codexCfg(key, configTOML string) string {
	b, _ := json.Marshal(map[string]any{
		"auth":   map[string]any{"OPENAI_API_KEY": key},
		"config": configTOML,
	})
	return string(b)
}

func codexTOML(baseURL, wireAPI, model string) string {
	var sb strings.Builder
	sb.WriteString("model_provider = \"relay\"\n")
	if model != "" {
		sb.WriteString("model = \"" + model + "\"\n")
	}
	sb.WriteString("\n[model_providers.relay]\nname = \"Relay\"\nbase_url = \"" + baseURL + "\"\n")
	if wireAPI != "" {
		sb.WriteString("wire_api = \"" + wireAPI + "\"\n")
	}
	return sb.String()
}

func TestReadCCSwitchDBMissingFileErrors(t *testing.T) {
	p := filepath.Join(t.TempDir(), "absent.db")
	if _, err := ReadCCSwitchDB(p); err == nil {
		t.Fatal("库不存在应报错（CLI 层给 --db 指引）")
	}
}

func TestImportCCSwitchClaudeFullMapping(t *testing.T) {
	db := writeFakeDB(t, [][3]string{{"智谱", "claude", claudeCfg(
		"https://open.bigmodel.cn/api/anthropic", "sk-test-zhipu-1234abcd",
		map[string]string{
			"ANTHROPIC_MODEL":                "glm-5.3",
			"ANTHROPIC_DEFAULT_OPUS_MODEL":   "glm-5.3",
			"ANTHROPIC_DEFAULT_SONNET_MODEL": "glm-5.3-air",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "glm-5.3-flash",
		})}})
	cands, err := ImportCCSwitch(db)
	if err != nil {
		t.Fatalf("ImportCCSwitch: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("候选数 = %d, want 1: %+v", len(cands), cands)
	}
	c := cands[0]
	if c.Skip {
		t.Fatalf("完整 claude 条目不应跳过: %s", c.Reason)
	}
	if c.Name != "智谱" || c.AppType != "claude" {
		t.Errorf("名/类 = %q/%q, want 智谱/claude", c.Name, c.AppType)
	}
	up := c.Up
	if up.BaseURL != "https://open.bigmodel.cn/api/anthropic" {
		t.Errorf("base_url = %q", up.BaseURL)
	}
	if up.APIKey != "sk-test-zhipu-1234abcd" {
		t.Errorf("密钥映射不符: %q", up.APIKey)
	}
	if up.Dialect != config.DialectAnthropic {
		t.Errorf("claude 类 dialect 应为 anthropic, got %q", up.Dialect)
	}
	for k, want := range map[string]string{
		"default": "glm-5.3", "opus": "glm-5.3", "sonnet": "glm-5.3-air", "haiku": "glm-5.3-flash",
	} {
		if up.ModelMap[k] != want {
			t.Errorf("model_map[%s] = %q, want %q", k, up.ModelMap[k], want)
		}
	}
}

func TestImportCCSwitchClaudeSmallFastFallbackAndAPIKeyFallback(t *testing.T) {
	db := writeFakeDB(t, [][3]string{{"只配了small", "claude", claudeCfg(
		"https://api.kimi.com/coding/", "",
		map[string]string{
			"ANTHROPIC_MODEL":            "kimi-for-coding",
			"ANTHROPIC_SMALL_FAST_MODEL": "k3-256k",
			"ANTHROPIC_API_KEY":          "sk-test-apifallback-5678",
		})}})
	cands, err := ImportCCSwitch(db)
	if err != nil {
		t.Fatalf("ImportCCSwitch: %v", err)
	}
	if len(cands) != 1 || cands[0].Skip {
		t.Fatalf("候选 = %+v", cands)
	}
	up := cands[0].Up
	if up.APIKey != "sk-test-apifallback-5678" {
		t.Errorf("AUTH_TOKEN 缺失应兜 ANTHROPIC_API_KEY, got %q", up.APIKey)
	}
	if up.ModelMap["haiku"] != "k3-256k" {
		t.Errorf("haiku 应兜 SMALL_FAST, got %q", up.ModelMap["haiku"])
	}
	if up.ModelMap["sonnet"] != "" {
		t.Errorf("sonnet 未显式配置不得凭空造值（映射只收显式键）, got %q", up.ModelMap["sonnet"])
	}
}

func TestImportCCSwitchCodexRowsDialectInference(t *testing.T) {
	db := writeFakeDB(t, [][3]string{
		{"中转responses", "codex", codexCfg("sk-test-codex-resp",
			codexTOML("https://relay.example.com/v1", "", "gpt-x"))},
		{"智谱翻译", "codex", codexCfg("sk-test-codex-glm",
			codexTOML("https://open.bigmodel.cn/api/anthropic", "", "glm-5.3"))},
	})
	cands, err := ImportCCSwitch(db)
	if err != nil {
		t.Fatalf("ImportCCSwitch: %v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("候选数 = %d, want 2", len(cands))
	}
	byName := map[string]CCSwitchCandidate{}
	for _, c := range cands {
		byName[c.Name] = c
	}
	rc := byName["中转responses"]
	if rc.Skip {
		t.Fatalf("responses 端点不应跳过: %s", rc.Reason)
	}
	if rc.Up.Dialect != config.DialectOpenAIResponses {
		t.Errorf("/v1 端点应推 openai_responses, got %q", rc.Up.Dialect)
	}
	if rc.Up.CodexAvailability() != config.CodexNative {
		t.Errorf("openai_responses 应推导原生透传, got %q", rc.Up.CodexAvailability())
	}
	if rc.Up.ModelMap["codex"] != "gpt-x" || rc.Up.ModelMap["default"] != "gpt-x" {
		t.Errorf("codex 条目模型位不符: %+v", rc.Up.ModelMap)
	}
	gc := byName["智谱翻译"]
	if gc.Skip {
		t.Fatalf("anthropic 端点不应跳过: %s", gc.Reason)
	}
	if gc.Up.Dialect != config.DialectAnthropic {
		t.Errorf("anthropic 端点应推 anthropic, got %q", gc.Up.Dialect)
	}
	if gc.Up.CodexAvailability() != config.CodexTranslation {
		t.Errorf("anthropic 方言应推导需翻译, got %q", gc.Up.CodexAvailability())
	}
	if gc.Up.APIKey != "sk-test-codex-glm" {
		t.Errorf("codex 密钥应取 auth.OPENAI_API_KEY, got %q", gc.Up.APIKey)
	}
}

func TestImportCCSwitchSkipPathsReported(t *testing.T) {
	db := writeFakeDB(t, [][3]string{
		{"缺模型", "claude", claudeCfg("https://a.example/anthropic", "sk-test-nomodel", nil)},
		{"坏JSON", "claude", "{not-json"},
		{"gemini条目", "gemini", "{}"},
		{"推不出", "codex", codexCfg("sk-test-uninf",
			codexTOML("https://relay.example.com/api/whatever", "", "m1"))},
		{"缺model键", "codex", codexCfg("sk-test-nomodelkey",
			codexTOML("https://relay.example.com/v1", "", ""))},
		{"缺base_url", "claude", claudeCfg("", "sk-test-nobase", map[string]string{"ANTHROPIC_MODEL": "m"})},
	})
	cands, err := ImportCCSwitch(db)
	if err != nil {
		t.Fatalf("ImportCCSwitch: %v", err)
	}
	if len(cands) != 6 {
		t.Fatalf("候选数 = %d, want 6（含跳过条目）", len(cands))
	}
	byName := map[string]CCSwitchCandidate{}
	for _, c := range cands {
		byName[c.Name] = c
	}
	cases := map[string]string{
		"缺模型":       "ANTHROPIC_MODEL",
		"坏JSON":     "JSON",
		"gemini条目":  "范围外",
		"推不出":       "推线协议",
		"缺model键":   "model",
		"缺base_url": "ANTHROPIC_BASE_URL",
	}
	for name, wantSub := range cases {
		c, ok := byName[name]
		if !ok {
			t.Errorf("缺候选 %q: %+v", name, cands)
			continue
		}
		if !c.Skip {
			t.Errorf("%q 应跳过", name)
			continue
		}
		if !strings.Contains(c.Reason, wantSub) {
			t.Errorf("%q 跳过原因应含 %q: %s", name, wantSub, c.Reason)
		}
	}
	// 跳过条目的 Up 不得带半成品密钥进报告输出面（T39：候选密钥只在映射值里，
	// 跳过行只用 reason 文本）。
	for _, c := range cands {
		if c.Skip && c.Up.APIKey != "" {
			t.Errorf("跳过条目 %q 不应携带密钥值", c.Name)
		}
	}
}

func TestImportCCSwitchLocalRelayEmptyDefaultAllowed(t *testing.T) {
	// 本地中转地址（守卫透传域）豁免 default 要求——cc-switch 自己的回退条目
	// 也能导入（14721→15721 形态）。
	db := writeFakeDB(t, [][3]string{{"cc-switch", "claude", claudeCfg(
		"http://127.0.0.1:15721", "", nil)}})
	cands, err := ImportCCSwitch(db)
	if err != nil {
		t.Fatalf("ImportCCSwitch: %v", err)
	}
	if len(cands) != 1 || cands[0].Skip {
		t.Fatalf("本地中转条目不应跳过: %+v", cands)
	}
}

func TestImportCCSwitchOrderDeterministic(t *testing.T) {
	db := writeFakeDB(t, [][3]string{
		{"zed", "claude", claudeCfg("https://z.example/anthropic", "sk-test-z",
			map[string]string{"ANTHROPIC_MODEL": "m-z"})},
		{"abc", "codex", codexCfg("sk-test-a", codexTOML("https://a.example/v1", "", "m-a"))},
		{"中央", "claude", claudeCfg("https://c.example/anthropic", "sk-test-c",
			map[string]string{"ANTHROPIC_MODEL": "m-c"})},
	})
	cands, err := ImportCCSwitch(db)
	if err != nil {
		t.Fatalf("ImportCCSwitch: %v", err)
	}
	var got []string
	for _, c := range cands {
		got = append(got, c.AppType+"/"+c.Name)
	}
	want := []string{"claude/zed", "claude/中央", "codex/abc"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("排序 = %v, want %v（app_type 再 name 字典序）", got, want)
	}
}

func TestInferCodexDialectTable(t *testing.T) {
	cases := []struct {
		name      string
		toml      string
		wantDial  string
		wantBase  string
		wantModel string
		wantOK    bool
	}{
		{"v1 后缀", codexTOML("https://api.openai.com/v1", "", "gpt-5"), config.DialectOpenAIResponses, "https://api.openai.com/v1", "gpt-5", true},
		{"v1 带尾斜杠", codexTOML("https://api.openai.com/v1/", "", "g"), config.DialectOpenAIResponses, "https://api.openai.com/v1/", "g", true},
		{"wire_api 显式", codexTOML("https://relay.example.net", "responses", "m"), config.DialectOpenAIResponses, "https://relay.example.net", "m", true},
		{"anthropic 端点优先", codexTOML("https://open.bigmodel.cn/api/anthropic", "responses", "glm"), config.DialectAnthropic, "https://open.bigmodel.cn/api/anthropic", "glm", true},
		{"推不出", codexTOML("https://relay.example.net/api/weird", "", "m"), "", "https://relay.example.net/api/weird", "m", false},
		{"缺 model_provider", "model = \"m\"\n", "", "", "", false},
		{"缺 active 表", "model_provider = \"ghost\"\n", "", "", "", false},
		{"缺 base_url", "model_provider = \"r\"\n\n[model_providers.r]\nname = \"R\"\n", "", "", "", false},
		{"非 TOML", "{{{", "", "", "", false},
	}
	for _, tc := range cases {
		dialect, base, model, ok := InferCodexDialect(tc.toml)
		if ok != tc.wantOK || dialect != tc.wantDial || base != tc.wantBase || model != tc.wantModel {
			t.Errorf("%s: got (%q,%q,%q,%v), want (%q,%q,%q,%v)",
				tc.name, dialect, base, model, ok, tc.wantDial, tc.wantBase, tc.wantModel, tc.wantOK)
		}
	}
}

func TestImportCCSwitchEmptyDBNoCandidates(t *testing.T) {
	db := writeFakeDB(t, nil)
	cands, err := ImportCCSwitch(db)
	if err != nil {
		t.Fatalf("ImportCCSwitch: %v", err)
	}
	if len(cands) != 0 {
		t.Errorf("空库应零候选, got %+v", cands)
	}
	if _, err := os.Stat(db); err != nil {
		t.Errorf("只读导入不得动库文件: %v", err)
	}
}
