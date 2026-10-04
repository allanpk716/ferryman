package report

// warm_cost_test.go — 票03：保温成本腿（支出腿）表驱动单测。
// 覆盖验收清单：四分支（paired/ambiguous/zero_cost/unpaired）各自用例 +
// 并发窗用例（按协调者裁定 2026-10-04：配对按签名命中数判——窗内混入未命中
// 签名的真实流量 dock 行（in+cr 偏离超容差或 output 超 8192）不误配，恰一
// 命中仍 paired 且实付取命中行；双命中才落 ambiguous）；另锁：签名容差两侧
// （绝对 512 / 相对 2%）、output 上限两侧、候选窗边界两侧（含端点）、
// session_id 不相等不成候选、预派发失败短路（含窗内杂行不照配）与派发后失败
// 照配实付、零命中
// wall_s 判分支、provider=local 与非 same_model handoff skip、beat 自带实付
// 直取、价书按 dock 行时刻版本化、econBook 缺席不造数、可配参数生效、
// 输入序即输出序。

import (
	"fmt"
	"testing"
	"time"

	"ferryman/internal/prices"
)

// ---- 测试行构造（字段形状对齐真实账本样例，票面背景材料） ----

func wcHandoff(sid, lid string, ts, prompt, wallS float64) map[string]any {
	return map[string]any{"kind": "handoff", "ts": ts, "agent": "cc",
		"session_id": sid, "lineage_id": lid, "lane": "same_model",
		"completion_tokens": 3994.0, "prompt_tokens": prompt,
		"provider": "智谱", "outcome": "fresh", "wall_s": wallS}
}

// wcFailHandoff 预派发失败行（outcome=failed / prompt_tokens=0 / wall_s=0）。
func wcFailHandoff(sid, lid string, ts float64) map[string]any {
	return map[string]any{"kind": "handoff", "ts": ts, "agent": "cc",
		"session_id": sid, "lineage_id": lid, "lane": "same_model",
		"completion_tokens": 0.0, "prompt_tokens": 0.0,
		"provider": "智谱", "outcome": "failed", "wall_s": 0.0,
		"err": "snapshot_missing"}
}

// wcPostFailHandoff 派发后失败行（watcher 产线实况：已发送、已扣费、仅产物
// 不合格，如 md_structure——prompt_tokens>0 且 wall_s>0，须照配实付）。
func wcPostFailHandoff(sid, lid string, ts, prompt, wallS float64) map[string]any {
	return map[string]any{"kind": "handoff", "ts": ts, "agent": "cc",
		"session_id": sid, "lineage_id": lid, "lane": "same_model",
		"completion_tokens": 0.0, "prompt_tokens": prompt,
		"provider": "智谱", "outcome": "failed", "wall_s": wallS,
		"err": "md_structure"}
}

func wcDock(sid string, ts, in, cr, cc, out float64) map[string]any {
	return map[string]any{"kind": "dock", "ts": ts, "agent": "cc",
		"session_id": sid, "mode": "rewrite", "model_in": "claude-opus-5",
		"model_out": "GLM-5.3", "input_tokens": in, "cache_read_tokens": cr,
		"cache_creation_tokens": cc, "output_tokens": out,
		"latency_s": 50.79, "status": 200.0}
}

func wcBeat(sid, lid string, ts, costActual, cacheRead float64) map[string]any {
	return map[string]any{"kind": "beat", "ts": ts, "agent": "dsh",
		"session_id": sid, "lineage_id": lid, "lane": "qwatch",
		"outcome": "observe", "prefix_tokens": 51908.0,
		"cost_actual": costActual, "cache_read": cacheRead}
}

