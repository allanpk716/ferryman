package backtest

// 票03 测试：报告两投影（markdown + --json）。
// 夹具为手算合成 SweepResult/Dataset（不依赖票02 引擎产出），断言数字全部
// 来自夹具设计值。数据集里的 Session ID（sess-*）作隐私哨兵：报告两投影
// 结构上都不得携带会话标识（报告只含元数据：时间/token 数/金额/项目路径/计数）。
// 确定性锚点：同输入两次渲染逐字节一致（无 wall-clock、无 map 直出）。

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// reportHeaders 九节标题（顺序即规格）。
var reportHeaders = []string{
	"## 1. 头部：装载时点戳与数据集双口径",
	"## 2. 差距表：当前闭式配置 vs 主网格最优",
	"## 3. TTL 三档场景轴与结论翻转点",
	"## 4. 诊断网格（诊断用，非可部署）",
	"## 5. 无效保温（单列）",
	"## 6. 双计检查",
	"## 7. 留出集：前半选参 / 后半验证",
	"## 8. 盲区声明",
	"## 9. 证据等级",
}

// sweepFixture 手算夹具：装载口径 4 窗（过滤后 3）、三档场景轴（结论在
// −1/2 档翻转：+80 → −20）、主网格最优点 ×0.9、双行诊断网格（第二行 cap=∞）。
func sweepFixture() (*SweepResult, *Dataset) {
	counts := LoadCounts{
		LoadedAt: baseT, LoadedAtISO: "2026-09-21T09:15:00+0800",
		TotalWindows: 4, AfterFilterWindows: 3, ReplayWindows: 3,
		UnresolvedWindows: 1, MultiMatchSessions: 2,
		CloseReasonBefore: map[string]int{"subagents_done": 2, "expired": 1, "": 1},
		CloseReasonAfter:  map[string]int{"subagents_done": 3},
	}
	cfgPoint := GridPoint{TTLMult: 1.0, TTLS: 600, TauS: 420, FirstBeatS: 480, CapS: 1800}
	bestPoint := GridPoint{TTLMult: 0.9, TTLS: 540, TauS: 378, FirstBeatS: 432, CapS: 1620}
	scen := func(name string, ttl float64, p GridPoint, beats int, cost, gross, net float64, trips, warm int) ScenarioResult {
		return ScenarioResult{Scenario: name, TTLS: ttl, Point: p, Windows: 3,
			Beats: beats, BeatCost: cost, GrossSavings: gross, NetSavings: net,
			BreakerTrips: trips, UselessWarm: warm}
	}
	scenA := scen(ScenarioNameConfig, 540, bestPoint, 30, 12.5, 150, 137.5, 1, 0)
	scenB := scen(ScenarioNameConfig, 600, cfgPoint, 25, 10, 120, 110, 0, 1)
	scenC := scen(ScenarioNameThird, 360,
		GridPoint{TTLMult: 0.9, TTLS: 360, TauS: 252, FirstBeatS: 288, CapS: 1080},
		28, 9, 89, 80, 2, 2)
	scenD := scen(ScenarioNameHalf, 270,
		GridPoint{TTLMult: 0.9, TTLS: 270, TauS: 189, FirstBeatS: 216, CapS: 810},
		40, 15, -5, -20, 3, 3)
	baseCfg := scen(ScenarioNameConfig, 600, cfgPoint, 25, 10, 120, 110, 0, 1)
	baseThird := scen(ScenarioNameThird, 400,
		GridPoint{TTLMult: 1.0, TTLS: 400, TauS: 280, FirstBeatS: 320, CapS: 1200},
		20, 8, 68, 60, 0, 0)
	baseHalf := scen(ScenarioNameHalf, 300,
		GridPoint{TTLMult: 1.0, TTLS: 300, TauS: 210, FirstBeatS: 240, CapS: 900},
		22, 9, 39, 30, 0, 0)

	res := &SweepResult{
		Dataset:   counts,
		Scenarios: []ScenarioResult{scenA, scenB, scenC, scenD},
		Baselines: []ScenarioResult{baseCfg, baseThird, baseHalf},
		Best:      &bestPoint, BestNet: 137.5,
		Diagnostic: []ScenarioResult{
			{Point: GridPoint{TauS: 200, FirstBeatS: 0, CapS: 900},
				Windows: 3, Beats: 40, BeatCost: 11, NetSavings: 95},
			{Point: GridPoint{TauS: 400, FirstBeatS: 100, CapS: math.Inf(1)},
				Windows: 3, Beats: 20, BeatCost: 7, NetSavings: 88,
				BreakerTrips: 1, UselessWarm: 1},
		},
	}
	ds := &Dataset{
		Windows: []Window{
			{SessionID: "sess-A", OpenedTS: baseT},
			{SessionID: "sess-B", OpenedTS: baseT + 100},
			{SessionID: "sess-C", OpenedTS: baseT + 300},
		},
		SelectHalf:  []Window{{SessionID: "sess-A", OpenedTS: baseT}, {SessionID: "sess-B", OpenedTS: baseT + 100}},
		HoldoutHalf: []Window{{SessionID: "sess-C", OpenedTS: baseT + 300}},
		Counts:      counts,
	}
	return res, ds
}

