package daemon

// settings_read_test.go — 设置视图票02：GET /settings 读面验收钉子。
//
// 验收对照（票面）：
//   - 响应含全部 config 节与三类实体集合（dock.upstreams / providers / prices）；
//   - 任何密钥字段不存在明文（全响应字节 grep 断言 + 毒名单键值只许
//     {masked,has_key} 形态，掩码口径「••••+尾四位」）；
//   - effects 按 spec 操作级语义：provider_switch / tuning 即时生效；
//     其余一切写操作 needs_restart=true；新增上游首次切换独立字段标注；
//   - 未经 Bearer 请求被拒（401，随既有端点行为）。
//
// 测试风格：httptest 同族——打真监听端口（newQueryEnv + serveBg），与
// query_config_tuning_test.go 同款环境夹具（FERRYMAN_CONFIG 指临时配置）。

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ferryman/internal/config"
)

// srFixtureTOML 端点夹具配置：密钥面四处真钥（providers 条目 api_key +
// dock 旧单值 api_key + dock 上游条目 api_key + pushover_token）、三类实体
// 集合各有实物（dock.upstreams.glm / providers.glm / prices.glm）、阈值两键
// 供数字对账；data_dir 指临时目录（与其他测试隔离）。
func srFixtureTOML(dataDir string) string {
	return `[gate]
cc_mode = "observe"
[thresholds]
summarize_s = 1500
block_s = 2100
[server]
port = 15700
data_dir = "` + filepath.ToSlash(dataDir) + `"
[ferry]
provider = "glm"
[ferry.same_model]
enabled = true
upstreams = ["glm"]
threshold_min = 25
[tuning]
mode = "recommend"
[providers.glm]
base_url = "https://open.bigmodel.cn/api/paas/v4"
model = "glm-5.3"
api_key = "sk-ferry-provider-key-8888"
window = 131072
[prices.glm]
unit = "智谱积分"
per = 10000
[[prices.glm.versions]]
effective_from = "2026-09-01"
p_in = 6.9
p_cache = 1.7
p_out = 24
[dock]
active = "glm"
api_key = "sk-dock-legacy-key-7777"
[dock.upstreams.glm]
base_url = "https://open.bigmodel.cn/api/anthropic"
api_key = "sk-dock-upstream-key-6666"
model_map = { default = "glm-5.3" }
[notify]
enabled = false
pushover_token = "pushover-token-abcdefgh"
`
}

// newSettingsEnv 端点测试环境：FERRYMAN_CONFIG 指夹具配置（端点经
// ResolveConfigPath 读同一文件解析 providers/prices），Daemon 的 cfg 由该
// 文件装载（读面 config 节 = 守护内存生效配置）。
func newSettingsEnv(t *testing.T) (*queryEnv, string) {
	t.Helper()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(srFixtureTOML(filepath.Join(tmp, "data"))), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FERRYMAN_CONFIG", cfgPath)
	cfg, err := config.Load(cfgPath, true)
	if err != nil {
		t.Fatal(err)
	}
	e := newQueryEnv(t)
	e.d.Cfg = cfg
	return e, cfgPath
}

// srGet 调 GET /settings，200 时返回（原始字节, 解码 JSON）。
func srGet(t *testing.T, e *queryEnv) ([]byte, map[string]any) {
	t.Helper()
	code, raw := getRaw(t, e.port, "/settings", e.token)
	if code != 200 {
		t.Fatalf("GET /settings = %d %q", code, raw)
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("响应非 JSON: %v (%q)", err, raw)
	}
	return raw, resp
}

