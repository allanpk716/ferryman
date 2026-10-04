package report

// warm_episode_test.go — 票02：保温回合切分与归因双轴四象限（收入腿）表驱动单测。
// 覆盖验收清单：三终型、多跳一次结、TTL 两侧 need 翻转、80% 阈值两侧 hit 翻转、
// 前缀 fallback、多保温动作只结一次；另锁：bypass 不改回合、过期后再保温开新回合、
// 非保温 handoff 不切回合、双 lineage 不串、无锚动作丢弃、乱序输入稳定、
// 跨月归属与按回归行时刻取价、可配参数生效、econBook 缺席不造数。

import (
	"fmt"
	"testing"
	"time"

	"ferryman/internal/prices"
)

// weBool 测试侧 *bool 构造。
func weBool(v bool) *bool { return &v }

// weSamePtr *bool 三态比对（nil/值）。
func weSamePtr(a, b *bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// wePtr 测试侧 *float64 构造（价书 p_cache）。
func wePtr(f float64) *float64 { return &f }

// weGlmBook [prices.glm] 测试价书（per=10000、p_in 6.9 / p_cache 1.7）。
func weGlmBook() *prices.PriceBook {
	return &prices.PriceBook{Key: "glm", Unit: "智谱积分", Per: 10000,
		Versions: []prices.PriceVersion{
			{EffectiveFrom: "2026-09-01", PIn: 6.9, PCache: wePtr(1.7), POut: 24},
		}}
}

// ---- 测试行构造（字段形状对齐真实账本样例） ----

func weUsage(lid string, ts, in, out, cc, cr float64) map[string]any {
	return map[string]any{"kind": "usage", "ts": ts, "agent": "cc",
		"session_id": "s-" + lid, "lineage_id": lid, "model": "GLM-5.3",
		"input_tokens": in, "output_tokens": out,
		"cache_creation_tokens": cc, "cache_read_tokens": cr}
}

func weHandoff(lid string, ts, prompt float64) map[string]any {
	return map[string]any{"kind": "handoff", "ts": ts, "agent": "cc",
		"session_id": "s-" + lid, "lineage_id": lid, "lane": "same_model",
		"completion_tokens": 3994.0, "prompt_tokens": prompt,
		"provider": "智谱", "outcome": "fresh", "wall_s": 50.8}
}

// weFailHandoff 预派发失败行（prompt_tokens=0 / wall_s=0，真实样例形态）。
func weFailHandoff(lid string, ts float64) map[string]any {
	return map[string]any{"kind": "handoff", "ts": ts, "agent": "cc",
		"session_id": "s-" + lid, "lineage_id": lid, "lane": "same_model",
		"completion_tokens": 0.0, "prompt_tokens": 0.0,
		"provider": "智谱", "outcome": "failed", "wall_s": 0.0, "err": "snap"}
}

func weBeat(lid string, ts, prefix float64) map[string]any {
	return map[string]any{"kind": "beat", "ts": ts, "agent": "dsh",
		"session_id": "s-" + lid, "lineage_id": lid, "lane": "qwatch",
		"outcome": "observe", "prefix_tokens": prefix, "cost_actual": 0.0}
}

func weBlock(lid string, ts float64) map[string]any {
	return map[string]any{"kind": "block", "ts": ts, "lineage_id": lid,
		"prefix_tokens": 1.0, "idle_s": 2400.0}
}

func weBypass(lid string, ts float64) map[string]any {
	return map[string]any{"kind": "bypass", "ts": ts, "lineage_id": lid,
		"prefix_tokens": 1.0}
}

// assertWE 逐字段比对回合清单（浮点走绝对容差；Need/Hit 三态比对）。
func assertWE(t *testing.T, name string, got, want []WarmEpisode) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: 回合数 = %d, want %d\ngot: %+v", name, len(got), len(want), got)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Lineage != w.Lineage || g.EndKind != w.EndKind || g.Class != w.Class ||
			g.Month != w.Month || g.PrefixFallback != w.PrefixFallback || g.Hops != w.Hops {
			t.Fatalf("%s[%d]: 基本字段不齐\ngot:  %+v\nwant: %+v", name, i, g, w)
		}
		approxAbs(t, fmt.Sprintf("%s[%d].StartTS", name, i), g.StartTS, w.StartTS, 1e-6)
		approxAbs(t, fmt.Sprintf("%s[%d].EndTS", name, i), g.EndTS, w.EndTS, 1e-6)
		approxAbs(t, fmt.Sprintf("%s[%d].Prefix", name, i), g.Prefix, w.Prefix, 1e-6)
		approxAbs(t, fmt.Sprintf("%s[%d].Savings", name, i), g.Savings, w.Savings, 1e-6)
		if !weSamePtr(g.Need, w.Need) || !weSamePtr(g.Hit, w.Hit) {
			t.Fatalf("%s[%d]: Need/Hit = %+v/%+v, want %+v/%+v（nil=无回归不可判）",
				name, i, g.Need, g.Hit, w.Need, w.Hit)
		}
	}
}

