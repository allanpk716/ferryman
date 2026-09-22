// 票06 测试：同模型阈值网格评分的黄金对拍 / 三线口径 / 样本门槛 / 拒算分桶 / 确定性。
//
// 金样本手算（bookB：per=10000、PIn 6.9、PCache 1.7、POut 24；前缀 150000、
// 叙事输出 1000 → S/per=15、OUT/per=0.1，全部经 policy.DeriveSameModelThreshold
// 闭式复算，无第二份算术）：
//
//	C（摆渡成本）= 15×1.7 + 0.1×24 = 27.9
//	T（参照全价）= 15×6.9           = 103.5
//	场景 TTL = median([25]) = 25 → 触发点 t < 25 判热（字面口径，引擎同款）
//	事件（前缀同规模）：e1 摆渡26min(死) / e2 返回15 / e3 返回30(死) / e4 返回12 / e5 截断76(不入评)
//	Net(t) = Σ_{触发∧死}(T−C) − Σ_{触发∧未死}C：
//	  t∈[10,12]  触发4 → 2×103.5 − 4×27.9 = 95.4
//	  t∈[13,15]  触发3 → 207 − 83.7       = 123.3
//	  t∈[16,24]  触发2 → 207 − 55.8       = 151.2   ← 并列取最小 t=16
//	  t=25       触发0（不严格早于 TTL）→ 0
package backtest

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"ferryman/internal/policy"
	"ferryman/internal/prices"
)

// smEv 合成闲置事件（前缀同规模；定价时点 = 闲置起点）。
func smEv(startTS, dMin float64, outcome string) IdleEvent {
	return IdleEvent{
		StartTS: startTS, EndTS: startTS + dMin*60, IdleMin: dMin,
		Outcome: outcome, PrefixTokens: 150000,
		Agent: "cc", SessionID: fmt.Sprintf("s-%s-%.0f", outcome, startTS),
		Project: "demo", ProjectResolved: true,
		ActualPriced: outcome == IdleFerry, ActualFerryCost: 105.9,
	}
}

// smGoldenEvents 金样本五事件（见文件头手算）。
func smGoldenEvents() []IdleEvent {
	return []IdleEvent{
		smEv(1000, 26, IdleFerry),
		smEv(2000, 15, IdleReturned),
		smEv(3000, 30, IdleReturned),
		smEv(4000, 12, IdleReturned),
		smEv(5000, 76, IdleCensored),
	}
}

// smGoldenDS 金样本数据集（gate=2 摆渡事件，e1+事实价 105.9 已在 smEv 设定）。
func smGoldenDS() *IdleDataset {
	return &IdleDataset{Events: smGoldenEvents(), Counts: IdleLoadCounts{
		FerryEventsInWindow: 2, WindowDays: 30, HorizonTS: 5000 + 76*60,
	}}
}

// smOpts 金样本扫参输入。
func smOpts() SameModelSweepOptions {
	return SameModelSweepOptions{
		Books:    map[string]prices.PriceBook{"glm": bookB()},
		Upstream: "glm", SummarizeMin: 25, TTLObsMin: []float64{25},
		CurrentThresholdMin: 20, HasCurrent: true, MinEvents: 1,
	}
}

// noGapBooks 无价差价表（缓存价 2 > 全价 1 → gap<0 → 拒算 KindNoGap；
// 成本常数仍随结果返回：C = 15×2 + 0.1×24 = 32.4、T = 15×1 = 15）。
func noGapBooks() map[string]prices.PriceBook {
	return map[string]prices.PriceBook{"glm": {Key: "glm", Unit: "u", Per: 10000,
		Versions: []prices.PriceVersion{{EffectiveFrom: "2026-01-01",
			PIn: 1, PCache: fptr(2), POut: 24}}}}
}