// ---- 九节顺序 + 关键行（验收 1/2） ----

func TestMarkdownSectionsOrderAndKeyLines(t *testing.T) {
	// 档名文案逐字锚（注意 − 为 U+2212，规格原文）。
	if ScenarioNameConfig != "config TTL（无实测，采集日期 N/A）" ||
		ScenarioNameThird != "−1/3" || ScenarioNameHalf != "−1/2" {
		t.Fatal("场景档名文案偏离规格逐字")
	}

	res, ds := sweepFixture()
	md := RenderMarkdown(res, ds)

	last := -1
	for _, h := range reportHeaders {
		i := strings.Index(md, h)
		if i < 0 {
			t.Fatalf("缺节：%s", h)
		}
		if i <= last {
			t.Fatalf("节序错乱：%s（index %d ≤ 前节 %d）", h, i, last)
		}
		last = i
	}

	keyLines := []string{
		// 1 头部：时点戳 + 双口径 + 桶计数
		"- 装载时点戳：2026-09-21T09:15:00+0800（epoch 1789948800）",
		"| 全部窗（过滤前） | 4 |",
		"| 真实项目过滤后 | 3 |",
		"| 其中可重放（价格可算） | 3 |",
		"| unknown 桶（未还原留存） | 0 |",
		"| 不可算桶（缺 P_cache） | 0 |",
		"- 未还原窗（过滤前口径）：1",
		"- 多值匹配 session 数：2",
		"（未记录） 1", // close_reason 空键照登
		"expired 1",
		// 2 差距表：闭式对照 vs 主网格最优（同档内对比 + 推荐）
		"| 当前闭式配置（config 档对照 ×1.0） | 600.0 | 420.0 | 480.0 | 1800.0 | 10.00 | 110.00 |",
		"| 主网格最优（档位乘数 ×0.90） | 540.0 | 378.0 | 432.0 | 1620.0 | 12.50 | 137.50 |",
		"- 差距（主网格最优 − 当前闭式配置）：**27.50**",
		"建议将 config ttl_s 校准至 540.0 s（档位乘数 ×0.90）",
		"| config TTL（无实测，采集日期 N/A） | 110.00 | 137.50 | 27.50 |",
		"| −1/3 | 60.00 | 80.00 | 20.00 |",
		"| −1/2 | 30.00 | -20.00 | -50.00 |",
		// 3 场景轴：档名逐字 + 翻转点
		"| config TTL（无实测，采集日期 N/A） | 600.0 | 110.00 | 137.50 | 30 | 12.50 | 1 | 0 |",
		"档「−1/3」与上一档对比：推荐参数组变化",
		"档「−1/2」与上一档对比：结论翻转",
		"由正变负",
		// 4 诊断网格（单列 + 标注；∞ 行）
		"诊断用，非可部署",
		"| 400.0 | 100.0 | ∞ | 3 | 20 | 88.00 | 1 |",
		// 5 无效保温单列（历史空表照登 + 三档场景）
		"历史空表照登",
		"| 0% | 0.00 | 137.50 |",
		"| 10% | 1.25 | 136.25 |",
		"| 30% | 3.75 | 133.75 |",
		// 6 双计检查（两列各列各的）
		"| 心跳花费（本扫参推演） | 12.50 |",
		"handoff 科目",
		"不互相抵扣",
		// 7 留出集两栏（后半栏参数集与前半一致）
		"| 前半 | 选参 | 2 |",
		"| 后半 | 只验证 | 1 |",
		"参数集与前半一致",
		// 8 盲区固定文案
		"DefaultBeatOutTokens=300",
		"渡口限流",
		"GLM 端 TTL 策略漂移",
		"外推",
		// 9 证据等级逐字
		EvidenceLevel,
	}
	for _, k := range keyLines {
		if !strings.Contains(md, k) {
			t.Fatalf("markdown 缺关键行：%q", k)
		}
	}
}

// ---- --json 与 markdown 数字互相印证（验收 3） ----

