// 票02 引擎测试：网格构造 / 逐窗模拟 / 评分 / tie-break / 确定性。
//
// 金样本手算过程见 TestGoldenSampleClosedForm 注释（已知 dur/prefix 的合成窗，
// 净节省与成本对到手闭式值）。全部夹具直接构造契约类型（内存结构，无账本文件）。
package backtest

import (
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"ferryman/internal/policy"
	"ferryman/internal/prices"
)

// ---- 夹具 ----

func fptr(f float64) *float64 { return &f }

// tsOfDay UTC 零点 epoch 秒（价格版本生效日判定用）。
func tsOfDay(y int, mo time.Month, d int) float64 {
	return float64(time.Date(y, mo, d, 0, 0, 0, 0, time.UTC).Unix())
}

// bookA 三版本价格表：窗口行时刻落在 v2 生效期内，网格参考价取最新 v3——
// 用于金样本同时锚定「参数取参考上下文、金额逐窗版本化」两条口径。
//
//	per=10000，前缀 100000 → S/per = 10：
//	  v1 (2026-01-01): PIn 6.9, PCache 1.7, POut 24 → perBeat 17.72, expire 69
//	  v2 (2026-06-01): PIn 12,  PCache 3.4, POut 24 → perBeat 34.72, expire 120
//	  v3 (2027-01-01): PIn 24,  PCache 6.8, POut 48 → perBeat 69.44, expire 240
func bookA() prices.PriceBook {
	return prices.PriceBook{Key: "glm", Unit: "积分", Per: 10000, Versions: []prices.PriceVersion{
		{EffectiveFrom: "2026-01-01", PIn: 6.9, PCache: fptr(1.7), POut: 24},
		{EffectiveFrom: "2026-06-01", PIn: 12, PCache: fptr(3.4), POut: 24},
		{EffectiveFrom: "2027-01-01", PIn: 24, PCache: fptr(6.8), POut: 48},
	}}
}

// bookB 单版本（v1 价）：金样本简单版与 tie-break / 无效保温夹具用。
func bookB() prices.PriceBook {
	return prices.PriceBook{Key: "glm", Unit: "积分", Per: 10000, Versions: []prices.PriceVersion{
		{EffectiveFrom: "2026-01-01", PIn: 6.9, PCache: fptr(1.7), POut: 24},
	}}
}

// bookC 高 cap/τ 比价表（PCache/POut 极小 → cap ≈ 238τ）：熔断先于上限触发，
// 供「miss≥2 停跳」的全链断言用。
func bookC() prices.PriceBook {
	return prices.PriceBook{Key: "glm", Unit: "积分", Per: 10000, Versions: []prices.PriceVersion{
		{EffectiveFrom: "2026-01-01", PIn: 24, PCache: fptr(0.1), POut: 0.1},
	}}
}

// mkWin 合成窗。
func mkWin(rowTS, durS float64, prefix int) Window {
	return Window{
		RowTS: rowTS, OpenedTS: rowTS, ClosedTS: rowTS + durS,
		DurS: durS, PrefixTokens: prefix,
		CloseReason: "main_resumed", Agent: "cc", SessionID: "s1",
		Project: "demo", ProjectResolved: true,
	}
}

// optsOf 组装 SweepOptions。
func optsOf(book prices.PriceBook, ttls float64) SweepOptions {
	return SweepOptions{Books: map[string]prices.PriceBook{book.Key: book}, TTLS: ttls}
}

