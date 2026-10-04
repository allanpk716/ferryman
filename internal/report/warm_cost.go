// warm_cost.go — 保温成本腿（支出腿，票03）：智谱档保温动作行 × 重放实付配对，
// 供 SavingsV2 组装保温支出明细。
//
// 记账合同逐条对齐（spec .scratch/warm-attribution-v2/spec.md rev1；决策 D4/D5）：
//   - 候选 dock 行：session_id 相等 ∧ dock.ts ∈ [handoff.ts−2, handoff.ts+wall_s+90]
//     （dock.ts = 记账盖章 ≈ 请求完成时刻，故窗右界按 wall_s 展开；端点含）。
//   - 命中签名：|dock.(input+cache_read) − handoff.prompt_tokens| ≤
//     max(512, 2%×prompt_tokens) ∧ dock.output ≤ 8192（阈值可配，WarmCostConfig）。
//   - 配对分支（签名命中数驱动，spec 字面「恰一候选命中 → paired」）：
//       零命中 → wall_s=0（预派发失败）zero_cost 成本 0；wall_s>0 → unpaired
//       回落价书推算（prompt_tokens×p_in/per）并标注「价书回落」；
//       恰一命中 → paired：成本 = 命中 dock 行四列 × econBook 实价（input 全价
//       p_in、cache_read p_cache、output p_out），按 dock 行时刻取价书版本，
//       实付四列随行透出；
//       ≥2 命中 → ambiguous：只计数披露，成本记 0 并带 pending=true——歧义
//       分支的成本处置是活动约束 F7（票05 落：与 unpaired 同处理价书推算），
//       本票不发明。
//   - 并发窗按命中数判（协调者裁定 2026-10-04，spec 字面与 F1 解除条件同向）：
//     窗内未命中签名的真实流量 dock 行（前缀已增长/输出超限）不与重放行混淆，
//     唯一命中行即可安全配对；若按窗内候选数判，会把这类窗错打 ambiguous 而
//     漏记实付（ambiguous 成本 F7 前为 0），净节省系统性偏乐观——恰是本票要
//     消灭的偏差方向。真实风险场景（同会话真实请求读了热缓存、前缀≈重放前缀
//     致双命中）在命中数口径下仍正确落 ambiguous。
//   - 预派发行（outcome=failed）先于候选扫描短路 zero_cost：未派发即无重放行，
//     窗内任何 dock 行皆无关流量（失败行 prompt_tokens=0 使签名容差退化成
//     512，照配会误记无关行实付——不配）。
//   - provider=local（本地 Qwen 档）与非 same_model 的 handoff 不是保温动作
//     （isWarmAction 同款判定，与票02 回合切分同口径），零成本不入本账（skip）。
//   - beat 行自带 cost_actual/cache_read，直取实付（当前等待窗零跳、qwatch
//     observe 零成本，仍按此口径支持将来），不走 dock 配对，标注「实收」。
//   - cache_creation 按输入全价 p_in 计（价书无独立写价列；票03口径，票05/评审
//     可改一行）。
//   - 价书缺席或该时刻无版本（paired 还要求 p_cache 在——缺则全价腿不可算，
//     部分计价会低估实付）：照 WarmEpisode.Savings 同款不造数——成本记 0，
//     配对结果与标注照常透出（结果=事实，金额=可算才记）。
//   - 跨月归属：保温动作成本计入发生月（Month = 动作行本地月键）。
//
// 纯函数：不读盘、不写账本行、SavingsV1 零触碰；输入序即输出序（确定性）。

package report

import (
	"math"
	"time"

	"ferryman/internal/mathx"
	"ferryman/internal/prices"
)

// 配对结果取值（报表拆分键，英文稳定）与口径标注（中文报表词汇）。
const (
	WarmCostPaired     = "paired"      // 恰一候选命中：dock 四列实付入账
	WarmCostAmbiguous  = "ambiguous"   // 多候选/并发窗污染：计数披露，成本处置 F7（票05）
	WarmCostZeroCost   = "zero_cost"   // 预派发失败/窗内无重放行：零成本
	WarmCostUnpaired   = "unpaired"    // 有派发无重放行：回落价书推算并标注
	WarmCostBeatDirect = "beat_direct" // beat 自带实付直取（非配对合同产物）

	WarmBasisActual   = "实收"     // dock 四列 / beat cost_actual 实付口径
	WarmBasisFallback = "价书回落" // unpaired 价书推算口径
)

// 配对合同默认常数（spec rev1 原文；可配性属实现细节）。
const (
	// WarmDefaultWindowBeforeS 候选窗左界（handoff.ts−2）。
	WarmDefaultWindowBeforeS = 2.0
	// WarmDefaultWindowAfterS 候选窗右界加数（handoff.ts+wall_s+90）。
	WarmDefaultWindowAfterS = 90.0
	// WarmDefaultSigTolAbs 签名绝对容差（token 数）。
	WarmDefaultSigTolAbs = 512.0
	// WarmDefaultSigTolRel 签名相对容差（×prompt_tokens）。
	WarmDefaultSigTolRel = 0.02
	// WarmDefaultMaxOutTokens dock.output_tokens 上限。
	WarmDefaultMaxOutTokens = 8192.0
)

