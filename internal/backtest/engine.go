// 票02：逐窗模拟 + 评分 + tie-break（端到端：窗数据集 + 价格表 + config ttl_s
// → 每档参数组的净节省/跳数/成本/熔断与推荐）。
//
// 保真口径（票面）：
//   - TTL 场景三档作用在「缓存存活」判定上——跳时点 < 场景 TTL′ → hit，
//     ≥ → miss（字面口径；生产按 cache_read 占比分类，连续保温下晚跳多为
//     hit，故此口径的熔断触发数是保守上界，第二遍以 cost_actual 对账校准）。
//   - 等待上限到点停跳；首跳时机锚点决定第一跳。
//   - 熔断照生产 internal/beat.Breaker 语义（连续 miss ≥ 2 停跳）——直接
//     实例化 beat.Breaker（纯内存计数器，无 I/O），不另写连击逻辑（D8）。
//   - 摆渡/闸门/交接为外生背景，引擎不模拟（D11）；双计检查在报告层（票03）。
//
// 评分复用公式单源：单跳成本/避免重付金额调 internal/policy.Compute 的
// PerBeatCost/ExpireCost（逐窗按行时刻生效版本现算——版本化反事实，与
// report.StrategyTable 同惯例），无第二份价格算术。避免重付的**兑现门槛**
// 是引擎保真逻辑（本来会过期 且 保温到关窗）——成效账「只计真实兑现的避免」
// 的逐窗化；跳了没兑现的窗进 UselessWarm 单列不合并。
package backtest

import (
	"errors"
	"fmt"

	"ferryman/internal/beat"
	"ferryman/internal/mathx"
	"ferryman/internal/policy"
	"ferryman/internal/prices"
)

// SweepOptions 扫参输入。Books/EconKey 定经济价格表（load.resolveEconBook
// 同语义：key 命中取之，否则仅一本时取唯一本，再否则不可算）。
type SweepOptions struct {
	Books   map[string]prices.PriceBook
	EconKey string  // 空 = 仅一本时取唯一本
	TTLS    float64 // config [heartbeat] ttl_s（场景轴与主网格档位的基准值）
}

// maxSimBeats 单窗排跳防御上限（正常数据不可达：最小 τ = 0.5×0.8×TTL/2，
// 窗长小时级；防病态夹具死循环）。触发即停，确定性不受影响。
const maxSimBeats = 1_000_000

// RunSweep 网格 × TTL 场景轴重放评分主入口：产出 SweepResult（--json 载荷，
// 票03 消费）。全部切片按固定构造序产出（场景序 × 网格表序），窗序 = 装载
// 全序（OpenedTS 升序）——同输入两次运行逐字节一致。
//
// v1 评分在全部可重放窗（ds.Windows）上进行；对半留出集切片在 Dataset 供
// 报告层消费（SweepResult 契约无分半字段）。
func RunSweep(ds *Dataset, opts SweepOptions) (*SweepResult, error) {
	if opts.TTLS <= 0 {
		return nil, policy.ErrTTLUnset
	}
	book := resolveEconBook(opts.Books, opts.EconKey)
	if book == nil || len(book.Versions) == 0 {
		return nil, errors.New("backtest: 无可用品价格表（prices.* 为空，或 EconKey 未命中且非唯一本）")
	}
	refPV := refVersion(book)
	if refPV == nil {
		return nil, errors.New("backtest: 价格表无可解析生效日的版本")
	}
	res := &SweepResult{Dataset: ds.Counts}
	if len(ds.Windows) == 0 {
		return res, nil // 空集：空网格空推荐（宁可空表不造数）
	}
	prefix := refPrefixTokens(ds)
	mainGrid, err := BuildMainGrid(book, refPV, opts.TTLS, prefix)
	if err != nil {
		return nil, err
	}
	for _, sc := range scenariosOf(opts.TTLS) {
		for _, pt := range mainGrid {
			r, err := runScenario(sc, pt, ds.Windows, book)
			if err != nil {
				return nil, err
			}
			res.Scenarios = append(res.Scenarios, r)
		}
		bp, err := BuildBaseline(book, refPV, sc.TTLS, prefix)
		if err != nil {
			return nil, err
		}
		br, err := runScenario(sc, bp, ds.Windows, book)
		if err != nil {
			return nil, err
		}
		res.Baselines = append(res.Baselines, br)
		diag, err := BuildDiagnosticGrid(book, refPV, sc.TTLS, prefix)
		if err != nil {
			return nil, err
		}
		for _, pt := range diag {
			r, err := runScenario(sc, pt, ds.Windows, book)
			if err != nil {
				return nil, err
			}
			res.Diagnostic = append(res.Diagnostic, r) // 单列「诊断用，非可部署」
		}
	}
	// Best：主网格 config 场景档内择优（ttl 单点为主栏，场景轴是敏感性检查）；
	// 并列（净节省相等）按 CompareGridPoint 取参数向量字典序最小
	// （τ 升序 → 首跳升序 → cap 升序，spec tie-break）。诊断列永不入 Best。
	for i := range res.Scenarios {
		r := &res.Scenarios[i]
		if r.Scenario != ScenarioConfig {
			continue
		}
		if res.Best == nil || r.NetSavings > res.BestNet ||
			(r.NetSavings == res.BestNet && CompareGridPoint(r.Point, *res.Best) < 0) {
			pt := r.Point
			res.Best = &pt
			res.BestNet = r.NetSavings
		}
	}
	return res, nil
}