// findScenario 取（场景档，乘数档）对应的主网格结果。
func findScenario(t *testing.T, res *SweepResult, name string, mult float64) *ScenarioResult {
	t.Helper()
	for i := range res.Scenarios {
		r := &res.Scenarios[i]
		if r.Scenario == name && math.Abs(r.Point.TTLMult-mult) < 1e-12 {
			return r
		}
	}
	t.Fatalf("主网格缺场景 %s × 乘数 %v", name, mult)
	return nil
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// ---- 1. 主网格：12 乘数档全集，每档参数组来自 policy.Compute（接线断言） ----

func TestTTLMultiplierTable(t *testing.T) {
	if len(TTLMultipliers) != 12 {
		t.Fatalf("主网格乘数表应 12 档，得 %d", len(TTLMultipliers))
	}
	want := []float64{0.5, 0.6, 0.7, 0.8, 0.9, 1.0, 1.1, 1.2, 1.4, 1.6, 1.8, 2.0}
	for i, m := range want {
		if TTLMultipliers[i] != m {
			t.Fatalf("乘数表[%d] = %v, want %v", i, TTLMultipliers[i], m)
		}
	}

	book := bookA()
	pv := &book.Versions[2] // 参考价 = 最新生效版本
	pts, err := BuildMainGrid(&book, pv, 900, 100000)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 12 {
		t.Fatalf("主网格应 12 档，得 %d", len(pts))
	}
	for i, m := range want {
		pol, err := policy.Compute(book, *pv, 900*m, 100000,
			policy.DefaultBeatOutTokens, policy.DefaultSafety,
			policy.DefaultGraceS, policy.DefaultMinPrefixTokens)
		if err != nil {
			t.Fatal(err)
		}
		wantPt := GridPoint{TTLMult: m, TTLS: 900 * m,
			TauS: pol.TauS, FirstBeatS: pol.TauS, CapS: pol.WorthwhileCapS}
		if pts[i] != wantPt {
			t.Fatalf("档 %v 参数组非 Compute 接线值：got %+v want %+v", m, pts[i], wantPt)
		}
	}
	// 档位 ttl_s′ 升序（乘数表序 = 档位序）。
	for i := 1; i < len(pts); i++ {
		if pts[i].TTLS <= pts[i-1].TTLS {
			t.Fatalf("档位 ttl_s′ 非升序：%v -> %v", pts[i-1].TTLS, pts[i].TTLS)
		}
	}
}

// ---- 2. 金样本：净节省与成本等于手算闭式值 ----

// 手算过程（书 A，config ttl=900，单窗 dur=2000s、prefix=100000、RowTS=2026-07-01）：
//
//	参考价上下文（网格闭式曲线）= 最新版本 v3 × 参考前缀 100000（单窗均值）：
//	  pin = 10×24 = 240；pcache = 10×6.8 = 68；perBeat = 68 + 300/10000×48 = 69.44
//	1.0 档（ttl′=900）：τ = 0.8×900 = 720；首跳 = 720；cap = 720×(240−68)/69.44 = 123840/69.44 ≈ 1783.4102
//	逐窗金额（RowTS 2026-07-01 → v2 生效，版本化反事实）：
//	  perBeat_w = 10×3.4 + 300/10000×24 = 34 + 0.72 = 34.72；expire_w = 10×12 = 120
//	模拟（config 场景 ttl′=900）：horizon = min(2000, 1783.41) → 跳点 720（<900 hit）、
//	  1440（≥900 miss，连击 1 不熔断）；2160 越界停。
//	  保温兑现：dur−末跳 = 2000−1440 = 560 < 900 → 暖 → 避免重付 = expire_w = 120
//	  心跳花费 = 2×34.72 = 69.44；净节省 = 120 − 69.44 = 50.56
//	Best：逐档重算后 1.6/1.8/2.0 三档并列最优 85.28（单跳即覆盖：如 1.6 档 τ=1152、
//	  cap≈2853→horizon=2000，只跳 1152 一次（miss 连击 1），2000−1152=848<900 仍暖，
//	  净省 120−34.72=85.28），tie-break 取 τ 最小 = 1.6 档（τ=1152）。
func TestGoldenSampleClosedForm(t *testing.T) {
	book := bookA()
	ds := &Dataset{
		Windows: []Window{mkWin(tsOfDay(2026, time.July, 1), 2000, 100000)},
		Counts:  LoadCounts{ReplayWindows: 1},
	}
	res, err := RunSweep(ds, optsOf(book, 900))
	if err != nil {
		t.Fatal(err)
	}
	if res.Dataset.ReplayWindows != 1 {
		t.Fatalf("计数未透传：ReplayWindows = %d", res.Dataset.ReplayWindows)
	}

	r := findScenario(t, res, ScenarioConfig, 1.0)
	if r.Windows != 1 || r.Beats != 2 || r.BreakerTrips != 0 || r.UselessWarm != 0 {
		t.Fatalf("金样本跳数面不符：windows=%d beats=%d trips=%d useless=%d",
			r.Windows, r.Beats, r.BreakerTrips, r.UselessWarm)
	}
	if !approx(r.Point.TTLS, 900) || !approx(r.Point.TauS, 720) || !approx(r.Point.FirstBeatS, 720) {
		t.Fatalf("1.0 档参数组不符：%+v", r.Point)
	}
	if !approx(r.Point.CapS, 123840/69.44) {
		t.Fatalf("1.0 档 cap 应为参考价闭式 123840/69.44，得 %v", r.Point.CapS)
	}
	if !approx(r.GrossSavings, 120) {
		t.Fatalf("避免重付应 = expire_w = 120，得 %v", r.GrossSavings)
	}
	if !approx(r.BeatCost, 69.44) {
		t.Fatalf("心跳花费应 = 2×34.72 = 69.44，得 %v", r.BeatCost)
	}
	if !approx(r.NetSavings, 50.56) {
		t.Fatalf("净节省应 = 120 − 69.44 = 50.56，得 %v", r.NetSavings)
	}
	if res.Best == nil || res.Best.TTLMult != 1.6 {
		t.Fatalf("三档并列 85.28 应 tie-break 取 τ 最小 = 1.6 档，得 %+v", res.Best)
	}
	if !approx(res.BestNet, 85.28) {
		t.Fatalf("BestNet 应 85.28，得 %v", res.BestNet)
	}
	// 对照性反例（同数据集）：0.6 档覆盖缺口（末跳 864，2000−864 ≥ 900）→ 零避免，
	// 净节省 = −2×34.72。网格对参数有区分度，非退化。
	bad := findScenario(t, res, ScenarioConfig, 0.6)
	if !approx(bad.NetSavings, -69.44) {
		t.Fatalf("0.6 档覆盖缺口应净省 −69.44，得 %v", bad.NetSavings)
	}
}

// ---- 3. 对照列：每个 TTL 场景档重算（τ=safety×TTL′ 同步变化） ----

func TestBaselineRecomputedPerScenario(t *testing.T) {
	book := bookA()
	ds := &Dataset{Windows: []Window{mkWin(tsOfDay(2026, time.July, 1), 2000, 100000)}}
	res, err := RunSweep(ds, optsOf(book, 900))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Baselines) != 3 {
		t.Fatalf("对照列应 3 场景档，得 %d", len(res.Baselines))
	}
	pv := &book.Versions[2]
	ttls := []float64{900, 900 * 2 / 3, 900 * 0.5}
	names := []string{ScenarioConfig, ScenarioMinusThird, ScenarioMinusHalf}
	for i, b := range res.Baselines {
		if b.Scenario != names[i] {
			t.Fatalf("对照列[%d] 场景 = %s, want %s", i, b.Scenario, names[i])
		}
		if !approx(b.Point.TTLS, ttls[i]) {
			t.Fatalf("对照列[%d] TTLS = %v, want %v", i, b.Point.TTLS, ttls[i])
		}
		if b.Point.TTLMult != 1.0 {
			t.Fatalf("对照列[%d] TTLMult = %v, want 1.0", i, b.Point.TTLMult)
		}
		pol, err := policy.Compute(book, *pv, ttls[i], 100000,
			policy.DefaultBeatOutTokens, policy.DefaultSafety,
			policy.DefaultGraceS, policy.DefaultMinPrefixTokens)
		if err != nil {
			t.Fatal(err)
		}
		// τ=safety×TTL′ 同步变化：与测试自调 Compute 的输出逐位一致（接线断言）。
		if b.Point.TauS != pol.TauS || b.Point.CapS != pol.WorthwhileCapS {
			t.Fatalf("对照列[%d] 非 Compute(TTL′) 重算值：got (%v,%v) want (%v,%v)",
				i, b.Point.TauS, b.Point.CapS, pol.TauS, pol.WorthwhileCapS)
		}
	}
	// 对照基线（config）与主网格 1.0 档同点（同一 Compute 上下文）。
	main := findScenario(t, res, ScenarioConfig, 1.0)
	if res.Baselines[0].Point != main.Point {
		t.Fatalf("config 对照基线应与主网格 1.0 档同点：%+v vs %+v",
			res.Baselines[0].Point, main.Point)
	}
}