// findGridRow 取指定 t 的网格行。
func findGridRow(t *testing.T, res *SameModelSweepResult, tMin float64) *SameModelPoint {
	t.Helper()
	for i := range res.Grid {
		if math.Abs(res.Grid[i].TMin-tMin) < 1e-9 {
			return &res.Grid[i]
		}
	}
	t.Fatalf("网格缺 t=%v 行", tMin)
	return nil
}

// ---- 1. 黄金对拍：成本常数接线公式单源 + 三线数字 ----

func TestSameModelSweepGolden(t *testing.T) {
	res, err := SameModelSweep(smGoldenDS(), smOpts())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Grid) != 16 {
		t.Fatalf("网格 = %d 档, want 16（10..25 整分钟）", len(res.Grid))
	}
	// 关键档对拍（手算见文件头）。
	cases := []struct {
		tMin  float64
		fired int
		dead  int
		warm  int
		net   float64
	}{
		{10, 4, 2, 2, 95.4},
		{13, 3, 2, 1, 123.3},
		{16, 2, 2, 0, 151.2},
		{24, 2, 2, 0, 151.2},
		{25, 0, 0, 0, 0},
	}
	for _, tc := range cases {
		r := findGridRow(t, res, tc.tMin)
		if r.Fired != tc.fired || r.DeadSaved != tc.dead || r.UselessWarm != tc.warm {
			t.Fatalf("t=%v 触发/兑现/浪费 = %d/%d/%d, want %d/%d/%d",
				tc.tMin, r.Fired, r.DeadSaved, r.UselessWarm, tc.fired, tc.dead, tc.warm)
		}
		if !approxF(r.NetSavings, tc.net) {
			t.Fatalf("t=%v 净节省 = %v, want %v", tc.tMin, r.NetSavings, tc.net)
		}
	}
	// 最优：并列 [16..24] 取最小 t=16。
	if res.Best == nil || !approxF(res.Best.TMin, 16) || !approxF(res.Best.NetSavings, 151.2) {
		t.Fatalf("Best = %+v, want t=16/net=151.2", res.Best)
	}
	// 三线之二：什么都不做 = 死事件参照全价合计 207；实际发生 = 事实 handoff 价 105.9。
	if !approxF(res.DoNothingCost, 207) {
		t.Fatalf("DoNothingCost = %v, want 207", res.DoNothingCost)
	}
	if !approxF(res.ActualFerryCost, 105.9) || res.ActualUnpriced != 0 {
		t.Fatalf("ActualFerryCost/Unpriced = %v/%d, want 105.9/0", res.ActualFerryCost, res.ActualUnpriced)
	}
	// 门槛：gate=2 ≥ MinEvents=1 → 充足。
	if !res.Sample.Sufficient || res.Sample.FerryEvents != 2 {
		t.Fatalf("SampleGate = %+v, want sufficient/2", res.Sample)
	}
	// 成本常数接线断言：网格行 FerryCostTotal = 触发数 × 27.9（公式出口值）。
	r := findGridRow(t, res, 16)
	if !approxF(r.FerryCostTotal, 2*27.9) || !approxF(r.GrossSavings, 2*103.5) {
		t.Fatalf("t=16 成本面 = %v/%v, want 55.8/207（DeriveSameModelThreshold 出口）",
			r.FerryCostTotal, r.GrossSavings)
	}
}

// ---- 2. 建议值出口：DeriveSameModelForUpstream 单源 ----

