package policy

import (
	"errors"
	"strings"
	"testing"
	"time"

	"ferryman/internal/config"
	"ferryman/internal/prices"
)

// same_model_test.go — 票05：同模型阈值闭式推导的黄金对拍、钳位、三态生效值
// 与四路拒算（缺 p_cache / 无价差 / 经济不可行 / 映射失败）。

// rep 构造 n 个相同观测（测试辅助）。
func rep(n int, v float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// goldenIdle 黄金用例闲置间隔样本（n=20）：10×5、5×10、3×15、1×20、1×40，
// 倒序传入证明内部排序不依赖输入顺序。F(25)=19/20=0.95，D=1/20=0.05。
func goldenIdle() []float64 {
	idle := append(append(append(rep(10, 5), rep(5, 10)...), rep(3, 15)...), 20, 40)
	// 倒序：[40,20,15,15,15,10,10,10,10,10,5×10]
	for i, j := 0, len(idle)-1; i < j; i, j = i+1, j-1 {
		idle[i], idle[j] = idle[j], idle[i]
	}
	return idle
}

// goldenObs GLM 口径黄金输入：150k 前缀、1000 输出预留、TTL 观测 [26,30,34]
// （中位 30 → 缓存安全点 0.8×30=24）。
func goldenObs() SameModelObs {
	return SameModelObs{
		PrefixTokens: 150000,
		OutTokens:    1000,
		TTLObsMin:    []float64{26, 30, 34},
		IdleObsMin:   goldenIdle(),
	}
}

// smKind 提取 SameModelError.Kind（断言拒算分类）。
func smKind(t *testing.T, err error) string {
	t.Helper()
	var e *SameModelError
	if !errors.As(err, &e) {
		t.Fatalf("err = %v (%T), want *SameModelError", err, err)
	}
	return e.Kind
}

// TestSameModelGoldenGLM 锚定整条复算链（手算）：
//
//	C   = 15×1.7 + 0.1×24          = 27.9      （摆渡成本：缓存读+输出）
//	T   = 15×6.9                    = 103.5     （参照：同前缀全价读）
//	D   = 1/20                      = 0.05      （闲置>25min 的死亡占比）
//	q   = 0.95 − 0.05×(75.6/27.9)   ≈ 0.8145161（最早不亏分位）
//	econ= ceil(0.8145×20)=17 → 第 17 小样本 = 15 分钟
//	cap = 0.8×median(26,30,34)=0.8×30 = 24 分钟
//	raw = min(24, 25) = 24；suggest = clamp(24,10,25) = 24；15 ≤ 24 可行
func TestSameModelGoldenGLM(t *testing.T) {
	b := glmBook()
	res, err := DeriveSameModelThreshold(b, b.Versions[0], goldenObs(), 25)
	if err != nil {
		t.Fatal(err)
	}
	approxAbs(t, "median_ttl", res.MedianTTLMin, 30, 1e-9)
	approxAbs(t, "cache_safe", res.CacheSafeMin, 24, 1e-9)
	approxAbs(t, "ferry_cost", res.FerryCost, 27.9, 1e-6)
	approxAbs(t, "ref_cost", res.RefCost, 103.5, 1e-6)
	approxAbs(t, "dead_frac", res.DeadFrac, 0.05, 1e-9)
	approxAbs(t, "hit_target_q", res.HitTargetQ, 0.8145161, 1e-6)
	approxAbs(t, "econ_floor", res.EconFloorMin, 15, 1e-9)
	approxAbs(t, "raw_min", res.RawMin, 24, 1e-9)
	approxAbs(t, "suggest_min", res.SuggestMin, 24, 1e-9)
	if res.SeedFallback {
		t.Fatal("有 TTL 观测不应走种子回退")
	}
}

// TestSameModelClampSumCap 缓存安全点超总结阈值 → 被总结阈值封顶。
func TestSameModelClampSumCap(t *testing.T) {
	b := glmBook()
	obs := goldenObs()
	obs.TTLObsMin = []float64{200, 300, 400} // 中位 300 → 安全点 240
	res, err := DeriveSameModelThreshold(b, b.Versions[0], obs, 25)
	if err != nil {
		t.Fatal(err)
	}
	approxAbs(t, "cache_safe", res.CacheSafeMin, 240, 1e-9)
	approxAbs(t, "raw_min", res.RawMin, 25, 1e-9) // min(240,25)
	approxAbs(t, "suggest_min", res.SuggestMin, 25, 1e-9)
}

// TestSameModelClampFloor 缓存安全点低于 10 分钟下限 → 抬到下限
// （间隔样本全早回归 → 最早不亏点 5 分钟，可行性不受下限钳位影响）。
func TestSameModelClampFloor(t *testing.T) {
	b := glmBook()
	obs := goldenObs()
	obs.TTLObsMin = []float64{8, 9, 10}     // 中位 9 → 安全点 7.2
	obs.IdleObsMin = append(rep(19, 5), 40) // F(25)=0.95,D=0.05 → econ=5
	res, err := DeriveSameModelThreshold(b, b.Versions[0], obs, 25)
	if err != nil {
		t.Fatal(err)
	}
	approxAbs(t, "raw_min", res.RawMin, 7.2, 1e-9)
	approxAbs(t, "suggest_min", res.SuggestMin, 10, 1e-9) // 抬到下限
}

// TestSameModelSeedFallback 无 TTL 观测 → 冷启动种子层（D2 三层供给第一层），
// 不拒算；种子同样受总结阈值钳位。
func TestSameModelSeedFallback(t *testing.T) {
	b := glmBook()
	obs := goldenObs()
	obs.TTLObsMin = nil
	res, err := DeriveSameModelThreshold(b, b.Versions[0], obs, 25)
	if err != nil {
		t.Fatal(err)
	}
	if !res.SeedFallback {
		t.Fatal("无 TTL 观测应走种子回退")
	}
	approxAbs(t, "raw_min", res.RawMin, 20, 1e-9) // min(种子20, 25)
	approxAbs(t, "suggest_min", res.SuggestMin, 20, 1e-9)

	// 种子被更小的总结阈值压住：总结 15 → 生效 15
	res, err = DeriveSameModelThreshold(b, b.Versions[0], obs, 15)
	if err != nil {
		t.Fatal(err)
	}
	approxAbs(t, "suggest_min", res.SuggestMin, 15, 1e-9)
}

// TestSameModelRefusesBadObs 脏输入拒算（bad_obs）：间隔缺失、TTL 非正、
// 前缀/输出非正、总结阈值非正。
func TestSameModelRefusesBadObs(t *testing.T) {
	b := glmBook()
	base := goldenObs()

	obs := base
	obs.IdleObsMin = nil // 有 TTL 但无间隔观测
	if _, err := DeriveSameModelThreshold(b, b.Versions[0], obs, 25); smKind(t, err) != KindBadObs {
		t.Fatalf("间隔缺失: kind = %v, want bad_obs", err)
	}

	obs = base
	obs.TTLObsMin = []float64{0, 30}
	if _, err := DeriveSameModelThreshold(b, b.Versions[0], obs, 25); smKind(t, err) != KindBadObs {
		t.Fatalf("TTL 含 0: kind = %v, want bad_obs", err)
	}

	obs = base
	obs.PrefixTokens = 0
	if _, err := DeriveSameModelThreshold(b, b.Versions[0], obs, 25); smKind(t, err) != KindBadObs {
		t.Fatalf("前缀 0: kind = %v, want bad_obs", err)
	}

	obs = base
	obs.OutTokens = 0
	if _, err := DeriveSameModelThreshold(b, b.Versions[0], obs, 25); smKind(t, err) != KindBadObs {
		t.Fatalf("输出 0: kind = %v, want bad_obs", err)
	}

	if _, err := DeriveSameModelThreshold(b, b.Versions[0], base, 0); smKind(t, err) != KindBadObs {
		t.Fatalf("总结阈值 0: kind = %v, want bad_obs", err)
	}
}

// TestSameModelRefusesNoPCache 价格表缺 p_cache → 拒算（无缓存经济红线），
// 错误文案带书键与"拒绝推导"。
func TestSameModelRefusesNoPCache(t *testing.T) {
	nb := prices.PriceBook{Key: "nocache", Unit: "元", Per: 1000000,
		Versions: []prices.PriceVersion{{EffectiveFrom: "2026-09-01", PIn: 1.0, PCache: nil, POut: 2.0}}}
	_, err := DeriveSameModelThreshold(nb, nb.Versions[0], goldenObs(), 25)
	if kind := smKind(t, err); kind != KindNoPCache {
		t.Fatalf("kind = %v, want no_pcache", kind)
	}
	for _, want := range []string{"nocache", "无 p_cache", "拒绝推导"} {
		if !contains(err.Error(), want) {
			t.Fatalf("err = %q, 缺 %q", err.Error(), want)
		}
	}
}

// TestSameModelRefusesNoGap 全价读与摆渡成本无价差（T−C ≤ 0）→ 拒算。
func TestSameModelRefusesNoGap(t *testing.T) {
	flat := prices.PriceBook{Key: "flat", Unit: "元", Per: 10000,
		Versions: []prices.PriceVersion{glmVer("2026-09-17", 1.7, 1.7, 24)}} // p_in=p_cache
	_, err := DeriveSameModelThreshold(flat, flat.Versions[0], goldenObs(), 25)
	if kind := smKind(t, err); kind != KindNoGap {
		t.Fatalf("kind = %v, want no_gap", kind)
	}
	if !contains(err.Error(), "价差") {
		t.Fatalf("err = %q, 缺 价差", err.Error())
	}
}

// TestSameModelRefusesInfeasible 最早不亏点晚于可触发值 → 经济不可行拒算。
// 样本（n=40）：30×5、5×23、4×24.5、1×40 → F(25)=39/40，D=0.025，
// q≈0.907258 → ceil(36.29)=37 → 第 37 小样本 24.5 > suggest 24。
func TestSameModelRefusesInfeasible(t *testing.T) {
	b := glmBook()
	obs := goldenObs()
	obs.IdleObsMin = append(append(append(rep(30, 5), rep(5, 23)...), rep(4, 24.5)...), 40)
	_, err := DeriveSameModelThreshold(b, b.Versions[0], obs, 25)
	if kind := smKind(t, err); kind != KindInfeasible {
		t.Fatalf("kind = %v, want infeasible", kind)
	}
	msg := err.Error()
	if !contains(msg, "24.5") || !contains(msg, "24") {
		t.Fatalf("err = %q, 应带两个对拍值(24.5 与 24)", msg)
	}
}

// TestSameModelSumBelowFloor 总结阈值低于钳位下限 → 钳位区间为空，拒算（防御）。
func TestSameModelSumBelowFloor(t *testing.T) {
	b := glmBook()
	_, err := DeriveSameModelThreshold(b, b.Versions[0], goldenObs(), 8)
	if kind := smKind(t, err); kind != KindClampEmpty {
		t.Fatalf("kind = %v, want clamp_empty", kind)
	}
}

// contains 子串断言（strings.Contains 直用，签名少打字）。
func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}

