// provider_test.go — 票06：`ferryman provider` 命令族验收钉子。
//
// 验收口径（票面）：
//   - list：全部条目 + active 标注 + dialect/codex 可用性（原生/需翻译/不支持）
//   - 模型位概要 + 密钥脱敏（只露尾 4 位，整钥零回显）；
//   - switch：不存在→拒绝并列可用；codex="unsupported"→默认拒绝并报因；
//     --cc-only 显式放行并明示"codex 暂断供，仅 CC"；成功回显新 active 与
//     codex 车道模式；守护不在线如实报错给拉起指引（热切换走管理口，不重启）；
//   - add/remove：密钥经参数或环境变量传入且输出永不回显全钥；remove 拒删
//     active；
//   - import-ccswitch：假库测试（modernc.org/sqlite 现建，绝不触真
//     ~/.cc-switch）；映射/dialect 推断/冲突跳过有断言；
//   - apply/--restore：参数（Targets 派生）与逐份回显齐。
//
// HTTP 层用 httptest 假管理口；进程零派生；全程不联网（loopback 除外）。
package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"ferryman/internal/config"
	"ferryman/internal/provider"
)

// providerCfgSrc list/switch/add/remove/import 共用底稿：三种 codex 可用性
// 各一条（需翻译/原生/否决），active = zhipu。
const providerCfgSrc = `
[server]
port = 7399

[dock]
listen = "127.0.0.1:15722"
active = "zhipu"

[dock.upstreams.zhipu]
base_url = "https://open.bigmodel.cn/api/anthropic"
api_key = "sk-zhipu-00001234ef12"
model_map = { default = "glm-5.3", haiku = "glm-5.3-flash", codex = "glm-5.3" }

[dock.upstreams.native]
base_url = "https://api.example.com/v1"
api_key = "sk-native-9988ccd01234"
model_map = { default = "gpt-x", codex = "gpt-x" }
dialect = "openai_responses"

[dock.upstreams.blocked]
base_url = "https://b.example/anthropic"
api_key = "sk-blocked-4433ab21"
model_map = { default = "m-b" }
codex = "unsupported"

[ferry]
provider = "deepseek"
`