// srIsSecretKeyForTest 测试侧毒名单（票面口径：既有毒名单正则命中者），
// 独立于实现硬编码——实现漏掩时这里不陪葬。
func srIsSecretKeyForTest(k string) bool {
	lk := strings.ToLower(k)
	if strings.Contains(lk, "api_key") || strings.Contains(lk, "apikey") ||
		strings.Contains(lk, "secret") || strings.Contains(lk, "password") ||
		strings.Contains(lk, "credential") {
		return true
	}
	for _, tail := range []string{"key", "token", "auth"} {
		if lk == tail || strings.HasSuffix(lk, "_"+tail) || strings.HasSuffix(lk, "-"+tail) || strings.HasSuffix(lk, "."+tail) {
			return true
		}
	}
	return false
}

// srCheckMaskedLeaves 递归走查：凡毒名单键，值只许 {masked,has_key} 两键对象
// （无旁路明文形态）；返回命中掩码处数。
func srCheckMaskedLeaves(t *testing.T, node any) int {
	t.Helper()
	switch v := node.(type) {
	case map[string]any:
		n := 0
		for k, sub := range v {
			if srIsSecretKeyForTest(k) {
				obj, ok := sub.(map[string]any)
				if !ok {
					t.Fatalf("毒名单键 %q 的值非 masked 对象（旁路明文?）: %#v", k, sub)
				}
				if _, ok := obj["masked"].(string); !ok {
					t.Fatalf("毒名单键 %q masked 缺席或非串: %#v", k, obj)
				}
				if _, ok := obj["has_key"].(bool); !ok {
					t.Fatalf("毒名单键 %q has_key 缺席或非布尔: %#v", k, obj)
				}
				if len(obj) != 2 {
					t.Fatalf("masked 对象只许 masked/has_key 两键: %#v", obj)
				}
				n++
				continue
			}
			n += srCheckMaskedLeaves(t, sub)
		}
		return n
	case []any:
		n := 0
		for _, e := range v {
			n += srCheckMaskedLeaves(t, e)
		}
		return n
	}
	return 0
}

func TestSettingsReadRequiresBearer(t *testing.T) {
	e, _ := newSettingsEnv(t)
	// 无 Authorization 头（httpDo token="" 不设头）→ 401 随既有端点行为。
	if code, _ := getRaw(t, e.port, "/settings", ""); code != http.StatusUnauthorized {
		t.Fatalf("无 Bearer GET /settings = %d, want 401", code)
	}
	// 错 token 同拒。
	if code, _ := getRaw(t, e.port, "/settings", "wrong-token"); code != http.StatusUnauthorized {
		t.Fatalf("错 Bearer GET /settings = %d, want 401", code)
	}
}

func TestSettingsReadMasksAllSecrets(t *testing.T) {
	e, _ := newSettingsEnv(t)
	raw, resp := srGet(t, e)
	body := string(raw)

	// 验收钉子①：全响应字节 grep——四处真钥明文一个都不许出现。
	for _, secret := range []string{
		"sk-ferry-provider-key-8888",
		"sk-dock-legacy-key-7777",
		"sk-dock-upstream-key-6666",
		"pushover-token-abcdefgh",
	} {
		if strings.Contains(body, secret) {
			t.Fatalf("明文密钥泄漏: %q 出现在响应中", secret)
		}
	}

	// 验收钉子②：掩码口径「••••+尾四位」——四处各按其尾四位核对。
	wantMasked := map[string]string{
		"providers.glm.api_key":             "••••8888",
		"config.dock.api_key":               "••••7777",
		"config.dock.upstreams.glm.api_key": "••••6666",
		"config.notify.pushover_token":      "••••efgh",
	}
	provs := resp["providers"].(map[string]any)
	cfg := resp["config"].(map[string]any)
	dock := cfg["dock"].(map[string]any)
	got := map[string]string{
		"providers.glm.api_key":             provs["glm"].(map[string]any)["api_key"].(map[string]any)["masked"].(string),
		"config.dock.api_key":               dock["api_key"].(map[string]any)["masked"].(string),
		"config.dock.upstreams.glm.api_key": dock["upstreams"].(map[string]any)["glm"].(map[string]any)["api_key"].(map[string]any)["masked"].(string),
		"config.notify.pushover_token":      cfg["notify"].(map[string]any)["pushover_token"].(map[string]any)["masked"].(string),
	}
	for path, want := range wantMasked {
		if got[path] != want {
			t.Fatalf("%s 掩码 = %q, want %q", path, got[path], want)
		}
		if hasKey := provs["glm"].(map[string]any)["api_key"].(map[string]any)["has_key"].(bool); !hasKey {
			t.Fatalf("非空密钥 has_key 应为 true")
		}
	}

	// 验收钉子③：结构走查——凡毒名单键一律 {masked,has_key}，无旁路形态。
	if n := srCheckMaskedLeaves(t, resp); n != 4 {
		t.Fatalf("掩码处数 = %d, want 4（api_key×3 + pushover_token）", n)
	}
}

