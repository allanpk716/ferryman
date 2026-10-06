package daemon

// settings_entities_test.go — 设置视图票04：实体集合端点 + 引用完整性 +
// 密钥合并 + ferry 自键合并验收钉子。
//
// 验收对照（票面）：
//   - PUT 上游省略 api_key → 磁盘真钥逐字节保留；掩码占位串 → 同样保留；
//     新值 → 覆盖（读面只回尾四位——经盘上现读的 providers 实体面断言，
//     dock 实体读面是守护内存、写后未换挡属 needs_restart 语义）；
//   - DELETE 被 [ferry].chain/.provider 引用的 providers 条目 → 409+错误含
//     引用方名；被 same_model.upstreams 白名单引用的 dock 上游 → 409+点名
//     [ferry].same_model.upstreams；被 active 引用 → 409（既有守卫透传）；
//   - 新增上游条目落盘、其余节逐字节不动；prices 增删 versions 子树保真；
//   - PUT /settings/ferry 改 provider/chain → 落盘且 [ferry.same_model] 子表
//     逐字节不动；GET /settings/ferry 回新值；守护内存不换挡（重启才生效）；
//   - 守门：无/错 Bearer 401；未知路径 404（auth 前）；坏 JSON 400 不落审计；
//     替身 404；实体写同样经单写者锁+写前快照+审计（密钥 <masked>）。
//
// 夹具复用票02/03 同族环境（FERRYMAN_CONFIG 指临时配置、冻结钟、真监听端口），
// 独立底稿：dock 三上游（glm=active、kimi=same_model 白名单、ds=自由身）、
// providers 三条（glm 被 provider+chain 引用、kimi 被 chain 引用、free 自由）、
// prices 两条（glm 带 p_cache 版本、kimi 无 p_cache 版本）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ferryman/internal/config"
	"ferryman/internal/ferry"
	"ferryman/internal/prices"
)