// ---- 4. 诊断网格：同表全集枚举、锚点齐全、非可部署标记 ----

func TestDiagnosticGridFullEnumeration(t *testing.T) {
	book := bookA()
	ds := &Dataset{Windows: []Window{mkWin(tsOfDay(2026, time.July, 1), 2000, 100000)}}
	res, err := RunSweep(ds, optsOf(book, 900))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Diagnostic) != 3*144 {
		t.Fatalf("诊断网格应 3 场景 × 144 点 = 432，得 %d", len(res.Diagnostic))
	}
	per := map[string]int{}
	for _, d := range res.Diagnostic {
		if d.Point.TTLMult != 0 {
			t.Fatalf("诊断点 TTLMult 应记 0（非可部署标记），得 %v", d.Point.TTLMult)
		}
		per[d.Scenario]++
	}
	for _, n := range []string{ScenarioConfig, ScenarioMinusThird, ScenarioMinusHalf} {
		if per[n] != 144 {
			t.Fatalf("场景 %s 诊断点应 144，得 %d", n, per[n])
		}
	}

	// config 场景 τ 锚 = 闭式 τ(720) × 主网格同表乘数；cap 锚 = 闭式 cap × {0.5,1,2,∞}。
	pv := &book.Versions[2]
	pol, err := policy.Compute(book, *pv, 900, 100000,
		policy.DefaultBeatOutTokens, policy.DefaultSafety,
		policy.DefaultGraceS, policy.DefaultMinPrefixTokens)
	if err != nil {
		t.Fatal(err)
	}
	cap0 := pol.WorthwhileCapS
	var tauRow []GridPoint
	for _, d := range res.Diagnostic {
		if d.Scenario == ScenarioConfig && approx(d.Point.TauS, 720) {
			tauRow = append(tauRow, d.Point)
		}
	}
	if len(tauRow) != 12 { // 3 首跳锚 × 4 cap 锚
		t.Fatalf("τ=720 锚行应 12 点，得 %d", len(tauRow))
	}
	fbCount := map[float64]int{}
	capCount := map[float64]int{}
	for _, p := range tauRow {
		fbCount[p.FirstBeatS]++
		capCount[p.CapS]++
	}
	// 首跳锚 {开窗即跳, τ/2, τ} 各 ×4（4 个 cap 锚）。
	for _, fb := range []float64{0, 360, 720} {
		if fbCount[fb] != 4 {
			t.Fatalf("首跳锚 %v 应出现 4 次，得 %d（行 %+v）", fb, fbCount[fb], tauRow)
		}
	}
	// cap 锚 {×0.5, ×1.0, ×2.0, ∞} 各 ×3（3 个首跳锚）。
	for _, c := range []float64{cap0 * 0.5, cap0, cap0 * 2} {
		if capCount[c] != 3 {
			t.Fatalf("cap 锚 %v 应出现 3 次，得 %d", c, capCount[c])
		}
	}
	if capCount[math.Inf(1)] != 3 {
		t.Fatalf("cap 锚 ∞ 应出现 3 次，得 %d", capCount[math.Inf(1)])
	}
	// τ 锚全集 = 闭式 τ × 12 乘数（config 场景：720×表）。
	tauAnchors := map[float64]bool{}
	for _, d := range res.Diagnostic {
		if d.Scenario == ScenarioConfig {
			tauAnchors[d.Point.TauS] = true
		}
	}
	if len(tauAnchors) != 12 {
		t.Fatalf("config 场景 τ 锚应 12 个（720×乘数表），得 %d", len(tauAnchors))
	}
}

