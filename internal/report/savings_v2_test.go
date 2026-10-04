package report

// savings_v2_test.go — 票04：SavingsV2 组装（v1 之上追加保温盈亏节）验收钉子。
// 覆盖验收清单：v2 节字段齐全、按保温动作类型拆分正确（handoff:same_model /
// beat:qwatch / beat:wait）、v1 段与同输入 SavingsV1 输出逐键 deep-equal（金测）、
// ambiguous 金额单列不并入 paired（F7）、useless_warm 科目行不混入 warm 节、
// 跨月归属（收益入回归月 / 支出入发生月）、warm 节与 SavingsV2["warm"] 同源。

import (
	"reflect"
	"testing"
	"time"

	"ferryman/internal/prices"
)

// ---- 夹具（复用 warm_episode_test.go 的 weUsage/weHandoff 与 warm_cost_test.go
// 的 wcDock、report_test.go 的 sep30_2300/approxAbs/intOf/anyNum） ----

// sv2Beat 带 lane 与实付的 beat 行（weBeat 固定 cost 0，本处需要非零成本与泳道）。
func sv2Beat(lid string, ts, prefix, cost float64, lane string) map[string]any {
	return map[string]any{"kind": "beat", "ts": ts, "agent": "dsh",
		"session_id": "s-" + lid, "lineage_id": lid, "lane": lane,
		"outcome": "observe", "prefix_tokens": prefix, "cost_actual": cost}
}

// sv2Fixture 合成账本（价书 weGlmBook：per=10000、6.9/1.7/24）：
//
//	LA 回合兑现（多跳 handoff+beat；回归 1900s>1800 → need 真、cr 95000≥80000
//	   → hit 真；节省 10 万前缀×(6.9−1.7)/万=52.0）
//	LB 白保温（回归 1000s<1800 → need 假、cr 45000≥40000 → hit 真；双 dock
//	   命中签名 → ambiguous 价书推算 34.5/票05）
//	LC 回归亏损（cr 60000<64000 → ¬hit；无 dock → unpaired 回落 55.2）
//	LD 过期亏损（等待窗心跳 beat:wait 0.25；数据末尾放任过期，终点月=10 月）
//	LE 双回合跨月：9-30 锚 + 23:10 beat（0.125）→ 2400s 处过期（9 月亏损）；
//	   10-01 12:00 handoff（paired 10.44）→ 12:30 回归兑现（31.2，收益入 10 月）。
func sv2Fixture() []map[string]any {
	base := float64(time.Date(2026, 10, 2, 12, 0, 0, 0, time.Local).Unix())
	return []map[string]any{
		weUsage("LA", base, 7141, 1712, 0, 145344),
		weHandoff("LA", base+100, 100000),
		wcDock("s-LA", base+150.8, 376, 99624, 0, 3994), // paired 实付 26.7811
		sv2Beat("LA", base+1000, 99000, 0.5, "qwatch"),
		weUsage("LA", base+1900, 1000, 500, 0, 95000),

		weUsage("LB", base, 7141, 1712, 0, 145344),
		weHandoff("LB", base+100, 50000),
		wcDock("s-LB", base+110, 0, 50000, 0, 100),
		wcDock("s-LB", base+120, 1000, 49000, 0, 200), // 双命中 → ambiguous
		weUsage("LB", base+1000, 1000, 500, 0, 45000),

		weUsage("LC", base, 7141, 1712, 0, 145344),
		weHandoff("LC", base+100, 80000),              // 无 dock → unpaired 回落 55.2
		weUsage("LC", base+1000, 1000, 500, 0, 60000), // cr<64000 → ¬hit 亏损

		weUsage("LD", base, 7141, 1712, 0, 145344),
		sv2Beat("LD", base+100, 40000, 0.25, "wait"),

		weUsage("LE", sep30_2300, 7141, 1712, 0, 145344), // 2026-09-30 23:00
		sv2Beat("LE", sep30_2300+600, 55000, 0.125, "qwatch"),
		weHandoff("LE", sep30_2300+46800, 60000),             // 10-01 12:00
		wcDock("s-LE", sep30_2300+46850, 0, 60000, 0, 100),   // paired 10.44
		weUsage("LE", sep30_2300+48600, 1000, 500, 0, 57000), // 10-01 12:30 回归
	}
}

// sv2Month 取 warm.months[m]（缺失即炸）。
func sv2Month(t *testing.T, warm map[string]any, m string) map[string]any {
	t.Helper()
	months, ok := warm["months"].(map[string]any)
	if !ok {
		t.Fatalf("warm 缺 months 键: %v", warm)
	}
	row, ok := months[m].(map[string]any)
	if !ok {
		t.Fatalf("warm.months[%s] 缺失: %v", m, months)
	}
	return row
}

// sv2Keys 键集断言（键数与键名都齐）。
func sv2Keys(t *testing.T, name string, got map[string]any, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s 键集 = %d 键 %v, want %v", name, len(got), got, want)
	}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Fatalf("%s 缺键 %q（got %v）", name, k, got)
		}
	}
}