func TestWarmEpisodes(t *testing.T) {
	base := float64(time.Date(2026, 10, 2, 12, 0, 0, 0, time.Local).Unix())
	book := weGlmBook() // sep30_2300 复用 report_test.go 的包级同名锚点

	cases := []struct {
		name    string
		entries []map[string]any
		cfg     WarmEpisodeConfig // 零值 = 默认参数
		book    *prices.PriceBook // nil = 用默认 glm 价书
		want    []WarmEpisode
	}{
		{
			name: "回归兑现·多跳一次结",
			entries: []map[string]any{
				weUsage("L1", base, 7141, 1712, 0, 145344),
				weHandoff("L1", base+100, 100000),
				weBeat("L1", base+1000, 99000),
				weBeat("L1", base+1500, 99000),
				weUsage("L1", base+1900, 1000, 500, 0, 95000),
			},
			want: []WarmEpisode{{
				Lineage: "L1", StartTS: base, EndKind: WarmEndRegression,
				EndTS: base + 1900, Prefix: 100000,
				Need: weBool(true), Hit: weBool(true),
				Class: WarmClassRealized, Savings: 52.0, // 10 万前缀×(6.9−1.7)/万
				Month: "2026-10", Hops: 3,
			}},
		},
		{
			name: "白保温无害·need假hit真",
			entries: []map[string]any{
				weUsage("L1", base, 7141, 1712, 0, 145344),
				weHandoff("L1", base+100, 100000),
				weUsage("L1", base+1000, 1000, 500, 0, 95000),
			},
			want: []WarmEpisode{{
				Lineage: "L1", StartTS: base, EndKind: WarmEndRegression,
				EndTS: base + 1000, Prefix: 100000,
				Need: weBool(false), Hit: weBool(true),
				Class: WarmClassBenign, Savings: 0, Month: "2026-10", Hops: 1,
			}},
		},
		{
			name: "亏损·hit假",
			entries: []map[string]any{
				weUsage("L1", base, 7141, 1712, 0, 145344),
				weHandoff("L1", base+100, 100000),
				weUsage("L1", base+1000, 1000, 500, 0, 79000),
			},
			want: []WarmEpisode{{
				Lineage: "L1", StartTS: base, EndKind: WarmEndRegression,
				EndTS: base + 1000, Prefix: 100000,
				Need: weBool(false), Hit: weBool(false),
				Class: WarmClassLoss, Savings: 0, Month: "2026-10", Hops: 1,
			}},
		},
		{
			name: "拦截终型·bypass不改变回合",
			entries: []map[string]any{
				weUsage("L1", base, 7141, 1712, 0, 145344),
				weHandoff("L1", base+100, 50000),
				weBlock("L1", base+500),
				weBypass("L1", base+600),
				weUsage("L1", base+700, 1000, 500, 0, 99000), // 强续后下一条 usage=新锚，非本回合回归
			},
			want: []WarmEpisode{{
				Lineage: "L1", StartTS: base, EndKind: WarmEndBlock,
				EndTS: base + 500, Prefix: 50000,
				Need: nil, Hit: nil, // 无回归，双轴不可判
				Class: WarmClassLoss, Savings: 0, Month: "", Hops: 1,
			}},
		},
		{
			name: "过期终型·数据末尾放任",
			entries: []map[string]any{
				weUsage("L1", base, 7141, 1712, 0, 145344),
				weBeat("L1", base+100, 51908),
			},
			want: []WarmEpisode{{
				Lineage: "L1", StartTS: base, EndKind: WarmEndExpire,
				EndTS: base + 100 + 1800, Prefix: 51908,
				Need: nil, Hit: nil,
				Class: WarmClassLoss, Savings: 0, Month: "", Hops: 1,
			}},
		},
		{
			name: "TTL下侧·need假",
			entries: []map[string]any{
				weUsage("L1", base, 7141, 1712, 0, 145344),
				weHandoff("L1", base+10, 100000),
				weUsage("L1", base+1799, 1000, 500, 0, 95000), // 距起点 1799 ≤ TTL
			},
			want: []WarmEpisode{{
				Lineage: "L1", StartTS: base, EndKind: WarmEndRegression,
				EndTS: base + 1799, Prefix: 100000,
				Need: weBool(false), Hit: weBool(true),
				Class: WarmClassBenign, Savings: 0, Month: "2026-10", Hops: 1,
			}},
		},
		{
			name: "TTL上侧·need真",
			entries: []map[string]any{
				weUsage("L1", base, 7141, 1712, 0, 145344),
				weHandoff("L1", base+1000, 100000),            // 末动作距回归 801s，未断链
				weUsage("L1", base+1801, 1000, 500, 0, 95000), // 距起点 1801 > TTL
			},
			want: []WarmEpisode{{
				Lineage: "L1", StartTS: base, EndKind: WarmEndRegression,
				EndTS: base + 1801, Prefix: 100000,
				Need: weBool(true), Hit: weBool(true),
				Class: WarmClassRealized, Savings: 52.0, Month: "2026-10", Hops: 1,
			}},
		},
		{
			name: "命中阈值上侧·hit真",
			entries: []map[string]any{
				weUsage("L1", base, 7141, 1712, 0, 145344),
				weHandoff("L1", base+100, 100000),
				weUsage("L1", base+1000, 1000, 500, 0, 81000), // ≥ 前缀×80%
			},
			want: []WarmEpisode{{
				Lineage: "L1", StartTS: base, EndKind: WarmEndRegression,
				EndTS: base + 1000, Prefix: 100000,
				Need: weBool(false), Hit: weBool(true),
				Class: WarmClassBenign, Savings: 0, Month: "2026-10", Hops: 1,
			}},
		},
		{
			name: "命中阈值下侧·hit假",
			entries: []map[string]any{
				weUsage("L1", base, 7141, 1712, 0, 145344),
				weHandoff("L1", base+100, 100000),
				weUsage("L1", base+1000, 1000, 500, 0, 79000), // < 前缀×80%
			},
			want: []WarmEpisode{{
				Lineage: "L1", StartTS: base, EndKind: WarmEndRegression,
				EndTS: base + 1000, Prefix: 100000,
				Need: weBool(false), Hit: weBool(false),
				Class: WarmClassLoss, Savings: 0, Month: "2026-10", Hops: 1,
			}},
		},
		{
			name: "前缀fallback·首动作是失败handoff",
			entries: []map[string]any{
				weUsage("L1", base, 7141, 1712, 0, 145344), // 四列和 154197
				weFailHandoff("L1", base+1700),
				weUsage("L1", base+1790, 1000, 500, 0, 145344),
			},
			want: []WarmEpisode{{
				Lineage: "L1", StartTS: base, EndKind: WarmEndRegression,
				EndTS: base + 1790, Prefix: 154197, PrefixFallback: true,
				Need: weBool(false), Hit: weBool(true), // 145344 ≥ 154197×80%
				Class: WarmClassBenign, Savings: 0, Month: "2026-10", Hops: 1,
			}},
		},
		{
			name: "前缀取首个动作·多保温动作只结一次",
			entries: []map[string]any{
				weUsage("L1", base, 7141, 1712, 0, 145344),
				weHandoff("L1", base+100, 120000),
				weHandoff("L1", base+900, 122000), // 第二跳前缀不覆盖首跳
				weUsage("L1", base+1900, 1000, 500, 0, 100000),
			},
			want: []WarmEpisode{{
				Lineage: "L1", StartTS: base, EndKind: WarmEndRegression,
				EndTS: base + 1900, Prefix: 120000,
				Need: weBool(true), Hit: weBool(true), // 100000 ≥ 120000×80%
				Class: WarmClassRealized, Savings: 62.4, // 12 万×(6.9−1.7)/万，一次结
				Month: "2026-10", Hops: 2,
			}},
		},
		{
			name: "动作间隔超TTL·过期后再保温开新回合",
			entries: []map[string]any{
				weUsage("L1", base, 7141, 1712, 0, 145344),
				weBeat("L1", base+100, 50000),
				weBeat("L1", base+2000, 50000), // 距上跳 1900s > TTL：上一回合已过期
				weUsage("L1", base+2100, 1000, 500, 0, 45000),
			},
			want: []WarmEpisode{
				{
					Lineage: "L1", StartTS: base, EndKind: WarmEndExpire,
					EndTS: base + 100 + 1800, Prefix: 50000,
					Need: nil, Hit: nil,
					Class: WarmClassLoss, Savings: 0, Month: "", Hops: 1,
				},
				{
					Lineage: "L1", StartTS: base, EndKind: WarmEndRegression, // 同一锚（其间无新 usage）
					EndTS: base + 2100, Prefix: 50000,
					Need: weBool(true), Hit: weBool(true),
					Class: WarmClassRealized, Savings: 26.0, Month: "2026-10", Hops: 1,
				},
			},
		},
		{
			name: "非保温handoff不切回合",
			entries: []map[string]any{
				weUsage("L1", base, 7141, 1712, 0, 145344),
				{"kind": "handoff", "ts": base + 100, "lineage_id": "L1",
					"provider": "local", "prompt_tokens": 2181.0, "wall_s": 17.8},
				{"kind": "handoff", "ts": base + 150, "lineage_id": "L1",
					"provider": "智谱", "lane": "cross_model", "prompt_tokens": 90000.0},
				weUsage("L1", base+300, 1000, 500, 0, 99000),
			},
			want: nil,
		},
		{
			name: "双lineage不串",
			entries: []map[string]any{
				weUsage("LA", base, 7141, 1712, 0, 145344),
				weUsage("LB", base+50, 1000, 500, 0, 60000),
				weBeat("LA", base+100, 40000),
				weBeat("LB", base+150, 60000),
			},
			want: []WarmEpisode{
				{
					Lineage: "LA", StartTS: base, EndKind: WarmEndExpire,
					EndTS: base + 100 + 1800, Prefix: 40000,
					Need: nil, Hit: nil, Class: WarmClassLoss, Savings: 0, Hops: 1,
				},
				{
					Lineage: "LB", StartTS: base + 50, EndKind: WarmEndExpire,
					EndTS: base + 150 + 1800, Prefix: 60000,
					Need: nil, Hit: nil, Class: WarmClassLoss, Savings: 0, Hops: 1,
				},
			},
		},
		{
			name: "无锚保温动作丢弃",
			entries: []map[string]any{
				weBeat("L1", base+100, 51908), // 该 lineage 无任何 usage 行
			},
			want: nil,
		},
		{
			name: "乱序输入·按ts稳定切分",
			entries: []map[string]any{
				weUsage("L1", base+1900, 1000, 500, 0, 95000),
				weBeat("L1", base+1500, 99000),
				weBeat("L1", base+1000, 99000),
				weHandoff("L1", base+100, 100000),
				weUsage("L1", base, 7141, 1712, 0, 145344),
			},
			want: []WarmEpisode{{
				Lineage: "L1", StartTS: base, EndKind: WarmEndRegression,
				EndTS: base + 1900, Prefix: 100000,
				Need: weBool(true), Hit: weBool(true),
				Class: WarmClassRealized, Savings: 52.0, Month: "2026-10", Hops: 3,
			}},
		},
		{
			name: "跨月归属·价书按回归行时刻取版本",
			entries: []map[string]any{
				weUsage("L1", sep30_2300, 7141, 1712, 0, 145344), // 锚：2026-09-30 23:00（本地）
				weHandoff("L1", sep30_2300+5*86400-300, 100000),
				weUsage("L1", sep30_2300+5*86400, 1000, 500, 0, 95000), // 回归：2026-10-05 23:00
			},
			// 双版本价书：回归时刻 ≥ 2026-10-05（UTC 零点生效）取新版本 (7.0−2.0)；
			// 若误按锚时刻（9-30）取旧版本会算出 52.0 而非 50.0。
			book: &prices.PriceBook{Key: "glm", Unit: "智谱积分", Per: 10000,
				Versions: []prices.PriceVersion{
					{EffectiveFrom: "2026-09-01", PIn: 6.9, PCache: wePtr(1.7), POut: 24},
					{EffectiveFrom: "2026-10-05", PIn: 7.0, PCache: wePtr(2.0), POut: 25},
				}},
			want: []WarmEpisode{{
				Lineage: "L1", StartTS: sep30_2300, EndKind: WarmEndRegression,
				EndTS: sep30_2300 + 5*86400, Prefix: 100000,
				Need: weBool(true), Hit: weBool(true),
				Class: WarmClassRealized, Savings: 50.0, // 10×(7.0−2.0)
				Month: "2026-10", Hops: 1,
			}},
		},
		{
			name: "可配参数·TTL与命中率生效",
			cfg:  WarmEpisodeConfig{TTLS: 600, HitRatio: 0.5},
			entries: []map[string]any{
				weUsage("L1", base, 7141, 1712, 0, 145344),
				weHandoff("L1", base+100, 100000),
				weUsage("L1", base+610, 1000, 500, 0, 55000), // 默认参数下 need/hit 均假
			},
			want: []WarmEpisode{{
				Lineage: "L1", StartTS: base, EndKind: WarmEndRegression,
				EndTS: base + 610, Prefix: 100000,
				Need: weBool(true), Hit: weBool(true), // 610>600；55000≥50000
				Class: WarmClassRealized, Savings: 52.0, Month: "2026-10", Hops: 1,
			}},
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
			assertWE(t, c.name, WarmEpisodes(c.entries, bk, c.cfg), c.want)
		})
	}
}

