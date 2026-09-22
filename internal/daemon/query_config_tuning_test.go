package daemon

// query_config_tuning_test.go — 票08:GET /config_tuning 验收钉子。
//
// 验收对照:
//   - 端点脱敏:任何 api_key 字段不出明文(全响应 grep 断言 + redact 标记);
//   - 三列按上游分列:配置值/生效值/建议值逐上游一行;白名单内无建议上游
//     "无建议"(suggestion=null);
//   - 建议状态:待审/已接受等状态与徽章文案;
//   - 配置缺席:found=false 如实,调参三列照常(不编造)。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ferryman/internal/config"
	"ferryman/internal/tuning"
)

// ctFixtureTOML 端点夹具配置:两个白名单上游(glm 有同名价格本;kimi 无同名
// 本且价格表多本无映射 → 生效值拒算 no_price_book 可观测)、密钥面(dock 条目
// api_key + 旧单值 api_key + pushover_token)、data_dir 指临时目录(调参流水
// 隔离)。
func ctFixtureTOML(dataDir string) string {
	return `[thresholds]
summarize_s = 1500
block_s = 2100
[server]
port = 7311
data_dir = "` + filepath.ToSlash(dataDir) + `"
[ferry.same_model]
enabled = true
upstreams = ["glm", "kimi"]
threshold_min = 25
[tuning]
mode = "recommend"
[prices.glm]
unit = "智谱积分"
per = 10000
[[prices.glm.versions]]
effective_from = "2026-09-01"
p_in = 6.9
p_cache = 1.7
p_out = 24
[prices.other]
unit = "分"
per = 1
[[prices.other.versions]]
effective_from = "2026-09-01"
p_in = 1.0
p_cache = 0.5
p_out = 2.0
[dock]
active = "glm"
api_key = "sk-old-single-value-key-9999"
[dock.upstreams.glm]
base_url = "https://open.bigmodel.cn/api/anthropic"
api_key = "sk-live-abcdef123456"
model_map = { default = "glm-5.3" }
[notify]
enabled = false
pushover_token = "pushover-token-abcdefgh"
`
}

// newCTEnv 端点测试环境:FERRYMAN_CONFIG 指夹具配置(端点经 ResolveConfigPath
// 读同一文件),Daemon 的 cfg 由该文件装载(数据目录=临时)。
func newCTEnv(t *testing.T) (*queryEnv, string) {
	t.Helper()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(ctFixtureTOML(filepath.Join(tmp, "data"))), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FERRYMAN_CONFIG", cfgPath)
	cfg, err := config.Load(cfgPath, true)
	if err != nil {
		t.Fatal(err)
	}
	e := newQueryEnv(t)
	e.d.Cfg = cfg // 端点读 d.Cfg(档位/白名单/数据目录)
	return e, cfgPath
}

// ctGet 调 GET /config_tuning,200 时返回(原始字节, 解码 JSON)。
func ctGet(t *testing.T, e *queryEnv) ([]byte, map[string]any) {
	t.Helper()
	code, raw := getRaw(t, e.port, "/config_tuning", e.token)
	if code != 200 {
		t.Fatalf("GET /config_tuning = %d %q", code, raw)
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("响应非 JSON: %v (%q)", err, raw)
	}
	return raw, resp
}

// ctUpstreamRow 按上游名取三列行。
func ctUpstreamRow(t *testing.T, resp map[string]any, up string) map[string]any {
	t.Helper()
	rows, ok := resp["upstreams"].([]any)
	if !ok {
		t.Fatalf("upstreams 缺席或非数组: %v", resp["upstreams"])
	}
	for _, r := range rows {
		row := r.(map[string]any)
		if row["upstream"] == up {
			return row
		}
	}
	t.Fatalf("缺上游行 %q: %v", up, rows)
	return nil
}

// ctSecretRows 收全部 redact 行的(键, 值)。
func ctSecretRows(t *testing.T, resp map[string]any) [][2]string {
	t.Helper()
	cfg := resp["config"].(map[string]any)
	sections := cfg["sections"].([]any)
	var out [][2]string
	for _, s := range sections {
		for _, r := range s.(map[string]any)["rows"].([]any) {
			row := r.(map[string]any)
			if row["redact"] == true {
				out = append(out, [2]string{row["key"].(string), row["value"].(string)})
			}
		}
	}
	return out
}

