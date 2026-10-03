// dock_upstreams_test.go — 票01：渡口上游表（[dock.upstreams] + [dock].active）
// 解析、校验、ActiveUpstream 解析单源与首启迁移的验收钉子。
//
// 验收口径（票 01）：
//   - 旧单值首启迁移为 cc-switch 条目并保持 active；同次生成三条未激活预置；
//   - 幂等（二次启动跳过）；已存在新表时跳过迁移、解析以新表+active 为准；
//   - model_map 校验：非本地 base_url 条目必含 default；本地（守卫透传域）豁免；
//   - 迁移原子写（临时文件+rename），失败不改原文件。
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// ---- 新表解析 ----

func TestLoadDockUpstreamsTable(t *testing.T) {
	f := filepath.Join(t.TempDir(), "dock-table.toml")
	src := `
[dock]
listen = "127.0.0.1:15922"
active = "zhipu"

[dock.upstreams."cc-switch"]
base_url = "http://127.0.0.1:15721"

[dock.upstreams.zhipu]
base_url = "https://open.bigmodel.cn/api/anthropic"
api_key = "sk-z"
text_only = ["glm-5.3"]
balance_url = "https://open.bigmodel.cn/api/user/balance"
[dock.upstreams.zhipu.model_map]
default = "glm-5.3"
haiku = "glm-5.3-flash"
`
	if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	d := cfg.Dock
	if d == nil {
		t.Fatal("cfg.Dock = nil")
	}
	if d.Listen != "127.0.0.1:15922" || d.Active != "zhipu" {
		t.Fatalf("listen/active = %q/%q", d.Listen, d.Active)
	}
	if len(d.Upstreams) != 2 {
		t.Fatalf("upstreams 数 = %d, want 2: %v", len(d.Upstreams), d.Upstreams)
	}
	fb := d.Upstreams["cc-switch"]
	if fb.BaseURL != "http://127.0.0.1:15721" || fb.APIKey != "" ||
		len(fb.ModelMap) != 0 || fb.BalanceURL != "" {
		t.Fatalf("cc-switch 条目 = %+v（缺省字段应为空：不配不显示）", fb)
	}
	zp := d.Upstreams["zhipu"]
	if zp.APIKey != "sk-z" || zp.BalanceURL != "https://open.bigmodel.cn/api/user/balance" {
		t.Fatalf("zhipu 条目 = %+v", zp)
	}
	if zp.ModelMap["default"] != "glm-5.3" || zp.ModelMap["haiku"] != "glm-5.3-flash" {
		t.Fatalf("zhipu model_map = %v", zp.ModelMap)
	}
	if len(zp.TextOnly) != 1 || zp.TextOnly[0] != "glm-5.3" {
		t.Fatalf("zhipu text_only = %v", zp.TextOnly)
	}
}

func TestLoadDockUpstreamsBadTypes(t *testing.T) {
	for name, src := range map[string]string{
		"upstreams 非表":  "[dock]\nupstreams = 3\n",
		"条目非表":          "[dock]\n[dock.upstreams]\nx = 1\n",
		"model_map 非表":  "[dock]\n[dock.upstreams.x]\nbase_url = \"https://a.b\"\nmodel_map = 3\n",
		"text_only 非数组": "[dock]\n[dock.upstreams.x]\nbase_url = \"https://a.b\"\ntext_only = 3\n",
	} {
		f := filepath.Join(t.TempDir(), "bad.toml")
		if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(f, false); err == nil {
			t.Fatalf("%s: Load err = nil, want 非空", name)
		}
	}
}

// ---- 校验 ----

