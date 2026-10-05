// settings_edit_test.go — 票01：config 节级/条目级写原语（SetSectionTOML /
// Set·RemoveProviderEntry / Set·RemovePriceEntry）验收钉子。
//
// 验收口径（与 dock_edit_test.go 同风格）：
//   - 节级整写只动目标节：目标节=新值，其余节与节外注释逐字节不变（全文
//     逐字节断言）；缺节=EOF 追加；
//   - 写回产物必须过 config.Load 全量校验（含跨节不变量：summarize<block、
//     阈值差≥120 等），非法改动拒写且原文件字节不动；
//   - 原子性：rename 前失败（目标被占用）/tmp 写失败＝不留半文件、原文件不动；
//   - providers/prices 条目增删：目标条目整树动（含 [[prices.k.versions]]
//     子表），同表其他条目与注释逐字节保真；被 [ferry].chain/.provider
//     引用的 provider 拒删且错误点名引用方。
package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ferryman/internal/prices"
)

// settingsCfgSrc 共用底稿：顶层注释 + 各形态节（节内注释/条目注释/子表数组），
// [notify] 压尾（EOF 整写用）、[wait_window] 缺席（缺节创建用）、ferry.chain
// 只引用 deepseek（glm 可删、deepseek 供引用守卫用）。
const settingsCfgSrc = `# 顶层注释：Ferryman 配置

[server]
port = 15700

[gate]
# 节内注释（旧内容，整写后随节替换）
cc_mode = "observe"
codex_mode = "off"

[thresholds]
summarize_s = 1500.0
block_s = 2100.0

[providers.deepseek]
# 深度求索：注释随条目保留
base_url = "https://api.deepseek.com/v1"
model = "deepseek-v4.1-flash"
window = 1048576

[providers.glm]
base_url = "https://open.bigmodel.cn/api/paas/v4"
model = "glm-5.3-flash"
window = 1048576

[ferry]
chain = ["deepseek"]

[prices.glm]
unit = "智谱积分"
per = 10000

[[prices.glm.versions]]
effective_from = "2026-09-17"
p_in = 6.9
p_cache = 1.7
p_out = 24

[prices.kimi]
unit = "元"
per = 10000

[[prices.kimi.versions]]
effective_from = "2026-10-01"
p_in = 1.0
p_out = 4.0

[notify]
enabled = true
`

func assertNoSettingsTmp(t *testing.T, f string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(f))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".settings-tmp") {
			t.Errorf("残留临时文件: %s", e.Name())
		}
	}
}

func TestSettingsSectionReplaceRewritesOnlyTargetSection(t *testing.T) {
	f := writeCfg(t, settingsCfgSrc)
	if err := SetSectionTOML(f, "gate", "cc_mode = \"enforce\"\ncodex_mode = \"enforce\"\n"); err != nil {
		t.Fatalf("SetSectionTOML: %v", err)
	}
	pre, rest, _ := strings.Cut(settingsCfgSrc, "[gate]\n")
	_, post, _ := strings.Cut(rest, "[thresholds]\n")
	want := pre + "[gate]\ncc_mode = \"enforce\"\ncodex_mode = \"enforce\"\n\n[thresholds]\n" + post
	if after := readFileEdit(t, f); after != want {
		t.Errorf("整写后全文不符：\n--got--\n%s\n--want--\n%s", after, want)
	}
	cfg := mustLoadEdit(t, f)
	if cfg.GateCC != "enforce" || cfg.GateCodex != "enforce" {
		t.Errorf("回读 gate 不符: cc=%q codex=%q", cfg.GateCC, cfg.GateCodex)
	}
}

func TestSettingsSectionReplaceLastSectionAtEOF(t *testing.T) {
	f := writeCfg(t, settingsCfgSrc)
	// body 不带尾换行：实现应归一补齐，EOF 无下一表头＝不补空行
	if err := SetSectionTOML(f, "notify", "enabled = false"); err != nil {
		t.Fatalf("SetSectionTOML: %v", err)
	}
	pre, _, _ := strings.Cut(settingsCfgSrc, "[notify]\n")
	want := pre + "[notify]\nenabled = false\n"
	if after := readFileEdit(t, f); after != want {
		t.Errorf("EOF 节整写不符：\n--got--\n%s\n--want--\n%s", after, want)
	}
	if cfg := mustLoadEdit(t, f); cfg.Notify.Enabled {
		t.Error("notify.enabled 回读仍为 true")
	}
}

