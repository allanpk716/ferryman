// same_model_test.go — 票01（同模型摆渡八张竖切 · 配置面地基）：[ferry.same_model]
// 与 [tuning] 两节的缺省值/解析/钳位与不变量链校验/doctor 四检查判定测试。
// 决策依据：ADR-0015 + 夜链 decisions.md D2/D5/D6/D10/D13。
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ferryman/internal/prices"
)

// writeTOML 临时配置文件辅助（路径环境隔离，绝不读真实用户目录）。
func writeTOML(t *testing.T, text string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(f, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// idxChecks 检查行按名索引（doctor 四查各自断言用）。
func idxChecks(rows []DoctorCheck) map[string]DoctorCheck {
	m := make(map[string]DoctorCheck, len(rows))
	for _, r := range rows {
		m[r.Name] = r
	}
	return m
}

// ---- 缺省值（验收：同模型 off、recommend、20min）----

func TestSameModelAndTuningDefaults(t *testing.T) {
	d := Default()
	if d.SameModel.Enabled {
		t.Fatal("same_model 默认应为 off（D5：E1 评测候选，不当默认）")
	}
	if d.SameModel.ThresholdMin != 20 {
		t.Fatalf("threshold_min 默认 = %v, want 20（D2 冷启动种子）", d.SameModel.ThresholdMin)
	}
	if len(d.SameModel.Upstreams) != 0 || len(d.SameModel.CeilingMin) != 0 {
		t.Fatalf("默认白名单/覆盖应空: %+v", d.SameModel)
	}
	if d.Tuning.Mode != "recommend" {
		t.Fatalf("tuning.mode 默认 = %q, want recommend（D10）", d.Tuning.Mode)
	}
	if d.Tuning.WindowDays != 30 || d.Tuning.MinEvents != 30 {
		t.Fatalf("tuning 窗/样本默认 = %d/%d, want 30/30（D10 护栏④）",
			d.Tuning.WindowDays, d.Tuning.MinEvents)
	}
	if err := Validate(d, false); err != nil {
		t.Fatalf("Validate(Default) = %v, want nil", err)
	}
}

// ---- 解析：全字段/部分字段/缺文件 ----

func TestLoadSameModelAndTuningSections(t *testing.T) {
	f := writeTOML(t, `
[ferry.same_model]
enabled = true
threshold_min = 18
upstreams = ["智谱", "deepseek"]

[ferry.same_model.ceiling]
"智谱" = 15

[tuning]
mode = "manual"
window_days = 14
min_events = 20
`)
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sm := cfg.SameModel
	if !sm.Enabled || sm.ThresholdMin != 18 ||
		len(sm.Upstreams) != 2 || sm.Upstreams[0] != "智谱" || sm.Upstreams[1] != "deepseek" {
		t.Fatalf("same_model = %+v", sm)
	}
	if got := sm.CeilingMin["智谱"]; got != 15 {
		t.Fatalf("ceiling[智谱] = %v, want 15", got)
	}
	tn := cfg.Tuning
	if tn.Mode != "manual" || tn.WindowDays != 14 || tn.MinEvents != 20 {
		t.Fatalf("tuning = %+v", tn)
	}
}

func TestLoadSameModelPartialKeepsDefaults(t *testing.T) {
	// .get 语义：节内缺字段回落默认（enabled 覆盖、其余保留）
	f := writeTOML(t, "[ferry.same_model]\nenabled = true\n")
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.SameModel.Enabled || cfg.SameModel.ThresholdMin != 20 ||
		len(cfg.SameModel.Upstreams) != 0 || len(cfg.SameModel.CeilingMin) != 0 {
		t.Fatalf("same_model = %+v, want enabled=true 其余默认", cfg.SameModel)
	}
	if cfg.Tuning != (TuningCfg{Mode: "recommend", WindowDays: 30, MinEvents: 30}) {
		t.Fatalf("tuning = %+v, want 全默认（节缺失不漂移）", cfg.Tuning)
	}
}

func TestLoadSameModelBadTypes(t *testing.T) {
	cases := []struct {
		name, toml, wantErr string
	}{
		{"upstreams 非数组", "[ferry.same_model]\nupstreams = \"智谱\"\n", "ferry.same_model.upstreams 不是数组"},
		{"ceiling 非表", "[ferry.same_model]\nceiling = \"智谱\"\n", "节 ferry.same_model.ceiling 不是表"},
		{"ceiling 值非数", "[ferry.same_model.ceiling]\n\"智谱\" = \"abc\"\n", "ferry.same_model.ceiling.智谱"},
		{"threshold_min 非数", "[ferry.same_model]\nthreshold_min = \"abc\"\n", "无法转换为浮点数"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := writeTOML(t, tc.toml)
			_, err := Load(f, false)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want 含 %q", err, tc.wantErr)
			}
		})
	}
}