func TestSettingsReadAllSectionsAndEntities(t *testing.T) {
	e, _ := newSettingsEnv(t)
	_, resp := srGet(t, e)
	cfg, ok := resp["config"].(map[string]any)
	if !ok {
		t.Fatalf("config 缺席: %v", keys(resp))
	}
	// 全部 config 节在位（11 节）。
	for _, section := range []string{"gate", "thresholds", "watch", "server", "notify",
		"heartbeat", "question_watch", "wait_window", "ferry", "tuning", "dock"} {
		if _, ok := cfg[section]; !ok {
			t.Fatalf("config 缺节 %q（现有 %v）", section, keys(cfg))
		}
	}
	// 数字对账样例：UI 数字以本端点为唯一事实源。
	th := cfg["thresholds"].(map[string]any)
	if th["summarize_s"] != float64(1500) || th["block_s"] != float64(2100) {
		t.Fatalf("thresholds 对账失败: %v", th)
	}
	if cfg["gate"].(map[string]any)["cc_mode"] != "observe" {
		t.Fatalf("gate.cc_mode 对账失败: %v", cfg["gate"])
	}
	if cfg["tuning"].(map[string]any)["mode"] != "recommend" {
		t.Fatalf("tuning.mode 对账失败: %v", cfg["tuning"])
	}
	// 实体集合一：dock.upstreams（含模型映射）。
	dock := cfg["dock"].(map[string]any)
	ups, ok := dock["upstreams"].(map[string]any)
	if !ok || len(ups) != 1 {
		t.Fatalf("dock.upstreams 实体集合缺席: %v", dock["upstreams"])
	}
	glm := ups["glm"].(map[string]any)
	if glm["base_url"] != "https://open.bigmodel.cn/api/anthropic" {
		t.Fatalf("dock.upstreams.glm.base_url = %v", glm["base_url"])
	}
	if glm["model_map"].(map[string]any)["default"] != "glm-5.3" {
		t.Fatalf("dock.upstreams.glm.model_map.default = %v", glm["model_map"])
	}
	// 实体集合二：providers（摆渡供应商 [providers.*]）。
	provs, ok := resp["providers"].(map[string]any)
	if !ok || len(provs) != 1 {
		t.Fatalf("providers 实体集合缺席: %v", resp["providers"])
	}
	pg := provs["glm"].(map[string]any)
	if pg["model"] != "glm-5.3" || pg["window"] != float64(131072) ||
		pg["base_url"] != "https://open.bigmodel.cn/api/paas/v4" {
		t.Fatalf("providers.glm 对账失败: %v", pg)
	}
	// 实体集合三：prices（价格表 [prices.*]，版本序完整）。
	prs, ok := resp["prices"].(map[string]any)
	if !ok || len(prs) != 1 {
		t.Fatalf("prices 实体集合缺席: %v", resp["prices"])
	}
	book := prs["glm"].(map[string]any)
	if book["name"] != "glm" || book["unit"] != "智谱积分" || book["per"] != float64(10000) {
		t.Fatalf("prices.glm 头部对账失败: %v", book)
	}
	versions := book["versions"].([]any)
	if len(versions) != 1 {
		t.Fatalf("prices.glm.versions 应 1 条: %v", versions)
	}
	v0 := versions[0].(map[string]any)
	if v0["effective_from"] != "2026-09-01" || v0["p_in"] != 6.9 ||
		v0["p_cache"] != 1.7 || v0["p_out"] != float64(24) {
		t.Fatalf("prices.glm.versions[0] 对账失败: %v", v0)
	}
}