func TestSettingsSectionCreateWhenMissingAppendsAtEOF(t *testing.T) {
	f := writeCfg(t, settingsCfgSrc)
	if err := SetSectionTOML(f, "wait_window", "mode = \"observe\"\nmanual_wait_cap_s = 60\n"); err != nil {
		t.Fatalf("SetSectionTOML: %v", err)
	}
	want := settingsCfgSrc + "\n[wait_window]\nmode = \"observe\"\nmanual_wait_cap_s = 60\n"
	if after := readFileEdit(t, f); after != want {
		t.Errorf("缺节创建不符：\n--got--\n%s\n--want--\n%s", after, want)
	}
	cfg := mustLoadEdit(t, f)
	if cfg.WaitWindow.Mode != "observe" || cfg.WaitWindow.ManualWaitCapS != 60 {
		t.Errorf("wait_window 回读不符: %+v", cfg.WaitWindow)
	}
}

func TestSettingsSectionRejectsInvalidLeavesFileUntouched(t *testing.T) {
	cases := []struct {
		name, section, body, wantErr string
	}{
		{"总结阈值不小于拦截", "thresholds", "summarize_s = 2100.0\nblock_s = 2100.0\n", "拦截阈值"},
		{"阈值差小于120", "thresholds", "summarize_s = 2000.0\nblock_s = 2050.0\n", "阈值差"},
		{"非法闸门模式", "gate", "cc_mode = \"bogus\"\n", "gate.cc_mode"},
		{"坏TOML正文", "gate", "= broken\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := writeCfg(t, settingsCfgSrc)
			before := readFileEdit(t, f)
			err := SetSectionTOML(f, tc.section, tc.body)
			if err == nil {
				t.Fatal("非法改动应被拒写")
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("拒绝信息应含 %q: %v", tc.wantErr, err)
			}
			if after := readFileEdit(t, f); after != before {
				t.Error("拒写路径原文件不得动")
			}
			assertNoSettingsTmp(t, f)
		})
	}
}

func TestSettingsSectionAtomicNoHalfFile(t *testing.T) {
	t.Run("rename失败清理tmp原文件不动", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("POSIX 允许 rename 覆盖已打开文件，此路径仅 Windows 可测")
		}
		f := writeCfg(t, settingsCfgSrc)
		before := readFileEdit(t, f)
		fh, err := os.Open(f) // Go 句柄不带 FILE_SHARE_DELETE → rename 必被拒
		if err != nil {
			t.Fatal(err)
		}
		defer fh.Close()
		err = SetSectionTOML(f, "notify", "enabled = false")
		if err == nil {
			t.Fatal("目标被占用时 rename 应失败")
		}
		if after := readFileEdit(t, f); after != before {
			t.Error("rename 失败原文件不得动")
		}
		assertNoSettingsTmp(t, f)
	})
	t.Run("tmp写入失败不留半文件", func(t *testing.T) {
		f := writeCfg(t, settingsCfgSrc)
		before := readFileEdit(t, f)
		if err := os.Mkdir(f+".settings-tmp", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := SetSectionTOML(f, "notify", "enabled = false"); err == nil {
			t.Fatal("tmp 路径被目录占据时写入应失败")
		}
		if after := readFileEdit(t, f); after != before {
			t.Error("写失败原文件不得动")
		}
		assertNoSettingsTmp(t, f)
	})
}