// sv2Count 计数＋金额小行断言。
func sv2Count(t *testing.T, name string, m map[string]any, count int, amount float64) {
	t.Helper()
	if intOf(m["count"]) != count {
		t.Fatalf("%s.count = %v, want %d（full: %v）", name, m["count"], count, m)
	}
	approxAbs(t, name+".amount", anyNum(m["amount"]), amount, 1e-9)
}

// ---- 主钉：保温盈亏节按月分桶、字段齐全、动作类型拆分、比率与净额 ----

func TestSavingsV2WarmSectionMonths(t *testing.T) {
	warm := WarmSection(sv2Fixture(), weGlmBook(),
		WarmEpisodeConfig{}, WarmCostConfig{})

	months := warm["months"].(map[string]any)
	if len(months) != 2 {
		t.Fatalf("months = %v, want 2026-09 与 2026-10 两桶", months)
	}

	// ---- 2026-09：仅 LE 过期亏损回合（终点 9-30 23:40）与其 beat 支出 ----
	s := sv2Month(t, warm, "2026-09")
	sv2Keys(t, "2026-09", s, "realized_count", "realized_savings", "benign_count",
		"loss_count", "loss_cost", "regression_count", "hit_ratio", "need_ratio",
		"spend_total", "spend", "net")
	if intOf(s["realized_count"]) != 0 || intOf(s["benign_count"]) != 0 ||
		intOf(s["loss_count"]) != 1 || intOf(s["regression_count"]) != 0 {
		t.Fatalf("2026-09 回合计数: %v", s)
	}
	approxAbs(t, "2026-09.loss_cost", anyNum(s["loss_cost"]), 0.125, 1e-9)
	approxAbs(t, "2026-09.hit_ratio", anyNum(s["hit_ratio"]), 0, 1e-9) // 分母 0 记 0
	approxAbs(t, "2026-09.need_ratio", anyNum(s["need_ratio"]), 0, 1e-9)
	approxAbs(t, "2026-09.spend_total", anyNum(s["spend_total"]), 0.125, 1e-9)
	approxAbs(t, "2026-09.net", anyNum(s["net"]), -0.125, 1e-9)
	sp := s["spend"].(map[string]any)
	if len(sp) != 1 {
		t.Fatalf("2026-09.spend = %v, want 仅 beat:qwatch", sp)
	}
	sv2Count(t, "2026-09.beat:qwatch", sp["beat:qwatch"].(map[string]any), 1, 0.125)
	sv2Count(t, "2026-09.beat_direct",
		sp["beat:qwatch"].(map[string]any)["results"].(map[string]any)["beat_direct"].(map[string]any),
		1, 0.125)

	// ---- 2026-10：兑现×2（LA 52.0 + LE 31.2）、白保温×1、亏损×2（LC 回归
	// 亏损 + LD 过期亏损）；回归回合 4（LA/LB/LC/LE-E2）为命中率分母（拦截/
	// 过期回合 LD 不入分母）。----
	o := sv2Month(t, warm, "2026-10")
	sv2Keys(t, "2026-10", o, "realized_count", "realized_savings", "benign_count",
		"loss_count", "loss_cost", "regression_count", "hit_ratio", "need_ratio",
		"spend_total", "spend", "net")
	if intOf(o["realized_count"]) != 2 || intOf(o["benign_count"]) != 1 ||
		intOf(o["loss_count"]) != 2 || intOf(o["regression_count"]) != 4 {
		t.Fatalf("2026-10 回合计数: %v", o)
	}
	approxAbs(t, "2026-10.realized_savings", anyNum(o["realized_savings"]), 83.2, 1e-9)
	// 亏损回合支出 = LC unpaired 55.2 + LD beat 0.25（LA/LB/LE 兑现与白保温的不入）。
	approxAbs(t, "2026-10.loss_cost", anyNum(o["loss_cost"]), 55.45, 1e-9)
	approxAbs(t, "2026-10.hit_ratio", anyNum(o["hit_ratio"]), 0.5, 1e-9)   // 2/4
	approxAbs(t, "2026-10.need_ratio", anyNum(o["need_ratio"]), 0.5, 1e-9) // 2/4（LA 与 LE-E2）
	approxAbs(t, "2026-10.spend_total", anyNum(o["spend_total"]), 127.6711, 1e-9)
	approxAbs(t, "2026-10.net", anyNum(o["net"]), -44.4711, 1e-9) // 83.2−127.6711

	// 支出明细按动作类型拆分：handoff:same_model / beat:qwatch / beat:wait。
	osp := o["spend"].(map[string]any)
	if len(osp) != 3 {
		t.Fatalf("2026-10.spend = %v, want 三动作类型", osp)
	}
	h := osp["handoff:same_model"].(map[string]any)
	sv2Keys(t, "handoff 行", h, "count", "amount", "results")
	sv2Count(t, "handoff:same_model", h, 4, 126.9211) // 26.7811+34.5+55.2+10.44
	hres := h["results"].(map[string]any)
	sv2Keys(t, "handoff results", hres, "paired", "ambiguous", "zero_cost",
		"unpaired", "beat_direct")
	sv2Count(t, "handoff.paired", hres["paired"].(map[string]any), 2, 37.2211)
	sv2Count(t, "handoff.ambiguous", hres["ambiguous"].(map[string]any), 1, 34.5) // 价书推算（票05）
	sv2Count(t, "handoff.zero_cost", hres["zero_cost"].(map[string]any), 0, 0)
	sv2Count(t, "handoff.unpaired", hres["unpaired"].(map[string]any), 1, 55.2)
	sv2Count(t, "handoff.beat_direct", hres["beat_direct"].(map[string]any), 0, 0)

	bq := osp["beat:qwatch"].(map[string]any)
	sv2Count(t, "beat:qwatch", bq, 1, 0.5)
	sv2Count(t, "beat:qwatch.beat_direct",
		bq["results"].(map[string]any)["beat_direct"].(map[string]any), 1, 0.5)
	bw := osp["beat:wait"].(map[string]any)
	sv2Count(t, "beat:wait", bw, 1, 0.25)
	sv2Count(t, "beat:wait.beat_direct",
		bw["results"].(map[string]any)["beat_direct"].(map[string]any), 1, 0.25)
}