// assertWC 逐字段比对成本行清单（浮点走绝对容差）。
func assertWC(t *testing.T, name string, got, want []WarmCost) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: 行数 = %d, want %d\ngot: %+v", name, len(got), len(want), got)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Lineage != w.Lineage || g.Kind != w.Kind || g.Lane != w.Lane ||
			g.Result != w.Result || g.Basis != w.Basis || g.Pending != w.Pending ||
			g.Month != w.Month {
			t.Fatalf("%s[%d]: 基本字段不齐\ngot:  %+v\nwant: %+v", name, i, g, w)
		}
		approxAbs(t, fmt.Sprintf("%s[%d].TS", name, i), g.TS, w.TS, 1e-6)
		approxAbs(t, fmt.Sprintf("%s[%d].Cost", name, i), g.Cost, w.Cost, 1e-6)
		approxAbs(t, fmt.Sprintf("%s[%d].InputTokens", name, i), g.InputTokens, w.InputTokens, 1e-6)
		approxAbs(t, fmt.Sprintf("%s[%d].CacheReadTokens", name, i), g.CacheReadTokens, w.CacheReadTokens, 1e-6)
		approxAbs(t, fmt.Sprintf("%s[%d].CacheCreationTokens", name, i), g.CacheCreationTokens, w.CacheCreationTokens, 1e-6)
		approxAbs(t, fmt.Sprintf("%s[%d].OutputTokens", name, i), g.OutputTokens, w.OutputTokens, 1e-6)
	}
}