func TestSettingsProviderEntrySetReplaceRemove(t *testing.T) {
	// 新增：EOF 追加，字段序固定，extra_body 键序确定
	f := writeCfg(t, settingsCfgSrc)
	e := ProviderEntry{
		BaseURL: "https://api.kimi.com/coding/", Model: "kimi-for-coding",
		APIKey: "sk-kimi-1", Window: 131072,
		ExtraBody: map[string]any{"thinking": "disabled", "limits": map[string]any{"max_tokens": 4096}},
	}
	if err := SetProviderEntry(f, "kimi", e); err != nil {
		t.Fatalf("SetProviderEntry: %v", err)
	}
	want := settingsCfgSrc + "\n[providers.kimi]\nbase_url = \"https://api.kimi.com/coding/\"\n" +
		"model = \"kimi-for-coding\"\napi_key = \"sk-kimi-1\"\nwindow = 131072\nprotocol = \"openai\"\n" +
		"extra_body = { limits = { max_tokens = 4096 }, thinking = \"disabled\" }\n"
	if after := readFileEdit(t, f); after != want {
		t.Errorf("新增条目不符：\n--got--\n%s\n--want--\n%s", after, want)
	}
	mustLoadEdit(t, f)

	// 整条替换：只动 glm 跨度，deepseek（含注释）逐字节保留
	f = writeCfg(t, settingsCfgSrc)
	e2 := ProviderEntry{BaseURL: "https://open.bigmodel.cn/api/paas/v4",
		Model: "glm-5.3", APIKey: "sk-glm-2", Window: 1048576, Protocol: "anthropic"}
	if err := SetProviderEntry(f, "glm", e2); err != nil {
		t.Fatalf("SetProviderEntry(替换): %v", err)
	}
	pre, rest, _ := strings.Cut(settingsCfgSrc, "[providers.glm]\n")
	_, post, _ := strings.Cut(rest, "[ferry]\n")
	want = pre + "[providers.glm]\nbase_url = \"https://open.bigmodel.cn/api/paas/v4\"\n" +
		"model = \"glm-5.3\"\napi_key = \"sk-glm-2\"\nwindow = 1048576\nprotocol = \"anthropic\"\n\n[ferry]\n" + post
	if after := readFileEdit(t, f); after != want {
		t.Errorf("替换条目不符：\n--got--\n%s\n--want--\n%s", after, want)
	}
	mustLoadEdit(t, f)

	// 删除未引用条目：目标条目整段摘除（含表头前空行），其余保真
	f = writeCfg(t, settingsCfgSrc)
	if err := RemoveProviderEntry(f, "glm"); err != nil {
		t.Fatalf("RemoveProviderEntry: %v", err)
	}
	pre, rest, _ = strings.Cut(settingsCfgSrc, "[providers.glm]\n")
	_, post, _ = strings.Cut(rest, "[ferry]\n")
	want = strings.TrimSuffix(pre, "\n") + "[ferry]\n" + post
	if after := readFileEdit(t, f); after != want {
		t.Errorf("删除条目不符：\n--got--\n%s\n--want--\n%s", after, want)
	}
	mustLoadEdit(t, f)

	// 未知条目拒删且原文件不动
	before := readFileEdit(t, f)
	if err := RemoveProviderEntry(f, "ghost"); err == nil {
		t.Fatal("未知条目应拒删")
	}
	if after := readFileEdit(t, f); after != before {
		t.Error("拒删路径原文件不得动")
	}

	// [providers] 表整缺的文件上新增：EOF 追加即隐式建表
	f3 := writeCfg(t, "[server]\nport = 7399\n")
	if err := SetProviderEntry(f3, "x", ProviderEntry{BaseURL: "https://x.example/v1", Model: "m", Window: 131072}); err != nil {
		t.Fatalf("SetProviderEntry(空表): %v", err)
	}
	want3 := "[server]\nport = 7399\n\n[providers.x]\nbase_url = \"https://x.example/v1\"\n" +
		"model = \"m\"\napi_key = \"\"\nwindow = 131072\nprotocol = \"openai\"\n"
	if after := readFileEdit(t, f3); after != want3 {
		t.Errorf("空表新增不符：\n--got--\n%s\n--want--\n%s", after, want3)
	}
	mustLoadEdit(t, f3)
}