// WarmCostConfig 配对合同可配参数；零值回落默认（DefaultWarmCostConfig）。
type WarmCostConfig struct {
	WindowBeforeS float64 // 候选窗左界秒数
	WindowAfterS  float64 // 候选窗右界加数秒数（wall_s 之外）
	SigTolAbs     float64 // 签名绝对容差
	SigTolRel     float64 // 签名相对容差
	MaxOutTokens  float64 // dock.output_tokens 上限
}

// DefaultWarmCostConfig 默认参数（spec rev1 合同原文：−2/+90 窗、512/2% 容差、
// 8192 output 上限）。
func DefaultWarmCostConfig() WarmCostConfig {
	return WarmCostConfig{
		WindowBeforeS: WarmDefaultWindowBeforeS,
		WindowAfterS:  WarmDefaultWindowAfterS,
		SigTolAbs:     WarmDefaultSigTolAbs,
		SigTolRel:     WarmDefaultSigTolRel,
		MaxOutTokens:  WarmDefaultMaxOutTokens,
	}
}

// normalized 非正值回落默认（零值结构体即默认配置）。
func (c WarmCostConfig) normalized() WarmCostConfig {
	if c.WindowBeforeS <= 0 {
		c.WindowBeforeS = WarmDefaultWindowBeforeS
	}
	if c.WindowAfterS <= 0 {
		c.WindowAfterS = WarmDefaultWindowAfterS
	}
	if c.SigTolAbs <= 0 {
		c.SigTolAbs = WarmDefaultSigTolAbs
	}
	if c.SigTolRel <= 0 {
		c.SigTolRel = WarmDefaultSigTolRel
	}
	if c.MaxOutTokens <= 0 {
		c.MaxOutTokens = WarmDefaultMaxOutTokens
	}
	return c
}

// WarmCost 一个保温动作行的成本配对结果（支出腿，供 SavingsV2 组装）。
type WarmCost struct {
	Lineage string  // 族系键（lineageKeyOf 同款）
	TS      float64 // 保温动作行 ts（发生时刻，月归属基准）
	Kind    string  // 动作类型："handoff"（同模型重放）/ "beat"（心跳）
	Lane    string  // lane 原值（same_model / qwatch / wait…），报表按动作类型拆分用
	Result  string  // 配对结果：WarmCostPaired / Ambiguous / ZeroCost / Unpaired / BeatDirect
	Cost    float64 // 成本额（econBook 单位；mathx.Round 四位；不可算/悬置记 0）
	Basis   string  // 口径标注：「实收」/「价书回落」；无成本口径留空
	Pending bool    // true = 成本处置悬置（ambiguous 分支；活动约束 F7，票05 落）
	// paired 实付四列（dock 行原值透出；beat 直取行仅 cache_read 有值）。
	InputTokens         float64
	CacheReadTokens     float64
	CacheCreationTokens float64
	OutputTokens        float64
	Month               string // 发生月键 "YYYY-MM"（保温动作成本计入发生月）
}

// WarmCosts 从已解析账本行产出每条保温动作行的成本配对结果。行形态与
// SavingsV1 同入口（[]map[string]any）；输出按输入序（保温动作行出现序），
// 确定纯函数。
func WarmCosts(entries []map[string]any, econBook *prices.PriceBook,
	cfg WarmCostConfig) []WarmCost {
	cfg = cfg.normalized()
	docks := map[string][]map[string]any{} // session_id → dock 行（匹配键）
	for _, e := range entries {
		if strOr(e, "kind") == "dock" {
			sid := strOr(e, "session_id")
			docks[sid] = append(docks[sid], e)
		}
	}
	var out []WarmCost
	for _, e := range entries {
		if !isWarmAction(e) {
			continue // provider=local / 非 same_model handoff / 其他行：skip 零成本
		}
		if strOr(e, "kind") == "beat" {
			out = append(out, warmBeatCost(e))
			continue
		}
		out = append(out, warmHandoffCost(e, docks[strOr(e, "session_id")],
			econBook, cfg))
	}
	return out
}