func TestSameModelSweepDerived(t *testing.T) {
	res, err := SameModelSweep(smGoldenDS(), smOpts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Derived == nil {
		t.Fatal("Derived 缺失")
	}
	// 手算：median TTL 25 → cap 20；idle [12,15,26,30] F(25)=0.5、D=0.5、
	// q=0.5−0.5×(75.6/27.9)<0 → 钳 0 → econ=12 ≤ 20 可行。
	d := res.Derived
	if !approxF(d.SuggestMin, 20) || !approxF(d.MedianTTLMin, 25) || !approxF(d.EconFloorMin, 12) {
		t.Fatalf("建议值链 = %+v, want suggest=20/median=25/econ=12", d)
	}
	if !approxF(d.FerryCost, 27.9) || !approxF(d.RefCost, 103.5) {
		t.Fatalf("聚合成本 = %v/%v, want 27.9/103.5（公式出口）", d.FerryCost, d.RefCost)
	}
	if res.DerivedErrKind != "" {
		t.Fatalf("DerivedErrKind = %q, want 空", res.DerivedErrKind)
	}
}

// ---- 3. 样本不足路径：不出建议值、Best 缺席 ----

func TestSameModelSweepInsufficientSample(t *testing.T) {
	opts := smOpts()
	opts.MinEvents = 30 // gate=2 < 30 → 不足
	res, err := SameModelSweep(smGoldenDS(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sample.Sufficient {
		t.Fatal("gate=2 < MinEvents=30 应判不足")
	}
	if res.Best != nil {
		t.Fatalf("样本不足时 Best 应缺席, got %+v", res.Best)
	}
	// 网格照算（数据面保留），只斩断建议出口。
	if len(res.Grid) != 16 {
		t.Fatalf("样本不足仍应产出网格 16 档, got %d", len(res.Grid))
	}
	// 报告标注：样本不足 + 不出建议值。
	md := RenderSameModelMarkdown(res, smGoldenDS())
	if !strings.Contains(md, "样本不足") {
		t.Fatal("报告缺「样本不足」标注")
	}
	if strings.Contains(md, "建议值：") {
		t.Fatal("样本不足时报告不得给出建议值")
	}
}

// ---- 4. 拒算分桶：全事件无 p_cache → 空网格不炸；no_gap 成本仍可用 ----

func TestSameModelSweepUncomputable(t *testing.T) {
	// 全事件 no_pcache：单版本缺 p_cache。
	np := prices.PriceBook{Key: "glm", Unit: "u", Per: 10000,
		Versions: []prices.PriceVersion{{EffectiveFrom: "2026-01-01", PIn: 6.9, POut: 24}}}
	opts := smOpts()
	opts.Books = map[string]prices.PriceBook{"glm": np}
	res, err := SameModelSweep(smGoldenDS(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Uncomputable["no_pcache"] != 4 {
		t.Fatalf("Uncomputable[no_pcache] = %d, want 4（截断事件本就不入评）", res.Uncomputable["no_pcache"])
	}
	if len(res.Grid) != 0 || res.Best != nil {
		t.Fatalf("全不可算应空网格/无 Best, got %d 档 / %+v", len(res.Grid), res.Best)
	}

	// no_gap：缓存价高于全价 → 成本仍可算、净节省为负、警告在案。
	opts2 := smOpts()
	opts2.Books = noGapBooks()
	res2, err := SameModelSweep(smGoldenDS(), opts2)
	if err != nil {
		t.Fatal(err)
	}
	if res2.DerivedErrKind != "no_gap" {
		t.Fatalf("DerivedErrKind = %q, want no_gap", res2.DerivedErrKind)
	}
	r := findGridRow(t, res2, 10)
	// C = 15×2 + 0.1×24 = 32.4；T = 15×1 = 15 → net = 30 − 129.6 = −99.6。
	if !approxF(r.NetSavings, -99.6) {
		t.Fatalf("no_gap 下 t=10 净节省 = %v, want −99.6", r.NetSavings)
	}
	found := false
	for _, w := range res2.Warnings {
		if strings.Contains(w, "no_gap") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Warnings 缺 no_gap: %v", res2.Warnings)
	}
}

// ---- 5. 版本化：旧行时点版本缺 p_cache → 该事件不可算，新行时点照算 ----

func TestSameModelSweepVersioned(t *testing.T) {
	book := prices.PriceBook{Key: "glm", Unit: "u", Per: 10000, Versions: []prices.PriceVersion{
		{EffectiveFrom: "2026-01-01", PIn: 6.9, POut: 24},                    // 无 p_cache
		{EffectiveFrom: "2026-06-01", PIn: 6.9, PCache: fptr(1.7), POut: 24}, // 有
	}}
	ev := []IdleEvent{
		smEv(tsOfDay(2026, 2, 1), 26, IdleFerry),    // 落 v1 → 不可算
		smEv(tsOfDay(2026, 7, 1), 30, IdleReturned), // 落 v2 → 可算
	}
	ds := &IdleDataset{Events: ev, Counts: IdleLoadCounts{
		FerryEventsInWindow: 1, WindowDays: 30, HorizonTS: tsOfDay(2026, 8, 1)}}
	opts := SameModelSweepOptions{
		Books:    map[string]prices.PriceBook{"glm": book},
		Upstream: "glm", SummarizeMin: 25, TTLObsMin: []float64{25}, MinEvents: 1,
	}
	res, err := SameModelSweep(ds, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Uncomputable["no_pcache"] != 1 {
		t.Fatalf("Uncomputable[no_pcache] = %d, want 1", res.Uncomputable["no_pcache"])
	}
	if len(res.Grid) == 0 {
		t.Fatal("存留可算事件应产出网格")
	}
	// 存留事件 e2：d=30 死亡侧，t<25 触发 → 每档 net = 103.5 − 27.9 = 75.6。
	r := findGridRow(t, res, 10)
	if !approxF(r.NetSavings, 75.6) || r.Fired != 1 {
		t.Fatalf("t=10 = %+v, want 触发1/net75.6", r)
	}
}

// ---- 6. 确定性：同输入两次扫参渲染逐字节一致 ----

func TestSameModelSweepDeterministic(t *testing.T) {
	a, err := SameModelSweep(smGoldenDS(), smOpts())
	if err != nil {
		t.Fatal(err)
	}
	b, err := SameModelSweep(smGoldenDS(), smOpts())
	if err != nil {
		t.Fatal(err)
	}
	ma, mb := RenderSameModelMarkdown(a, smGoldenDS()), RenderSameModelMarkdown(b, smGoldenDS())
	if ma != mb {
		t.Fatal("两次渲染不一致")
	}
}

// ---- 7. 入参防御：总结阈值非法 / 价格本定位失败 / TTL 脏值 ----

func TestSameModelSweepBadInput(t *testing.T) {
	opts := smOpts()
	opts.SummarizeMin = 0
	if _, err := SameModelSweep(smGoldenDS(), opts); err == nil {
		t.Fatal("总结阈值 0 应报错")
	}
	opts = smOpts()
	opts.SummarizeMin = 5 // < 钳位下限 10 → 网格域空
	if _, err := SameModelSweep(smGoldenDS(), opts); err == nil {
		t.Fatal("总结阈值低于钳位下限应报错")
	}
	opts = smOpts()
	opts.Upstream = "absent"
	opts.Books = map[string]prices.PriceBook{"a": bookB(), "b": bookB()} // 多本无映射
	if _, err := SameModelSweep(smGoldenDS(), opts); err == nil {
		t.Fatal("价格本无法定位应报错")
	}
	opts = smOpts()
	opts.TTLObsMin = []float64{0}
	if _, err := SameModelSweep(smGoldenDS(), opts); err == nil {
		t.Fatal("TTL 场景值非正应报错")
	}
	// 空数据集：空网格、无推荐、不炸。
	empty := &IdleDataset{Counts: IdleLoadCounts{WindowDays: 30}}
	opts = smOpts()
	if _, err := SameModelSweep(empty, opts); err != nil {
		t.Fatalf("空数据集不应报错: %v", err)
	}
}

// 编译锚：policy.DeriveSameModelForUpstream 签名在场（建议值出口红线）。
var _ = policy.DeriveSameModelForUpstream