func TestSettingsProviderEntryRefGuard(t *testing.T) {
	// 被 [ferry].chain 引用 → 拒删且点名引用方
	f := writeCfg(t, settingsCfgSrc)
	before := readFileEdit(t, f)
	err := RemoveProviderEntry(f, "deepseek")
	if err == nil {
		t.Fatal("被 chain 引用的条目应拒删")
	}
	if !strings.Contains(err.Error(), "[ferry].chain") {
		t.Errorf("拒绝信息应点名 [ferry].chain: %v", err)
	}
	if after := readFileEdit(t, f); after != before {
		t.Error("拒删路径原文件不得动")
	}
	// 被 [ferry].provider 单键引用 → 同样拒删
	variant := strings.Replace(settingsCfgSrc, "chain = [\"deepseek\"]", "provider = \"glm\"", 1)
	f2 := writeCfg(t, variant)
	before2 := readFileEdit(t, f2)
	err = RemoveProviderEntry(f2, "glm")
	if err == nil {
		t.Fatal("被 provider 单键引用的条目应拒删")
	}
	if !strings.Contains(err.Error(), "[ferry].provider") {
		t.Errorf("拒绝信息应点名 [ferry].provider: %v", err)
	}
	if after := readFileEdit(t, f2); after != before2 {
		t.Error("拒删路径原文件不得动")
	}
}

func TestSettingsPriceEntryReplaceWithVersions(t *testing.T) {
	f := writeCfg(t, settingsCfgSrc)
	cache := 1.7
	e := PriceEntry{Unit: "智谱积分", Per: 10000, Versions: []PriceVersionEntry{
		{EffectiveFrom: "2026-09-17", PIn: 6.9, PCache: &cache, POut: 24},
		{EffectiveFrom: "2026-10-01", PIn: 7.0, POut: 26},
	}}
	if err := SetPriceEntry(f, "glm", e); err != nil {
		t.Fatalf("SetPriceEntry: %v", err)
	}
	pre, rest, _ := strings.Cut(settingsCfgSrc, "[prices.glm]\n")
	_, post, _ := strings.Cut(rest, "[prices.kimi]\n")
	want := pre + "[prices.glm]\nunit = \"智谱积分\"\nper = 10000\n\n" +
		"[[prices.glm.versions]]\neffective_from = \"2026-09-17\"\np_in = 6.9\np_cache = 1.7\np_out = 24\n\n" +
		"[[prices.glm.versions]]\neffective_from = \"2026-10-01\"\np_in = 7\np_out = 26\n\n" +
		"[prices.kimi]\n" + post
	if after := readFileEdit(t, f); after != want {
		t.Errorf("价表替换不符：\n--got--\n%s\n--want--\n%s", after, want)
	}
	mustLoadEdit(t, f)
	// 生产解析器回读（internal/prices 同文件同源）
	books := prices.LoadPrices(f)
	b, ok := books["glm"]
	if !ok {
		t.Fatal("LoadPrices 缺 glm")
	}
	if b.Unit != "智谱积分" || b.Per != 10000 || len(b.Versions) != 2 {
		t.Fatalf("glm 回读不符: %+v", b)
	}
	if b.Versions[0].PIn != 6.9 || b.Versions[0].PCache == nil || *b.Versions[0].PCache != 1.7 || b.Versions[0].POut != 24 {
		t.Errorf("版本0回读不符: %+v", b.Versions[0])
	}
	if b.Versions[1].PIn != 7 || b.Versions[1].PCache != nil || b.Versions[1].POut != 26 {
		t.Errorf("版本1回读不符（p_cache 缺失应=无缓存价）: %+v", b.Versions[1])
	}
	kimi := books["kimi"]
	if len(kimi.Versions) != 1 || kimi.Versions[0].PIn != 1.0 || kimi.Versions[0].POut != 4.0 {
		t.Errorf("kimi 条目应保真: %+v", kimi)
	}
}