func jget(t *testing.T, v any, path ...any) any {
	t.Helper()
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("路径 %v：非对象（%#v）", path, v)
			}
			v = m[k]
		case int:
			a, ok := v.([]any)
			if !ok {
				t.Fatalf("路径 %v：非数组（%#v）", path, v)
			}
			if k < 0 || k >= len(a) {
				t.Fatalf("路径 %v：越界（len=%d）", path, len(a))
			}
			v = a[k]
		}
	}
	return v
}

func jnum(t *testing.T, v map[string]any, path ...any) float64 {
	t.Helper()
	n, ok := jget(t, v, path...).(float64)
	if !ok {
		t.Fatalf("路径 %v 非 number：%#v", path, jget(t, v, path...))
	}
	return n
}

func jstr(t *testing.T, v map[string]any, path ...any) string {
	t.Helper()
	s, ok := jget(t, v, path...).(string)
	if !ok {
		t.Fatalf("路径 %v 非 string：%#v", path, jget(t, v, path...))
	}
	return s
}

// checkNum JSON 数字与 markdown 格式化数字互相印证。
func checkNum(t *testing.T, md, label string, got, want float64, mdNeedle string) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
	if mdNeedle != "" && !strings.Contains(md, mdNeedle) {
		t.Fatalf("markdown 缺 %q（应与 JSON %s=%v 印证）", mdNeedle, label, want)
	}
}

func TestJSONMarkdownConsistency(t *testing.T) {
	res, ds := sweepFixture()
	md := RenderMarkdown(res, ds)
	jb, err := RenderJSON(res, ds)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := json.Unmarshal(jb, &top); err != nil {
		t.Fatal(err)
	}

	// 时点戳：两投影同值。
	iso := jstr(t, top, "loaded_at_iso")
	if iso != "2026-09-21T09:15:00+0800" || !strings.Contains(md, iso) {
		t.Fatalf("loaded_at_iso = %q，markdown 未印证", iso)
	}

	// 计数。
	checkNum(t, md, "counts.total_windows", jnum(t, top, "counts", "total_windows"), 4, "| 全部窗（过滤前） | 4 |")
	checkNum(t, md, "counts.after_filter", jnum(t, top, "counts", "after_filter_windows"), 3, "| 真实项目过滤后 | 3 |")
	checkNum(t, md, "counts.unresolved", jnum(t, top, "counts", "unresolved_windows"), 1, "- 未还原窗（过滤前口径）：1")
	checkNum(t, md, "counts.unknown", jnum(t, top, "counts", "unknown_count"), 0, "")

	// 差距表。
	checkNum(t, md, "gap.delta", jnum(t, top, "gap", "delta"), 27.5, "27.50")
	checkNum(t, md, "gap.baseline.net", jnum(t, top, "gap", "baseline", "net_savings"), 110, "110.00")
	checkNum(t, md, "gap.grid_best.net", jnum(t, top, "gap", "grid_best", "net_savings"), 137.5, "137.50")
	checkNum(t, md, "gap.grid_best.beat_cost", jnum(t, top, "gap", "grid_best", "beat_cost"), 12.5, "")
	if s := jstr(t, top, "gap", "recommend"); !strings.Contains(s, "校准") || !strings.Contains(md, s) {
		t.Fatalf("gap.recommend = %q，markdown 未印证", s)
	}

	// 场景轴：三档 + 逐字档名 + 翻转。
	nTiers := len(jget(t, top, "tiers").([]any))
	if nTiers != 3 {
		t.Fatalf("tiers len = %d, want 3", nTiers)
	}
	for i, want := range []string{ScenarioNameConfig, "−1/3", "−1/2"} {
		if got := jstr(t, top, "tiers", i, "tier"); got != want {
			t.Fatalf("tiers[%d].tier = %q, want %q", i, got, want)
		}
	}
	if n := len(jget(t, top, "tiers", 0, "rows").([]any)); n != 2 {
		t.Fatalf("config 档 rows len = %d, want 2", n)
	}
	checkNum(t, md, "tiers[2].best.net", jnum(t, top, "tiers", 2, "best", "net_savings"), -20, "-20.00")
	if s := jstr(t, top, "tiers", 2, "flip"); !strings.Contains(s, "结论翻转") || !strings.Contains(md, s) {
		t.Fatalf("tiers[2].flip = %q，markdown 未印证", s)
	}

	// 诊断网格：∞ → JSON null。
	nDiag := len(jget(t, top, "diagnostic").([]any))
	if nDiag != 2 {
		t.Fatalf("diagnostic len = %d, want 2", nDiag)
	}
	checkNum(t, md, "diagnostic[0].cap_s", jnum(t, top, "diagnostic", 0, "cap_s"), 900, "")
	if got := jget(t, top, "diagnostic", 1, "cap_s"); got != nil {
		t.Fatalf("diagnostic[1].cap_s = %#v, want null（+Inf 不可 JSON 化）", got)
	}

	// 无效保温三档场景。
	checkNum(t, md, "warm.expired_after", jnum(t, top, "useless_warm", "expired_after"), 0, "历史空表照登")
	nWarm := len(jget(t, top, "useless_warm", "scenarios").([]any))
	if nWarm != 3 {
		t.Fatalf("warm scenarios len = %d, want 3（0/10/30%%）", nWarm)
	}
	checkNum(t, md, "warm[1].ratio_pct", jnum(t, top, "useless_warm", "scenarios", 1, "ratio_pct"), 10, "| 10% |")
	checkNum(t, md, "warm[1].loss", jnum(t, top, "useless_warm", "scenarios", 1, "loss"), 1.25, "1.25")
	checkNum(t, md, "warm[1].adjusted_net", jnum(t, top, "useless_warm", "scenarios", 1, "adjusted_net"), 136.25, "136.25")
	checkNum(t, md, "warm[2].loss", jnum(t, top, "useless_warm", "scenarios", 2, "loss"), 3.75, "")

	// 双计检查：心跳推演列。
	checkNum(t, md, "double_count.beat_cost", jnum(t, top, "double_count", "beat_cost"), 12.5, "12.50")
	if s := jstr(t, top, "double_count", "ferry_note"); !strings.Contains(s, "handoff") || !strings.Contains(md, s) {
		t.Fatalf("ferry_note = %q，markdown 未印证", s)
	}

	// 留出集两栏。
	checkNum(t, md, "holdout.select_n", jnum(t, top, "holdout", "select_n"), 2, "| 前半 | 选参 | 2 |")
	checkNum(t, md, "holdout.holdout_n", jnum(t, top, "holdout", "holdout_n"), 1, "| 后半 | 只验证 | 1 |")
	if s := jstr(t, top, "holdout", "param_desc"); !strings.Contains(s, "τ=378.0") || !strings.Contains(md, s) {
		t.Fatalf("holdout.param_desc = %q，markdown 未印证", s)
	}

	// 证据等级与盲区。
	if got := jstr(t, top, "evidence_level"); got != EvidenceLevel || !strings.Contains(md, got) {
		t.Fatalf("evidence_level 偏离逐字规格：%q", got)
	}
	if n := len(jget(t, top, "blind_spots").([]any)); n != 4 {
		t.Fatalf("blind_spots len = %d, want 4", n)
	}
}

