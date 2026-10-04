// savings_v2.go — 成效账 v2 组装（票04）：SavingsV1 之上追加保温盈亏节。
//
// 组装口径（spec .scratch/warm-attribution-v2/spec.md rev1；决策 D6/D7）：
//   - SavingsV2 = SavingsV1 输出逐字段不动（金测锁定，见 savings_v2_test.go）
//     ＋ formula 升 "v2" ＋ 追加 "warm" 保温盈亏节；v1 消费者零感知——daemon
//     cost_report 的 savings 段仍挂 SavingsV1 原样，warm 节单独透传（票04）。
//   - warm 节按月分桶（"months"→"YYYY-MM"，本地时区）：
//     收入腿（票02 回合）：兑现节省额/白保温计数/回归亏损计数/命中率/need
//     占比按回归所在月归属（记账合同）；拦截/过期回合无回归月（票02 Month
//     留空），亏损计数按回合终点月归属——收益侧本为 0，仅计数需要落桶。
//     成本腿（票03 动作行）：支出与亏损成本按动作发生月归属（记账合同）。
//   - 命中率 hit_ratio = 兑现回合数 / 回归回合数（票面口径「兑现/(兑现+白保温+
//     亏损)，有回归的回合为分母」——拦截/过期回合无回归不入分母；分母 0 记 0
//     ＝无样本，非零命中）；regression_count 即分母，随桶透出可复算。
//   - need_ratio = need=true 的回归回合占比（F6 sanity-check 用：¬need∧hit
//     占比 = benign_count/regression_count，对 TTL 取值留底）。
//   - 支出明细 spend 按保温动作类型拆分（spec 用户故事4：同模型重放/等待窗
//     心跳/问询心跳不被总论稀释）：键 = Kind:Lane（handoff:same_model /
//     beat:wait / beat:qwatch…；lane 空回落裸 Kind），每型给 count/amount 与
//     results 五分支（paired/ambiguous/zero_cost/unpaired/beat_direct）计数与
//     金额——ambiguous 金额单列不并入 paired（F7 暂停票05：ambiguous 成本=0
//     带 pending，此处如实单列披露）。
//   - 亏损成本 loss_cost = 亏损回合内保温动作的支出（同 lineage ∧ 动作 ts ∈
//     [回合起点, 终点)），按动作发生月归属；无回合可归的动作（如月切片边界
//     无锚残留 beat）只入 spend_total 不入 loss_cost。
//   - 净额 net = 兑现节省额 − spend_total（正＝保温赚）。
//
// 纯查询：不写任何账本行；SavingsV1 零触碰。
package report

import (
	"ferryman/internal/mathx"
	"ferryman/internal/prices"
)

// SavingsV2Formula 成效账 v2 公式章（v1 语义不动 + 保温两腿）。
const SavingsV2Formula = "v2"

// warmResultKeys 支出明细五分支结果键（WarmCost 结果常量同源）。
var warmResultKeys = []string{
	WarmCostPaired, WarmCostAmbiguous, WarmCostZeroCost,
	WarmCostUnpaired, WarmCostBeatDirect,
}

// warmCount 计数＋金额小行（spend 五分支与动作类型小计共用形状）。
type warmCount struct {
	count  int
	amount float64
}

func (c warmCount) toMap() map[string]any {
	return map[string]any{"count": c.count, "amount": mathx.Round(c.amount, 4)}
}

// warmSpend 单动作类型的支出行：小计＋五分支明细。
type warmSpend struct {
	warmCount
	results map[string]*warmCount
}

func newWarmSpend() *warmSpend {
	s := &warmSpend{results: map[string]*warmCount{}}
	for _, k := range warmResultKeys {
		s.results[k] = &warmCount{}
	}
	return s
}

func (s *warmSpend) add(amount float64, result string) {
	s.count++
	s.amount += amount
	r := s.results[result]
	if r == nil { // 未知结果值防御（WarmCost 结果常量之外不至出现）
		r = &warmCount{}
		s.results[result] = r
	}
	r.count++
	r.amount += amount
}

func (s *warmSpend) toMap() map[string]any {
	results := map[string]any{}
	for k, v := range s.results {
		results[k] = v.toMap()
	}
	return map[string]any{"count": s.count, "amount": mathx.Round(s.amount, 4),
		"results": results}
}

// warmMonth 单月保温盈亏桶（收入腿计数/金额＋成本腿支出明细）。
type warmMonth struct {
	realized, benign, loss    int
	regression, needTrue      int
	realizedSavings, lossCost float64
	spendTotal                float64
	spend                     map[string]*warmSpend
}