// warmHandoffCost 单条智谱档同模型 handoff 行的配对计价（合同分支见文件头）。
func warmHandoffCost(h map[string]any, cands []map[string]any,
	econBook *prices.PriceBook, cfg WarmCostConfig) WarmCost {
	row := WarmCost{
		Lineage: lineageKeyOf(h),
		TS:      numOr(h, "ts"),
		Kind:    "handoff",
		Lane:    strOr(h, "lane"),
		Month:   warmMonthKey(numOr(h, "ts")),
	}
	// 预派发失败短路：未派发即无重放行，窗内杂行不参与（见文件头）。
	if strOr(h, "outcome") == "failed" {
		row.Result = WarmCostZeroCost
		return row
	}
	ts, wall := numOr(h, "ts"), numOr(h, "wall_s")
	prompt := numOr(h, "prompt_tokens")
	lo, hi := ts-cfg.WindowBeforeS, ts+wall+cfg.WindowAfterS
	var inWin []map[string]any
	for _, d := range cands {
		if dt := numOr(d, "ts"); dt >= lo && dt <= hi {
			inWin = append(inWin, d)
		}
	}
	switch {
	case len(inWin) == 0: // 窗内零候选
		if wall == 0 {
			row.Result = WarmCostZeroCost
			return row
		}
		return warmFallbackCost(row, prompt, econBook, ts)
	default: // 签名命中数驱动（spec 字面；见文件头并发窗裁定）
		hits := 0
		hit := map[string]any(nil)
		for _, d := range inWin {
			if warmSigHit(d, prompt, cfg) {
				hits++
				hit = d
			}
		}
		switch {
		case hits == 0: // 全未命中：重放行缺失 → wall_s 判零成本/回落
			if wall == 0 {
				row.Result = WarmCostZeroCost
				return row
			}
			return warmFallbackCost(row, prompt, econBook, ts)
		case hits == 1: // 恰一命中：实付配对（未命中行不混淆，见文件头裁定）
			return warmPairedCost(row, hit, econBook)
		default: // ≥2 命中：无法唯一指认重放行 → ambiguous（F7：票05 落成本处置）
			row.Result = WarmCostAmbiguous
			row.Pending = true
			return row
		}
	}
}

// warmSigHit 命中签名：|in+cache_read − prompt| ≤ max(abs, rel×prompt)
// ∧ output ≤ 上限。
func warmSigHit(d map[string]any, prompt float64, cfg WarmCostConfig) bool {
	tok := numOr(d, "input_tokens") + numOr(d, "cache_read_tokens")
	tol := cfg.SigTolAbs
	if rel := cfg.SigTolRel * prompt; rel > tol {
		tol = rel
	}
	if math.Abs(tok-prompt) > tol {
		return false
	}
	return numOr(d, "output_tokens") <= cfg.MaxOutTokens
}

// warmPairedCost 恰一命中候选的实付计价：四列 × econBook 实价，按 dock 行
// 时刻取价书版本；价书/版本/p_cache 缺 → 成本记 0 不造数（结果与四列照透）。
func warmPairedCost(row WarmCost, d map[string]any, econBook *prices.PriceBook) WarmCost {
	row.Result = WarmCostPaired
	row.Basis = WarmBasisActual
	row.InputTokens = numOr(d, "input_tokens")
	row.CacheReadTokens = numOr(d, "cache_read_tokens")
	row.CacheCreationTokens = numOr(d, "cache_creation_tokens")
	row.OutputTokens = numOr(d, "output_tokens")
	if econBook != nil {
		if pv := econBook.At(numOr(d, "ts")); pv != nil && pv.PCache != nil {
			per := float64(econBook.Per)
			row.Cost = mathx.Round(
				(row.InputTokens+row.CacheCreationTokens)*pv.PIn/per+
					row.CacheReadTokens**pv.PCache/per+
					row.OutputTokens*pv.POut/per, 4)
		}
	}
	return row
}

// warmFallbackCost unpaired 回落：prompt_tokens×p_in/per，按动作行时刻取版本
// （成本归发生方）；价书/版本缺 → 成本记 0 不造数，标注照常。
func warmFallbackCost(row WarmCost, prompt float64,
	econBook *prices.PriceBook, ts float64) WarmCost {
	row.Result = WarmCostUnpaired
	row.Basis = WarmBasisFallback
	if econBook != nil {
		if pv := econBook.At(ts); pv != nil {
			row.Cost = mathx.Round(prompt/float64(econBook.Per)*pv.PIn, 4)
		}
	}
	return row
}

// warmBeatCost beat 行直取自带实付（cost_actual/cache_read），不走 dock 配对。
func warmBeatCost(b map[string]any) WarmCost {
	return WarmCost{
		Lineage:         lineageKeyOf(b),
		TS:              numOr(b, "ts"),
		Kind:            "beat",
		Lane:            strOr(b, "lane"),
		Result:          WarmCostBeatDirect,
		Cost:            numOr(b, "cost_actual"),
		Basis:           WarmBasisActual,
		CacheReadTokens: numOr(b, "cache_read"),
		Month:           warmMonthKey(numOr(b, "ts")),
	}
}

// warmMonthKey 本地时区月键（WarmEpisode.Month 同款格式）。
func warmMonthKey(ts float64) string {
	return time.Unix(int64(ts), 0).Local().Format("2006-01")
}