func TestValidateDockUpstreams(t *testing.T) {
	local := func() map[string]DockUpstream {
		return map[string]DockUpstream{
			"cc-switch": {BaseURL: "http://127.0.0.1:15721"},
		}
	}
	table := []struct {
		name    string
		dock    *DockCfg
		wantErr string // 空＝应通过
	}{
		{"本地条目无 model_map＝守卫透传域豁免",
			&DockCfg{Active: "cc-switch", Upstreams: local()}, ""},
		{"非本地条目缺 default＝拒",
			&DockCfg{Active: "x", Upstreams: map[string]DockUpstream{
				"x": {BaseURL: "https://open.bigmodel.cn/api/anthropic",
					ModelMap: map[string]string{"opus": "glm-5.3"}}}},
			"default"},
		{"非本地条目 model_map 为空＝拒",
			&DockCfg{Active: "x", Upstreams: map[string]DockUpstream{
				"x": {BaseURL: "https://api.deepseek.com/anthropic"}}},
			"default"},
		{"条目缺 base_url＝拒",
			&DockCfg{Active: "x", Upstreams: map[string]DockUpstream{
				"x": {ModelMap: map[string]string{"default": "m"}}}},
			"base_url"},
		{"表在但 active 空＝拒",
			&DockCfg{Upstreams: map[string]DockUpstream{
				"x": {BaseURL: "https://a.b", ModelMap: map[string]string{"default": "m"}}}},
			"active"},
		{"active 指向不存在条目＝拒",
			&DockCfg{Active: "nope", Upstreams: map[string]DockUpstream{
				"x": {BaseURL: "https://a.b", ModelMap: map[string]string{"default": "m"}}}},
			"nope"},
		// 票02：dialect/codex 枚举校验（合法形态零告警通过）
		{"dialect 非法＝拒",
			&DockCfg{Active: "x", Upstreams: map[string]DockUpstream{
				"x": {BaseURL: "https://a.b", Dialect: "grpc", ModelMap: map[string]string{"default": "m"}}}},
			"dialect"},
		{"codex 否决位非法值＝拒",
			&DockCfg{Active: "x", Upstreams: map[string]DockUpstream{
				"x": {BaseURL: "https://a.b", Codex: "maybe", ModelMap: map[string]string{"default": "m"}}}},
			"codex"},
		{"openai_responses＋显式否决＝合法",
			&DockCfg{Active: "x", Upstreams: map[string]DockUpstream{
				"x": {BaseURL: "https://a.b", Dialect: DialectOpenAIResponses,
					Codex: CodexUnsupported, ModelMap: map[string]string{"default": "m", "codex": "c1"}}}},
			""},
	}
	for _, tc := range table {
		cfg := Default()
		cfg.Dock = tc.dock
		err := Validate(cfg, false)
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("%s: Validate err = %v, want nil", tc.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: err = %v, want 含 %q", tc.name, err, tc.wantErr)
		}
	}
}

// ---- ActiveUpstream 解析单源（F9：新表+active 优先；旧单值兜底包装） ----

func TestActiveUpstream(t *testing.T) {
	// 表+active：返回表内条目
	d := &DockCfg{Active: "b", Upstreams: map[string]DockUpstream{
		"a": {BaseURL: "https://a.example"},
		"b": {BaseURL: "https://b.example", APIKey: "kb"},
	}}
	name, up := d.ActiveUpstream()
	if name != "b" || up == nil || up.BaseURL != "https://b.example" || up.APIKey != "kb" {
		t.Fatalf("name/up = %q/%+v", name, up)
	}
	// 修改返回值不得污染配置表（拷贝语义）
	up.APIKey = "mutated"
	if d.Upstreams["b"].APIKey != "kb" {
		t.Fatal("ActiveUpstream 返回值须为拷贝")
	}

	// active 悬空：nil（Validate 拒启；防御路径调用方判 nil）
	d.Active = "missing"
	if name, up := d.ActiveUpstream(); up != nil || name != "" {
		t.Fatalf("悬空 active: %q/%+v, want nil", name, up)
	}

	// 无表（旧单值）：包装为隐式回退条目（含解析层默认：余额端点回落单源）
	legacy := &DockCfg{
		UpstreamBaseURL: "http://127.0.0.1:15721",
		APIKey:          "k-old",
		ModelMap:        map[string]string{"default": "glm-5.3"},
		BalanceURL:      DefaultDockBalanceURL,
	}
	name, up = legacy.ActiveUpstream()
	if name != "" || up == nil {
		t.Fatalf("旧单值应包装为匿名回退条目: %q/%+v", name, up)
	}
	if up.BaseURL != "http://127.0.0.1:15721" || up.APIKey != "k-old" ||
		up.ModelMap["default"] != "glm-5.3" || up.BalanceURL != DefaultDockBalanceURL {
		t.Fatalf("旧单值包装不符: %+v", up)
	}

	// 并存（新表+旧单值同时在）：以新表+active 为准
	both := &DockCfg{
		Active:          "b",
		UpstreamBaseURL: "http://127.0.0.1:19999",
		APIKey:          "k-stale",
		Upstreams: map[string]DockUpstream{
			"b": {BaseURL: "https://b.example", APIKey: "kb"},
		},
	}
	if name, up := both.ActiveUpstream(); name != "b" || up.BaseURL != "https://b.example" || up.APIKey != "kb" {
		t.Fatalf("并存须以新表为准: %q/%+v", name, up)
	}
}

// ---- 票02（供应商接管）：dialect / codex 可用性 / codex 模型位 ----