// SavingsV2 成效账 v2：SavingsV1 输出之上追加保温节并升公式章 "v2"。v1 段
// （lineages/totals/unpriced）逐字段不变（金测锁定）；epCfg/wcCfg 零值回落
// 两腿各自默认参数。纯函数。
func SavingsV2(entries []map[string]any, books map[string]prices.PriceBook,
	econBook *prices.PriceBook, epCfg WarmEpisodeConfig,
	wcCfg WarmCostConfig) map[string]any {
	out := SavingsV1(entries, books, econBook)
	out["formula"] = SavingsV2Formula
	out["warm"] = WarmSection(entries, econBook, epCfg, wcCfg)
	return out
}

// WarmSection 保温盈亏节（SavingsV2["warm"] 的同一构造；daemon cost_report
// 挂接复用——不重算 v1 段、结构逐字段同源）。纯函数。
func WarmSection(entries []map[string]any, econBook *prices.PriceBook,
	epCfg WarmEpisodeConfig, wcCfg WarmCostConfig) map[string]any {
	eps := WarmEpisodes(entries, econBook, epCfg)
	costs := WarmCosts(entries, econBook, wcCfg)

	months := map[string]*warmMonth{}
	row := func(m string) *warmMonth { // months.setdefault
		if r, ok := months[m]; ok {
			return r
		}
		r := &warmMonth{spend: map[string]*warmSpend{}}
		months[m] = r
		return r
	}
	spendRow := func(r *warmMonth, action string) *warmSpend {
		if s, ok := r.spend[action]; ok {
			return s
		}
		s := newWarmSpend()
		r.spend[action] = s
		return s
	}

	// 收入腿：回合按月落桶（回归月；拦截/过期按终点月——见文件头）。
	// byLineage 供成本腿亏损归属（同族系回合按时间序，含 [起点,终点) 判属）。
	byLineage := map[string][]WarmEpisode{}
	for _, ep := range eps {
		byLineage[ep.Lineage] = append(byLineage[ep.Lineage], ep)
		m := ep.Month
		if m == "" {
			m = warmMonthKey(ep.EndTS)
		}
		r := row(m)
		switch ep.Class {
		case WarmClassRealized:
			r.realized++
			r.realizedSavings += ep.Savings
		case WarmClassBenign:
			r.benign++
		default: // WarmClassLoss（¬hit 回归 + 拦截/过期）
			r.loss++
		}
		if ep.EndKind == WarmEndRegression { // hit/need 只在回归可判 → 分母
			r.regression++
			if ep.Need != nil && *ep.Need {
				r.needTrue++
			}
		}
	}

	// 成本腿：动作行按发生月落桶；亏损回合内动作（同 lineage ∧ ts ∈ [起点,
	// 终点)，族系内回合按时间序首中即归属）另计 loss_cost。
	for _, c := range costs {
		r := row(c.Month)
		r.spendTotal += c.Cost
		spendRow(r, warmActionKey(c)).add(c.Cost, c.Result)
		for _, ep := range byLineage[c.Lineage] {
			if ep.Class == WarmClassLoss && c.TS >= ep.StartTS && c.TS < ep.EndTS {
				r.lossCost += c.Cost
				break
			}
		}
	}

	out := map[string]any{}
	for m, r := range months {
		spend := map[string]any{}
		for k, s := range r.spend {
			spend[k] = s.toMap()
		}
		out[m] = map[string]any{
			"realized_count":   r.realized,
			"realized_savings": mathx.Round(r.realizedSavings, 4),
			"benign_count":     r.benign,
			"loss_count":       r.loss,
			"loss_cost":        mathx.Round(r.lossCost, 4),
			"regression_count": r.regression,
			"hit_ratio":        warmRatio(r.realized, r.regression),
			"need_ratio":       warmRatio(r.needTrue, r.regression),
			"spend_total":      mathx.Round(r.spendTotal, 4),
			"spend":            spend,
			"net":              mathx.Round(r.realizedSavings-r.spendTotal, 4),
		}
	}
	return map[string]any{"months": out}
}

// warmRatio num/den（den≤0 记 0：无样本，非零命中/零 need 占比）。
func warmRatio(num, den int) float64 {
	if den <= 0 {
		return 0
	}
	return mathx.Round(float64(num)/float64(den), 4)
}

// warmActionKey 保温动作类型键：Kind:Lane（handoff:same_model=同模型重放、
// beat:wait=等待窗心跳、beat:qwatch=问询心跳）；lane 空回落裸 Kind。
func warmActionKey(c WarmCost) string {
	if c.Lane == "" {
		return c.Kind
	}
	return c.Kind + ":" + c.Lane
}