// ---- 5. 熔断（全链）：TTL 场景档短于间隔的夹具窗，miss≥2 停跳 ----

// 手算：书 C（perBeat = 1 + 300/10000×0.1 = 1.003；cap/τ = 239/1.003 ≈ 238）。
// 场景 −1/2（ttl′=450）× 0.5 档（τ=360，cap≈85802，horizon=min(200000, cap)）：
// 跳点 360（<450 hit）→ 720（miss，连击 1）→ 1080（miss，连击 2 → 熔断停跳）。
// 若无熔断将持续跳到 cap（≈238 跳）；断言 3 跳即停 = 停跳语义生效。
func TestBreakerStopsAfterTwoMisses(t *testing.T) {
	book := bookC()
	ds := &Dataset{Windows: []Window{mkWin(tsOfDay(2026, time.July, 1), 200000, 100000)}}
	res, err := RunSweep(ds, optsOf(book, 900))
	if err != nil {
		t.Fatal(err)
	}
	r := findScenario(t, res, ScenarioMinusHalf, 0.5)
	if r.Beats != 3 {
		t.Fatalf("熔断应停在第 3 跳（miss 连击 2），得 %d 跳", r.Beats)
	}
	if r.BreakerTrips != 1 {
		t.Fatalf("熔断触发窗数应 1，得 %d", r.BreakerTrips)
	}
}