// TestLoadDockUpstreamDialectCodex 新字段解析：dialect（缺省归一 anthropic）、
// codex 否决位、model_map 的 codex 主模型键（既有档位键之外的扩位键）。
func TestLoadDockUpstreamDialectCodex(t *testing.T) {
	f := filepath.Join(t.TempDir(), "dialect.toml")
	src := `
[dock]
listen = "127.0.0.1:15922"
active = "glm"

[dock.upstreams.glm]
base_url = "https://open.bigmodel.cn/api/anthropic"
api_key = "sk-z"
dialect = "anthropic"

[dock.upstreams.glm.model_map]
default = "glm-5.3"
opus = "glm-5.3"
codex = "glm-5.3"

[dock.upstreams.kimi]
base_url = "https://api.kimi.com/coding/"
api_key = "sk-k"
dialect = "openai_responses"

[dock.upstreams.kimi.model_map]
default = "kimi-for-coding"
codex = "kimi-for-coding"

[dock.upstreams.vetoed]
base_url = "https://veto.example/api"
codex = "unsupported"

[dock.upstreams.vetoed.model_map]
default = "m"
`
	if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ups := cfg.Dock.Upstreams

	glm := ups["glm"]
	if glm.Dialect != DialectAnthropic || glm.Codex != "" {
		t.Fatalf("glm dialect/codex = %q/%q, want anthropic/\"\"", glm.Dialect, glm.Codex)
	}
	if glm.CodexModel() != "glm-5.3" {
		t.Fatalf("glm codex 模型位 = %q, want glm-5.3", glm.CodexModel())
	}
	// 既有档位键语义不变（default/opus 原样解析）
	if glm.ModelMap["default"] != "glm-5.3" || glm.ModelMap["opus"] != "glm-5.3" {
		t.Fatalf("glm 既有档位键被破坏: %v", glm.ModelMap)
	}

	km := ups["kimi"]
	if km.Dialect != DialectOpenAIResponses || km.Codex != "" {
		t.Fatalf("kimi dialect/codex = %q/%q, want openai_responses/\"\"", km.Dialect, km.Codex)
	}
	if km.CodexModel() != "kimi-for-coding" {
		t.Fatalf("kimi codex 模型位 = %q", km.CodexModel())
	}

	// 缺 dialect 的条目：归一 anthropic（缺省=既有行为）；codex 否决位显式在位
	vt := ups["vetoed"]
	if vt.Dialect != DialectAnthropic || vt.Codex != "unsupported" {
		t.Fatalf("vetoed dialect/codex = %q/%q, want anthropic/unsupported", vt.Dialect, vt.Codex)
	}
	if got := vt.CodexAvailability(); got != CodexUnsupported {
		t.Fatalf("vetoed 可用性 = %q, want unsupported", got)
	}
}

// TestLoadDockUpstreamBadDialectCodex 非法枚举值拒启（Load 内含 Validate，
// 错误文案须点名坏键）。
func TestLoadDockUpstreamBadDialectCodex(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		wantErr string
	}{
		{"dialect 非法",
			"[dock]\nactive = \"x\"\n[dock.upstreams.x]\nbase_url = \"https://a.b\"\ndialect = \"grpc\"\nmodel_map = { default = \"m\" }\n",
			"dialect"},
		{"codex 非法",
			"[dock]\nactive = \"x\"\n[dock.upstreams.x]\nbase_url = \"https://a.b\"\ncodex = \"maybe\"\nmodel_map = { default = \"m\" }\n",
			"codex"},
	}
	for _, tc := range cases {
		f := filepath.Join(t.TempDir(), "bad.toml")
		if err := os.WriteFile(f, []byte(tc.src), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load(f, false)
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Fatalf("%s: err = %v, want 含 %q", tc.name, err, tc.wantErr)
		}
	}
}

// TestCodexAvailabilityDerivation 可用性推导矩阵：dialect 推导＋显式否决覆盖。
func TestCodexAvailabilityDerivation(t *testing.T) {
	table := []struct {
		name string
		up   DockUpstream
		want string
	}{
		{"零值（旧形态结构体）＝缺省 anthropic→需翻译", DockUpstream{}, CodexTranslation},
		{"显式 anthropic→需翻译", DockUpstream{Dialect: DialectAnthropic}, CodexTranslation},
		{"openai_responses→原生透传", DockUpstream{Dialect: DialectOpenAIResponses}, CodexNative},
		{"显式否决盖过原生推导", DockUpstream{Dialect: DialectOpenAIResponses, Codex: CodexUnsupported}, CodexUnsupported},
		{"显式否决盖过翻译推导", DockUpstream{Dialect: DialectAnthropic, Codex: CodexUnsupported}, CodexUnsupported},
	}
	for _, tc := range table {
		if got := tc.up.CodexAvailability(); got != tc.want {
			t.Errorf("%s: 可用性 = %q, want %q", tc.name, got, tc.want)
		}
	}
	// codex 模型位：缺键为空（不凭空造默认）
	var zero DockUpstream
	if got := zero.CodexModel(); got != "" {
		t.Fatalf("缺 codex 键的条目 CodexModel = %q, want 空", got)
	}
}

