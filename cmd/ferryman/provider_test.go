// provider_test.go — 票06：`ferryman provider` 命令族验收钉子。
//
// 验收口径（票面）：
//   - list：全部条目 + active 标注 + dialect/codex 可用性（原生/需翻译/不支持）
//   - 模型位概要 + 密钥脱敏（只露尾 4 位，整钥零回显）；
//   - switch：不存在→拒绝并列可用；codex="unsupported"→默认拒绝并报因；
//     --cc-only 显式放行并逐 agent 列明断供面（codex 暂断供/pi 暂断供）；成功回显新 active 与
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
// 各一条（需翻译/原生/否决），active = zhipu（pi 全可用——主模型键在位）；票12
// 增 pi 三种不可用形态各一条（piveto 显式否决/native 方言/nokey 缺主模型键）。
const providerCfgSrc = `
[server]
port = 7399

[dock]
listen = "127.0.0.1:15722"
active = "zhipu"

[dock.upstreams.zhipu]
base_url = "https://open.bigmodel.cn/api/anthropic"
api_key = "sk-zhipu-00001234ef12"
model_map = { default = "glm-5.3", haiku = "glm-5.3-flash", codex = "glm-5.3", pi = "glm-5.3" }

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

[dock.upstreams.piveto]
base_url = "https://pv.example/anthropic"
api_key = "sk-piveto-7788ccdd"
model_map = { default = "m-pv", pi = "glm-pv" }
pi = "unsupported"

[dock.upstreams.nokey]
base_url = "https://nk.example/anthropic"
api_key = "sk-nokey-5566eeff"
model_map = { default = "m-nk" }

[ferry]
provider = "deepseek"
`

// providerCfgWithActive providerCfgSrc 换 active 的底稿变体（表不动，单选键改指）。
func providerCfgWithActive(active string) string {
	return strings.Replace(providerCfgSrc, `active = "zhipu"`, `active = "`+active+`"`, 1)
}

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

// TestProviderListShowsPiAvailability 票09：list 行补 pi 三态可用性显示
// （可用/不可用/不支持），与 codex 行并列；pi 主模型位概要随行。
func TestProviderListShowsPiAvailability(t *testing.T) {
	f := writeProviderCfg(t, `
[server]
port = 7399

[dock]
listen = "127.0.0.1:15722"
active = "glm"

[dock.upstreams.glm]
base_url = "https://open.bigmodel.cn/api/anthropic"
model_map = { default = "glm-5.3", pi = "glm-5.3" }

[dock.upstreams.native]
base_url = "https://n.example/v1"
dialect = "openai_responses"
model_map = { default = "gpt-x" }

[dock.upstreams.vetoed]
base_url = "https://v.example/anthropic"
pi = "unsupported"
model_map = { default = "m-v" }

[ferry]
provider = "deepseek"
`)
	var buf bytes.Buffer
	if code := providerList(f, &buf); code != 0 {
		t.Fatalf("list 退出码 = %d\n%s", code, buf.String())
	}
	out := buf.String()
	for _, want := range []string{
		// anthropic 方言推导可用＋pi 主模型位概要
		"可用（anthropic 方言，pi 复用 CC 车道）· pi 主模型 glm-5.3",
		// openai_responses 方言推导不可用＋模型位缺省
		"不可用（openai_responses 方言，pi 无入站车道）· pi 主模型位未配",
		// 显式否决位回显
		"pi = \"unsupported\"",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("list pi 行缺 %q:\n%s", want, out)
		}
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

// ---- switch：pi 不可用三形态（票12） ----

// TestProviderSwitchPiUnavailableRejectedByDefault pi 三种不可用（显式否决/
// 方言非 anthropic/缺 pi 主模型键）默认都拒绝并报因：不触管理口、不动配置。
func TestProviderSwitchPiUnavailableRejectedByDefault(t *testing.T) {
	for _, tc := range []struct {
		name, entry, wantWhy string
	}{
		{"显式否决", "piveto", `pi = "unsupported"`},
		{"方言非anthropic", "native", "openai_responses"},
		{"缺pi主模型键", "nokey", "pi 主模型键"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := writeProviderCfg(t, providerCfgSrc)
			deps, called := fakeSwitchDeps(nil, nil)
			var buf bytes.Buffer
			if code := providerSwitch(f, tc.entry, false, &buf, deps); code != 1 {
				t.Fatalf("pi 不可用默认应 exit 1, got %d\n%s", code, buf.String())
			}
			out := buf.String()
			for _, want := range []string{tc.entry, "对 pi 不可用", "--cc-only", tc.wantWhy} {
				if !strings.Contains(out, want) {
					t.Errorf("拒绝输出应含 %q（报因与放行指引）:\n%s", want, out)
				}
			}
			if len(*called) != 0 {
				t.Fatalf("默认拒绝不得调管理口（被调 %v）", *called)
			}
			if strings.Contains(readFileProvider(t, f), `active = "`+tc.entry+`"`) {
				t.Fatal("拒绝路径不得写配置")
			}
		})
	}
}