func TestSettingsPriceEntryAddAndRemove(t *testing.T) {
	// 新 key：EOF 追加
	f := writeCfg(t, settingsCfgSrc)
	e := PriceEntry{Unit: "元", Per: 10000, Versions: []PriceVersionEntry{
		{EffectiveFrom: "2026-10-01", PIn: 2, POut: 8},
	}}
	if err := SetPriceEntry(f, "ds", e); err != nil {
		t.Fatalf("SetPriceEntry(新增): %v", err)
	}
	want := settingsCfgSrc + "\n[prices.ds]\nunit = \"元\"\nper = 10000\n\n" +
		"[[prices.ds.versions]]\neffective_from = \"2026-10-01\"\np_in = 2\np_out = 8\n"
	if after := readFileEdit(t, f); after != want {
		t.Errorf("价表新增不符：\n--got--\n%s\n--want--\n%s", after, want)
	}
	mustLoadEdit(t, f)
	if b, ok := prices.LoadPrices(f)["ds"]; !ok || len(b.Versions) != 1 {
		t.Errorf("ds 回读不符: %+v", b)
	}

	// 删除：versions 子表随条目整树摘除
	f = writeCfg(t, settingsCfgSrc)
	if err := RemovePriceEntry(f, "glm"); err != nil {
		t.Fatalf("RemovePriceEntry: %v", err)
	}
	pre, rest, _ := strings.Cut(settingsCfgSrc, "[prices.glm]\n")
	_, post, _ := strings.Cut(rest, "[prices.kimi]\n")
	want = strings.TrimSuffix(pre, "\n") + "[prices.kimi]\n" + post
	if after := readFileEdit(t, f); after != want {
		t.Errorf("价表删除不符：\n--got--\n%s\n--want--\n%s", after, want)
	}
	if strings.Contains(want, "prices.glm") {
		t.Fatal("测试自身期望值构造错误")
	}
	mustLoadEdit(t, f)
	books := prices.LoadPrices(f)
	if _, ok := books["glm"]; ok {
		t.Error("glm 应已删除")
	}
	if _, ok := books["kimi"]; !ok {
		t.Error("kimi 应保留")
	}

	// 未知 key 拒删且原文件不动
	before := readFileEdit(t, f)
	if err := RemovePriceEntry(f, "ghost"); err == nil {
		t.Fatal("未知价表条目应拒删")
	}
	if after := readFileEdit(t, f); after != before {
		t.Error("拒删路径原文件不得动")
	}
}

func TestSettingsQuotedAndDottedNames(t *testing.T) {
	// 引号键条目名（含点）：段感知定位，不会被前缀误伤
	src := strings.Replace(settingsCfgSrc, "[providers.glm]", "[providers.\"a.b\"]", 1)
	f := writeCfg(t, src)
	if err := RemoveProviderEntry(f, "a.b"); err != nil {
		t.Fatalf("引号键条目删除: %v", err)
	}
	if after := readFileEdit(t, f); strings.Contains(after, "a.b") {
		t.Errorf("引号键条目应整段删除:\n%s", after)
	}
	mustLoadEdit(t, f)

	// 点分节名（ferry.same_model）：缺节创建与整写同原语
	f2 := writeCfg(t, settingsCfgSrc)
	if err := SetSectionTOML(f2, "ferry.same_model", "enabled = false\nthreshold_min = 20\n"); err != nil {
		t.Fatalf("SetSectionTOML(点分节名): %v", err)
	}
	want := settingsCfgSrc + "\n[ferry.same_model]\nenabled = false\nthreshold_min = 20\n"
	if after := readFileEdit(t, f2); after != want {
		t.Errorf("点分节名创建不符：\n--got--\n%s\n--want--\n%s", after, want)
	}
	cfg := mustLoadEdit(t, f2)
	if cfg.SameModel.Enabled || cfg.SameModel.ThresholdMin != 20 {
		t.Errorf("same_model 回读不符: %+v", cfg.SameModel)
	}
}