func TestSettingsReadEffectsOperationLevel(t *testing.T) {
	e, _ := newSettingsEnv(t)
	_, resp := srGet(t, e)
	eff, ok := resp["effects"].(map[string]any)
	if !ok {
		t.Fatalf("effects 缺席: %v", keys(resp))
	}
	// 即时生效：provider_switch 与 tuning（spec「读面」操作级语义）。
	for _, op := range []string{"provider_switch", "tuning"} {
		entry, ok := eff[op].(map[string]any)
		if !ok || entry["needs_restart"] != false {
			t.Fatalf("effects.%s 应即时生效（needs_restart=false）: %v", op, eff[op])
		}
	}
	// 独立字段：新增上游条目后对其首次 switch 亦需重启。
	entry, ok := eff["new_upstream_first_switch"].(map[string]any)
	if !ok || entry["needs_restart"] != true {
		t.Fatalf("effects.new_upstream_first_switch 应需重启: %v", eff["new_upstream_first_switch"])
	}
	// 其余一切写操作 needs_restart=true（九节 + 三实体 + restore 全覆盖）。
	for _, op := range []string{"gate", "thresholds", "watch", "notify", "heartbeat",
		"question_watch", "wait_window", "server",
		"dock_upstreams", "providers", "prices", "restore"} {
		entry, ok := eff[op].(map[string]any)
		if !ok || entry["needs_restart"] != true {
			t.Fatalf("effects.%s 应 needs_restart=true: %v", op, eff[op])
		}
	}
	// 每个 effects 条目都带布尔 needs_restart（UI 对账的字段契约）。
	for op, raw := range eff {
		entry, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("effects.%s 非对象: %#v", op, raw)
		}
		if _, ok := entry["needs_restart"].(bool); !ok {
			t.Fatalf("effects.%s 缺布尔 needs_restart: %#v", op, entry)
		}
	}
}

func TestSettingsReadConfigMissingStillServes(t *testing.T) {
	e, cfgPath := newSettingsEnv(t)
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	// 守护内存配置换缺省（模拟"盘上无配置起的服务"——Load 缺文件=全默认）。
	cfg, err := config.Load(cfgPath, true)
	if err != nil {
		t.Fatal(err)
	}
	e.d.Cfg = cfg
	raw, resp := srGet(t, e)
	// 缺 [dock] 节如实缺省（F11：nil=渡口不启动），不编造。
	if resp["config"].(map[string]any)["dock"] != nil {
		t.Fatalf("缺 [dock] 节应如实缺省: %v", resp["config"].(map[string]any)["dock"])
	}
	// providers/prices 盘上无文件 → 空集合（不炸、不编造）。
	if provs := resp["providers"].(map[string]any); len(provs) != 0 {
		t.Fatalf("providers 应空集合: %v", provs)
	}
	if prs := resp["prices"].(map[string]any); len(prs) != 0 {
		t.Fatalf("prices 应空集合: %v", prs)
	}
	// effects 元数据照常在位（面板不因配置缺席拖垮）。
	if _, ok := resp["effects"].(map[string]any); !ok {
		t.Fatalf("effects 缺席: %s", raw)
	}
	// 内置默认值对账（thresholds.summarize_s 默认 25min=1500）。
	th := resp["config"].(map[string]any)["thresholds"].(map[string]any)
	if th["summarize_s"] != float64(1500) {
		t.Fatalf("缺省配置 thresholds.summarize_s = %v, want 1500", th["summarize_s"])
	}
}