// ---- 确定性：同输入两次渲染逐字节一致（验收 5） ----

func TestReportDeterministic(t *testing.T) {
	res, ds := sweepFixture()
	if a, b := RenderMarkdown(res, ds), RenderMarkdown(res, ds); a != b {
		t.Fatal("markdown 两次渲染不一致")
	}
	ja, err := RenderJSON(res, ds)
	if err != nil {
		t.Fatal(err)
	}
	jb, err := RenderJSON(res, ds)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ja, jb) {
		t.Fatal("json 两次渲染不一致")
	}
}

// ---- 隐私：报告只含元数据，结构上无消息内容/会话标识（验收 4） ----

func TestReportMetadataOnly(t *testing.T) {
	res, ds := sweepFixture()
	md := RenderMarkdown(res, ds)
	jb, err := RenderJSON(res, ds)
	if err != nil {
		t.Fatal(err)
	}
	for i, blob := range []string{md, string(jb)} {
		if strings.Contains(blob, "sess-") {
			t.Fatalf("投影 %d 泄漏 session ID（报告只含元数据）", i)
		}
	}
}

// ---- 零值优雅呈现：空 SweepResult / nil Dataset 不炸且九节俱在 ----

func TestReportEmptyGraceful(t *testing.T) {
	md := RenderMarkdown(&SweepResult{}, nil)
	for _, h := range reportHeaders {
		if !strings.Contains(md, h) {
			t.Fatalf("空结果缺节：%s", h)
		}
	}
	for _, k := range []string{
		"（未装载）", "（无）", "（引擎未产出）", "主网格最优（引擎未产出）",
		"（引擎未产出诊断网格）", "历史空表照登", "handoff 科目", "参数集与前半一致",
		"DefaultBeatOutTokens=300", EvidenceLevel,
		ScenarioNameConfig, "−1/3", "−1/2",
	} {
		if !strings.Contains(md, k) {
			t.Fatalf("空结果缺占位/固定文案：%q", k)
		}
	}
	if _, err := RenderJSON(&SweepResult{}, nil); err != nil {
		t.Fatalf("空结果 JSON 渲染失败：%v", err)
	}
}