func TestSettingsSectionCRLFLineEndings(t *testing.T) {
	f := writeCfg(t, strings.ReplaceAll(settingsCfgSrc, "\n", "\r\n"))
	if err := SetSectionTOML(f, "gate", "cc_mode = \"off\"\ncodex_mode = \"off\"\n"); err != nil {
		t.Fatalf("SetSectionTOML(CRLF): %v", err)
	}
	after := readFileEdit(t, f)
	if n, crlf := strings.Count(after, "\n"), strings.Count(after, "\r\n"); n != crlf {
		t.Errorf("行尾应全为 CRLF: LF=%d CRLF=%d\n%q", n, crlf, after)
	}
	cfg := mustLoadEdit(t, f)
	if cfg.GateCC != "off" || cfg.GateCodex != "off" {
		t.Errorf("CRLF 回读不符: cc=%q codex=%q", cfg.GateCC, cfg.GateCodex)
	}
}

func TestSettingsEditMissingFileErrors(t *testing.T) {
	p := filepath.Join(t.TempDir(), "absent.toml")
	if err := SetSectionTOML(p, "gate", "cc_mode = \"off\"\n"); err == nil {
		t.Error("配置不存在应报错: SetSectionTOML")
	}
	if err := SetProviderEntry(p, "x", ProviderEntry{BaseURL: "https://x.example"}); err == nil {
		t.Error("配置不存在应报错: SetProviderEntry")
	}
	if err := RemoveProviderEntry(p, "x"); err == nil {
		t.Error("配置不存在应报错: RemoveProviderEntry")
	}
	if err := SetPriceEntry(p, "k", PriceEntry{Unit: "u", Per: 10000}); err == nil {
		t.Error("配置不存在应报错: SetPriceEntry")
	}
	if err := RemovePriceEntry(p, "k"); err == nil {
		t.Error("配置不存在应报错: RemovePriceEntry")
	}
}

// seOwnKeysCfgSrc 自键层变体（票04④）夹具：[ferry] 带自键 provider/chain +
// 子表 [ferry.same_model]（内含更深一层 ceiling 子表）——SetSectionOwnKeys
// 只动自键层的验收面；chain 引用的 providers 都在（Load 校验前提）。
const seOwnKeysCfgSrc = `# 顶层注释

[server]
port = 15700

[thresholds]
summarize_s = 1500
block_s = 2100

[providers.glm]
base_url = "https://open.bigmodel.cn/api/paas/v4"
model = "glm-5.3"
api_key = "sk-glm-1"
window = 131072

[providers.kimi]
base_url = "https://api.moonshot.cn/v1"
model = "kimi-for-coding"
api_key = "sk-kimi-1"
window = 131072

[ferry]
provider = "glm"
chain = ["glm"]

[ferry.same_model]
enabled = true
upstreams = ["glm"]
threshold_min = 20

[ferry.same_model.ceiling]
kimi = 15

[notify]
enabled = true
`

// seCut 把 src 切成（[ferry] 前，[ferry] 自键段，[ferry.same_model] 起）三段，
// 供自键层改写的逐字节期望值拼接。
func seCut(src string) (pre, own, post string) {
	pre, rest, _ := strings.Cut(src, "[ferry]\n")
	own, post, _ = strings.Cut(rest, "[ferry.same_model]\n")
	return pre, "[ferry]\n" + own + "[ferry.same_model]\n", post
}

// TestSettingsSectionOwnKeysRewritesOnlyOwnKeyLayer 自键层变体主钉子：
// [ferry] 自键改写后——自键段=新值、[ferry.same_model] 子树（含 ceiling）
// 逐字节不动、其余节逐字节不动；回读 provider/chain 换新、same_model 原值。
func TestSettingsSectionOwnKeysRewritesOnlyOwnKeyLayer(t *testing.T) {
	f := writeCfg(t, seOwnKeysCfgSrc)
	if err := SetSectionOwnKeys(f, "ferry", "provider = \"kimi\"\nchain = [\"kimi\", \"glm\"]\n"); err != nil {
		t.Fatalf("SetSectionOwnKeys: %v", err)
	}
	pre, _, post := seCut(seOwnKeysCfgSrc)
	want := pre + "[ferry]\nprovider = \"kimi\"\nchain = [\"kimi\", \"glm\"]\n\n[ferry.same_model]\n" + post
	if after := readFileEdit(t, f); after != want {
		t.Errorf("自键层改写不符：\n--got--\n%s\n--want--\n%s", after, want)
	}
	cfg := mustLoadEdit(t, f)
	if cfg.FerryProvider != "kimi" || len(cfg.FerryChain) != 2 ||
		cfg.FerryChain[0] != "kimi" || cfg.FerryChain[1] != "glm" {
		t.Errorf("ferry 自键回读不符: provider=%q chain=%v", cfg.FerryProvider, cfg.FerryChain)
	}
	if !cfg.SameModel.Enabled || len(cfg.SameModel.Upstreams) != 1 ||
		cfg.SameModel.Upstreams[0] != "glm" || cfg.SameModel.ThresholdMin != 20 ||
		cfg.SameModel.CeilingMin["kimi"] != 15 {
		t.Errorf("same_model 子表回读不符: %+v", cfg.SameModel)
	}
}