// dsBook 第二本价格表（映射用例）。
func dsBook() prices.PriceBook {
	return prices.PriceBook{Key: "ds", Unit: "美元", Per: 1000000,
		Versions: []prices.PriceVersion{glmVer("2026-09-01", 0.30, 0.006, 0.62)}}
}

// TestSameModelEntryMapping 入口映射口径（票01 评审备注钉死）：显式键优先；
// 否则上游名精确命中；否则仅一本回退；多本无映射拒算给人话。
func TestSameModelEntryMapping(t *testing.T) {
	glm := glmBook()
	two := map[string]prices.PriceBook{"glm": glm, "ds": dsBook()}
	one := map[string]prices.PriceBook{"glm": glm}

	// 单本 + 中文名上游 → 回退该本
	res, err := DeriveSameModelForUpstream(one, "智谱", "", goldenObs(), 25, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.BookKey != "glm" || res.Upstream != "智谱" {
		t.Fatalf("book/upstream = (%q,%q), want (glm,智谱)", res.BookKey, res.Upstream)
	}
	approxAbs(t, "suggest", res.SuggestMin, 24, 1e-9)

	// 多本 + 中文名 + 无显式键 → 拒算，文案言明口径
	_, err = DeriveSameModelForUpstream(two, "智谱", "", goldenObs(), 25, 0)
	if kind := smKind(t, err); kind != KindNoPriceBook {
		t.Fatalf("kind = %v, want no_price_book", kind)
	}
	for _, want := range []string{"智谱", "价格本"} {
		if !contains(err.Error(), want) {
			t.Fatalf("err = %q, 缺 %q", err.Error(), want)
		}
	}

	// 多本 + 显式键命中 → 取显式
	if _, err = DeriveSameModelForUpstream(two, "智谱", "glm", goldenObs(), 25, 0); err != nil {
		t.Fatal(err)
	}

	// 多本 + 显式键未命中 → 拒算（显式映射不给回退）
	_, err = DeriveSameModelForUpstream(two, "智谱", "nope", goldenObs(), 25, 0)
	if kind := smKind(t, err); kind != KindNoPriceBook {
		t.Fatalf("kind = %v, want no_price_book", kind)
	}
	if !contains(err.Error(), "显式") {
		t.Fatalf("err = %q, 缺 显式", err.Error())
	}

	// 上游名恰为书键 → 精确命中（多本之中也直接取）
	if res, err = DeriveSameModelForUpstream(two, "glm", "", goldenObs(), 25, 0); err != nil {
		t.Fatal(err)
	}
	if res.BookKey != "glm" {
		t.Fatalf("book = %q, want glm（精确命中）", res.BookKey)
	}

	// 价格表未装配（空表）→ 拒算
	if _, err = DeriveSameModelForUpstream(nil, "智谱", "", goldenObs(), 25, 0); smKind(t, err) != KindNoPriceBook {
		t.Fatalf("空表: kind = %v, want no_price_book", err)
	}
}

// TestSameModelEntryVersionAt 入口按 nowS 取现价版本；早于一切版本回落末版。
func TestSameModelEntryVersionAt(t *testing.T) {
	pc17, pc99 := 1.7, 9.9
	b := prices.PriceBook{Key: "glm", Unit: "智谱积分", Per: 10000,
		Versions: []prices.PriceVersion{
			{EffectiveFrom: "2026-01-01", PIn: 6.9, PCache: &pc17, POut: 24},
			{EffectiveFrom: "2027-01-01", PIn: 69, PCache: &pc99, POut: 24},
		}}
	books := map[string]prices.PriceBook{"glm": b}
	obs := goldenObs()
	// 同前缀下 2027 版缓存读 9.9 → C = 15×9.9+2.4 = 150.9
	wantC27 := 150.9

	res, err := DeriveSameModelForUpstream(books, "glm", "", obs, 25, 0) // nowS=0 早于一切 → 末版
	if err != nil {
		t.Fatal(err)
	}
	approxAbs(t, "ferry_cost(末版)", res.FerryCost, wantC27, 1e-6)

	// nowS 落在两版之间 → 取 2026-01-01 版（C=27.9）
	mid, err := time.Parse("2006-01-02", "2026-06-01")
	if err != nil {
		t.Fatal(err)
	}
	res, err = DeriveSameModelForUpstream(books, "glm", "", obs, 25, float64(mid.Unix()))
	if err != nil {
		t.Fatal(err)
	}
	approxAbs(t, "ferry_cost(2026版)", res.FerryCost, 27.9, 1e-6)
}

// TestSameModelEntryNoVersion 价格本无版本 → 拒算。
func TestSameModelEntryNoVersion(t *testing.T) {
	empty := prices.PriceBook{Key: "empty", Unit: "元", Per: 10000}
	_, err := DeriveSameModelForUpstream(map[string]prices.PriceBook{"empty": empty},
		"empty", "", goldenObs(), 25, 0)
	if kind := smKind(t, err); kind != KindNoVersion {
		t.Fatalf("kind = %v, want no_version", kind)
	}
}

// smCfg 测试用配置骨架：总结 25min（1500s）、种子 20、recommend。
func smCfg() *config.Config {
	return &config.Config{
		Thresholds: config.ThresholdCfg{SummarizeS: 25 * 60},
		SameModel: config.SameModelCfg{
			Enabled:      true,
			ThresholdMin: config.SameModelSeedMin,
			CeilingMin:   map[string]float64{},
		},
		Tuning: config.TuningCfg{Mode: "recommend"},
	}
}

// TestSameModelEffectiveManual manual 档：生效值=配置值（CeilingFor 单源），
// 计算器不参与——空观测、无价格表也不报错。
func TestSameModelEffectiveManual(t *testing.T) {
	c := smCfg()
	c.Tuning.Mode = "manual"
	c.SameModel.CeilingMin["智谱"] = 22
	res, err := SameModelEffectiveThreshold(c, nil, "智谱", SameModelObs{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != "manual" {
		t.Fatalf("mode = %q, want manual", res.Mode)
	}
	approxAbs(t, "threshold", res.ThresholdMin, 22, 1e-9) // 每上游覆盖优先于全局种子 20

	// 无覆盖 → 全局种子值
	delete(c.SameModel.CeilingMin, "智谱")
	res, err = SameModelEffectiveThreshold(c, nil, "智谱", SameModelObs{})
	if err != nil {
		t.Fatal(err)
	}
	approxAbs(t, "threshold(全局)", res.ThresholdMin, config.SameModelSeedMin, 1e-9)
}

// TestSameModelEffectiveRecommendAuto recommend/auto 档：计算器值受
// CeilingFor 上限钳位（D13）。黄金推导 24：全局上限 20 → 生效 20；
// 每上游覆盖 25 → 生效 24；auto 与 recommend 同口径。
func TestSameModelEffectiveRecommendAuto(t *testing.T) {
	glm := glmBook()
	books := map[string]prices.PriceBook{"glm": glm}

	c := smCfg() // ThresholdMin=20 全局上限
	res, err := SameModelEffectiveThreshold(c, books, "智谱", goldenObs())
	if err != nil {
		t.Fatal(err)
	}
	approxAbs(t, "suggest", res.SuggestMin, 24, 1e-9)
	approxAbs(t, "threshold(上限20)", res.ThresholdMin, 20, 1e-9) // min(24, 20)

	c.SameModel.CeilingMin["智谱"] = 25
	res, err = SameModelEffectiveThreshold(c, books, "智谱", goldenObs())
	if err != nil {
		t.Fatal(err)
	}
	approxAbs(t, "threshold(上限25)", res.ThresholdMin, 24, 1e-9) // min(24, 25)

	c.Tuning.Mode = "auto"
	res, err = SameModelEffectiveThreshold(c, books, "智谱", goldenObs())
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != "auto" {
		t.Fatalf("mode = %q, want auto", res.Mode)
	}
	approxAbs(t, "threshold(auto)", res.ThresholdMin, 24, 1e-9)

	// 总结阈值走 c.Thresholds.SummarizeS（秒→分换算）：1080s=18min → 生效 18
	c.Thresholds.SummarizeS = 1080
	res, err = SameModelEffectiveThreshold(c, books, "智谱", goldenObs())
	if err != nil {
		t.Fatal(err)
	}
	approxAbs(t, "threshold(总结18)", res.ThresholdMin, 18, 1e-9)
}

// TestSameModelEffectiveSeed 无 TTL 观测经出口同样走种子层并受上限钳位。
func TestSameModelEffectiveSeed(t *testing.T) {
	glm := glmBook()
	books := map[string]prices.PriceBook{"glm": glm}
	c := smCfg() // 全局上限 20
	obs := goldenObs()
	obs.TTLObsMin = nil
	res, err := SameModelEffectiveThreshold(c, books, "智谱", obs)
	if err != nil {
		t.Fatal(err)
	}
	if !res.SeedFallback {
		t.Fatal("应走种子回退")
	}
	approxAbs(t, "threshold(种子)", res.ThresholdMin, 20, 1e-9)
}

// TestSameModelEffectiveBadMode 未知档位防御拒算（config.Validate 应已拦截）。
func TestSameModelEffectiveBadMode(t *testing.T) {
	c := smCfg()
	c.Tuning.Mode = "bogus"
	_, err := SameModelEffectiveThreshold(c, nil, "智谱", SameModelObs{})
	if kind := smKind(t, err); kind != KindBadMode {
		t.Fatalf("kind = %v, want bad_mode", kind)
	}
}

// TestSameModelEffectiveClampEmpty 配置上限低于钳位下限 → 区间为空拒算
// （配置矛盾归 config.Validate/doctor same_model_clamp 管，此处防御）。
func TestSameModelEffectiveClampEmpty(t *testing.T) {
	glm := glmBook()
	books := map[string]prices.PriceBook{"glm": glm}
	c := smCfg()
	c.SameModel.CeilingMin["智谱"] = 8
	_, err := SameModelEffectiveThreshold(c, books, "智谱", goldenObs())
	if kind := smKind(t, err); kind != KindClampEmpty {
		t.Fatalf("kind = %v, want clamp_empty", kind)
	}
}