// ---- 金测：v1 段与同输入 SavingsV1 输出逐键 deep-equal；v2 只加 warm 与升章 ----

func TestSavingsV2V1Gold(t *testing.T) {
	entries := sv2Fixture()
	books := map[string]prices.PriceBook{"glm": *weGlmBook()}
	v1 := SavingsV1(entries, books, weGlmBook())
	v2 := SavingsV2(entries, books, weGlmBook(),
		WarmEpisodeConfig{}, WarmCostConfig{})

	if v1["formula"] != SavingsFormula || SavingsFormula != "v1" {
		t.Fatalf("SavingsV1 formula = %v（v1 必须不动）", v1["formula"])
	}
	if v2["formula"] != SavingsV2Formula || SavingsV2Formula != "v2" {
		t.Fatalf("SavingsV2 formula = %v, want v2", v2["formula"])
	}
	sv2Keys(t, "v1 键集", v1, "formula", "lineages", "totals", "unpriced")
	sv2Keys(t, "v2 键集", v2, "formula", "lineages", "totals", "unpriced", "warm")
	for _, k := range []string{"lineages", "totals", "unpriced"} {
		if !reflect.DeepEqual(v2[k], v1[k]) {
			t.Fatalf("v1 段键 %q 与同输入 SavingsV1 输出不一致（金测破坏）\nv2: %#v\nv1: %#v",
				k, v2[k], v1[k])
		}
	}
	// warm 节与独立 WarmSection 同源（daemon 挂接同一构造）。
	if !reflect.DeepEqual(v2["warm"],
		WarmSection(entries, weGlmBook(), WarmEpisodeConfig{}, WarmCostConfig{})) {
		t.Fatalf("SavingsV2[\"warm\"] 与 WarmSection 输出不一致")
	}
}

// ---- 空输入：v1 键集照常、warm.months 空（纯查询零样本不造桶） ----

func TestSavingsV2Empty(t *testing.T) {
	v2 := SavingsV2(nil, nil, nil, WarmEpisodeConfig{}, WarmCostConfig{})
	sv2Keys(t, "v2 键集", v2, "formula", "lineages", "totals", "unpriced", "warm")
	if months := v2["warm"].(map[string]any)["months"].(map[string]any); len(months) != 0 {
		t.Fatalf("空输入 months = %v, want 空", months)
	}
}

// ---- useless_warm 科目行（wait_close）不混入 warm 节 ----

func TestWarmSectionUselessWarmNotMixed(t *testing.T) {
	entries := sv2Fixture()
	withUW := append(append([]map[string]any{}, entries...),
		map[string]any{"kind": "wait_close", "ts": entries[0]["ts"].(float64) + 50,
			"agent": "cc", "session_id": "s-LW", "lineage_id": "LW", "project": "C:/p",
			"lane": "wait", "opened_ts": entries[0]["ts"].(float64),
			"closed_ts": entries[0]["ts"].(float64) + 50, "dur_s": 50.0,
			"beats_fired": 1.0, "cost_actual": 0.7, "main_resumed": false,
			"useless_warm": true, "close_reason": "window_closed"})
	a := WarmSection(entries, weGlmBook(), WarmEpisodeConfig{}, WarmCostConfig{})
	b := WarmSection(withUW, weGlmBook(), WarmEpisodeConfig{}, WarmCostConfig{})
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("wait_close/useless_warm 行混入了 warm 节\na: %#v\nb: %#v", a, b)
	}
}