// TestDockUpstreamNewFieldsLegacyZeroChange 旧配置（无新字段）行为零变化锁死：
// 表条目与旧单值兜底包装的 dialect 缺省一律 anthropic、codex 一律空——新字段
// 缺省＝旧行为零变化（票 02 解析纪律）。
func TestDockUpstreamNewFieldsLegacyZeroChange(t *testing.T) {
	src := `
[dock]
active = "z"

[dock.upstreams.z]
base_url = "https://z.example/api"

[dock.upstreams.z.model_map]
default = "m"
`
	f := writeCfg(t, src)
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	up := cfg.Dock.Upstreams["z"]
	if up.Dialect != DialectAnthropic || up.Codex != "" {
		t.Fatalf("旧形态表条目 dialect/codex = %q/%q, want anthropic/\"\"", up.Dialect, up.Codex)
	}
	if got := up.CodexAvailability(); got != CodexTranslation {
		t.Fatalf("旧形态条目可用性 = %q, want translation（既有语义：anthropic 需翻译）", got)
	}
	// 旧单值兜底包装（无表形态）同款缺省
	legacy := &DockCfg{UpstreamBaseURL: "http://127.0.0.1:15721"}
	_, lup := legacy.ActiveUpstream()
	if lup == nil || lup.Dialect != DialectAnthropic || lup.Codex != "" {
		t.Fatalf("旧单值包装 dialect/codex = %q/%q, want anthropic/\"\"", lup.Dialect, lup.Codex)
	}
}

// ---- 票09（pi 面）：model_map pi 主模型键 + pi 可用性位 ----

// TestLoadDockUpstreamPiKeyAndBit pi 两键解析：model_map 的 pi 主模型键（与
// codex 键并列的扩位键，非本地条目可选）＋条目级 pi 显式否决位；两键缺省＝空
// （可用性按 dialect 推导，模型位不凭空造默认）。
func TestLoadDockUpstreamPiKeyAndBit(t *testing.T) {
	f := writeCfg(t, `
[dock]
active = "glm"

[dock.upstreams.glm]
base_url = "https://open.bigmodel.cn/api/anthropic"
codex = "unsupported"

[dock.upstreams.glm.model_map]
default = "glm-5.3"
codex = "glm-5.3"
pi = "glm-5.3"

[dock.upstreams.plain]
base_url = "https://plain.example/api"

[dock.upstreams.plain.model_map]
default = "m"
`)
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ups := cfg.Dock.Upstreams

	glm := ups["glm"]
	if got := glm.PiModel(); got != "glm-5.3" {
		t.Fatalf("glm pi 模型位 = %q, want glm-5.3", got)
	}
	if glm.Pi != "" {
		t.Fatalf("glm pi 否决位 = %q, want 空（缺省按 dialect 推导）", glm.Pi)
	}
	// 既有档位键与 codex 键不受 pi 键并列影响
	if glm.ModelMap["default"] != "glm-5.3" || glm.CodexModel() != "glm-5.3" {
		t.Fatalf("既有键被 pi 键破坏: %v", glm.ModelMap)
	}
	// codex 否决位与 pi 可用性互不串扰（各管各的车道）
	if glm.CodexAvailability() != CodexUnsupported || glm.PiAvailability() != PiAvailable {
		t.Fatalf("codex/pi 可用性串扰: codex=%q pi=%q",
			glm.CodexAvailability(), glm.PiAvailability())
	}

	// 缺 pi 两键的条目：模型位空、否决位空（推导可用）
	pl := ups["plain"]
	if got := pl.PiModel(); got != "" {
		t.Fatalf("plain pi 模型位 = %q, want 空（缺键不凭空造默认）", got)
	}
	if pl.Pi != "" || pl.PiAvailability() != PiAvailable {
		t.Fatalf("plain pi 位/可用性 = %q/%q, want \"\"/%q",
			pl.Pi, pl.PiAvailability(), PiAvailable)
	}
}