// ---- 6. 熔断（单元）：hit 清连击 / 连续 2 miss 停跳 / cap 到点停跳不触发 ----

func TestSimulateBeatsBreakerSemantics(t *testing.T) {
	cases := []struct {
		name          string
		dur, ttl      float64
		tau, fb, capS float64
		wantBeats     int
		wantTrips     int
		wantWarm      bool
	}{
		{"连续2miss熔断", 10000, 100, 150, 150, 10000, 2, 1, false},
		{"hit清连击后再计", 10000, 250, 150, 150, 10000, 3, 1, false},
		{"cap到点停跳不触发", 10000, 100, 150, 150, 200, 1, 0, false},
		{"开窗即跳仍受熔断", 10000, 100, 150, 0, 10000, 3, 1, false},
		{"短窗跳了也白跳", 500, 900, 360, 360, 1056, 1, 0, true},
	}
	for _, c := range cases {
		pt := GridPoint{TauS: c.tau, FirstBeatS: c.fb, CapS: c.capS}
		beats, trips, warm := simulateBeats(c.dur, c.ttl, pt)
		if beats != c.wantBeats || trips != c.wantTrips || warm != c.wantWarm {
			t.Fatalf("%s：got (beats=%d trips=%d warm=%v) want (%d %d %v)",
				c.name, beats, trips, warm, c.wantBeats, c.wantTrips, c.wantWarm)
		}
	}
}

// ---- 7. 无效保温单列：本无过期可免，跳了白跳 ----

// 手算（书 B，config ttl=900，窗 dur=500、prefix=100000）：0.5 档 τ=360 ≤ 500
// → 1 跳（hit）；dur ≤ ttl′ → 避免重付 0；心跳花费 = 1×17.72 = 17.72；
// 净节省 = −17.72；无效保温窗数 = 1。
func TestUselessWarmCountedSeparately(t *testing.T) {
	book := bookB()
	ds := &Dataset{Windows: []Window{mkWin(tsOfDay(2026, time.July, 1), 500, 100000)}}
	res, err := RunSweep(ds, optsOf(book, 900))
	if err != nil {
		t.Fatal(err)
	}
	r := findScenario(t, res, ScenarioConfig, 0.5)
	if r.Beats != 1 || r.UselessWarm != 1 {
		t.Fatalf("无效保温窗应 1（beats=%d useless=%d）", r.Beats, r.UselessWarm)
	}
	if !approx(r.GrossSavings, 0) {
		t.Fatalf("无效保温避免重付应 0，得 %v", r.GrossSavings)
	}
	if !approx(r.NetSavings, -17.72) {
		t.Fatalf("净节省应 −17.72，得 %v", r.NetSavings)
	}
}

// ---- 8. tie-break：净节省并列取 (τ, 首跳, cap) 字典序最小 ----