// ---- 钳位与不变量链校验（验收：越界/倒挂被拒并给人话错误）----

func TestSameModelClampRejectedOnLoad(t *testing.T) {
	cases := []struct {
		name    string
		enabled bool
		toml    string
		want    string
	}{
		{"全局阈值超总结阈值", true, "threshold_min = 30\n",
			"ferry.same_model.threshold_min=30.0 分钟超出钳位上限（总结阈值 25.0 分钟=1500.0s；不变量链:同模型 ≤ 总结 ≤ 拦截）"},
		{"全局阈值低于下限", true, "threshold_min = 8\n",
			"ferry.same_model.threshold_min=8.0 分钟低于钳位下限 10.0 分钟（不变量链:同模型 ≤ 总结 ≤ 拦截）"},
		{"每上游覆盖超总结阈值", true, "[ferry.same_model.ceiling]\n\"智谱\" = 30\n",
			"ferry.same_model.ceiling[\"智谱\"]=30.0 分钟超出钳位上限（总结阈值 25.0 分钟=1500.0s；不变量链:同模型 ≤ 总结 ≤ 拦截）"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			onOff := "false"
			if tc.enabled {
				onOff = "true"
			}
			f := writeTOML(t, "[ferry.same_model]\nenabled = "+onOff+"\n"+tc.toml)
			_, err := Load(f, false)
			if tc.enabled {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("err = %v, want 含 %q", err, tc.want)
				}
			} else if err != nil {
				// 关闭态休眠键不拦启动（问询守望 lead 同款口径）
				t.Fatalf("disabled 时 err = %v, want nil（休眠不拦）", err)
			}
		})
	}
}

func TestSameModelClampBoundariesPass(t *testing.T) {
	// 钳位区间 [10, 总结阈值] 双端含：10 与 25 恰好合法
	for _, v := range []string{"10", "25"} {
		f := writeTOML(t, "[ferry.same_model]\nenabled = true\nthreshold_min = "+v+"\n")
		if _, err := Load(f, false); err != nil {
			t.Fatalf("threshold_min=%s 应合法, err = %v", v, err)
		}
	}
	// 每上游覆盖双端同理
	f := writeTOML(t, "[ferry.same_model]\nenabled = true\n[ferry.same_model.ceiling]\n\"智谱\" = 25\n")
	if _, err := Load(f, false); err != nil {
		t.Fatalf("ceiling=25 应合法, err = %v", err)
	}
}

func TestTuningValidation(t *testing.T) {
	cases := []struct {
		name, toml, want string
	}{
		{"mode 非法", "[tuning]\nmode = \"bogus\"\n",
			"tuning.mode 非法: bogus（可选 ('manual', 'recommend', 'auto')）"},
		{"window_days 非正", "[tuning]\nwindow_days = 0\n",
			"tuning.window_days 须 > 0（当前 0）"},
		{"min_events 非正", "[tuning]\nmin_events = -1\n",
			"tuning.min_events 须 > 0（当前 -1）"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := writeTOML(t, tc.toml)
			_, err := Load(f, false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want 含 %q", err, tc.want)
			}
		})
	}
}

// ---- D13 语义缝：manual 档配置值=生效值，取 CeilingFor 即生效上限 ----

func TestCeilingForOverrideWins(t *testing.T) {
	c := Default()
	c.SameModel.Upstreams = []string{"智谱", "deepseek"}
	if got := c.SameModel.CeilingFor("deepseek"); got != 20 {
		t.Fatalf("无覆盖上游 = %v, want 回落全局 20", got)
	}
	c.SameModel.CeilingMin["智谱"] = 15
	if got := c.SameModel.CeilingFor("智谱"); got != 15 {
		t.Fatalf("有覆盖上游 = %v, want 15", got)
	}
}

// ---- doctor 四检查（验收：各有文案与判定）----

// pcacheBook 构造单本价格表（p_cache 可指针注入 nil=无缓存价）。
func pcacheBook(pcache *float64, versions ...prices.PriceVersion) map[string]prices.PriceBook {
	return map[string]prices.PriceBook{
		"glm": {Key: "glm", Unit: "智谱积分", Per: 10000, Versions: versions},
	}
}