func TestWarmCosts(t *testing.T) {
	base := float64(time.Date(2026, 10, 2, 12, 0, 0, 0, time.Local).Unix())
	book := weGlmBook() // 复用 warm_episode_test.go 的 [prices.glm] 价书（6.9/1.7/24，per=10000）

	cases := []struct {
		name    string
		entries []map[string]any
		cfg     WarmCostConfig // 零值 = 默认参数
		book    *prices.PriceBook // nil = 用默认 glm 价书
		want    []WarmCost
	}{
		{
			// 票面真实配对实测：prompt=122424 ↔ dock in=376/cr=122048/out=3994。
			name: "paired·真实样例四列实付",
			entries: []map[string]any{
				wcHandoff("s1", "L1", base+1000, 122424, 50.8),
				wcDock("s1", base+1050.8, 376, 122048, 0, 3994),
			},
			want: []WarmCost{{
				Lineage: "L1", TS: base + 1000, Kind: "handoff", Lane: "same_model",
				Result: WarmCostPaired, Cost: 30.5932, // (376×6.9 + 122048×1.7 + 3994×24)/万
				Basis:               WarmBasisActual,
				InputTokens:         376,
				CacheReadTokens:     122048,
				CacheCreationTokens: 0,
				OutputTokens:        3994,
				Month:               "2026-10",
			}},
		},
		{
			// cache_creation 按输入全价计（价书无独立写价列，票03口径）。
			name: "paired·缓存写入按输入全价",
			entries: []map[string]any{
				wcHandoff("s1", "L1", base+100, 50000, 10),
				wcDock("s1", base+105, 1000, 49000, 1000, 2000),
			},
			want: []WarmCost{{
				Lineage: "L1", TS: base + 100, Kind: "handoff", Lane: "same_model",
				Result: WarmCostPaired,
				Cost:   14.51, // (1000×6.9 + 49000×1.7 + 1000×6.9 + 2000×24)/万
				Basis:               WarmBasisActual,
				InputTokens:         1000,
				CacheReadTokens:     49000,
				CacheCreationTokens: 1000,
				OutputTokens:        2000,
				Month:               "2026-10",
			}},
		},
		{
			// prompt 小：容差 = max(512, 2%) = 512；|10512−10000| = 512 恰在界内。
			name: "paired·签名绝对容差恰在512",
			entries: []map[string]any{
				wcHandoff("s1", "L1", base+100, 10000, 30),
				wcDock("s1", base+120, 10000, 512, 0, 100),
			},
			want: []WarmCost{{
				Lineage: "L1", TS: base + 100, Kind: "handoff", Lane: "same_model",
				Result: WarmCostPaired, Cost: 7.227, // (10000×6.9 + 512×1.7 + 100×24)/万
				Basis:           WarmBasisActual,
				InputTokens:     10000,
				CacheReadTokens: 512,
				OutputTokens:    100,
				Month:           "2026-10",
			}},
		},
		{
			// prompt 大：容差 = 2%×100000 = 2000 压过 512；差 2000 恰在界内。
			name: "paired·签名相对容差2%生效",
			entries: []map[string]any{
				wcHandoff("s1", "L1", base+100, 100000, 30),
				wcDock("s1", base+120, 98000, 0, 0, 3994),
			},
			want: []WarmCost{{
				Lineage: "L1", TS: base + 100, Kind: "handoff", Lane: "same_model",
				Result: WarmCostPaired, Cost: 77.2056, // (98000×6.9 + 3994×24)/万
				Basis:           WarmBasisActual,
				InputTokens:     98000,
				OutputTokens:    3994,
				Month:           "2026-10",
			}},
		},
		{
			name: "unpaired·绝对容差外一行未命中回落价书",
			entries: []map[string]any{
				wcHandoff("s1", "L1", base+100, 10000, 30),
				wcDock("s1", base+120, 10513, 0, 0, 100), // 差 513 > 512
			},
			want: []WarmCost{{
				Lineage: "L1", TS: base + 100, Kind: "handoff", Lane: "same_model",
				Result: WarmCostUnpaired, Cost: 6.9, // 10000×6.9/万 回落推算
				Basis: WarmBasisFallback, Month: "2026-10",
			}},
		},
		{
			name: "unpaired·相对容差外回落",
			entries: []map[string]any{
				wcHandoff("s1", "L1", base+100, 100000, 30),
				wcDock("s1", base+120, 102001, 0, 0, 100), // 差 2001 > 2000
			},
			want: []WarmCost{{
				Lineage: "L1", TS: base + 100, Kind: "handoff", Lane: "same_model",
				Result: WarmCostUnpaired, Cost: 69.0, // 100000×6.9/万
				Basis: WarmBasisFallback, Month: "2026-10",
			}},
		},
		{
			name: "unpaired·output超上限8193",
			entries: []map[string]any{
				wcHandoff("s1", "L1", base+100, 50000, 30),
				wcDock("s1", base+120, 0, 50000, 0, 8193),
			},
			want: []WarmCost{{
				Lineage: "L1", TS: base + 100, Kind: "handoff", Lane: "same_model",
				Result: WarmCostUnpaired, Cost: 34.5, // 50000×6.9/万
				Basis: WarmBasisFallback, Month: "2026-10",
			}},
		},
		{
			name: "paired·output恰在上限8192",
			entries: []map[string]any{
				wcHandoff("s1", "L1", base+100, 50000, 30),
				wcDock("s1", base+120, 0, 50000, 0, 8192),
			},
			want: []WarmCost{{
				Lineage: "L1", TS: base + 100, Kind: "handoff", Lane: "same_model",
				Result: WarmCostPaired, Cost: 28.1608, // (50000×1.7 + 8192×24)/万
				Basis:           WarmBasisActual,
				CacheReadTokens: 50000,
				OutputTokens:    8192,
				Month:           "2026-10",
			}},
		},
		{
			// 窗右界 = handoff.ts + wall_s + 90 = base+1140，端点含（dock.ts=记账
			// 盖章≈请求完成时刻，恰在完成时限上的行属本派发）。
			name: "paired·候选窗右界恰在wall加90",
			entries: []map[string]any{
				wcHandoff("s1", "L1", base+1000, 50000, 50),
				wcDock("s1", base+1140, 0, 50000, 0, 100),
			},
			want: []WarmCost{{
				Lineage: "L1", TS: base + 1000, Kind: "handoff", Lane: "same_model",
				Result: WarmCostPaired, Cost: 8.74, // (50000×1.7 + 100×24)/万
				Basis:           WarmBasisActual,
				CacheReadTokens: 50000,
				OutputTokens:    100,
				Month:           "2026-10",
			}},
		},
		{
			name: "paired·候选窗左界恰在负2",
			entries: []map[string]any{
				wcHandoff("s1", "L1", base+1000, 50000, 50),
				wcDock("s1", base+998, 0, 50000, 0, 100),
			},
			want: []WarmCost{{
				Lineage: "L1", TS: base + 1000, Kind: "handoff", Lane: "same_model",
				Result: WarmCostPaired, Cost: 8.74,
				Basis:           WarmBasisActual,
				CacheReadTokens: 50000,
				OutputTokens:    100,
				Month:           "2026-10",
			}},
		},
		{
			name: "unpaired·候选晚于窗右界",
			entries: []map[string]any{
				wcHandoff("s1", "L1", base+1000, 50000, 50),
				wcDock("s1", base+1140.1, 0, 50000, 0, 100),
			},
			want: []WarmCost{{
				Lineage: "L1", TS: base + 1000, Kind: "handoff", Lane: "same_model",
				Result: WarmCostUnpaired, Cost: 34.5,
				Basis: WarmBasisFallback, Month: "2026-10",
			}},
		},
		{
			name: "unpaired·候选早于窗左界",
			entries: []map[string]any{
				wcHandoff("s1", "L1", base+1000, 50000, 50),
				wcDock("s1", base+997.9, 0, 50000, 0, 100),
			},
			want: []WarmCost{{
				Lineage: "L1", TS: base + 1000, Kind: "handoff", Lane: "same_model",
				Result: WarmCostUnpaired, Cost: 34.5,
				Basis: WarmBasisFallback, Month: "2026-10",
			}},
		},
		{
			// 匹配键第一条款：session_id 相等；异会话 dock 行即便同窗同形不成候选。
			name: "unpaired·session不相等不成候选",
			entries: []map[string]any{
				wcHandoff("s1", "L1", base+1000, 50000, 50),
				wcDock("s2", base+1020, 0, 50000, 0, 100),
			},
			want: []WarmCost{{
				Lineage: "L1", TS: base + 1000, Kind: "handoff", Lane: "same_model",
				Result: WarmCostUnpaired, Cost: 34.5,
				Basis: WarmBasisFallback, Month: "2026-10",
			}},
		},
		{
			// 多候选（双命中）→ ambiguous：与 unpaired 同处理——价书推算
			// （50000×6.9/万=34.5）并标注「价书回落」，不冒充实收（票05 用户确认）。
			name: "ambiguous·双候选皆命中",
			entries: []map[string]any{
				wcHandoff("s1", "L1", base+100, 50000, 50),
				wcDock("s1", base+110, 0, 50000, 0, 100),
				wcDock("s1", base+120, 1000, 49000, 0, 200),
			},
			want: []WarmCost{{
				Lineage: "L1", TS: base + 100, Kind: "handoff", Lane: "same_model",
				Result: WarmCostAmbiguous, Cost: 34.5,
				Basis: WarmBasisFallback, Month: "2026-10",
			}},
		},
		{
			// 并发窗裁定（一）：真实流量行 output 超 8192 未命中签名，不与重放
			// 行混淆——恰一命中仍 paired，实付取命中行（非错配到真实流量行）。
			// 若按窗内候选数判会错打 ambiguous 漏记实付（净节省偏乐观）。
			name: "paired·并发窗真实流量行output超上限不混淆",
			entries: []map[string]any{
				wcHandoff("s1", "L1", base+100, 122424, 50.8),
				wcDock("s1", base+150.8, 376, 122048, 0, 3994), // 重放行：命中签名
				wcDock("s1", base+160, 500, 122000, 0, 20000),  // 真实流量行：out 20000 > 8192
			},
			want: []WarmCost{{
				Lineage: "L1", TS: base + 100, Kind: "handoff", Lane: "same_model",
				Result: WarmCostPaired, Cost: 30.5932, Basis: WarmBasisActual,
				InputTokens:     376,
				CacheReadTokens: 122048,
				OutputTokens:    3994,
				Month:           "2026-10",
			}},
		},
		{
			// 并发窗裁定（二）：真实流量行 input+cache_read 不匹配 prompt_tokens
			// （差 102424 ≫ 容差）同样不混淆，恰一命中 paired 实付取命中行。
			name: "paired·并发窗真实流量行签名不匹配不混淆",
			entries: []map[string]any{
				wcHandoff("s1", "L1", base+100, 122424, 50.8),
				wcDock("s1", base+150.8, 376, 122048, 0, 3994), // 重放行：命中签名
				wcDock("s1", base+160, 10000, 10000, 0, 100),   // 真实流量行：in+cr=20000
			},
			want: []WarmCost{{
				Lineage: "L1", TS: base + 100, Kind: "handoff", Lane: "same_model",
				Result: WarmCostPaired, Cost: 30.5932, Basis: WarmBasisActual,
				InputTokens:     376,
				CacheReadTokens: 122048,
				OutputTokens:    3994,
				Month:           "2026-10",
			}},
		},
		{
			// 全未命中（双候选皆非重放形态）：重放行缺失 → wall_s>0 回落价书
			// 推算。真风险场景（同会话真实请求读热缓存致双命中）落 ambiguous，
			// 见上方「ambiguous·双候选皆命中」。
			name: "unpaired·双候选皆未命中回落价书",
			entries: []map[string]any{
				wcHandoff("s1", "L1", base+100, 50000, 50),
				wcDock("s1", base+110, 0, 30000, 0, 100),
				wcDock("s1", base+120, 0, 40000, 0, 100),
			},
			want: []WarmCost{{
				Lineage: "L1", TS: base + 100, Kind: "handoff", Lane: "same_model",
				Result: WarmCostUnpaired, Cost: 34.5, // 50000×6.9/万 回落推算
				Basis: WarmBasisFallback, Month: "2026-10",
			}},
		},
		{
			// 预派发失败（真实样例形态：outcome=failed / prompt_tokens=0 /
			// wall_s=0）：未派发即无重放行，零成本。
			name: "zero_cost·预派发失败零候选",
			entries: []map[string]any{
				wcFailHandoff("s1", "L1", base+100),
			},
			want: []WarmCost{{
				Lineage: "L1", TS: base + 100, Kind: "handoff", Lane: "same_model",
				Result: WarmCostZeroCost, Cost: 0, Month: "2026-10",
			}},
		},
		{
			// 失败行窗内杂行不照配：prompt_tokens=0 会使签名容差退化成 512，
			// 照配会误记无关行实付——预派发短路优先于候选扫描。
			name: "zero_cost·预派发失败窗内杂行不照配",
			entries: []map[string]any{
				wcFailHandoff("s1", "L1", base+100),
				wcDock("s1", base+120, 100, 0, 0, 50), // in+cr=100 ≤ 512 会退化命中
			},
			want: []WarmCost{{
				Lineage: "L1", TS: base + 100, Kind: "handoff", Lane: "same_model",
				Result: WarmCostZeroCost, Cost: 0, Month: "2026-10",
			}},
		},
		{
			// 派发后失败（终局评审补：failed ∧ prompt>0 ∧ wall>0，已扣费）照配
			// 实付——无条款授权按 outcome 整族短路，漏记即净节省虚高。
			name: "paired·派发后失败行照配实付",
			entries: []map[string]any{
				wcPostFailHandoff("s1", "L1", base+100, 97393, 60),
				wcDock("s1", base+150, 97393, 0, 0, 3994), // 签名全中
			},
			want: []WarmCost{{
				Lineage: "L1", TS: base + 100, Kind: "handoff", Lane: "same_model",
				Result: WarmCostPaired, Cost: 76.7868, // (97393×6.9 + 3994×24)/万
				Basis:           WarmBasisActual,
				InputTokens:     97393,
				OutputTokens:    3994,
				Month:           "2026-10",
			}},
		},
		{
			// 派发后失败 ∧ 零候选：重放行缺失，wall_s>0 → unpaired 回落（非
			// zero_cost——派发已发生，成本按价书推算并标注）。
			name: "unpaired·派发后失败零候选回落",
			entries: []map[string]any{
				wcPostFailHandoff("s1", "L1", base+100, 80000, 45),
			},
			want: []WarmCost{{
				Lineage: "L1", TS: base + 100, Kind: "handoff", Lane: "same_model",
				Result: WarmCostUnpaired, Cost: 55.2, // 80000×6.9/万
				Basis: WarmBasisFallback, Month: "2026-10",
			}},
		},
		{
			// 合同条款：零候选 ∧ wall_s=0 → zero_cost（非 failed 形态同款）。
			name: "zero_cost·零候选且wall为零",
			entries: []map[string]any{
				wcHandoff("s1", "L1", base+100, 50000, 0),
			},
			want: []WarmCost{{
				Lineage: "L1", TS: base + 100, Kind: "handoff", Lane: "same_model",
				Result: WarmCostZeroCost, Cost: 0, Month: "2026-10",
			}},
		},
		{
			// provider=local（本地 Qwen 档）零成本 skip：不入保温成本账，
			// 其窗内 dock 行不被消费、也不产出该行。
			name: "skip·provider为local不入账",
			entries: []map[string]any{
				{"kind": "handoff", "ts": base + 100, "agent": "cc",
					"session_id": "s1", "lineage_id": "L1", "lane": "same_model",
					"completion_tokens": 500.0, "prompt_tokens": 2181.0,
					"provider": "local", "model": "qwen", "outcome": "fresh",
					"wall_s": 17.8},
				wcDock("s1", base+110, 2181, 0, 0, 500),
			},
			want: nil,
		},
		{
			// 非 same_model 的智谱 handoff 不是保温动作（isWarmAction 同款判定，
			// 与票02 回合切分同口径），不入本账。
			name: "skip·非same_model智谱handoff不入账",
			entries: []map[string]any{
				{"kind": "handoff", "ts": base + 100, "agent": "cc",
					"session_id": "s1", "lineage_id": "L1", "lane": "cross_model",
					"completion_tokens": 900.0, "prompt_tokens": 90000.0,
					"provider": "智谱", "outcome": "fresh", "wall_s": 20.0},
				{"kind": "handoff", "ts": base + 200, "agent": "cc",
					"session_id": "s1", "lineage_id": "L1", // 无 lane 的存量行
					"completion_tokens": 900.0, "prompt_tokens": 90000.0,
					"provider": "智谱", "outcome": "fresh", "wall_s": 20.0},
				wcDock("s1", base+110, 0, 90000, 0, 900),
			},
			want: nil,
		},
		{
			// beat 行自带 cost_actual/cache_read 直取实付（等待窗零跳、qwatch
			// observe 零成本，仍按此口径支持将来）；不走 dock 配对。
			name: "beat·自带实付直取",
			entries: []map[string]any{
				wcBeat("s1", "L1", base+100, 0.5, 880),
				wcBeat("s1", "L1", base+200, 0, 0),
			},
			want: []WarmCost{
				{
					Lineage: "L1", TS: base + 100, Kind: "beat", Lane: "qwatch",
					Result: WarmCostBeatDirect, Cost: 0.5, Basis: WarmBasisActual,
					CacheReadTokens: 880, Month: "2026-10",
				},
				{
					Lineage: "L1", TS: base + 200, Kind: "beat", Lane: "qwatch",
					Result: WarmCostBeatDirect, Cost: 0, Basis: WarmBasisActual,
					CacheReadTokens: 0, Month: "2026-10",
				},
			},
		},
		{
			// 按行时刻取价书版本 = 按 dock 行时刻（实付发生点），非 handoff 时刻：
			// dock 落 2026-10-05（本地）取 10-05 生效新版本 (7.0/2.0/25)。
			name: "paired·价书按dock行时刻版本化",
			entries: []map[string]any{
				wcHandoff("s1", "L1", sep30_2300+5*86400-50, 100000, 40),
				wcDock("s1", sep30_2300+5*86400, 0, 100000, 0, 100),
			},
			book: &prices.PriceBook{Key: "glm", Unit: "智谱积分", Per: 10000,
				Versions: []prices.PriceVersion{
					{EffectiveFrom: "2026-09-01", PIn: 6.9, PCache: wePtr(1.7), POut: 24},
					{EffectiveFrom: "2026-10-05", PIn: 7.0, PCache: wePtr(2.0), POut: 25},
				}},
			want: []WarmCost{{
				Lineage: "L1", TS: sep30_2300 + 5*86400 - 50, Kind: "handoff",
				Lane: "same_model",
				Result: WarmCostPaired, Cost: 20.25, // (100000×2.0 + 100×25)/万；误按 handoff 时刻取旧版会算 17.24
				Basis:           WarmBasisActual,
				CacheReadTokens: 100000,
				OutputTokens:    100,
				Month:           "2026-10",
			}},
		},
		{
			// unpaired 回落按动作行（handoff）时刻取版本——成本归发生方。
			name: "unpaired·回落按动作行时刻版本化",
			entries: []map[string]any{
				wcHandoff("s1", "L1", sep30_2300+5*86400, 100000, 40),
			},
			book: &prices.PriceBook{Key: "glm", Unit: "智谱积分", Per: 10000,
				Versions: []prices.PriceVersion{
					{EffectiveFrom: "2026-09-01", PIn: 6.9, PCache: wePtr(1.7), POut: 24},
					{EffectiveFrom: "2026-10-05", PIn: 7.0, PCache: wePtr(2.0), POut: 25},
				}},
			want: []WarmCost{{
				Lineage: "L1", TS: sep30_2300 + 5*86400, Kind: "handoff", Lane: "same_model",
				Result: WarmCostUnpaired, Cost: 70.0, // 100000×7.0/万（新版本 p_in）；旧版会算 69.0
				Basis: WarmBasisFallback, Month: "2026-10",
			}},
		},
		{
			// 可配参数：窗右界加数收窄到 30s → 落在默认窗（90s）内的候选出局。
			name: "可配参数·候选窗收窄生效",
			cfg:  WarmCostConfig{WindowAfterS: 30},
			entries: []map[string]any{
				wcHandoff("s1", "L1", base+1000, 50000, 50),
				wcDock("s1", base+1120, 0, 50000, 0, 100), // 默认窗内可配对；30s 窗外
			},
			want: []WarmCost{{
				Lineage: "L1", TS: base + 1000, Kind: "handoff", Lane: "same_model",
				Result: WarmCostUnpaired, Cost: 34.5, Basis: WarmBasisFallback,
				Month: "2026-10",
			}},
		},
		{
			// 可配参数：output 上限与签名容差收紧 → 均未命中回落（SigTolRel
			// 收紧到 0.001——零值回落默认语义下禁用相对容差须给正小值）。
			cfg: WarmCostConfig{MaxOutTokens: 4096, SigTolRel: 0.001},
			name: "可配参数·output上限与容差收紧生效",
			entries: []map[string]any{
				wcHandoff("s1", "L1", base+100, 50000, 30),
				wcDock("s1", base+110, 0, 50000, 0, 5000), // > 4096：默认可配对，收紧后未命中
				wcHandoff("s2", "L2", base+500, 100000, 30),
				wcDock("s2", base+520, 98000, 0, 0, 100), // 差 2000 > 512（SigTolRel=0.001）
			},
			want: []WarmCost{
				{
					Lineage: "L1", TS: base + 100, Kind: "handoff", Lane: "same_model",
					Result: WarmCostUnpaired, Cost: 34.5, Basis: WarmBasisFallback,
					Month: "2026-10",
				},
				{
					Lineage: "L2", TS: base + 500, Kind: "handoff", Lane: "same_model",
					Result: WarmCostUnpaired, Cost: 69.0, Basis: WarmBasisFallback,
					Month: "2026-10",
				},
			},
		},
		{
			// 输入序即输出序：多动作混合（paired / beat / unpaired）各归各形。
			name: "多动作混合·输入序即输出序",
			entries: []map[string]any{
				wcHandoff("s1", "L1", base+100, 122424, 50.8),
				wcDock("s1", base+150.8, 376, 122048, 0, 3994),
				wcBeat("s1", "L1", base+300, 0, 0),
				wcHandoff("s2", "L2", base+1000, 50000, 50),
			},
			want: []WarmCost{
				{
					Lineage: "L1", TS: base + 100, Kind: "handoff", Lane: "same_model",
					Result: WarmCostPaired, Cost: 30.5932, Basis: WarmBasisActual,
					InputTokens:     376,
					CacheReadTokens: 122048,
					OutputTokens:    3994,
					Month:           "2026-10",
				},
				{
					Lineage: "L1", TS: base + 300, Kind: "beat", Lane: "qwatch",
					Result: WarmCostBeatDirect, Cost: 0, Basis: WarmBasisActual,
					Month: "2026-10",
				},
				{
					Lineage: "L2", TS: base + 1000, Kind: "handoff", Lane: "same_model",
					Result: WarmCostUnpaired, Cost: 34.5, Basis: WarmBasisFallback,
					Month: "2026-10",
				},
			},
		},
		{
			name:    "空输入",
			entries: nil,
			want:    nil,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bk := c.book
			if bk == nil {
				bk = book
			}
			assertWC(t, c.name, WarmCosts(c.entries, bk, c.cfg), c.want)
		})
	}
}