// 夹具：dur=100 ≤ 全部场景 TTL → 零避免零跳数，主网格 12 档净节省全为 0 并列；
// Best 应取 τ 最小的 0.5 档（τ=0.8×450=360）。
func TestTieBreakLexicographicMin(t *testing.T) {
	book := bookB()
	ds := &Dataset{Windows: []Window{mkWin(tsOfDay(2026, time.July, 1), 100, 100000)}}
	res, err := RunSweep(ds, optsOf(book, 900))
	if err != nil {
		t.Fatal(err)
	}
	if res.Best == nil {
		t.Fatal("并列网格应产出 Best")
	}
	if res.Best.TTLMult != 0.5 {
		t.Fatalf("全并列应取字典序最小 = 0.5 档，得 %+v", res.Best)
	}
	if !approx(res.Best.TauS, 360) || !approx(res.Best.TTLS, 450) {
		t.Fatalf("0.5 档参数应为 ttl′=450/τ=360，得 %+v", res.Best)
	}
	if !approx(res.BestNet, 0) {
		t.Fatalf("全并列 BestNet 应 0，得 %v", res.BestNet)
	}
}

// ---- 9. 确定性：同输入两次运行逐字节一致 ----

func TestDeterministicByteIdentical(t *testing.T) {
	build := func() *Dataset {
		return &Dataset{
			Windows: []Window{
				mkWin(tsOfDay(2026, time.July, 1), 2000, 100000),
				mkWin(tsOfDay(2026, time.July, 2), 3000, 60000),
			},
			Counts: LoadCounts{ReplayWindows: 2,
				CloseReasonBefore: map[string]int{"main_resumed": 2},
				CloseReasonAfter:  map[string]int{"main_resumed": 2}},
		}
	}
	run := func() string {
		res, err := RunSweep(build(), optsOf(bookA(), 900))
		if err != nil {
			t.Fatal(err)
		}
		// %+v 全量序列化：诊断网格 cap 锚含 +Inf，json.Marshal 不支持
		//（Inf 的 JSON 哨兵形是票04 --json 出口的职责）；%+v 对全部字段
		// 确定性打印（map 键序 Go fmt 亦排序）。Best 是指针字段（%+v 会打
		// 印地址），显式解引用序列化。
		best := "<nil>"
		if res.Best != nil {
			best = fmt.Sprintf("%+v", *res.Best)
		}
		return fmt.Sprintf("%+v|%+v|%+v|%+v|%s|%v",
			res.Dataset, res.Scenarios, res.Baselines, res.Diagnostic, best, res.BestNet)
	}
	a, b := run(), run()
	if a != b {
		for i := 0; i < len(a) && i < len(b); i++ {
			if a[i] != b[i] {
				lo, hi := max(0, i-80), min(len(a), i+80)
				t.Fatalf("输出不一致 @%d:\nA...%s...\nB...%s...", i, a[lo:hi], b[lo:hi])
			}
		}
		t.Fatalf("输出长度不同：A=%d B=%d", len(a), len(b))
	}
}

// ---- 10. 护栏：空集 / ttl 未配置 / 无可用品价格表 ----

func TestRunSweepGuardrails(t *testing.T) {
	// 空数据集：空网格空推荐，不报错。
	res, err := RunSweep(&Dataset{}, optsOf(bookA(), 900))
	if err != nil {
		t.Fatalf("空数据集不应报错：%v", err)
	}
	if len(res.Scenarios) != 0 || len(res.Baselines) != 0 || len(res.Diagnostic) != 0 || res.Best != nil {
		t.Fatalf("空数据集应产空结果：scenarios=%d best=%+v", len(res.Scenarios), res.Best)
	}
	// ttl 未配置：policy.ErrTTLUnset 原样透传。
	_, err = RunSweep(&Dataset{Windows: []Window{mkWin(0, 1, 1)}}, optsOf(bookA(), 0))
	if !errors.Is(err, policy.ErrTTLUnset) {
		t.Fatalf("ttl=0 应透传 ErrTTLUnset，得 %v", err)
	}
	// 无可用品价格表：空表报错。
	_, err = RunSweep(&Dataset{Windows: []Window{mkWin(0, 1, 1)}},
		SweepOptions{Books: map[string]prices.PriceBook{}, TTLS: 900})
	if err == nil {
		t.Fatal("空价格表应报错")
	}
}