// runScenario 单场景 × 单参数组：全部重放窗逐一模拟并聚合。
// 浮点求和按装载全序累加，序固定 → 两次运行逐位一致。
func runScenario(sc scenario, pt GridPoint, windows []Window,
	book *prices.PriceBook) (ScenarioResult, error) {
	res := ScenarioResult{Scenario: sc.Name, TTLS: sc.TTLS, Point: pt, Windows: len(windows)}
	for _, w := range windows {
		pv := book.At(w.RowTS)
		if pv == nil {
			// 装载已保证可算桶行时刻有生效版本；兜底最新版本（StrategyTable 同惯例），
			// 理论不可达路径——宁可炸出不造数。
			pv = &book.Versions[len(book.Versions)-1]
		}
		// 金额常数逐窗版本化：perBeat = S/per·PCache + 300/per·POut，
		// expire = S/per·PIn——调 Compute 现算。ttl 只进 τ/cap（网格绝对值），
		// 此处传场景档仅为签名完整。
		pol, err := policy.Compute(*book, *pv, sc.TTLS, w.PrefixTokens,
			policy.DefaultBeatOutTokens, policy.DefaultSafety,
			policy.DefaultGraceS, policy.DefaultMinPrefixTokens)
		if err != nil {
			return ScenarioResult{}, fmt.Errorf("backtest: 窗 %s 金额常数不可算: %w", w.SessionID, err)
		}
		beats, trips, warm := simulateBeats(w.DurS, sc.TTLS, pt)
		res.Beats += beats
		res.BreakerTrips += trips
		res.BeatCost += float64(beats) * pol.PerBeatCost
		// 避免重付兑现门槛：本来会过期（dur > 场景 TTL′，即不作为侧要全价重付）
		// 且 保温到关窗（末跳覆盖到窗末——末跳后 TTL′ 内关窗，下次请求命中缓存）。
		// 金额 = ExpireCost（Compute 版本化常数）。
		if w.DurS > sc.TTLS && warm {
			res.GrossSavings += pol.ExpireCost
		} else if beats > 0 {
			// 无效保温单列：花了跳数零兑现——含「本来就不过期的白保温」
			// 与「没保温住（覆盖缺口）」两种，契约仅此一格，合并如实计数。
			res.UselessWarm++
		}
	}
	// 净节省 = Σ避免重付 − Σ心跳花费；圆整 4 位与成效账同精度惯例
	// （明细不圆整，净额圆整；同输入同浮点序列，确定性不受影响）。
	res.NetSavings = mathx.Round(res.GrossSavings-res.BeatCost, 4)
	return res, nil
}

// simulateBeats 单窗排跳：首跳 FirstBeatS、此后每 τ，停于 min(窗末 DurS, CapS)
// （时点比较带 1e-9 容差，policy.SimulateBeats 同惯例）。熔断照生产语义：
// 每窗一只新 beat.Breaker（触发后连击清零、下窗从头计——生产同水位），
// Record 返回 "demote"（连续 2 MISS，beat.MissLimit=2）即停跳并计一次触发；
// 包内动作字面量未导出，故此处以 "demote" 字符串对接。返回末跳时点供
// 保温兑现判定（warmAtEnd：整窗未过期，或末跳距关窗不足一个场景 TTL′）。
func simulateBeats(durS, scenTTLS float64, pt GridPoint) (beats, trips int, warmAtEnd bool) {
	horizon := durS
	if pt.CapS < horizon {
		horizon = pt.CapS
	}
	if pt.TauS <= 0 { // 防御：非正间隔不排跳（网格构造不可达）
		return 0, 0, durS <= scenTTLS
	}
	var br beat.Breaker // 生产熔断计数器（连续 miss≥2 停跳），每窗从零计
	hasBeat := false
	var last float64
	for t := pt.FirstBeatS; t <= horizon+1e-9; t += pt.TauS {
		beats++
		hasBeat = true
		last = t
		outcome := beat.OutMiss
		if t < scenTTLS { // 字面口径：跳时点 < 场景 TTL′ → hit，≥ → miss
			outcome = beat.OutHit
		}
		if br.Record(outcome) == "demote" {
			trips++
			break // 停跳：本窗剩余跳全部取消（末跳即本跳，计入覆盖判定）
		}
		if beats >= maxSimBeats {
			break
		}
	}
	if !hasBeat {
		return 0, 0, durS <= scenTTLS // 零跳：未过期即暖；本来会过期则冷（与不作为同局）
	}
	return beats, trips, durS-last < scenTTLS
}