// TestPiAvailabilityDerivation pi 可用性推导矩阵（对标 CodexAvailability 的
// "显式否决位+按 dialect 推导"，推导方向相反）：否决位优先；anthropic→可用
// （pi 说 anthropic-messages，复用 CC 车道）；openai_responses→不可用（无入站
// 车道）。
func TestPiAvailabilityDerivation(t *testing.T) {
	table := []struct {
		name string
		up   DockUpstream
		want string
	}{
		{"零值（旧形态结构体）＝缺省 anthropic→可用", DockUpstream{}, PiAvailable},
		{"显式 anthropic→可用", DockUpstream{Dialect: DialectAnthropic}, PiAvailable},
		{"openai_responses→不可用", DockUpstream{Dialect: DialectOpenAIResponses}, PiUnavailable},
		{"显式否决盖过可用推导", DockUpstream{Dialect: DialectAnthropic, Pi: PiUnsupported}, PiUnsupported},
		{"显式否决盖过不可用推导", DockUpstream{Dialect: DialectOpenAIResponses, Pi: PiUnsupported}, PiUnsupported},
	}
	for _, tc := range table {
		if got := tc.up.PiAvailability(); got != tc.want {
			t.Errorf("%s: 可用性 = %q, want %q", tc.name, got, tc.want)
		}
	}
	// pi 模型位：缺键为空（不凭空造默认）
	var zero DockUpstream
	if got := zero.PiModel(); got != "" {
		t.Fatalf("缺 pi 键的条目 PiModel = %q, want 空", got)
	}
}

// TestValidateDockUpstreamsPiEnum pi 否决位枚举校验（合法/非法值/缺省表驱动；
// 仅认 "unsupported"——把推导值当显式值写也拒，与 codex 同款纪律）。
func TestValidateDockUpstreamsPiEnum(t *testing.T) {
	table := []struct {
		name    string
		pi      string
		wantErr string // 空＝应通过
	}{
		{"缺省（空）＝合法，按 dialect 推导", "", ""},
		{"显式否决＝合法", PiUnsupported, ""},
		{"非法值＝拒", "maybe", "pi 非法"},
		{"把推导值当显式值写＝拒", PiAvailable, "pi 非法"},
	}
	for _, tc := range table {
		cfg := Default()
		cfg.Dock = &DockCfg{Active: "x", Upstreams: map[string]DockUpstream{
			"x": {BaseURL: "https://a.b", Pi: tc.pi,
				ModelMap: map[string]string{"default": "m"}},
		}}
		err := Validate(cfg, false)
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("%s: Validate err = %v, want nil", tc.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: err = %v, want 含 %q", tc.name, err, tc.wantErr)
		}
	}
}

// TestLoadDockUpstreamBadPi 文件形态的非法 pi 值拒启（Load 内含 Validate，
// 错误文案点名坏键）。
func TestLoadDockUpstreamBadPi(t *testing.T) {
	f := writeCfg(t, "[dock]\nactive = \"x\"\n[dock.upstreams.x]\n"+
		"base_url = \"https://a.b\"\npi = \"maybe\"\nmodel_map = { default = \"m\" }\n")
	_, err := Load(f, false)
	if err == nil || !strings.Contains(err.Error(), "pi") {
		t.Fatalf("err = %v, want 含 pi 非法", err)
	}
}

// TestDockUpstreamPiLegacyZeroChange 旧配置（无 pi 两键）行为零变化：Pi 解析
// 为空、推导可用——但本票只做展示面，无 switch/apply 行为面（票12 才接）。
func TestDockUpstreamPiLegacyZeroChange(t *testing.T) {
	f := writeCfg(t, "[dock]\nactive = \"z\"\n[dock.upstreams.z]\n"+
		"base_url = \"https://z.example/api\"\n"+
		"[dock.upstreams.z.model_map]\ndefault = \"m\"\n")
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	up := cfg.Dock.Upstreams["z"]
	if up.Pi != "" || up.PiModel() != "" {
		t.Fatalf("旧形态条目 pi 位/模型位 = %q/%q, want 空/空", up.Pi, up.PiModel())
	}
	if got := up.PiAvailability(); got != PiAvailable {
		t.Fatalf("旧形态条目 pi 可用性 = %q, want %q（anthropic 方言推导）", got, PiAvailable)
	}
}

// ---- 本地中转地址判定单源（自 dock/guard 迁入；guard.DoubleRewriteRisk 委托此处） ----