// TestSettingsSectionOwnKeysOmittedKeyCleared 自键层整写语义：body 省略的自键
// （chain）被清除（provider 单键兜底等价单元素链）、子表照旧逐字节保留。
func TestSettingsSectionOwnKeysOmittedKeyCleared(t *testing.T) {
	f := writeCfg(t, seOwnKeysCfgSrc)
	if err := SetSectionOwnKeys(f, "ferry", "provider = \"kimi\"\n"); err != nil {
		t.Fatalf("SetSectionOwnKeys: %v", err)
	}
	cfg := mustLoadEdit(t, f)
	if cfg.FerryProvider != "kimi" || len(cfg.FerryChain) != 0 {
		t.Errorf("省略自键应清除: provider=%q chain=%v", cfg.FerryProvider, cfg.FerryChain)
	}
	pre, _, post := seCut(seOwnKeysCfgSrc)
	want := pre + "[ferry]\nprovider = \"kimi\"\n\n[ferry.same_model]\n" + post
	if after := readFileEdit(t, f); after != want {
		t.Errorf("省略自键改写不符：\n--got--\n%s\n--want--\n%s", after, want)
	}
}

// TestSettingsSectionOwnKeysRejectsInvalid 拒写面：body 引用未定义 provider
// （Load 期 chain 校验）/坏 TOML 正文 → 报错且原文件逐字节不动（子表不殃及）。
func TestSettingsSectionOwnKeysRejectsInvalid(t *testing.T) {
	cases := []struct {
		name, body, wantErr string
	}{
		{"chain 引用未定义 provider", "provider = \"glm\"\nchain = [\"ghost\"]\n", "未定义的 provider"},
		{"坏TOML正文", "= broken\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := writeCfg(t, seOwnKeysCfgSrc)
			before := readFileEdit(t, f)
			err := SetSectionOwnKeys(f, "ferry", tc.body)
			if err == nil {
				t.Fatal("非法自键层写应被拒")
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("拒绝信息应含 %q: %v", tc.wantErr, err)
			}
			if after := readFileEdit(t, f); after != before {
				t.Error("拒写路径原文件不得动")
			}
			assertNoSettingsTmp(t, f)
		})
	}
}

// TestSettingsSectionOwnKeysCreateWhenMissing 缺节创建：EOF 追加（与整写
// 原语同形）；带子表节先自键层创建、子表照常后续经点分节名单独写。
func TestSettingsSectionOwnKeysCreateWhenMissing(t *testing.T) {
	src := strings.ReplaceAll(seOwnKeysCfgSrc,
		"[ferry]\nprovider = \"glm\"\nchain = [\"glm\"]\n\n", "")
	f := writeCfg(t, src)
	if err := SetSectionOwnKeys(f, "ferry", "provider = \"glm\"\n"); err != nil {
		t.Fatalf("SetSectionOwnKeys(缺节): %v", err)
	}
	want := src + "\n[ferry]\nprovider = \"glm\"\n"
	if after := readFileEdit(t, f); after != want {
		t.Errorf("缺节创建不符：\n--got--\n%s\n--want--\n%s", after, want)
	}
	if cfg := mustLoadEdit(t, f); cfg.FerryProvider != "glm" {
		t.Errorf("回读 provider = %q, want glm", cfg.FerryProvider)
	}
}