func writeProviderCfg(t *testing.T, src string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func readFileProvider(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// fakeSwitchDeps switch 桩：记录调用名，回可设的响应/错误。
func fakeSwitchDeps(resp map[string]any, err error) (*providerSwitchDeps, *[]string) {
	var called []string
	return &providerSwitchDeps{
		Port:  7399,
		Token: "tok",
		Switch: func(name string) (map[string]any, error) {
			called = append(called, name)
			if err != nil {
				return nil, err
			}
			return resp, nil
		},
	}, &called
}

// ---- list ----

func TestProviderListShowsAvailabilityModelMasking(t *testing.T) {
	f := writeProviderCfg(t, providerCfgSrc)
	var buf bytes.Buffer
	if code := providerList(f, &buf); code != 0 {
		t.Fatalf("list 退出码 = %d, want 0\n%s", code, buf.String())
	}
	out := buf.String()
	for _, want := range []string{
		"zhipu", "native", "blocked", // 全部条目
		"（active）", // active 标注
		"https://open.bigmodel.cn/api/anthropic",
		"https://api.example.com/v1",
		"anthropic", "openai_responses", // dialect
		"需翻译", "原生透传", "不支持", // codex 可用性三态
		"glm-5.3", "gpt-x", // 模型位（含 codex 主模型位）
		"****ef12", "****1234", "****ab21", // 脱敏只露尾 4 位
	} {
		if !strings.Contains(out, want) {
			t.Errorf("list 输出缺 %q:\n%s", want, out)
		}
	}
	for _, secret := range []string{
		"sk-zhipu-00001234ef12", "sk-native-9988ccd01234", "sk-blocked-4433ab21",
	} {
		if strings.Contains(out, secret) {
			t.Errorf("list 泄漏整钥 %q:\n%s", secret, out)
		}
	}
}

func TestProviderListNoDock(t *testing.T) {
	f := writeProviderCfg(t, "[server]\nport = 7399\n")
	var buf bytes.Buffer
	if code := providerList(f, &buf); code != 0 {
		t.Fatalf("无 [dock] list 应退出 0:\n%s", buf.String())
	}
	if out := buf.String(); !strings.Contains(out, "[dock]") {
		t.Errorf("无 [dock] 应如实说明:\n%s", out)
	}
}

// ---- switch：拒绝路径 ----

func TestProviderSwitchUnknownEntryRejected(t *testing.T) {
	f := writeProviderCfg(t, providerCfgSrc)
	deps, called := fakeSwitchDeps(nil, nil)
	var buf bytes.Buffer
	if code := providerSwitch(f, "nope", false, &buf, deps); code == 0 {
		t.Fatal("未知条目应非零退出")
	}
	out := buf.String()
	if !strings.Contains(out, "nope") || !strings.Contains(out, "zhipu") ||
		!strings.Contains(out, "blocked") {
		t.Errorf("拒绝输出应含条目名与可用清单:\n%s", out)
	}
	if len(*called) != 0 {
		t.Fatalf("拒绝路径不得调管理口（被调 %v）", *called)
	}
	if strings.Contains(readFileProvider(t, f), `active = "nope"`) {
		t.Fatal("拒绝路径不得写配置")
	}
}

func TestProviderSwitchUnsupportedRejectedByDefault(t *testing.T) {
	f := writeProviderCfg(t, providerCfgSrc)
	deps, called := fakeSwitchDeps(nil, nil)
	var buf bytes.Buffer
	if code := providerSwitch(f, "blocked", false, &buf, deps); code == 0 {
		t.Fatal("codex=unsupported 默认应拒绝")
	}
	out := buf.String()
	for _, want := range []string{"blocked", "--cc-only", "codex"} {
		if !strings.Contains(out, want) {
			t.Errorf("拒绝输出应含 %q（报因与放行指引）:\n%s", want, out)
		}
	}
	if len(*called) != 0 {
		t.Fatalf("默认拒绝不得调管理口（被调 %v）", *called)
	}
	if strings.Contains(readFileProvider(t, f), `active = "blocked"`) {
		t.Fatal("拒绝路径不得写配置")
	}
}

// ---- switch：--cc-only 放行 ----

func TestProviderSwitchUnsupportedCCOnlyWarnsAndProceeds(t *testing.T) {
	f := writeProviderCfg(t, providerCfgSrc)
	deps, called := fakeSwitchDeps(map[string]any{
		"ok": true, "active": "blocked", "base_url": "https://b.example/anthropic",
		"dialect": "anthropic", "codex": config.CodexUnsupported,
	}, nil)
	var buf bytes.Buffer
	if code := providerSwitch(f, "blocked", true, &buf, deps); code != 0 {
		t.Fatalf("--cc-only 放行应成功:\n%s", buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "codex 暂断供") || !strings.Contains(out, "仅 CC") {
		t.Errorf("放行必须明示后果:\n%s", out)
	}
	if !strings.Contains(out, "blocked") || !strings.Contains(out, "不支持") {
		t.Errorf("成功输出应含新 active 与 codex 车道模式:\n%s", out)
	}
	if !strings.Contains(out, "不重启") {
		t.Errorf("成功输出应明示热切换不重启:\n%s", out)
	}
	if got := *called; len(got) != 1 || got[0] != "blocked" {
		t.Fatalf("管理口被调 = %v, want [blocked]", got)
	}
}

// ---- switch：成功回显 codex 车道模式 ----

func TestProviderSwitchSuccessEchoesCodexLaneModes(t *testing.T) {
	for _, tc := range []struct {
		codex    string
		wantText string
	}{
		{config.CodexTranslation, "需翻译"},
		{config.CodexNative, "原生透传"},
	} {
		f := writeProviderCfg(t, providerCfgSrc)
		deps, called := fakeSwitchDeps(map[string]any{
			"ok": true, "active": "zhipu", "base_url": "https://open.bigmodel.cn/api/anthropic",
			"dialect": "anthropic", "codex": tc.codex,
		}, nil)
		var buf bytes.Buffer
		if code := providerSwitch(f, "zhipu", false, &buf, deps); code != 0 {
			t.Fatalf("switch(%s) 退出码 = %d\n%s", tc.codex, code, buf.String())
		}
		out := buf.String()
		if !strings.Contains(out, "zhipu") || !strings.Contains(out, tc.wantText) {
			t.Errorf("codex=%s 成功输出缺 active/车道模式 %q:\n%s", tc.codex, tc.wantText, out)
		}
		if strings.Contains(out, "codex 暂断供") {
			t.Errorf("非放行路径不应出现断供告警:\n%s", out)
		}
		if got := *called; len(got) != 1 || got[0] != "zhipu" {
			t.Fatalf("管理口被调 = %v, want [zhipu]", got)
		}
	}
}

func TestProviderSwitchDaemonOfflineHonestError(t *testing.T) {
	f := writeProviderCfg(t, providerCfgSrc)
	deps, _ := fakeSwitchDeps(nil, errors.New("守护不在线（dial 127.0.0.1:7399: connect: connection refused）"))
	var buf bytes.Buffer
	if code := providerSwitch(f, "zhipu", false, &buf, deps); code == 0 {
		t.Fatal("守护不在线应非零退出（不静默失败）")
	}
	out := buf.String()
	for _, want := range []string{"ferryman serve", "watchdog", "守护不在线"} {
		if !strings.Contains(out, want) {
			t.Errorf("失败输出缺如实报告/拉起指引 %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "已热切换") {
		t.Errorf("失败路径不得宣称已切换:\n%s", out)
	}
}

// ---- switch：真 HTTP 客户端层（httptest 假管理口） ----

func TestProviderSwitchRequestRealHTTP(t *testing.T) {
	var gotName, gotAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /provider_switch", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotName = body.Name
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"active":"zhipu","base_url":"https://u.example",` +
			`"dialect":"anthropic","codex":"translation"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	port := portOfURL(t, srv.URL)
	resp, err := providerSwitchRequest(port, "tk", "zhipu")
	if err != nil {
		t.Fatalf("providerSwitchRequest: %v", err)
	}
	if gotAuth != "Bearer tk" || gotName != "zhipu" {
		t.Errorf("请求形态不符: auth=%q name=%q", gotAuth, gotName)
	}
	if resp["active"] != "zhipu" || resp["codex"] != "translation" {
		t.Errorf("200 响应解析不符: %v", resp)
	}
}

func TestProviderSwitchRequest400Passthrough(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /provider_switch", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"条目 \"x\" 不在上游表内（可用: a, b）"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	_, err := providerSwitchRequest(portOfURL(t, srv.URL), "tk", "x")
	if err == nil {
		t.Fatal("400 应报错")
	}
	if !strings.Contains(err.Error(), "不在上游表内") {
		t.Errorf("错误应透传端点报因: %v", err)
	}
}

func TestProviderSwitchRequestUnauthorized(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /provider_switch", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	_, err := providerSwitchRequest(portOfURL(t, srv.URL), "WRONG", "x")
	if err == nil || !strings.Contains(err.Error(), "鉴权") {
		t.Errorf("401 应报鉴权失败: %v", err)
	}
}

func TestProviderSwitchRequestDeadPortReportsOffline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close() // 拿一个确定无监听的口
	_, err = providerSwitchRequest(port, "tk", "x")
	if err == nil || !strings.Contains(err.Error(), "守护不在线") {
		t.Errorf("死口应报守护不在线: %v", err)
	}
}

// ---- add ----

func TestProviderAddWritesEntryAndMasksKey(t *testing.T) {
	f := writeProviderCfg(t, providerCfgSrc)
	var buf bytes.Buffer
	opts := providerAddOpts{
		BaseURL:  "https://api.kimi.com/coding/",
		Key:      "sk-test-kimi-77ab21cd",
		ModelMap: "default=kimi-for-coding,codex=k3",
	}
	if code := providerAdd(f, "kimi", opts, &buf); code != 0 {
		t.Fatalf("add 退出码 = %d\n%s", code, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "kimi") || !strings.Contains(out, "****21cd") {
		t.Errorf("成功输出应含条目名与脱敏钥:\n%s", out)
	}
	if strings.Contains(out, "sk-test-kimi-77ab21cd") {
		t.Errorf("输出泄漏整钥:\n%s", out)
	}
	after := readFileProvider(t, f)
	if !strings.Contains(after, "[dock.upstreams.kimi]") ||
		!strings.Contains(after, "sk-test-kimi-77ab21cd") {
		t.Errorf("条目（含真钥）应落本机 config:\n%s", after)
	}
	cfg, err := config.Load(f, false)
	if err != nil {
		t.Fatalf("写回产物过不了 Load: %v", err)
	}
	got := cfg.Dock.Upstreams["kimi"]
	if got.ModelMap["default"] != "kimi-for-coding" || got.ModelMap["codex"] != "k3" ||
		got.Dialect != config.DialectAnthropic || got.BaseURL != opts.BaseURL {
		t.Errorf("条目回读不符: %+v", got)
	}
	if strings.Contains(after, "balance_url") {
		t.Errorf("未配 balance_url 不得落该键:\n%s", after)
	}
}

func TestProviderAddKeyEnvAndMissingEnv(t *testing.T) {
	f := writeProviderCfg(t, providerCfgSrc)
	t.Setenv("FERRYMAN_TEST_PROVIDER_KEY", "sk-test-envkey-88cc11dd")
	var buf bytes.Buffer
	if code := providerAdd(f, "fromenv", providerAddOpts{
		BaseURL: "https://e.example/anthropic", KeyEnv: "FERRYMAN_TEST_PROVIDER_KEY",
		ModelMap: "default=m-e",
	}, &buf); code != 0 {
		t.Fatalf("--key-env 退出码 = %d\n%s", code, buf.String())
	}
	if out := buf.String(); strings.Contains(out, "sk-test-envkey-88cc11dd") {
		t.Errorf("输出泄漏整钥:\n%s", out)
	}
	if after := readFileProvider(t, f); !strings.Contains(after, "sk-test-envkey-88cc11dd") {
		t.Errorf("env 钥应落本机 config:\n%s", after)
	}

	// 环境变量未设置 → 拒绝并点名变量名
	f2 := writeProviderCfg(t, providerCfgSrc)
	buf.Reset()
	if code := providerAdd(f2, "noenv", providerAddOpts{
		BaseURL: "https://e.example/anthropic", KeyEnv: "FERRYMAN_TEST_ABSENT_KEY",
		ModelMap: "default=m-e",
	}, &buf); code == 0 {
		t.Fatal("环境变量未设置应拒绝")
	}
	if out := buf.String(); !strings.Contains(out, "FERRYMAN_TEST_ABSENT_KEY") {
		t.Errorf("拒绝应点名环境变量:\n%s", out)
	}
}

func TestProviderAddRefusalsKeepFileUntouched(t *testing.T) {
	cases := []struct {
		name    string
		opts    providerAddOpts
		wantSub string
	}{
		{"zhipu", providerAddOpts{BaseURL: "https://z.example", ModelMap: "default=m"}, "已存在"},
		{"baddialect", providerAddOpts{BaseURL: "https://b.example", Dialect: "xml",
			ModelMap: "default=m"}, "dialect"},
		{"badcodex", providerAddOpts{BaseURL: "https://b.example", Codex: "maybe",
			ModelMap: "default=m"}, "codex"},
		{"nobase", providerAddOpts{ModelMap: "default=m"}, "base-url"},
		{"nodefault", providerAddOpts{BaseURL: "https://n.example"}, "default"},
	}
	for _, tc := range cases {
		f := writeProviderCfg(t, providerCfgSrc)
		before := readFileProvider(t, f)
		var buf bytes.Buffer
		if code := providerAdd(f, tc.name, tc.opts, &buf); code == 0 {
			t.Errorf("%s: 应拒绝:\n%s", tc.name, buf.String())
			continue
		}
		if out := buf.String(); !strings.Contains(out, tc.wantSub) {
			t.Errorf("%s: 拒绝输出应含 %q:\n%s", tc.name, tc.wantSub, out)
		}
		if after := readFileProvider(t, f); after != before {
			t.Errorf("%s: 拒绝路径原文件不得动", tc.name)
		}
	}
}

func TestProviderAddNoKeyIsInactivePreset(t *testing.T) {
	f := writeProviderCfg(t, providerCfgSrc)
	var buf bytes.Buffer
	if code := providerAdd(f, "preset", providerAddOpts{
		BaseURL: "https://p.example/anthropic", ModelMap: "default=m-p",
	}, &buf); code != 0 {
		t.Fatalf("无钥 add（未激活预置）应成功:\n%s", buf.String())
	}
	if out := buf.String(); !strings.Contains(out, "未配置") {
		t.Errorf("无钥应如实显示未配置:\n%s", out)
	}
}

// ---- remove ----

func TestProviderRemoveDeletesEntryAndListsRemaining(t *testing.T) {
	f := writeProviderCfg(t, providerCfgSrc)
	var buf bytes.Buffer
	if code := providerRemove(f, "native", &buf); code != 0 {
		t.Fatalf("remove 退出码 = %d\n%s", code, buf.String())
	}
	after := readFileProvider(t, f)
	if strings.Contains(after, "native") {
		t.Errorf("native 条目应删除:\n%s", after)
	}
	out := buf.String()
	if !strings.Contains(out, "zhipu") || !strings.Contains(out, "blocked") {
		t.Errorf("成功输出应列剩余条目:\n%s", out)
	}
	if _, err := config.Load(f, false); err != nil {
		t.Fatalf("删除后配置应仍过校验: %v", err)
	}
}

func TestProviderRemoveRefusesActive(t *testing.T) {
	f := writeProviderCfg(t, providerCfgSrc)
	before := readFileProvider(t, f)
	var buf bytes.Buffer
	if code := providerRemove(f, "zhipu", &buf); code == 0 {
		t.Fatal("active 条目应拒删")
	}
	out := buf.String()
	if !strings.Contains(out, "active") || !strings.Contains(out, "switch") {
		t.Errorf("拒绝应点名 active 并给 switch 指引:\n%s", out)
	}
	if readFileProvider(t, f) != before {
		t.Fatal("拒绝路径原文件不得动")
	}
}

func TestProviderRemoveUnknown(t *testing.T) {
	f := writeProviderCfg(t, providerCfgSrc)
	var buf bytes.Buffer
	if code := providerRemove(f, "ghost", &buf); code == 0 {
		t.Fatal("未知条目应拒绝")
	}
	if out := buf.String(); !strings.Contains(out, "ghost") {
		t.Errorf("拒绝应含条目名:\n%s", out)
	}
}

// ---- import-ccswitch（假库；绝不触真 ~/.cc-switch） ----

// writeFakeCCSwitchDB 现建假 cc-switch 库（providers 表真 schema 子集；假密钥）。
func writeFakeCCSwitchDB(t *testing.T, rows [][3]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cc-switch.db")
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE providers (
		id TEXT NOT NULL, app_type TEXT NOT NULL, name TEXT NOT NULL,
		settings_config TEXT NOT NULL, is_current BOOLEAN NOT NULL DEFAULT 0,
		PRIMARY KEY (id, app_type))`); err != nil {
		t.Fatal(err)
	}
	for i, r := range rows {
		if _, err := db.Exec(`INSERT INTO providers (id, app_type, name, settings_config)
			VALUES (?, ?, ?, ?)`, fmt.Sprintf("id-%d", i), r[1], r[0], r[2]); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func claudeSettingsJSON(baseURL, token, model string) string {
	env := map[string]any{}
	if baseURL != "" {
		env["ANTHROPIC_BASE_URL"] = baseURL
	}
	if token != "" {
		env["ANTHROPIC_AUTH_TOKEN"] = token
	}
	if model != "" {
		env["ANTHROPIC_MODEL"] = model
	}
	b, _ := json.Marshal(map[string]any{"env": env})
	return string(b)
}

func codexSettingsJSON(key, baseURL, model string) string {
	cfg := "model_provider = \"relay\"\n"
	if model != "" {
		cfg += "model = \"" + model + "\"\n"
	}
	cfg += "\n[model_providers.relay]\nname = \"Relay\"\nbase_url = \"" + baseURL + "\"\n"
	b, _ := json.Marshal(map[string]any{
		"auth": map[string]any{"OPENAI_API_KEY": key}, "config": cfg})
	return string(b)
}

func TestProviderImportCCSwitchEndToEnd(t *testing.T) {
	f := writeProviderCfg(t, providerCfgSrc)
	db := writeFakeCCSwitchDB(t, [][3]string{
		{"zhipu", "claude", claudeSettingsJSON("https://open.bigmodel.cn/api/anthropic",
			"sk-test-import-conflict01", "glm-5.3")}, // 与既有条目重名 → 跳过
		{"kimi-import", "claude", claudeSettingsJSON("https://api.kimi.com/coding/",
			"sk-test-import-kimi2203", "kimi-for-coding")}, // 导入
		{"relay-native", "codex", codexSettingsJSON("sk-test-import-codex4412",
			"https://relay.example.com/v1", "gpt-x")}, // 导入（原生）
		{"gm", "gemini", "{}"}, // 范围外
	})
	var buf bytes.Buffer
	if code := providerImportCCSwitch(f, db, &buf); code != 0 {
		t.Fatalf("import 退出码 = %d\n%s", code, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "重名") || !strings.Contains(out, "范围外") {
		t.Errorf("导入报告缺跳过说明:\n%s", out)
	}
	if strings.Contains(out, "sk-test-import") {
		t.Errorf("导入输出泄漏整钥:\n%s", out)
	}
	cfg, err := config.Load(f, false)
	if err != nil {
		t.Fatalf("导入产物过不了 Load/Validate: %v", err)
	}
	ki, ok := cfg.Dock.Upstreams["kimi-import"]
	if !ok || ki.Dialect != config.DialectAnthropic || ki.APIKey != "sk-test-import-kimi2203" {
		t.Errorf("kimi-import 映射不符: %+v", ki)
	}
	rn, ok := cfg.Dock.Upstreams["relay-native"]
	if !ok || rn.Dialect != config.DialectOpenAIResponses ||
		rn.CodexAvailability() != config.CodexNative || rn.ModelMap["codex"] != "gpt-x" {
		t.Errorf("relay-native 映射不符: %+v", rn)
	}
	if _, ok := cfg.Dock.Upstreams["gm"]; ok {
		t.Error("gemini 条目不得入表")
	}
	if cfg.Dock.Active != "zhipu" {
		t.Errorf("active 不得被导入改变: %q", cfg.Dock.Active)
	}
	// 既有 zhipu 条目未被覆盖（原钥仍在）
	if cfg.Dock.Upstreams["zhipu"].APIKey != "sk-zhipu-00001234ef12" {
		t.Errorf("重名条目被覆盖: %+v", cfg.Dock.Upstreams["zhipu"])
	}
}

func TestProviderImportCCSwitchDBErrorAndNoTable(t *testing.T) {
	f := writeProviderCfg(t, providerCfgSrc)
	var buf bytes.Buffer
	if code := providerImportCCSwitch(f, filepath.Join(t.TempDir(), "absent.db"), &buf); code == 0 {
		t.Fatal("库打不开应非零退出")
	}
	if out := buf.String(); !strings.Contains(out, "--db") {
		t.Errorf("报错应给 --db 指引:\n%s", out)
	}

	// 无上游表（旧单值形态）→ 拒绝
	f2 := writeProviderCfg(t, "[dock]\napi_key = \"k\"\n")
	buf.Reset()
	if code := providerImportCCSwitch(f2, writeFakeCCSwitchDB(t, [][3]string{
		{"a", "claude", claudeSettingsJSON("https://a.example/anthropic", "sk-test-a000", "m-a")},
	}), &buf); code == 0 {
		t.Fatal("无上游表应拒绝")
	}
	if out := buf.String(); !strings.Contains(out, "上游表") {
		t.Errorf("拒绝应说明无上游表:\n%s", out)
	}
}

func TestProviderImportCCSwitchAllSkippedZeroExit(t *testing.T) {
	f := writeProviderCfg(t, providerCfgSrc)
	db := writeFakeCCSwitchDB(t, [][3]string{{"gm", "gemini", "{}"}})
	var buf bytes.Buffer
	if code := providerImportCCSwitch(f, db, &buf); code != 0 {
		t.Fatalf("全部跳过（无可导入）应退出 0:\n%s", buf.String())
	}
	if out := buf.String(); !strings.Contains(out, "无可导入") {
		t.Errorf("应如实说明无可导入:\n%s", out)
	}
}

// ---- apply / --restore ----

func TestProviderApplyTargetsAndReport(t *testing.T) {
	home := t.TempDir()
	oldHome := osUserHomeDir
	osUserHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { osUserHomeDir = oldHome })

	var gotTargets provider.Targets
	oldApply := providerApplyFn
	providerApplyFn = func(tg provider.Targets) (provider.ApplyReport, error) {
		gotTargets = tg
		return provider.ApplyReport{Targets: []provider.TargetReport{
			{Name: "cc", Path: tg.CCSettings, Action: provider.ActionWritten,
				Backup: tg.CCSettings + ".bak-ferryman-20260930-120000", Detail: "已定向写入"},
			{Name: "codex", Path: tg.CodexConfig, Action: provider.ActionUnchanged,
				Detail: "已是目标形态（零写入）"},
		}}, nil
	}
	t.Cleanup(func() { providerApplyFn = oldApply })

	f := writeProviderCfg(t, providerCfgSrc)
	var buf bytes.Buffer
	if code := providerApply(f, false, &buf); code != 0 {
		t.Fatalf("apply 退出码 = %d\n%s", code, buf.String())
	}
	// Targets 派生：渡口地址单源（DockURLFromListen），三份路径自家目录拼装
	if gotTargets.DockBaseURL != "http://127.0.0.1:15722" {
		t.Errorf("DockBaseURL = %q, want http://127.0.0.1:15722", gotTargets.DockBaseURL)
	}
	if gotTargets.CCSettings != filepath.Join(home, ".claude", "settings.json") {
		t.Errorf("CCSettings = %q", gotTargets.CCSettings)
	}
	if gotTargets.CodexConfig != filepath.Join(home, ".codex", "config.toml") {
		t.Errorf("CodexConfig = %q", gotTargets.CodexConfig)
	}
	if gotTargets.OrcaCodexConfig != filepath.Join(home, "AppData", "Roaming", "orca",
		"codex-runtime-home", "home", "config.toml") {
		t.Errorf("OrcaCodexConfig = %q", gotTargets.OrcaCodexConfig)
	}
	// 回显逐份结果（名/路径/动作/备份）
	out := buf.String()
	for _, want := range []string{"[cc]", "[codex]", "已定向写入", "已定向写入",
		".bak-ferryman-20260930-120000", "已是目标形态"} {
		if !strings.Contains(out, want) {
			t.Errorf("apply 回显缺 %q:\n%s", want, out)
		}
	}
}

func TestProviderApplyRestoreEcho(t *testing.T) {
	home := t.TempDir()
	oldHome := osUserHomeDir
	osUserHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { osUserHomeDir = oldHome })

	var restored bool
	oldRestore := providerRestoreFn
	providerRestoreFn = func(tg provider.Targets) (provider.ApplyReport, error) {
		restored = true
		return provider.ApplyReport{Targets: []provider.TargetReport{
			{Name: "cc", Path: tg.CCSettings, Action: provider.ActionRestored,
				Backup: tg.CCSettings + ".bak-ferryman-20260930-110000", Detail: "已还原"},
		}}, nil
	}
	t.Cleanup(func() { providerRestoreFn = oldRestore })

	f := writeProviderCfg(t, providerCfgSrc)
	var buf bytes.Buffer
	if code := providerApply(f, true, &buf); code != 0 {
		t.Fatalf("--restore 退出码 = %d\n%s", code, buf.String())
	}
	if !restored {
		t.Fatal("--restore 应走 Restore 缝")
	}
	out := buf.String()
	for _, want := range []string{"还原", "[cc]", ".bak-ferryman-20260930-110000"} {
		if !strings.Contains(out, want) {
			t.Errorf("--restore 回显缺 %q:\n%s", want, out)
		}
	}
}

func TestProviderApplyErrorAndNoDock(t *testing.T) {
	home := t.TempDir()
	oldHome := osUserHomeDir
	osUserHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { osUserHomeDir = oldHome })

	oldApply := providerApplyFn
	providerApplyFn = func(provider.Targets) (provider.ApplyReport, error) {
		return provider.ApplyReport{}, errors.New("provider: settings.json 解析失败（转人工）")
	}
	t.Cleanup(func() { providerApplyFn = oldApply })

	f := writeProviderCfg(t, providerCfgSrc)
	var buf bytes.Buffer
	if code := providerApply(f, false, &buf); code == 0 {
		t.Fatal("apply 出错应非零退出")
	}
	if out := buf.String(); !strings.Contains(out, "解析失败") {
		t.Errorf("错误应如实透传:\n%s", out)
	}

	// 无 [dock] 节 → 拒绝（无 listen 可派生目标地址）
	f2 := writeProviderCfg(t, "[server]\nport = 7399\n")
	buf.Reset()
	if code := providerApply(f2, false, &buf); code == 0 {
		t.Fatal("无 [dock] 应拒绝")
	}
	if out := buf.String(); !strings.Contains(out, "[dock]") {
		t.Errorf("拒绝应说明无 [dock]:\n%s", out)
	}
}

// ---- 分发与用法 ----

// TestProviderTargetsFromHomeDerivation apply 的 Targets 派生纯函数：渡口地址
// 与 provider 包单源一致（评审留话：不自造拼接），三份配置路径自家目录拼装。
func TestProviderTargetsFromHomeDerivation(t *testing.T) {
	home := filepath.Join("some", "home")
	tg := providerTargetsFromHome(home, "127.0.0.1:15999")
	if tg.DockBaseURL != provider.DockURLFromListen("127.0.0.1:15999") {
		t.Errorf("DockBaseURL 应与 provider 单源一致: %q", tg.DockBaseURL)
	}
	if tg.DockBaseURL != "http://127.0.0.1:15999" {
		t.Errorf("DockBaseURL = %q", tg.DockBaseURL)
	}
	wantOrca := filepath.Join(home, "AppData", "Roaming", "orca",
		"codex-runtime-home", "home", "config.toml")
	if tg.OrcaCodexConfig != wantOrca {
		t.Errorf("orca 路径 = %q, want %q", tg.OrcaCodexConfig, wantOrca)
	}
	if tg.CCSettings != filepath.Join(home, ".claude", "settings.json") ||
		tg.CodexConfig != filepath.Join(home, ".codex", "config.toml") {
		t.Errorf("CC/codex 路径派生不符: %q / %q", tg.CCSettings, tg.CodexConfig)
	}
}

func TestCmdProviderDispatch(t *testing.T) {
	f := writeProviderCfg(t, providerCfgSrc)
	var buf bytes.Buffer
	if code := cmdProvider([]string{"list", "--config", f}, &buf); code != 0 {
		t.Fatalf("cmdProvider list = %d\n%s", code, buf.String())
	}
	if out := buf.String(); !strings.Contains(out, "zhipu") {
		t.Errorf("--config 未生效:\n%s", out)
	}
	buf.Reset()
	if code := cmdProvider([]string{"bogus"}, &buf); code != 2 {
		t.Fatalf("未知子命令应退出 2, got %d", code)
	}
	buf.Reset()
	if code := cmdProvider(nil, &buf); code != 2 {
		t.Fatalf("无子命令应退出 2, got %d", code)
	}
	buf.Reset()
	if code := cmdProvider([]string{"switch", "nope", "--config", f}, &buf); code != 1 {
		t.Fatalf("switch 未知条目（拒绝先于任何网络）应退出 1, got %d\n%s", code, buf.String())
	}
	buf.Reset()
	if code := cmdProvider([]string{"add"}, &buf); code != 2 {
		t.Fatalf("add 缺名应退出 2, got %d", code)
	}
	// apply 分发：注入缝全桩（家目录指临时目录、Apply 恒错）——绝不触真用户
	// 目录（真实 Apply 会读写 ~/.claude ~/.codex，测试零触碰）。
	tmpHome := t.TempDir()
	oldHome := osUserHomeDir
	osUserHomeDir = func() (string, error) { return tmpHome, nil }
	oldApply := providerApplyFn
	providerApplyFn = func(provider.Targets) (provider.ApplyReport, error) {
		return provider.ApplyReport{}, errors.New("桩：不落盘")
	}
	t.Cleanup(func() { osUserHomeDir = oldHome; providerApplyFn = oldApply })
	buf.Reset()
	if code := cmdProvider([]string{"apply", "--restore", "--config", f}, &buf); code != 1 {
		t.Fatalf("apply --restore 出错应退出 1, got %d\n%s", code, buf.String())
	}
}