func TestIsLoopbackHostNormalization(t *testing.T) {
	table := []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"LOCALHOST", true}, // 大小写变体同归一
		{"127.0.0.1", true},
		{"127.9.9.9", true}, // 127.0.0.0/8 全归一
		{"127.0.0.0", true},
		{"::1", true},
		{"::ffff:127.0.0.1", true}, // IPv4 映射形
		{"open.bigmodel.cn", false},
		{"10.0.0.5", false},
		{"localhost.example.com", false}, // 前缀相似但非回环
		{"", false},
	}
	for _, tc := range table {
		if got := isLoopbackHost(tc.host); got != tc.want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

func TestIsLocalRelayAddrForms(t *testing.T) {
	table := []struct {
		url  string
		want bool
	}{
		{"http://127.0.0.1:15721", true},
		{"http://localhost:15721", true},
		{"http://[::1]:15722", true},
		{"http://127.0.0.1:15723", true},
		{"https://open.bigmodel.cn/api/paas/v4", false},
		{"http://localhost:8080", false}, // 回环但非中转端口
		{"http://10.0.0.5:15721", false}, // 中转端口但非回环
		{"http://127.0.0.1", false},      // 无端口不算
	}
	for _, tc := range table {
		if got := IsLocalRelayAddr(tc.url); got != tc.want {
			t.Errorf("IsLocalRelayAddr(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

// ---- 首启迁移 ----

// legacyDockTOML 旧单值形态的完整 [dock] 节（六字段全显式）＋其余节，
// 用于迁移用例的底稿。
const legacyDockTOML = `
[gate]
cc_mode = "observe"

[dock]
upstream_base_url = "http://127.0.0.1:15721"
api_key = "sk-legacy-key"
rewrite_enabled = true
text_only = ["glm-5.3"]
balance_url = "https://open.bigmodel.cn/api/user/balance"

[dock.model_map]
claude-opus-5 = "glm-5.5"
default = "glm-4.7-flash"

[providers.deepseek]
base_url = "https://api.deepseek.com/v1"
model = "deepseek-v4.1-flash"
window = 1048576

[ferry]
provider = "deepseek"
`

func writeCfg(t *testing.T, src string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestMigrateFirstBootLegacyToTable(t *testing.T) {
	f := writeCfg(t, legacyDockTOML)
	before := readFileT(t, f)

	cfg, err := Load(f, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := MigrateDockFirstBoot(f, cfg); err != nil {
		t.Fatalf("MigrateDockFirstBoot: %v", err)
	}

	after := readFileT(t, f)
	if after == before {
		t.Fatal("迁移未写回文件")
	}
	// 验收：config.toml 含 [dock.upstreams.cc-switch]（裸键形态）
	if !strings.Contains(after, "[dock.upstreams.cc-switch]") {
		t.Fatalf("文件缺 [dock.upstreams.cc-switch]:\n%s", after)
	}

	// 就地换新表：cfg.Dock 已按新表解析（active=cc-switch）
	if cfg.Dock.Active != "cc-switch" {
		t.Fatalf("active = %q, want cc-switch", cfg.Dock.Active)
	}
	_, up := cfg.Dock.ActiveUpstream()
	if up == nil || up.BaseURL != "http://127.0.0.1:15721" || up.APIKey != "sk-legacy-key" {
		t.Fatalf("cc-switch 条目不符: %+v", up)
	}
	if up.ModelMap["claude-opus-5"] != "glm-5.5" || up.ModelMap["default"] != "glm-4.7-flash" {
		t.Fatalf("model_map 未继承: %v", up.ModelMap)
	}
	if len(up.TextOnly) != 1 || up.TextOnly[0] != "glm-5.3" {
		t.Fatalf("text_only 未继承: %v", up.TextOnly)
	}
	if up.BalanceURL != "https://open.bigmodel.cn/api/user/balance" {
		t.Fatalf("balance_url 未继承: %q", up.BalanceURL)
	}

	// 重新 Load：行为等价（迁移后文件自洽可解析）
	cfg2, err := Load(f, false)
	if err != nil {
		t.Fatalf("迁移后 Load: %v", err)
	}
	if cfg2.Dock.Active != "cc-switch" || cfg2.FerryProvider != "deepseek" ||
		cfg2.GateCC != "observe" {
		t.Fatalf("迁移后其余节/active 不符: %+v", cfg2)
	}
	if name, up := cfg2.Dock.ActiveUpstream(); name != "cc-switch" ||
		up.APIKey != "sk-legacy-key" || up.ModelMap["default"] != "glm-4.7-flash" {
		t.Fatalf("迁移后 active 条目不符: %q/%+v", name, up)
	}

	// 旧单值字段已移除（解码层不再含旧键）
	data := decodeFileT(t, f)
	dock, _ := data["dock"].(map[string]any)
	if dock == nil {
		t.Fatal("迁移后无 [dock] 节")
	}
	for _, k := range []string{"upstream_base_url", "api_key", "rewrite_enabled", "model_map", "text_only", "balance_url"} {
		if _, ok := dock[k]; ok {
			t.Fatalf("旧单值键 %q 未移除: %v", k, dockKeys(dock))
		}
	}
	if v, _ := dock["active"].(string); v != "cc-switch" {
		t.Fatalf("active = %v, want cc-switch", dock["active"])
	}
}

func TestMigrateGeneratesThreePresets(t *testing.T) {
	f := writeCfg(t, "[dock]\napi_key = \"k\"\n")
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := MigrateDockFirstBoot(f, cfg); err != nil {
		t.Fatalf("MigrateDockFirstBoot: %v", err)
	}
	ups := cfg.Dock.Upstreams
	if len(ups) != 4 { // cc-switch + 三预置
		t.Fatalf("条目数 = %d, want 4: %v", len(ups), ups)
	}
	want := map[string]DockUpstream{
		"智谱": {
			BaseURL:    "https://open.bigmodel.cn/api/anthropic",
			ModelMap:   map[string]string{"default": "glm-5.3", "opus": "glm-5.3", "sonnet": "glm-5.3", "haiku": "glm-5.3-flash"},
			BalanceURL: "https://open.bigmodel.cn/api/user/balance",
		},
		"kimi": {
			BaseURL:  "https://api.kimi.com/coding/",
			ModelMap: map[string]string{"default": "kimi-for-coding", "sonnet": "kimi-for-coding", "opus": "k3", "haiku": "k3-256k"},
		},
		"deepseek": {
			BaseURL:  "https://api.deepseek.com/anthropic",
			ModelMap: map[string]string{"default": "deepseek-flash", "sonnet": "deepseek-flash", "haiku": "deepseek-flash", "opus": "deepseek-v4-pro"},
		},
	}
	for name, w := range want {
		got, ok := ups[name]
		if !ok {
			t.Fatalf("缺预置条目 %q: %v", name, ups)
		}
		if got.APIKey != "" {
			t.Fatalf("预置 %s api_key = %q, want 空（未激活占位）", name, got.APIKey)
		}
		if got.BaseURL != w.BaseURL || got.BalanceURL != w.BalanceURL {
			t.Fatalf("预置 %s base/balance = %q/%q, want %q/%q",
				name, got.BaseURL, got.BalanceURL, w.BaseURL, w.BalanceURL)
		}
		if len(got.ModelMap) != len(w.ModelMap) {
			t.Fatalf("预置 %s model_map = %v, want %v", name, got.ModelMap, w.ModelMap)
		}
		for k, v := range w.ModelMap {
			if got.ModelMap[k] != v {
				t.Fatalf("预置 %s model_map[%q] = %q, want %q", name, k, got.ModelMap[k], v)
			}
		}
	}
}

func TestMigrateIdempotentSecondBootSkips(t *testing.T) {
	f := writeCfg(t, legacyDockTOML)
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateDockFirstBoot(f, cfg); err != nil {
		t.Fatalf("首启迁移: %v", err)
	}
	snap := readFileT(t, f)

	cfg2, err := Load(f, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateDockFirstBoot(f, cfg2); err != nil {
		t.Fatalf("二次启动: %v", err)
	}
	if got := readFileT(t, f); got != snap {
		t.Fatalf("二次启动不得重复迁移（文件被改动）:\n%s", got)
	}
}

func TestMigrateSkipsWhenUpstreamsTableExists(t *testing.T) {
	// 手写新表＋旧单值并存：跳过迁移；解析以新表+active 为准（F9）
	src := `
[dock]
upstream_base_url = "http://127.0.0.1:19999"
api_key = "k-stale"
active = "hand"

[dock.upstreams.hand]
base_url = "https://hand.example/api"
api_key = "k-hand"
model_map = { default = "m1" }
`
	f := writeCfg(t, src)
	before := readFileT(t, f)
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := MigrateDockFirstBoot(f, cfg); err != nil {
		t.Fatalf("应跳过迁移: %v", err)
	}
	if got := readFileT(t, f); got != before {
		t.Fatal("新表已存在时不得改写文件")
	}
	if name, up := cfg.Dock.ActiveUpstream(); name != "hand" ||
		up.BaseURL != "https://hand.example/api" || up.APIKey != "k-hand" {
		t.Fatalf("并存解析须以新表为准: %q/%+v", name, up)
	}
}

func TestMigrateNoDockSectionNoop(t *testing.T) {
	f := writeCfg(t, "[server]\nport = 7399\n")
	before := readFileT(t, f)
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateDockFirstBoot(f, cfg); err != nil {
		t.Fatalf("无 [dock] 节应静默跳过: %v", err)
	}
	if got := readFileT(t, f); got != before {
		t.Fatal("无 [dock] 节不得改写文件")
	}
	if cfg.Dock != nil {
		t.Fatal("无 [dock] 节 cfg.Dock 应保持 nil（F11）")
	}
}

func TestMigrateEmptyDockSectionUsesDefaults(t *testing.T) {
	// 空 [dock]（无任何旧单值）：默认值迁移（上游默认 15721；余额默认单源）
	f := writeCfg(t, "[dock]\n")
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateDockFirstBoot(f, cfg); err != nil {
		t.Fatalf("MigrateDockFirstBoot: %v", err)
	}
	_, up := cfg.Dock.ActiveUpstream()
	if up.BaseURL != "http://127.0.0.1:15721" || up.APIKey != "" ||
		up.BalanceURL != DefaultDockBalanceURL {
		t.Fatalf("空节迁移应物化旧默认值: %+v", up)
	}
}

func TestMigrateAbortsOnUnknownDockKey(t *testing.T) {
	f := writeCfg(t, "[dock]\nupstream_base_url = \"http://127.0.0.1:15721\"\nfuture_key = true\n")
	before := readFileT(t, f)
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatal(err)
	}
	err = MigrateDockFirstBoot(f, cfg)
	if err == nil || !strings.Contains(err.Error(), "future_key") {
		t.Fatalf("err = %v, want 含未知键名", err)
	}
	if got := readFileT(t, f); got != before {
		t.Fatal("迁移失败不得改写原文件")
	}
}

func TestMigrateAbortsWhenFutureTableWouldBeInvalid(t *testing.T) {
	// 旧配置：非本地上游＋model_map 缺 default＋rewrite 开——旧守卫静默退透传；
	// 迁移成新表会违反"非本地必含 default"校验 → 保守不迁移（保留旧行为）。
	src := `
[dock]
upstream_base_url = "https://open.bigmodel.cn/api/anthropic"
api_key = "k"
rewrite_enabled = true

[dock.model_map]
claude-opus-5 = "glm-5.5"
`
	f := writeCfg(t, src)
	before := readFileT(t, f)
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatal(err)
	}
	err = MigrateDockFirstBoot(f, cfg)
	if err == nil {
		t.Fatal("未来表不合法应报错拒迁")
	}
	if got := readFileT(t, f); got != before {
		t.Fatal("拒迁不得改写原文件")
	}
	// 拒迁后 cfg 仍按旧单值解析（退回旧行为）
	if _, up := cfg.Dock.ActiveUpstream(); up.BaseURL != "https://open.bigmodel.cn/api/anthropic" {
		t.Fatalf("拒迁后应保留旧单值视图: %+v", up)
	}
}

func TestMigrateAbortsOnInlineDockForm(t *testing.T) {
	// dock 内联表形态（无 [dock] 表头）：文本手术无从定位 → 保守报错不改文件
	f := writeCfg(t, "dock = { upstream_base_url = \"http://127.0.0.1:15721\" }\n")
	before := readFileT(t, f)
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateDockFirstBoot(f, cfg); err == nil {
		t.Fatal("内联形态应报错拒迁")
	}
	if got := readFileT(t, f); got != before {
		t.Fatal("拒迁不得改写原文件")
	}
}

func TestMigrateMissingFileNoop(t *testing.T) {
	f := filepath.Join(t.TempDir(), "absent.toml")
	cfg := Default()
	if err := MigrateDockFirstBoot(f, cfg); err != nil {
		t.Fatalf("文件不存在应静默跳过: %v", err)
	}
	if cfg.Dock != nil {
		t.Fatal("不得凭空构造 Dock")
	}
}

// TestExampleConfigDockTableIsValid 样例文件钉子：config.example.toml 的新
// schema 须被生产解析器接受并通过校验（三预置＋active 在位）。
func TestExampleConfigDockTableIsValid(t *testing.T) {
	f := filepath.Join("..", "..", "config.example.toml")
	if _, err := os.Stat(f); err != nil {
		t.Skipf("样例文件不可达: %v", err)
	}
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatalf("样例配置解析/校验失败: %v", err)
	}
	if cfg.Dock == nil {
		t.Fatal("样例应含 [dock] 节")
	}
	if _, up := cfg.Dock.ActiveUpstream(); up == nil {
		t.Fatalf("样例 active=%q 未指向条目", cfg.Dock.Active)
	}
	for _, name := range []string{"智谱", "kimi", "deepseek"} {
		if _, ok := cfg.Dock.Upstreams[name]; !ok {
			t.Fatalf("样例缺预置条目 %q", name)
		}
	}
	if cfg.FerryProvider != "deepseek" {
		t.Fatalf("摆渡 provider 样例被破坏: %q", cfg.FerryProvider)
	}
}

// readFileT 读文件全文（失败即 Fatal）。
func readFileT(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// decodeFileT TOML 解码为 map（失败即 Fatal）。
func decodeFileT(t *testing.T, path string) map[string]any {
	t.Helper()
	data := map[string]any{}
	if _, err := toml.DecodeFile(path, &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func dockKeys(dock map[string]any) []string {
	var ks []string
	for k := range dock {
		ks = append(ks, k)
	}
	return ks
}