// TestProviderSwitchPiUnavailableCCOnlyWarnsAndProceeds --cc-only 显式放行并
// 明示 pi 断供面（pi 暂断供，仅 CC/codex）。
func TestProviderSwitchPiUnavailableCCOnlyWarnsAndProceeds(t *testing.T) {
	f := writeProviderCfg(t, providerCfgSrc)
	deps, called := fakeSwitchDeps(map[string]any{
		"ok": true, "active": "piveto", "base_url": "https://pv.example/anthropic",
		"dialect": "anthropic", "codex": config.CodexTranslation,
	}, nil)
	var buf bytes.Buffer
	if code := providerSwitch(f, "piveto", true, &buf, deps); code != 0 {
		t.Fatalf("--cc-only 放行应成功:\n%s", buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "pi 暂断供") || !strings.Contains(out, "仅 CC/codex") {
		t.Errorf("放行必须明示 pi 断供后果:\n%s", out)
	}
	if got := *called; len(got) != 1 || got[0] != "piveto" {
		t.Fatalf("管理口被调 = %v, want [piveto]", got)
	}
}

// TestProviderSwitchBothAgentsOutageListedSeparately 两 agent 同时断供时拒绝
// 报因与放行告警都分开列明各自断供面（blocked：codex 否决位＋缺 pi 主模型键）。
func TestProviderSwitchBothAgentsOutageListedSeparately(t *testing.T) {
	f := writeProviderCfg(t, providerCfgSrc)
	deps, called := fakeSwitchDeps(nil, nil)
	var buf bytes.Buffer
	if code := providerSwitch(f, "blocked", false, &buf, deps); code != 1 {
		t.Fatalf("双断供默认应 exit 1, got %d\n%s", code, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "对 codex 不可用") || !strings.Contains(out, "对 pi 不可用") {
		t.Errorf("拒绝应分开列明两 agent 断供因:\n%s", out)
	}
	if len(*called) != 0 {
		t.Fatalf("默认拒绝不得调管理口（被调 %v）", *called)
	}

	// --cc-only 放行：告警段两 agent 断供面都列
	deps2, called2 := fakeSwitchDeps(map[string]any{
		"ok": true, "active": "blocked", "base_url": "https://b.example/anthropic",
		"dialect": "anthropic", "codex": config.CodexUnsupported,
	}, nil)
	buf.Reset()
	if code := providerSwitch(f, "blocked", true, &buf, deps2); code != 0 {
		t.Fatalf("--cc-only 放行应成功:\n%s", buf.String())
	}
	out = buf.String()
	if !strings.Contains(out, "codex 暂断供") || !strings.Contains(out, "pi 暂断供") {
		t.Errorf("放行告警应两 agent 断供面都列:\n%s", out)
	}
	if got := *called2; len(got) != 1 || got[0] != "blocked" {
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

// ---- add：--pi 否决位（票12） ----

// hasTopLevelTOMLLine 文件里是否有 `key = "value"` 独立行（model_map 内联里
// 的同名键不算——渲染行判据按行匹配）。
func hasTopLevelTOMLLine(src, key, val string) bool {
	want := key + ` = "` + val + `"`
	for _, ln := range strings.Split(src, "\n") {
		if strings.TrimSpace(ln) == want {
			return true
		}
	}
	return false
}

// TestProviderAddPiFlagFourForms --pi 旗标四形态：unsupported 落盘＋回显／
// 异值拒且文件不动／缺省不落 pi 行／与 --codex 并存各一行（回读 verifyDockEdit
// 过＝写入不报错、Load 过校验）。
func TestProviderAddPiFlagFourForms(t *testing.T) {
	// (1) --pi unsupported：落盘＋否决推导＋回显
	f := writeProviderCfg(t, providerCfgSrc)
	var buf bytes.Buffer
	if code := providerAdd(f, "piflag", providerAddOpts{
		BaseURL: "https://pi.example/anthropic", ModelMap: "default=m-pi",
		Pi: "unsupported",
	}, &buf); code != 0 {
		t.Fatalf("add --pi unsupported 退出码 = %d\n%s", code, buf.String())
	}
	after := readFileProvider(t, f)
	if !hasTopLevelTOMLLine(after, "pi", "unsupported") {
		t.Errorf("条目应含 pi = \"unsupported\" 独立行:\n%s", after)
	}
	if out := buf.String(); !strings.Contains(out, "否决位") {
		t.Errorf("回显应明示 pi 否决位:\n%s", out)
	}
	cfg, err := config.Load(f, false)
	if err != nil {
		t.Fatalf("写回产物过不了 Load（写入时 verifyDockEdit 应已过）: %v", err)
	}
	if got := cfg.Dock.Upstreams["piflag"]; got.Pi != config.PiUnsupported ||
		got.PiAvailability() != config.PiUnsupported {
		t.Errorf("pi 否决位回读不符: %+v", got)
	}

	// (2) 异值：拒且原文件不动
	f2 := writeProviderCfg(t, providerCfgSrc)
	before := readFileProvider(t, f2)
	buf.Reset()
	if code := providerAdd(f2, "badpi", providerAddOpts{
		BaseURL: "https://b2.example/anthropic", ModelMap: "default=m-b2",
		Pi: "maybe",
	}, &buf); code == 0 {
		t.Fatal("pi 异值应拒绝")
	}
	if out := buf.String(); !strings.Contains(out, "pi") {
		t.Errorf("拒绝应点名 pi:\n%s", out)
	}
	if readFileProvider(t, f2) != before {
		t.Fatal("拒绝路径原文件不得动")
	}

	// (3) 缺省：不落 pi 行（可用性按 dialect 推导，非否决不显式落盘）
	f3 := writeProviderCfg(t, providerCfgSrc)
	buf.Reset()
	if code := providerAdd(f3, "nopi", providerAddOpts{
		BaseURL: "https://n3.example/anthropic", ModelMap: "default=m-n3",
	}, &buf); code != 0 {
		t.Fatalf("不带 --pi 的 add 应成功:\n%s", buf.String())
	}
	after3 := readFileProvider(t, f3)
	// 断言圈定新条目块（fixture 里 piveto 条目本就带 pi 行，全文扫会误报）
	block3 := after3[strings.Index(after3, "[dock.upstreams.nopi]"):]
	if hasTopLevelTOMLLine(block3, "pi", "unsupported") {
		t.Errorf("缺省不得落 pi 否决行:\n%s", after3)
	}
	cfg3, err := config.Load(f3, false)
	if err != nil {
		t.Fatalf("写回产物过不了 Load: %v", err)
	}
	if got := cfg3.Dock.Upstreams["nopi"]; got.Pi != "" {
		t.Errorf("缺省条目 pi 位应为空: %+v", got)
	}

	// (4) 与 --codex 并存：两行各自落盘、回读双否决位
	f4 := writeProviderCfg(t, providerCfgSrc)
	buf.Reset()
	if code := providerAdd(f4, "bothveto", providerAddOpts{
		BaseURL: "https://bv.example/anthropic", ModelMap: "default=m-bv",
		Codex: "unsupported", Pi: "unsupported",
	}, &buf); code != 0 {
		t.Fatalf("add --codex --pi 应成功:\n%s", buf.String())
	}
	after4 := readFileProvider(t, f4)
	if !hasTopLevelTOMLLine(after4, "codex", "unsupported") ||
		!hasTopLevelTOMLLine(after4, "pi", "unsupported") {
		t.Errorf("codex/pi 否决位应各自成行落盘:\n%s", after4)
	}
	cfg4, err := config.Load(f4, false)
	if err != nil {
		t.Fatalf("写回产物过不了 Load: %v", err)
	}
	got4 := cfg4.Dock.Upstreams["bothveto"]
	if got4.CodexAvailability() != config.CodexUnsupported ||
		got4.PiAvailability() != config.PiUnsupported {
		t.Errorf("双否决位回读不符: %+v", got4)
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
	// 票12 核验：import-ccswitch 推断条目缺省不带 pi 否决位（可用性按 dialect
	// 推导，推断面零改动即达标）
	if ki.Pi != "" || rn.Pi != "" {
		t.Errorf("导入条目不得带 pi 否决位: %+v / %+v", ki, rn)
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
	// pi 第四目标（票10）：路径自家目录拼装；主模型位与可用性位自 active 条目
	// 派生透传——providerCfgSrc 的 active=zhipu pi 键在位（票12 fixture 补齐），
	// 可用性推导可用。
	if gotTargets.PiModels != filepath.Join(home, ".pi", "agent", "models.json") ||
		gotTargets.PiSettings != filepath.Join(home, ".pi", "agent", "settings.json") {
		t.Errorf("pi 目标路径不符: %q / %q", gotTargets.PiModels, gotTargets.PiSettings)
	}
	if gotTargets.PiModel != "glm-5.3" {
		t.Errorf("zhipu pi 键在位，PiModel 应为 glm-5.3: %q", gotTargets.PiModel)
	}
	if gotTargets.PiAvailability != config.PiAvailable {
		t.Errorf("PiAvailability 应为可用（缺省推导）: %q", gotTargets.PiAvailability)
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

// ---- apply：pi 可用性位（票12） ----

// TestProviderApplyTargetsCarryPiAvailability active 上游的 pi 可用性裁决值与
// 主模型位透传给写入器 Targets（票09 PiAvailability 单源）。
func TestProviderApplyTargetsCarryPiAvailability(t *testing.T) {
	home := t.TempDir()
	oldHome := osUserHomeDir
	osUserHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { osUserHomeDir = oldHome })
	oldApply := providerApplyFn
	providerApplyFn = func(provider.Targets) (provider.ApplyReport, error) {
		return provider.ApplyReport{}, nil
	}
	t.Cleanup(func() { providerApplyFn = oldApply })

	for _, tc := range []struct {
		active    string
		wantAvail string
		wantModel string
	}{
		{"zhipu", config.PiAvailable, "glm-5.3"},     // anthropic＋pi 键在位＝可用
		{"piveto", config.PiUnsupported, "glm-pv"},   // 显式否决位
		{"native", config.PiUnavailable, ""},         // openai_responses 方言
	} {
		f := writeProviderCfg(t, providerCfgWithActive(tc.active))
		var gotTargets provider.Targets
		providerApplyFn = func(tg provider.Targets) (provider.ApplyReport, error) {
			gotTargets = tg
			return provider.ApplyReport{}, nil
		}
		var buf bytes.Buffer
		if code := providerApply(f, false, &buf); code != 0 {
			t.Fatalf("%s: apply 退出码 = %d\n%s", tc.active, code, buf.String())
		}
		if gotTargets.PiAvailability != tc.wantAvail || gotTargets.PiModel != tc.wantModel {
			t.Errorf("%s: PiAvailability/PiModel = %q/%q, want %q/%q", tc.active,
				gotTargets.PiAvailability, gotTargets.PiModel, tc.wantAvail, tc.wantModel)
		}
	}
}

// writeProviderTestTree 临时家目录下落 CC/codex 合形配置（真写入器走写路径）。
func writeProviderTestTree(t *testing.T, home string) {
	t.Helper()
	mkdirsWrite := func(path, content string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mkdirsWrite(filepath.Join(home, ".claude", "settings.json"),
		`{"env":{"ANTHROPIC_BASE_URL":"https://old.example"}}`)
	mkdirsWrite(filepath.Join(home, ".codex", "config.toml"),
		"model_provider = \"relay\"\n\n[model_providers.relay]\nname = \"Relay\"\n"+
			"base_url = \"https://old.example/v1\"\nwire_api = \"responses\"\n"+
			"experimental_bearer_token = \"PROXY_MANAGED\"\n")
}

// TestProviderApplyRealWriterSkipsPiTargetWhenUnavailable 真写入器全链（不走
// 桩）：active 上游 pi 不可用 → pi 目标跳过并回显"pi 目标跳过:<原因>"，其余
// 目标照常写入，exit 0；~/.pi 零触碰（跳过发生在读取之前，不建目录不落文件）。
func TestProviderApplyRealWriterSkipsPiTargetWhenUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name, active, wantWhy string
	}{
		{"显式否决", "piveto", `pi = "unsupported"`},
		{"方言非anthropic", "native", "无入站车道"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			oldHome := osUserHomeDir
			osUserHomeDir = func() (string, error) { return home, nil }
			t.Cleanup(func() { osUserHomeDir = oldHome })
			writeProviderTestTree(t, home)
			// ~/.pi 刻意不建：跳过必须发生在读取之前，且不得代建目录
			f := writeProviderCfg(t, providerCfgWithActive(tc.active))
			var buf bytes.Buffer
			if code := providerApply(f, false, &buf); code != 0 {
				t.Fatalf("apply 部分跳过应 exit 0, got %d\n%s", code, buf.String())
			}
			out := buf.String()
			for _, want := range []string{"[pi]", "pi 目标跳过", tc.wantWhy, "其余目标照常"} {
				if !strings.Contains(out, want) {
					t.Errorf("回显缺 %q:\n%s", want, out)
				}
			}
			for _, absent := range []string{"[pi-models]", "[pi-settings]"} {
				if strings.Contains(out, absent) {
					t.Errorf("pi 目标跳过不得产生 %s 行:\n%s", absent, out)
				}
			}
			if _, err := os.Stat(filepath.Join(home, ".pi")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("pi 跳过不得创建 ~/.pi: %v", err)
			}
			// 其余目标照常：cc/codex 照常回显（真写入器已写入或零改动）
			if !strings.Contains(out, "[cc]") || !strings.Contains(out, "[codex]") {
				t.Errorf("其余目标应照常回显:\n%s", out)
			}
		})
	}
}

// ---- 分发与用法 ----

// TestProviderTargetsFromHomeDerivation apply 的 Targets 派生纯函数：渡口地址
// 与 provider 包单源一致（评审留话：不自造拼接），三份配置路径自家目录拼装；
// 票10 第四目标 pi 两文件同源拼装、pi 主模型位与可用性位透传（票12 起可用性
// 位进行为面——写入器据此跳过 pi 目标）。
func TestProviderTargetsFromHomeDerivation(t *testing.T) {
	home := filepath.Join("some", "home")
	tg := providerTargetsFromHome(home, "127.0.0.1:15999", "glm-5.3", config.PiUnavailable)
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
	if tg.PiModels != filepath.Join(home, ".pi", "agent", "models.json") ||
		tg.PiSettings != filepath.Join(home, ".pi", "agent", "settings.json") {
		t.Errorf("pi 路径派生不符: %q / %q", tg.PiModels, tg.PiSettings)
	}
	if tg.PiModel != "glm-5.3" {
		t.Errorf("PiModel 透传不符: %q", tg.PiModel)
	}
	if tg.PiAvailability != config.PiUnavailable {
		t.Errorf("PiAvailability 透传不符: %q", tg.PiAvailability)
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

// ---- 票04：list --json（机器可读出口＋F3 脱敏契约） ----

// TestProviderListJSONFieldsAndMasking --json 出口验收：合法 JSON、与文本表
// 同构（dialect/codex 与 pi 可用性（CodexAvailability/PiAvailability 单源值，
// 票09 providerPiLine 同素材）+ codex_model/pi_model 模型位 + api_key 尾 4 位
// 掩码 + key_status），夹具 providerCfgSrc 即真实形态假钥——五钥任一原文都
// 不得出现在 JSON 全文（F3 脱敏契约）；经 cmdProvider 分发钉 --json 旗标面。
func TestProviderListJSONFieldsAndMasking(t *testing.T) {
	f := writeProviderCfg(t, providerCfgSrc)
	var buf bytes.Buffer
	if code := cmdProvider([]string{"list", "--config", f, "--json"}, &buf); code != 0 {
		t.Fatalf("list --json 退出码 = %d\n%s", code, buf.String())
	}
	out := buf.String()
	var rep struct {
		Config    string `json:"config"`
		Active    string `json:"active"`
		Providers []struct {
			Name       string            `json:"name"`
			Active     bool              `json:"active"`
			BaseURL    string            `json:"base_url"`
			Dialect    string            `json:"dialect"`
			Codex      string            `json:"codex"`
			CodexModel string            `json:"codex_model"`
			Pi         string            `json:"pi"`
			PiModel    string            `json:"pi_model"`
			ModelMap   map[string]string `json:"model_map"`
			APIKey     string            `json:"api_key"`
			KeyStatus  string            `json:"key_status"`
			BalanceURL string            `json:"balance_url"`
		} `json:"providers"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("--json 非法 JSON: %v\n%s", err, out)
	}
	if rep.Active != "zhipu" {
		t.Errorf("顶层 active = %q, want zhipu", rep.Active)
	}
	if len(rep.Providers) != 5 {
		t.Fatalf("应有 5 条供应商, got %d:\n%s", len(rep.Providers), out)
	}
	idx := map[string]int{}
	for i, r := range rep.Providers {
		idx[r.Name] = i
	}
	z := rep.Providers[idx["zhipu"]]
	if !z.Active || z.Dialect != config.DialectAnthropic ||
		z.Codex != config.CodexTranslation || z.CodexModel != "glm-5.3" ||
		z.Pi != config.PiAvailable || z.PiModel != "glm-5.3" ||
		z.APIKey != "****ef12" || z.KeyStatus != "configured" {
		t.Errorf("zhipu 行不符: %+v", z)
	}
	n := rep.Providers[idx["native"]]
	if n.Codex != config.CodexNative || n.CodexModel != "gpt-x" ||
		n.Pi != config.PiUnavailable || n.Dialect != config.DialectOpenAIResponses {
		t.Errorf("native 行不符: %+v", n)
	}
	b := rep.Providers[idx["blocked"]]
	if b.Codex != config.CodexUnsupported || b.APIKey != "****ab21" {
		t.Errorf("blocked 行不符: %+v", b)
	}
	pv := rep.Providers[idx["piveto"]]
	if pv.Pi != config.PiUnsupported || pv.PiModel != "glm-pv" {
		t.Errorf("piveto 行不符: %+v", pv)
	}
	// F3 脱敏契约：夹具五钥（真实形态假钥）任一原文都不得出现在 JSON 全文
	for _, secret := range []string{
		"sk-zhipu-00001234ef12", "sk-native-9988ccd01234", "sk-blocked-4433ab21",
		"sk-piveto-7788ccdd", "sk-nokey-5566eeff",
	} {
		if strings.Contains(out, secret) {
			t.Errorf("--json 全文泄漏明文钥 %q", secret)
		}
	}
}

// TestProviderListJSONErrorAndEmptyShapes --json 错误面与空态（票04）：配置坏 →
// JSON error 对象＋退出 1（与文本面同判）；无 [dock] 与旧单值形态 → 退出 0 且
// providers 为空数组（恒非 null）。
func TestProviderListJSONErrorAndEmptyShapes(t *testing.T) {
	// 配置坏（active 悬空）：--json → 退出 1 + error 键
	bad := writeProviderCfg(t, "[dock]\nactive = \"ghost\"\n\n[dock.upstreams.a]\n"+
		"base_url = \"https://a.example\"\nmodel_map = { default = \"m\" }\n")
	var buf bytes.Buffer
	if code := cmdProvider([]string{"list", "--config", bad, "--json"}, &buf); code != 1 {
		t.Fatalf("坏配置 --json 应退出 1（与文本面同判）, got %d\n%s", code, buf.String())
	}
	var errShape struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(buf.Bytes(), &errShape); err != nil || errShape.Error == "" {
		t.Fatalf("坏配置 --json 应输出含 error 键的 JSON: %v\n%s", err, buf.String())
	}

	// 无 [dock]：退出 0 + providers 空数组（非 null）
	nodock := writeProviderCfg(t, "[server]\nport = 7399\n")
	buf.Reset()
	if code := cmdProvider([]string{"list", "--config", nodock, "--json"}, &buf); code != 0 {
		t.Fatalf("无 [dock] --json 应退出 0, got %d\n%s", code, buf.String())
	}
	var rep struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(buf.Bytes(), &rep); err != nil {
		t.Fatalf("非法 JSON: %v\n%s", err, buf.String())
	}
	if rep.Providers == nil || len(rep.Providers) != 0 {
		t.Errorf("无 [dock] 的 providers 应为空数组（非 null）: %v", rep.Providers)
	}

	// 旧单值形态（无上游表）：退出 0 + providers 空数组（非 null）
	legacy := writeProviderCfg(t, "[dock]\napi_key = \"k\"\n")
	buf.Reset()
	if code := cmdProvider([]string{"list", "--config", legacy, "--json"}, &buf); code != 0 {
		t.Fatalf("旧单值 --json 应退出 0, got %d\n%s", code, buf.String())
	}
	buf2 := struct {
		Providers []map[string]any `json:"providers"`
	}{}
	if err := json.Unmarshal(buf.Bytes(), &buf2); err != nil {
		t.Fatalf("非法 JSON: %v\n%s", err, buf.String())
	}
	if buf2.Providers == nil || len(buf2.Providers) != 0 {
		t.Errorf("旧单值的 providers 应为空数组（非 null）: %v", buf2.Providers)
	}
}

// ---- doctor --json（票04；cmd/ferryman 侧分发钉——序列化本体在
// internal/installer，另有单测） ----

// TestCmdDoctorJSONFlagDispatch doctor 参数面（票04）：恰好一个可选 --json
// 合法（进 runDoctorJSONEntry 缝）；其余参数（含 -h/--help/未知词/--json 重复）
// 仍用法错退 2 且缝零调用（拒绝先于执行，票01 零参数契约的延续）。
func TestCmdDoctorJSONFlagDispatch(t *testing.T) {
	origJSON, origText := runDoctorJSONEntry, runDoctorEntry
	defer func() { runDoctorJSONEntry, runDoctorEntry = origJSON, origText }()
	var jsonCalls, textCalls int
	runDoctorJSONEntry = func(string) int { jsonCalls++; return 0 }
	runDoctorEntry = func(string) int { textCalls++; return 0 }

	captureStd(t, func() {
		if code := cmdDoctor([]string{"--json"}); code != 0 {
			t.Errorf("--json 应进 JSON 缝退 0, got %d", code)
		}
	})
	if jsonCalls != 1 || textCalls != 0 {
		t.Fatalf("--json 缝调用: json=%d text=%d, want 1/0", jsonCalls, textCalls)
	}

	for _, tc := range [][]string{
		{"-h"}, {"--help"}, {"extra"}, {"--json", "--json"}, {"--json", "extra"},
	} {
		before := jsonCalls
		var code int
		_, stderr := captureStd(t, func() { code = cmdDoctor(tc) })
		if code != 2 {
			t.Errorf("参数 %v 应用法错退 2, got %d", tc, code)
		}
		if !strings.Contains(stderr, "用法:") {
			t.Errorf("参数 %v 拒绝应打印用法到 stderr, got %q", tc, stderr)
		}
		if jsonCalls != before {
			t.Errorf("参数 %v 拒绝不得进缝（拒绝先于执行）", tc)
		}
	}
	if textCalls != 0 {
		t.Errorf("本测全程不应触文本缝, got %d", textCalls)
	}
}