func TestConfigTuningMasksAllKeys(t *testing.T) {
	e, _ := newCTEnv(t)
	raw, resp := ctGet(t, e)

	// 深检:全响应字节 grep——三处明文密钥一个都不许出现。
	for _, secret := range []string{"sk-live-abcdef123456", "sk-old-single-value-key-9999",
		"pushover-token-abcdefgh"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("明文密钥泄漏: %q 出现在响应中", secret)
		}
	}
	// 打码口径:保留前 3 后 4——glm 条目掩码形如 sk-****3456(页面上以掩码呈现)。
	if !strings.Contains(string(raw), "sk-****3456") {
		t.Fatalf("应含前3后4掩码 sk-****3456: %s", raw)
	}
	// 全部密钥行 redact=true 且值仍非空(掩码在位,不是丢行)。
	secrets := ctSecretRows(t, resp)
	if len(secrets) < 3 {
		t.Fatalf("应有 ≥3 条脱敏行(api_key×2+pushover_token), got %d: %v", len(secrets), secrets)
	}
	for _, kv := range secrets {
		if kv[1] == "" {
			t.Fatalf("脱敏行值不应为空: %v", kv)
		}
	}
	// 配置总览在场且指向夹具文件。
	cfg := resp["config"].(map[string]any)
	if cfg["found"] != true {
		t.Fatalf("config.found 应为 true: %v", cfg)
	}
}

func TestConfigTuningThreeColumnsPerUpstream(t *testing.T) {
	e, _ := newCTEnv(t)
	// 建议:glm 待审(最新——晚于 kimi 的接受事件时点),kimi 已接受。
	ts := tuning.NewStore(e.d.Cfg.DataDir())
	sugGlm := &tuning.Suggestion{ID: "s1-glm", Upstream: "glm", SuggestMin: 22,
		CurrentMin: 25, HasCurrent: true, BestMin: 22, NetSavings: 8.5,
		FerryEvents: 42, MinEvents: 30, CreatedAt: 1_800_000_100}
	if err := ts.RecordSuggestion(sugGlm, 1_800_000_100); err != nil {
		t.Fatal(err)
	}
	if err := ts.Accept("s1-glm", "recommend", 25, 1_800_000_200); err != nil {
		t.Fatal(err)
	}
	sugKimi := &tuning.Suggestion{ID: "s2-kimi", Upstream: "kimi", SuggestMin: 18,
		CurrentMin: 25, HasCurrent: true, FerryEvents: 35, MinEvents: 30,
		CreatedAt: 1_800_000_050}
	if err := ts.RecordSuggestion(sugKimi, 1_800_000_050); err != nil {
		t.Fatal(err)
	}

	_, resp := ctGet(t, e)
	if resp["mode"] != "recommend" {
		t.Fatalf("mode = %v", resp["mode"])
	}

	// glm 行:配置值 25;生效值=计算器种子路径现算 20(≠配置值,三列可区分);
	// 建议 22 状态已接受(徽章文案)。
	glm := ctUpstreamRow(t, resp, "glm")
	if glm["configured_min"] != float64(25) {
		t.Fatalf("glm 配置值 = %v, want 25", glm["configured_min"])
	}
	if glm["effective_ok"] != true || glm["effective_min"] != float64(20) {
		t.Fatalf("glm 生效值 = %v/%v, want ok/20(种子路径现算)", glm["effective_ok"], glm["effective_min"])
	}
	sug := glm["suggestion"].(map[string]any)
	if sug["id"] != "s1-glm" || sug["suggest_min"] != float64(22) ||
		sug["status"] != "accepted" || sug["status_text"] != "已接受" {
		t.Fatalf("glm 建议 = %v", sug)
	}

	// kimi 行:无价格本 → 生效值拒算如实(err kind);建议 18 待审。
	kimi := ctUpstreamRow(t, resp, "kimi")
	if kimi["effective_ok"] != false || kimi["effective_err"] != "no_price_book" {
		t.Fatalf("kimi 生效值应拒算 no_price_book: %v/%v", kimi["effective_ok"], kimi["effective_err"])
	}
	ksug := kimi["suggestion"].(map[string]any)
	if ksug["status"] != "pending" || ksug["status_text"] != "待审" {
		t.Fatalf("kimi 建议状态 = %v/%v", ksug["status"], ksug["status_text"])
	}
}

func TestConfigTuningNoObservationUpstreamShowsNone(t *testing.T) {
	e, _ := newCTEnv(t) // 白名单 glm/kimi,流水为空——两上游都无建议
	_, resp := ctGet(t, e)
	for _, up := range []string{"glm", "kimi"} {
		row := ctUpstreamRow(t, resp, up)
		if row["suggestion"] != nil {
			t.Fatalf("%s 无建议时 suggestion 应为 null, got %v", up, row["suggestion"])
		}
		if row["suggestion_text"] != "无建议" {
			t.Fatalf("%s suggestion_text = %v, want 无建议", up, row["suggestion_text"])
		}
	}
}

func TestConfigTuningConfigMissingStillServesTuning(t *testing.T) {
	e, cfgPath := newCTEnv(t)
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	raw, resp := ctGet(t, e)
	cfg := resp["config"].(map[string]any)
	if cfg["found"] != false {
		t.Fatalf("config.found 应为 false: %v", cfg)
	}
	if !strings.Contains(string(raw), "不可读") && !strings.Contains(string(raw), "找不到") {
		t.Fatalf("应附人话说明: %s", raw)
	}
	if rows, ok := resp["upstreams"].([]any); !ok || len(rows) != 2 {
		t.Fatalf("配置缺席不得拖垮调参三列: %v", resp["upstreams"])
	}
}