// seEntTOML 票04 实体端点夹具底稿（data_dir 钉沙箱 → 审计文件也落沙箱）。
func seEntTOML(dataDir string) string {
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
chain = ["glm", "kimi"]
[ferry.same_model]
enabled = true
upstreams = ["kimi"]
threshold_min = 20
[ferry.same_model.ceiling]
kimi = 15
[tuning]
mode = "recommend"
[providers.glm]
base_url = "https://open.bigmodel.cn/api/paas/v4"
model = "glm-5.3"
api_key = "sk-provider-glm-8888"
window = 131072
[providers.kimi]
base_url = "https://api.moonshot.cn/v1"
model = "kimi-for-coding"
api_key = "sk-provider-kimi-6666"
window = 131072
[providers.free]
base_url = "https://api.free.example/v1"
model = "free-m"
api_key = "sk-provider-free-5555"
window = 131072
[prices.glm]
unit = "智谱积分"
per = 10000
[[prices.glm.versions]]
effective_from = "2026-09-01"
p_in = 6.9
p_cache = 1.7
p_out = 24
[prices.kimi]
unit = "元"
per = 10000
[[prices.kimi.versions]]
effective_from = "2026-10-01"
p_in = 1
p_out = 4
[dock]
listen = "127.0.0.1:15722"
active = "glm"
[dock.upstreams.glm]
base_url = "https://open.bigmodel.cn/api/anthropic"
api_key = "sk-dock-glm-1234"
model_map = { default = "glm-5.3" }
balance_url = "https://open.bigmodel.cn/api/user/balance"
[dock.upstreams.kimi]
base_url = "https://api.kimi.com/coding/"
api_key = "sk-dock-kimi-5678"
model_map = { default = "kimi-for-coding" }
[dock.upstreams.ds]
base_url = "https://api.deepseek.com/anthropic"
api_key = "sk-dock-ds-9012"
model_map = { default = "deepseek-flash" }
[notify]
enabled = false
`
}

// seNewEnv 实体端点测试环境（票02 newSettingsEnv 同款装配、独立底稿）。
func seNewEnv(t *testing.T) (*queryEnv, string) {
	t.Helper()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(seEntTOML(filepath.Join(tmp, "data"))), 0o600); err != nil {
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

// sePut 主协程实体 PUT 助手。
func sePut(t *testing.T, e *queryEnv, path string, body map[string]any) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return swReqNF(http.MethodPut, e.port, e.token, path, raw)
}

// seDel 主协程实体 DELETE 助手。
func seDel(t *testing.T, e *queryEnv, path string) (int, []byte) {
	t.Helper()
	return swReqNF(http.MethodDelete, e.port, e.token, path, nil)
}

// seDisk 读盘上配置全文。
func seDisk(t *testing.T, cfgPath string) string {
	t.Helper()
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// seRegion 截取 start 标记（含）到 end 标记（不含）的字节区间——保真断言用。
func seRegion(s, start, end string) string {
	_, rest, ok := strings.Cut(s, start)
	if !ok {
		return ""
	}
	region, _, ok := strings.Cut(rest, end)
	if !ok {
		return ""
	}
	return start + region
}

// ---- ①② 上游实体：密钥合并 + 新增/替换落盘 ----

// TestSettingsEntitiesUpstreamPutSecretMerge F3 密钥合并落到上游实体 PUT：
// 省略 api_key / 掩码占位串 → 磁盘真钥逐字节保留；显式新值 → 覆盖；旁观
// 条目（glm/ds）与 [notify] 尾巴逐字节不动。
func TestSettingsEntitiesUpstreamPutSecretMerge(t *testing.T) {
	e, cfgPath := seNewEnv(t)
	const diskKey = `api_key = "sk-dock-kimi-5678"`

	assertKept := func(stage string) {
		t.Helper()
		if !strings.Contains(seDisk(t, cfgPath), diskKey) {
			t.Fatalf("%s: 盘上真钥未逐字节保留", stage)
		}
		cfg, err := config.Load(cfgPath, true)
		if err != nil {
			t.Fatalf("%s: 写后 Load: %v", stage, err)
		}
		if got := cfg.Dock.Upstreams["kimi"].APIKey; got != "sk-dock-kimi-5678" {
			t.Fatalf("%s: 回读 kimi api_key = %q", stage, got)
		}
	}

	// ① 省略 api_key：整条替换不得误清真钥。
	code, raw := sePut(t, e, "/settings/dock/upstreams/kimi", map[string]any{
		"base_url":  "https://api.kimi.com/coding/",
		"model_map": map[string]any{"default": "kimi-for-coding"},
	})
	if code != http.StatusOK {
		t.Fatalf("省略 api_key PUT = %d %q, want 200", code, raw)
	}
	assertKept("省略")

	// ② 掩码占位串（读面回显形）：同样保留现值。
	code, raw = sePut(t, e, "/settings/dock/upstreams/kimi", map[string]any{
		"base_url": "https://api.kimi.com/coding/", "api_key": "••••5678",
		"model_map": map[string]any{"default": "kimi-for-coding"},
	})
	if code != http.StatusOK {
		t.Fatalf("掩码占位 PUT = %d %q, want 200", code, raw)
	}
	assertKept("掩码占位串")

	// 旁观条目与尾巴逐字节不动（文本手术只动 kimi 跨度）。
	disk := seDisk(t, cfgPath)
	for _, region := range []string{
		seRegion(disk, "[dock.upstreams.glm]", "[dock.upstreams.kimi]"),
		seRegion(disk, "[dock.upstreams.ds]", "[notify]"),
	} {
		if region == "" {
			t.Fatal("保真区间截取失败（夹具形态变了?）")
		}
	}
	base := seEntTOML(filepath.Join(filepath.Dir(cfgPath), "data"))
	if got := seRegion(disk, "[dock.upstreams.glm]", "[dock.upstreams.kimi]"); got != seRegion(base, "[dock.upstreams.glm]", "[dock.upstreams.kimi]") {
		t.Errorf("旁观条目 glm 被殃及:\n%s", got)
	}
	if got := seRegion(disk, "[dock.upstreams.ds]", "[notify]"); got != seRegion(base, "[dock.upstreams.ds]", "[notify]") {
		t.Errorf("旁观条目 ds/尾巴被殃及:\n%s", got)
	}

	// ③ 显式新值：覆盖。
	code, raw = sePut(t, e, "/settings/dock/upstreams/kimi", map[string]any{
		"base_url": "https://api.kimi.com/coding/", "api_key": "sk-dock-rotate-9999",
		"model_map": map[string]any{"default": "kimi-for-coding"},
	})
	if code != http.StatusOK {
		t.Fatalf("显式新值 PUT = %d %q, want 200", code, raw)
	}
	cfg, err := config.Load(cfgPath, true)
	if err != nil {
		t.Fatalf("显式新值后 Load: %v", err)
	}
	if got := cfg.Dock.Upstreams["kimi"].APIKey; got != "sk-dock-rotate-9999" {
		t.Fatalf("显式新值未覆盖: %q", got)
	}

	// ④ 怪类型（数字密钥）：400 拒写、盘上真钥不动。
	code, raw = sePut(t, e, "/settings/dock/upstreams/kimi", map[string]any{
		"base_url": "https://api.kimi.com/coding/", "api_key": 12345,
		"model_map": map[string]any{"default": "kimi-for-coding"},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("怪类型 PUT = %d %q, want 400", code, raw)
	}
	if got := seDisk(t, cfgPath); !strings.Contains(got, `api_key = "sk-dock-rotate-9999"`) {
		t.Fatal("怪类型拒写后盘上真钥被动了")
	}
}

// TestSettingsEntitiesUpstreamCreateAndReplaceActive 新增条目落盘、其余节
// 逐字节不动；替换 active 条目本身可行（守卫守的是删除/悬空，不是就地换值）。
func TestSettingsEntitiesUpstreamCreateAndReplaceActive(t *testing.T) {
	e, cfgPath := seNewEnv(t)
	before := seDisk(t, cfgPath)

	// 新增：EOF 追加完整条目块（dialect 缺省归一 anthropic 恒显式落盘）。
	code, raw := sePut(t, e, "/settings/dock/upstreams/newup", map[string]any{
		"base_url":    "https://new.example/api",
		"model_map":   map[string]any{"default": "m1", "opus": "m2"},
		"text_only":   []any{"img-model"},
		"balance_url": "https://new.example/bal",
		"codex":       "unsupported",
	})
	if code != http.StatusOK {
		t.Fatalf("新增上游 PUT = %d %q, want 200", code, raw)
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil || resp["saved"] != true || resp["needs_restart"] != true {
		t.Fatalf("新增响应 = %q (%v)", raw, err)
	}
	wantBlock := "\n[dock.upstreams.newup]\nbase_url = \"https://new.example/api\"\n" +
		"api_key = \"\"\nmodel_map = { default = \"m1\", opus = \"m2\" }\n" +
		"text_only = [\"img-model\"]\nbalance_url = \"https://new.example/bal\"\n" +
		"dialect = \"anthropic\"\ncodex = \"unsupported\"\n"
	if got := seDisk(t, cfgPath); got != before+wantBlock {
		t.Fatalf("新增条目落盘不符：\n--got--\n%s\n--want 尾块--\n%s", got, wantBlock)
	}
	cfg, err := config.Load(cfgPath, true)
	if err != nil {
		t.Fatalf("新增后 Load: %v", err)
	}
	up := cfg.Dock.Upstreams["newup"]
	if up.BaseURL != "https://new.example/api" || up.ModelMap["opus"] != "m2" ||
		up.Dialect != "anthropic" || up.Codex != "unsupported" || len(up.TextOnly) != 1 {
		t.Fatalf("newup 回读不符: %+v", up)
	}

	// 替换 active 条目（glm）：名字仍在、active 不悬空，允许就地换值。
	code, raw = sePut(t, e, "/settings/dock/upstreams/glm", map[string]any{
		"base_url": "https://open.bigmodel.cn/api/anthropic",
		"model_map": map[string]any{
			"default": "glm-5.3", "sonnet": "glm-5.3-air", "codex": "glm-5.3"},
		"balance_url": "https://open.bigmodel.cn/api/user/balance",
	})
	if code != http.StatusOK {
		t.Fatalf("替换 active 条目 PUT = %d %q, want 200", code, raw)
	}
	cfg, err = config.Load(cfgPath, true)
	if err != nil {
		t.Fatalf("替换后 Load: %v", err)
	}
	if got := cfg.Dock.Upstreams["glm"].ModelMap["sonnet"]; got != "glm-5.3-air" {
		t.Fatalf("active 条目替换未生效: %q", got)
	}
	if cfg.Dock.Upstreams["glm"].APIKey != "sk-dock-glm-1234" {
		t.Fatal("替换 active 条目时真钥被误清（省略 api_key 应保留）")
	}
}

// ---- ③ 引用完整性：DELETE 守卫矩阵 ----

// TestSettingsEntitiesUpstreamDeleteGuards 上游删除守卫：白名单引用 → 409
// 点名 [ferry].same_model.upstreams（daemon 层补位）；active 引用 → 409
// （config 层既有守卫透传）；自由身 → 200 落删；未知 → 404。
func TestSettingsEntitiesUpstreamDeleteGuards(t *testing.T) {
	e, cfgPath := seNewEnv(t)

	// kimi 在 same_model 白名单：409 + 点名引用方。
	code, raw := seDel(t, e, "/settings/dock/upstreams/kimi")
	if code != http.StatusConflict {
		t.Fatalf("白名单引用 DELETE = %d %q, want 409", code, raw)
	}
	if !strings.Contains(string(raw), "[ferry].same_model.upstreams") {
		t.Fatalf("409 文案应点名 [ferry].same_model.upstreams: %q", raw)
	}

	// glm 是 active（不在白名单）：既有 active 守卫透传 → 409。
	code, raw = seDel(t, e, "/settings/dock/upstreams/glm")
	if code != http.StatusConflict {
		t.Fatalf("active 引用 DELETE = %d %q, want 409", code, raw)
	}
	if !strings.Contains(string(raw), "active") {
		t.Fatalf("active 守卫文案应含 active: %q", raw)
	}

	// 拒删路径盘上条目仍在。
	if got := seDisk(t, cfgPath); !strings.Contains(got, "[dock.upstreams.kimi]") || !strings.Contains(got, "[dock.upstreams.glm]") {
		t.Fatal("拒删路径条目被动了")
	}

	// ds 自由身：200 删除、其余条目保真。
	code, raw = seDel(t, e, "/settings/dock/upstreams/ds")
	if code != http.StatusOK {
		t.Fatalf("自由身 DELETE = %d %q, want 200", code, raw)
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil || resp["deleted"] != true || resp["needs_restart"] != true {
		t.Fatalf("删除响应 = %q (%v)", raw, err)
	}
	cfg, err := config.Load(cfgPath, true)
	if err != nil {
		t.Fatalf("删除后 Load: %v", err)
	}
	if _, ok := cfg.Dock.Upstreams["ds"]; ok {
		t.Fatal("ds 应已删除")
	}
	if len(cfg.Dock.Upstreams) != 2 {
		t.Fatalf("其余条目应保真: %v", cfg.Dock.Upstreams)
	}

	// 未知条目：404。
	if code, raw = seDel(t, e, "/settings/dock/upstreams/ghost"); code != http.StatusNotFound {
		t.Fatalf("未知条目 DELETE = %d %q, want 404", code, raw)
	}
}

// TestSettingsEntitiesProviderDeleteGuards providers 删除守卫（config 层
// 票01 落地、daemon 透传）：被 [ferry].provider / [ferry].chain 引用 → 409
// 点名引用方；自由身 → 200；未知 → 404。
func TestSettingsEntitiesProviderDeleteGuards(t *testing.T) {
	e, cfgPath := seNewEnv(t)

	// glm 被 provider 单键引用（guardProviderRefs 先查 provider）。
	code, raw := seDel(t, e, "/settings/providers/glm")
	if code != http.StatusConflict {
		t.Fatalf("provider 引用 DELETE = %d %q, want 409", code, raw)
	}
	if !strings.Contains(string(raw), "[ferry].provider") {
		t.Fatalf("409 文案应点名 [ferry].provider: %q", raw)
	}

	// kimi 只被 chain 引用。
	code, raw = seDel(t, e, "/settings/providers/kimi")
	if code != http.StatusConflict {
		t.Fatalf("chain 引用 DELETE = %d %q, want 409", code, raw)
	}
	if !strings.Contains(string(raw), "[ferry].chain") {
		t.Fatalf("409 文案应点名 [ferry].chain: %q", raw)
	}

	// free 自由身：200 删除。
	if code, raw = seDel(t, e, "/settings/providers/free"); code != http.StatusOK {
		t.Fatalf("自由身 DELETE = %d %q, want 200", code, raw)
	}
	ps, err := ferry.LoadProviders(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ps["free"]; ok {
		t.Fatal("free 应已删除")
	}
	if len(ps) != 2 {
		t.Fatalf("其余供应商应保真: %v", ps)
	}

	if code, raw = seDel(t, e, "/settings/providers/ghost"); code != http.StatusNotFound {
		t.Fatalf("未知供应商 DELETE = %d %q, want 404", code, raw)
	}
}

// ---- ① providers/prices 实体 PUT ----

// TestSettingsEntitiesProviderPut providers 实体 PUT：省略 api_key 真钥保留、
// 新值覆盖后读面（盘上现读）只回尾四位掩码、新增条目落盘。
func TestSettingsEntitiesProviderPut(t *testing.T) {
	e, cfgPath := seNewEnv(t)

	// 省略 api_key：盘上真钥逐字节保留。
	code, raw := sePut(t, e, "/settings/providers/free", map[string]any{
		"base_url": "https://api.free.example/v2", "model": "free-m2",
	})
	if code != http.StatusOK {
		t.Fatalf("省略 api_key PUT = %d %q, want 200", code, raw)
	}
	ps, err := ferry.LoadProviders(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if ps["free"].APIKey != "sk-provider-free-5555" {
		t.Fatalf("省略 api_key 误清真钥: %q", ps["free"].APIKey)
	}
	if ps["free"].BaseURL != "https://api.free.example/v2" || ps["free"].Model != "free-m2" {
		t.Fatalf("free 覆盖不符: %+v", ps["free"])
	}

	// 新值覆盖：读面（GET /settings 的 providers 实体＝盘上现读）只回尾四位。
	code, raw = sePut(t, e, "/settings/providers/free", map[string]any{
		"base_url": "https://api.free.example/v2", "model": "free-m2",
		"api_key":    "sk-provider-rotate-1111",
		"extra_body": map[string]any{"thinking": "disabled"},
	})
	if code != http.StatusOK {
		t.Fatalf("新值 PUT = %d %q, want 200", code, raw)
	}
	gcode, graw := swReqNF(http.MethodGet, e.port, e.token, "/settings", nil)
	if gcode != http.StatusOK {
		t.Fatalf("GET /settings = %d", gcode)
	}
	if strings.Contains(string(graw), "sk-provider-rotate-1111") {
		t.Fatal("新真钥明文泄漏进读面")
	}
	var settings map[string]any
	if err := json.Unmarshal(graw, &settings); err != nil {
		t.Fatal(err)
	}
	provs, _ := settings["providers"].(map[string]any)
	free, _ := provs["free"].(map[string]any)
	key, _ := free["api_key"].(map[string]any)
	if key["masked"] != "••••1111" || key["has_key"] != true {
		t.Fatalf("读面 api_key = %v, want 掩码只回尾四位", free["api_key"])
	}

	// 新增条目：落盘可载。
	code, raw = sePut(t, e, "/settings/providers/newco", map[string]any{
		"base_url": "https://newco.example/v1", "model": "nc-1", "api_key": "sk-newco-2222",
		"window": 262144, "protocol": "anthropic",
	})
	if code != http.StatusOK {
		t.Fatalf("新增 PUT = %d %q, want 200", code, raw)
	}
	ps, err = ferry.LoadProviders(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	nc := ps["newco"]
	if nc.BaseURL != "https://newco.example/v1" || nc.APIKey != "sk-newco-2222" ||
		nc.Window != 262144 || nc.Protocol != "anthropic" {
		t.Fatalf("newco 回读不符: %+v", nc)
	}
}

// TestSettingsEntitiesPricePutDelete prices 实体：新增/替换/删除，旁观条目
// 的 versions 子树逐字节保真；versions 整组覆盖（p_cache 缺省=nil 语义）。
func TestSettingsEntitiesPricePutDelete(t *testing.T) {
	e, cfgPath := seNewEnv(t)
	base := seEntTOML(filepath.Join(filepath.Dir(cfgPath), "data"))

	// 新增：[[prices.glm.versions]] 子树逐字节保真。
	code, raw := sePut(t, e, "/settings/prices/ds", map[string]any{
		"unit": "元", "per": 10000,
		"versions": []any{
			map[string]any{"effective_from": "2026-10-05", "p_in": 2, "p_out": 8},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("新增价表 PUT = %d %q, want 200", code, raw)
	}
	disk := seDisk(t, cfgPath)
	if got, want := seRegion(disk, "[prices.glm]", "[prices.kimi]"), seRegion(base, "[prices.glm]", "[prices.kimi]"); got != want {
		t.Fatalf("旁观价表 glm versions 子树被殃及:\n--got--\n%s\n--want--\n%s", got, want)
	}

	// 替换 kimi：versions 整组覆盖（两条，第二条无 p_cache）。
	code, raw = sePut(t, e, "/settings/prices/kimi", map[string]any{
		"unit": "元", "per": 10000,
		"versions": []any{
			map[string]any{"effective_from": "2026-09-01", "p_in": 1.5, "p_cache": 0.4, "p_out": 5},
			map[string]any{"effective_from": "2026-10-01", "p_in": 2, "p_out": 6},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("替换价表 PUT = %d %q, want 200", code, raw)
	}
	books := prices.LoadPrices(cfgPath)
	kimi := books["kimi"]
	if len(kimi.Versions) != 2 {
		t.Fatalf("kimi versions = %d, want 2（整组覆盖）", len(kimi.Versions))
	}
	if kimi.Versions[0].PCache == nil || *kimi.Versions[0].PCache != 0.4 {
		t.Fatalf("版本0 p_cache 不符: %+v", kimi.Versions[0])
	}
	if kimi.Versions[1].PCache != nil {
		t.Fatalf("版本1 p_cache 应缺省=nil: %+v", kimi.Versions[1])
	}

	// 删除 kimi：glm 条目与 versions 子树仍逐字节保真（kimi 已删，区间断言
	// 换成对 glm 子树原文的精确字节包含）。
	if code, raw = seDel(t, e, "/settings/prices/kimi"); code != http.StatusOK {
		t.Fatalf("价表 DELETE = %d %q, want 200", code, raw)
	}
	disk = seDisk(t, cfgPath)
	glmBlock := seRegion(base, "[prices.glm]", "[prices.kimi]")
	if !strings.Contains(disk, strings.TrimRight(glmBlock, "\n")) {
		t.Fatalf("删除旁观价表 glm（含 versions 子树）被殃及:\n%s", disk)
	}
	if strings.Contains(disk, "prices.kimi") {
		t.Fatal("kimi 价表应整树删除（含 versions）")
	}
	if _, ok := prices.LoadPrices(cfgPath)["kimi"]; ok {
		t.Fatal("kimi 应已删除")
	}

	if code, raw = seDel(t, e, "/settings/prices/ghost"); code != http.StatusNotFound {
		t.Fatalf("未知价表 DELETE = %d %q, want 404", code, raw)
	}
}

// ---- ④ ferry 自键合并 + 盘上对账读面 ----

// TestSettingsEntitiesFerryOwnKeys PUT /settings/ferry：provider/chain 落盘、
// [ferry.same_model] 子表（含 ceiling）逐字节不动；GET /settings/ferry 回
// 盘上新值；守护内存不换挡（重启才生效）；对象形子表键明确拒写。
func TestSettingsEntitiesFerryOwnKeys(t *testing.T) {
	e, cfgPath := seNewEnv(t)
	snap := swSwapSnapshot(t)
	base := seEntTOML(filepath.Join(filepath.Dir(cfgPath), "data"))

	code, raw := sePut(t, e, "/settings/ferry", map[string]any{
		"provider": "kimi", "chain": []any{"kimi", "glm"},
	})
	if code != http.StatusOK {
		t.Fatalf("PUT /settings/ferry = %d %q, want 200", code, raw)
	}
	// same_model 子树逐字节不动（含 ceiling 子表）。
	disk := seDisk(t, cfgPath)
	if got, want := seRegion(disk, "[ferry.same_model]", "[tuning]"), seRegion(base, "[ferry.same_model]", "[tuning]"); got != want {
		t.Fatalf("[ferry.same_model] 子表被动：\n--got--\n%s\n--want--\n%s", got, want)
	}
	cfg, err := config.Load(cfgPath, true)
	if err != nil {
		t.Fatalf("写后 Load: %v", err)
	}
	if cfg.FerryProvider != "kimi" || len(cfg.FerryChain) != 2 || cfg.FerryChain[0] != "kimi" {
		t.Fatalf("ferry 自键落盘不符: provider=%q chain=%v", cfg.FerryProvider, cfg.FerryChain)
	}

	// GET /settings/ferry 回盘上新值（对账读面）。
	gcode, graw := swReqNF(http.MethodGet, e.port, e.token, "/settings/ferry", nil)
	if gcode != http.StatusOK {
		t.Fatalf("GET /settings/ferry = %d %q, want 200", gcode, graw)
	}
	var ferrySec map[string]any
	if err := json.Unmarshal(graw, &ferrySec); err != nil {
		t.Fatal(err)
	}
	if ferrySec["provider"] != "kimi" {
		t.Fatalf("GET /settings/ferry provider = %v, want kimi", ferrySec["provider"])
	}
	chain, _ := ferrySec["chain"].([]any)
	if len(chain) != 2 || chain[0] != "kimi" || chain[1] != "glm" {
		t.Fatalf("GET /settings/ferry chain = %v", ferrySec["chain"])
	}
	sm, _ := ferrySec["same_model"].(map[string]any)
	if sm == nil || sm["enabled"] != true {
		t.Fatalf("GET /settings/ferry same_model = %v（子表应在场）", ferrySec["same_model"])
	}

	// 守护内存不换挡：写面只落盘，换挡在重启（needs_restart 语义）。
	if e.d.Cfg.FerryProvider != "glm" {
		t.Fatalf("守护内存 ferry = %q, want glm（写后不得热换挡）", e.d.Cfg.FerryProvider)
	}

	// 审计行 + 快照桩：实体面同款接线。
	lines := swAuditLines(t, e)
	if len(lines) != 1 {
		t.Fatalf("审计行数 = %d, want 1", len(lines))
	}
	if lines[0]["section"] != "ferry" || lines[0]["outcome"] != "saved" {
		t.Fatalf("审计行头 = %v", lines[0])
	}
	if calls := snap.get(); len(calls) != 1 || !strings.Contains(calls[0], "ferry") {
		t.Fatalf("快照桩调用 = %v", calls)
	}

	// 对象形子表键（same_model）明确拒写：自键层写只收标量/数组。
	code, raw = sePut(t, e, "/settings/ferry", map[string]any{
		"provider": "kimi", "same_model": map[string]any{"enabled": false},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("对象形子表键 PUT = %d %q, want 400", code, raw)
	}
	if !strings.Contains(string(raw), "子表") {
		t.Fatalf("400 文案应说明子表不经本端点: %q", raw)
	}

	// chain 引用未定义 provider：Load 期校验拒写、盘不动。
	before := seDisk(t, cfgPath)
	code, raw = sePut(t, e, "/settings/ferry", map[string]any{
		"provider": "kimi", "chain": []any{"ghost"},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("坏 chain PUT = %d %q, want 400", code, raw)
	}
	if seDisk(t, cfgPath) != before {
		t.Fatal("拒写路径盘上被动了")
	}
}

// ---- 守门与审计 ----

// TestSettingsEntitiesGuards 守门面：无/错 Bearer 401；未知路径 404（auth 前，
// 无 token 也 404 非 401）；实体路径不支持的方法组合 404；坏 JSON 400 不落
// 审计；替身（非 *Daemon）404。
func TestSettingsEntitiesGuards(t *testing.T) {
	e, _ := seNewEnv(t)
	b, _ := json.Marshal(map[string]any{"base_url": "https://x.example"})

	// 无/错 Bearer：实体路由已知 → 401。
	for _, p := range []string{"/settings/dock/upstreams/kimi", "/settings/providers/glm", "/settings/prices/glm"} {
		if c, _ := swReqNF(http.MethodPut, e.port, "", p, b); c != http.StatusUnauthorized {
			t.Fatalf("无 Bearer PUT %s = %d, want 401", p, c)
		}
		if c, _ := swReqNF(http.MethodDelete, e.port, "wrong", p, nil); c != http.StatusUnauthorized {
			t.Fatalf("错 Bearer DELETE %s = %d, want 401", p, c)
		}
	}

	// 未知路径 404 在 auth 前（无 token 也 404 非 401）。
	for _, p := range []string{"/settings/unknown", "/settings/gate/deeper", "/settings/dock",
		"/settings/providers", "/settings/prices", "/settings/dock/upstreams", "/settings/providers/a/b"} {
		if c, _ := swReqNF(http.MethodPut, e.port, "", p, b); c != http.StatusNotFound {
			t.Fatalf("PUT %s = %d, want 404（auth 前）", p, c)
		}
	}
	// DELETE 落在节级路径：无此面 → 404（auth 前）。
	if c, _ := swReqNF(http.MethodDelete, e.port, "", "/settings/gate", nil); c != http.StatusNotFound {
		t.Fatalf("DELETE /settings/gate = %d, want 404", c)
	}

	// 坏 JSON：400 且不落审计行（未成写尝试）。
	if c, _ := swReqNF(http.MethodPut, e.port, e.token, "/settings/providers/glm", []byte("not-json")); c != http.StatusBadRequest {
		t.Fatalf("坏 JSON PUT = %d, want 400", c)
	}
	if lines := swAuditLines(t, e); len(lines) != 0 {
		t.Fatalf("坏 JSON 不应落审计行: %v", lines)
	}

	// 替身（非 *Daemon）：实体 PUT/DELETE 与 ferry 读面均 404。
	h := makeHandler(&stopDaemon{}, "tok-se", nil, nil)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPut, "/settings/providers/x"},
		{http.MethodDelete, "/settings/prices/x"},
		{http.MethodGet, "/settings/ferry"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer tok-se")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("替身 %s %s = %d %q, want 404", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

// TestSettingsEntitiesAuditAndLock 实体写经票03 同款接线：单写者锁内完成、
// 审计行密钥 <masked>（改前/改后真钥都不出现）、快照桩写前在位调用。
func TestSettingsEntitiesAuditAndLock(t *testing.T) {
	e, cfgPath := seNewEnv(t)
	snap := swSwapSnapshot(t)

	code, raw := sePut(t, e, "/settings/providers/free", map[string]any{
		"base_url": "https://api.free.example/v1", "model": "free-m",
		"api_key": "sk-provider-new-4321",
	})
	if code != http.StatusOK {
		t.Fatalf("PUT providers/free = %d %q, want 200", code, raw)
	}

	auditRaw, err := os.ReadFile(swAuditPath(e))
	if err != nil {
		t.Fatalf("读审计文件: %v", err)
	}
	audit := string(auditRaw)
	if strings.Contains(audit, "sk-provider-new-4321") || strings.Contains(audit, "sk-provider-free-5555") {
		t.Fatal("改前/改后真钥泄漏进审计行")
	}
	lines := swAuditLines(t, e)
	if len(lines) != 1 {
		t.Fatalf("审计行数 = %d, want 1", len(lines))
	}
	ln := lines[0]
	if ln["section"] != "providers.free" || ln["outcome"] != "saved" || ln["ts"] != e.t0 {
		t.Fatalf("审计行头 = %v", ln)
	}
	before, _ := ln["before"].(map[string]any)
	after, _ := ln["after"].(map[string]any)
	if before == nil || before["api_key"] != "<masked>" || after == nil || after["api_key"] != "<masked>" {
		t.Fatalf("审计密钥面 = before:%v after:%v, want <masked>", ln["before"], ln["after"])
	}
	if after["model"] != "free-m" {
		t.Fatalf("after.model = %v, want free-m（非密钥不误伤）", after["model"])
	}
	if calls := snap.get(); len(calls) != 1 || !strings.Contains(calls[0], "providers.free") {
		t.Fatalf("快照桩调用 = %v, want 1 次点名 providers.free", calls)
	}

	// 真钥照常落盘（掩码只在审计面）。
	if got := seDisk(t, cfgPath); !strings.Contains(got, `api_key = "sk-provider-new-4321"`) {
		t.Fatal("新真钥应照常落盘")
	}
}

// TestSettingsEntitiesBadBodyTypes 坏 body 形态：字段类型不对 → 400 且盘不动
// （拒写不落半形）。
func TestSettingsEntitiesBadBodyTypes(t *testing.T) {
	e, cfgPath := seNewEnv(t)
	before := seDisk(t, cfgPath)

	cases := []struct {
		path string
		body map[string]any
		want string
	}{
		{"/settings/dock/upstreams/kimi", map[string]any{
			"base_url": "https://api.kimi.com/coding/", "model_map": map[string]any{"default": 42}}, "model_map"},
		{"/settings/dock/upstreams/kimi", map[string]any{
			"base_url": 99, "api_key": "x"}, "base_url"},
		{"/settings/providers/glm", map[string]any{
			"base_url": "https://x/v1", "window": "abc"}, "window"},
		{"/settings/prices/glm", map[string]any{
			"unit": "元", "versions": "nope"}, "versions"},
		{"/settings/prices/glm", map[string]any{
			"unit": "元", "versions": []any{map[string]any{"effective_from": 2026}}}, "effective_from"},
	}
	for _, tc := range cases {
		code, raw := sePut(t, e, tc.path, tc.body)
		if code != http.StatusBadRequest {
			t.Fatalf("PUT %s body=%v = %d %q, want 400", tc.path, tc.body, code, raw)
		}
		if !strings.Contains(string(raw), tc.want) {
			t.Fatalf("400 文案应点名 %q: %q", tc.want, raw)
		}
		if got := seDisk(t, cfgPath); got != before {
			t.Fatalf("坏 body 拒写路径盘上被动了: %s", tc.path)
		}
	}
}