func TestSameModelDoctorChecksDefaultZeroRows(t *testing.T) {
	// 默认配置（off/recommend/空白名单）零新增检查行——存量 doctor 输出零漂移
	if got := SameModelDoctorChecks(Default(), nil, nil); len(got) != 0 {
		t.Fatalf("默认配置应零行, got %+v", got)
	}
}

func TestSameModelDoctorChecksEnabledEmptyWhitelist(t *testing.T) {
	c := Default()
	c.SameModel.Enabled = true
	rows := idxChecks(SameModelDoctorChecks(c, nil, nil))
	r, ok := rows["same_model_whitelist"]
	if !ok || r.OK {
		t.Fatalf("开启+白名单空应 fail: %+v", r)
	}
	if !strings.Contains(r.Detail, "白名单为空") {
		t.Fatalf("文案缺要害: %q", r.Detail)
	}
	if _, ok := rows["same_model_arm_verdict"]; ok {
		t.Fatal("白名单空不应出 arm_verdict 行")
	}
	clamp := rows["same_model_clamp"]
	if clamp.Name == "" || !clamp.OK {
		t.Fatalf("钳位无冲突应 pass: %+v", clamp)
	}
	if _, ok := rows["tuning_price_pcache"]; ok {
		t.Fatal("非 auto 不应出 pcache 行")
	}
}

func TestSameModelDoctorChecksArmVerdicts(t *testing.T) {
	c := Default()
	c.SameModel.Enabled = true
	c.SameModel.Upstreams = []string{"智谱", "kimi"}

	// arm 缝未装配（nil）= 按无结论对待：白名单「全未启用」fail + 结论 fail
	rows := idxChecks(SameModelDoctorChecks(c, nil, nil))
	if r := rows["same_model_whitelist"]; r.OK || !strings.Contains(r.Detail, "均未启用") {
		t.Fatalf("无结论应判全未启用: %+v", r)
	}
	if r := rows["same_model_arm_verdict"]; r.OK ||
		!strings.Contains(r.Detail, "智谱、kimi") || !strings.Contains(r.Detail, "实跳臂") {
		t.Fatalf("arm_verdict 应列出缺结论条目: %+v", r)
	}

	// 结论在位但均未启用 → 白名单仍 fail；结论 fail 只列无结论者
	armNone := func(string) (bool, bool) { return true, false }
	rows = idxChecks(SameModelDoctorChecks(c, nil, armNone))
	if r := rows["same_model_whitelist"]; r.OK || !strings.Contains(r.Detail, "均未启用") {
		t.Fatalf("有结论未启用应仍 fail: %+v", r)
	}
	if r := rows["same_model_arm_verdict"]; !r.OK {
		t.Fatalf("有结论（未启用）arm_verdict 应 pass: %+v", r)
	}
	armPartial := func(u string) (bool, bool) { return u == "智谱", true }
	rows = idxChecks(SameModelDoctorChecks(c, nil, armPartial))
	if r := rows["same_model_arm_verdict"]; r.OK || !strings.Contains(r.Detail, "kimi") {
		t.Fatalf("部分结论应只列缺失者: %+v", r)
	}

	// 全启用 → 两查全绿
	armAll := func(string) (bool, bool) { return true, true }
	rows = idxChecks(SameModelDoctorChecks(c, nil, armAll))
	if r := rows["same_model_whitelist"]; !r.OK {
		t.Fatalf("全启用 whitelist 应 pass: %+v", r)
	}
	if r := rows["same_model_arm_verdict"]; !r.OK {
		t.Fatalf("全启用 arm_verdict 应 pass: %+v", r)
	}
}