// ---- econBook 缺席：判类照常，兑现节省额不可算记 0（SavingsV1 同款不造数） ----

func TestWarmEpisodeNoEconBook(t *testing.T) {
	base := float64(time.Date(2026, 10, 2, 12, 0, 0, 0, time.Local).Unix())
	entries := []map[string]any{
		weUsage("L1", base, 7141, 1712, 0, 145344),
		weHandoff("L1", base+1000, 100000),
		weUsage("L1", base+1801, 1000, 500, 0, 95000),
	}
	assertWE(t, "无价书", WarmEpisodes(entries, nil, WarmEpisodeConfig{}),
		[]WarmEpisode{{
			Lineage: "L1", StartTS: base, EndKind: WarmEndRegression,
			EndTS: base + 1801, Prefix: 100000,
			Need: weBool(true), Hit: weBool(true),
			Class: WarmClassRealized, Savings: 0, Month: "2026-10", Hops: 1,
		}})
}

// ---- 默认参数（1800s / 0.80）与零值回落 ----

func TestWarmEpisodeConfigDefaults(t *testing.T) {
	d := DefaultWarmEpisodeConfig()
	if d.TTLS != 1800 || d.HitRatio != 0.80 {
		t.Fatalf("默认参数 = %+v, want {1800 0.8}", d)
	}
	if z := (WarmEpisodeConfig{}).normalized(); z != d {
		t.Fatalf("零值回落 = %+v, want %+v", z, d)
	}
	if p := d.normalized(); p != d { // 已配置值不被覆写
		t.Fatalf("normalized 覆写了已配置值: %+v", p)
	}
}