// ---- econBook 缺席：配对结果与标注照常透出，金额不可算记 0 不造数 ----
// （WarmEpisode.Savings 同款；单独函数与表内「用默认价书」区分开。）

func TestWarmCostNoEconBook(t *testing.T) {
	base := float64(time.Date(2026, 10, 2, 12, 0, 0, 0, time.Local).Unix())
	entries := []map[string]any{
		wcHandoff("s1", "L1", base+1000, 122424, 50.8),
		wcDock("s1", base+1050.8, 376, 122048, 0, 3994),
		wcHandoff("s2", "L2", base+2000, 50000, 50),
	}
	assertWC(t, "无价书", WarmCosts(entries, nil, WarmCostConfig{}), []WarmCost{
		{
			Lineage: "L1", TS: base + 1000, Kind: "handoff", Lane: "same_model",
			Result: WarmCostPaired, Cost: 0, Basis: WarmBasisActual,
			InputTokens:     376,
			CacheReadTokens: 122048,
			OutputTokens:    3994,
			Month:           "2026-10",
		},
		{
			Lineage: "L2", TS: base + 2000, Kind: "handoff", Lane: "same_model",
			Result: WarmCostUnpaired, Cost: 0, Basis: WarmBasisFallback,
			Month: "2026-10",
		},
	})
}

// ---- 默认参数（spec rev1 合同原文）与零值回落 ----

func TestWarmCostConfigDefaults(t *testing.T) {
	d := DefaultWarmCostConfig()
	want := WarmCostConfig{WindowBeforeS: 2, WindowAfterS: 90,
		SigTolAbs: 512, SigTolRel: 0.02, MaxOutTokens: 8192}
	if d != want {
		t.Fatalf("默认参数 = %+v, want %+v", d, want)
	}
	if z := (WarmCostConfig{}).normalized(); z != want {
		t.Fatalf("零值回落 = %+v, want %+v", z, want)
	}
	p := WarmCostConfig{WindowBeforeS: 5, WindowAfterS: 60,
		SigTolAbs: 256, SigTolRel: 0.05, MaxOutTokens: 4096}
	if g := p.normalized(); g != p { // 已配置值不被覆写
		t.Fatalf("normalized 覆写了已配置值: %+v", g)
	}
}