func TestSameModelDoctorChecksPricePCache(t *testing.T) {
	newAuto := func() *Config {
		c := Default()
		c.Tuning.Mode = "auto"
		c.SameModel.Upstreams = []string{"智谱"}
		return c
	}

	// books 未装配 → not_checked（如实标注，不伪造）
	rows := idxChecks(SameModelDoctorChecks(newAuto(), nil, nil))
	if r := rows["tuning_price_pcache"]; !r.Skip || r.Detail == "" {
		t.Fatalf("books 未装配应 Skip: %+v", r)
	}

	// key 命中且现价有 p_cache → pass
	cache := 1.7
	rows = idxChecks(SameModelDoctorChecks(newAuto(),
		pcacheBook(&cache, prices.PriceVersion{EffectiveFrom: "2026-09-01", PIn: 6.9, PCache: &cache, POut: 24}), nil))
	if r := rows["tuning_price_pcache"]; !r.OK {
		t.Fatalf("有 p_cache 应 pass: %+v", r)
	}

	// 现价无 p_cache → fail（计算器拒算，无缓存经济红线）
	rows = idxChecks(SameModelDoctorChecks(newAuto(),
		pcacheBook(nil, prices.PriceVersion{EffectiveFrom: "2026-09-01", PIn: 6.9, POut: 24}), nil))
	if r := rows["tuning_price_pcache"]; r.OK || !strings.Contains(r.Detail, "p_cache") {
		t.Fatalf("缺 p_cache 应 fail: %+v", r)
	}

	// 版本全在未来（At 不命中）→ 回落末版判定（watcher 同款），缺 p_cache 照 fail
	rows = idxChecks(SameModelDoctorChecks(newAuto(),
		pcacheBook(nil, prices.PriceVersion{EffectiveFrom: "2099-01-01", PIn: 6.9, POut: 24}), nil))
	if r := rows["tuning_price_pcache"]; r.OK {
		t.Fatalf("末版缺 p_cache 应 fail: %+v", r)
	}

	// 多本且 key 不命中 → 无法定位价格表，fail
	books := map[string]prices.PriceBook{
		"a": {Key: "a", Versions: []prices.PriceVersion{{EffectiveFrom: "2026-09-01", PIn: 1, PCache: &cache, POut: 2}}},
		"b": {Key: "b", Versions: []prices.PriceVersion{{EffectiveFrom: "2026-09-01", PIn: 1, PCache: &cache, POut: 2}}},
	}
	rows = idxChecks(SameModelDoctorChecks(newAuto(), books, nil))
	if r := rows["tuning_price_pcache"]; r.OK || !strings.Contains(r.Detail, "价格表") {
		t.Fatalf("无法定位价格表应 fail: %+v", r)
	}

	// 白名单空 + auto → pass（无同模型阈值需现算）
	c := Default()
	c.Tuning.Mode = "auto"
	rows = idxChecks(SameModelDoctorChecks(c, nil, nil))
	if r := rows["tuning_price_pcache"]; r.Skip || !r.OK {
		t.Fatalf("白名单空应 pass: %+v", r)
	}

	// 非 auto 不出行
	c2 := Default()
	if rows := SameModelDoctorChecks(c2, nil, nil); len(rows) != 0 {
		t.Fatalf("recommend 不应出任何行: %+v", rows)
	}
}

func TestSameModelDoctorChecksClamp(t *testing.T) {
	// 休眠冲突（关闭态）：fail 并注明休眠
	c := Default()
	c.SameModel.Upstreams = []string{"智谱"} // 仅让特性面被碰
	c.SameModel.ThresholdMin = 30
	rows := idxChecks(SameModelDoctorChecks(c, nil, nil))
	if r := rows["same_model_clamp"]; r.OK || !strings.Contains(r.Detail, "休眠") {
		t.Fatalf("休眠冲突应 fail 并注明: %+v", r)
	}

	// 启用态冲突（Load 已拒，doctor 面兜底）：注明将拒绝启动
	c.SameModel.Enabled = true
	rows = idxChecks(SameModelDoctorChecks(c, nil, nil))
	if r := rows["same_model_clamp"]; r.OK || !strings.Contains(r.Detail, "拒绝启动") {
		t.Fatalf("启用冲突应注明拒绝启动: %+v", r)
	}

	// 无冲突：pass 且锁格式
	c2 := Default()
	c2.SameModel.Enabled = true
	rows = idxChecks(SameModelDoctorChecks(c2, nil, nil))
	want := "同模型阈值钳位无冲突：20.0 分钟 ∈ [10.0, 25.0]（总结阈值）"
	if r := rows["same_model_clamp"]; !r.OK || r.Detail != want {
		t.Fatalf("无冲突文案 = %q, want %q", r.Detail, want)
	}
}

func TestSameModelDoctorChecksRowOrder(t *testing.T) {
	// 满配触发四行：顺序稳定 = whitelist → arm_verdict → price_pcache → clamp
	c := Default()
	c.SameModel.Enabled = true
	c.SameModel.Upstreams = []string{"智谱"}
	c.SameModel.ThresholdMin = 30
	c.Tuning.Mode = "auto"
	rows := SameModelDoctorChecks(c, nil, nil)
	wantOrder := []string{"same_model_whitelist", "same_model_arm_verdict",
		"tuning_price_pcache", "same_model_clamp"}
	if len(rows) != len(wantOrder) {
		t.Fatalf("行数 = %d, want %d: %+v", len(rows), len(wantOrder), rows)
	}
	for i, r := range rows {
		if r.Name != wantOrder[i] {
			t.Fatalf("第 %d 行 = %s, want %s", i, r.Name, wantOrder[i])
		}
	}
}
