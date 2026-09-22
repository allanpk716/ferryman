// 票06：同模型触发阈值网格评分——把扫参网格纳入同模型阈值，对历史闲置/摆渡
// 事件做三线对比（ADR-0015 决定四：「若当时阈值=t / 实际发生 / 什么都不做」）。
//
// 公式单源红线（policy/same_model.go）：
//   - 逐事件成本常数 C（摆渡成本）/T（参照全价）一律调 policy.DeriveSameModelThreshold
//     现算（版本按事件闲置起点取——金额版本化，等待窗引擎逐窗 Compute 同惯例）；
//   - 建议值一律调 policy.DeriveSameModelForUpstream（建议值出口）；
//   - 本包不出现第二份推导算术。评分模拟（触发判定/三线记账）是反跑保真逻辑，
//     与等待窗引擎的 simulateBeats 同一地位。
//
// 三线口径（每事件、价格逐事件版本化）：
//   - 若当时阈值=t：触发（t ≤ 闲置时长 且 t < 场景 TTL，字面口径同等待窗引擎）
//     即付 C；触发且死事件（闲置 > 总结阈值——交接叙事确会被用到）省 T；
//     触发而未死（会在总结阈值前回归）则 C 花费无兑现，单列浪费。
//   - 实际发生：历史事实——死事件中 handoff 行按 price_ver 钉死的真实摆渡支出
//     （装载层折算）；不可折算行如实单列。
//   - 什么都不做：死事件按版本化参照全价 T 合计（前缀迟早要在某家按全价重读——
//     公式的保守参照口径）。
//
// 数据局限（如实标注，优于伪造精度）：账本历史数据不含逐会话 TTL 观测，判热
// 用场景 TTL（字面口径），由调用方供给、缺省取种子隐含中位 25 分钟；截断事件
// 不入评分总体。
package backtest

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"

	"ferryman/internal/config"
	"ferryman/internal/mathx"
	"ferryman/internal/policy"
	"ferryman/internal/prices"
)

// smDefaultOutTokens 追加重放叙事输出预留（token）——金样本对拍口径
// （policy/same_model_test.go golden 同值）；调用方可经 SameModelSweepOptions
// 覆盖。
const smDefaultOutTokens = 1000.0

// smCalibIdleCap 建议投影携带的闲置样本封顶（最近 N 个）：防校准文件与调参
// 流水随事件总量线性膨胀——中位/分位在 64 样本下已稳定（终局修复：跨票
// 生效值链的校准载荷）。
const smCalibIdleCap = 64

// maxSMGridPoints 阈值网格防御上限（整数分钟步进，正常总结阈值 ≤ 60 → 51 档；
// 防病态输入死循环）。
const maxSMGridPoints = 1000

// SameModelSweepOptions 同模型阈值扫参输入。Books/Upstream/BookKey 的定位口径
// 与 DeriveSameModelForUpstream 逐字一致（显式键精确命中，否则 BookFor：
// 上游名精确命中 → 仅一本回退 → 多本拒算）。
type SameModelSweepOptions struct {
	Books    map[string]prices.PriceBook
	Upstream string // 评分上游（=[dock.upstreams] 条目键，价格本定位键）
	BookKey  string // 显式价格本键（空 = 按上游名定位）
	// 总结阈值（分钟；全局不变量上限，网格域右端点）。
	SummarizeMin float64
	// TTL 场景观测（分钟）——历史账本无此字段，为场景输入非历史观测（报告
	// 层如实标注）；空 = [种子隐含中位 25]（SameModelSeedMin/DefaultSafety）。
	TTLObsMin []float64
	// 追加重放叙事输出预留（token）；0 = smDefaultOutTokens。
	OutTokens float64
	// 现值（配置生效口径，CLI 层从 config 取 CeilingFor/ThresholdMin 传入）；
	// HasCurrent=false = 未供给（报告 diff 列占位）。
	CurrentThresholdMin float64
	HasCurrent          bool
	// 样本门槛（滚动窗内摆渡事件数下限，护栏④）；0 = config.TuningMinEvents。
	MinEvents int
}

// SameModelPoint 单阈值档的三线评分结果。
type SameModelPoint struct {
	TMin           float64 // 阈值档（分钟）
	Events         int     // 参与评分的事件数（可算总体）
	Fired          int     // 触发数（阈值时点仍闲置且判热）
	DeadSaved      int     // 触发且死事件数（叙事兑现）
	UselessWarm    int     // 触发而未死（回归前白触发，浪费 C）
	FerryCostTotal float64 // Σ C（触发事件摆渡成本，版本化）
	GrossSavings   float64 // Σ T（兑现事件避免的参照全价，版本化）
	NetSavings     float64 // 净节省 = GrossSavings − FerryCostTotal（圆整 4 位）
}

// SameModelSampleGate 样本门槛判定（护栏④：滚动窗摆渡事件 < MinEvents = 不足）。
type SameModelSampleGate struct {
	WindowDays  int
	MinEvents   int
	FerryEvents int
	Sufficient  bool
}

// SameModelSweepResult 扫参产物（markdown 报告唯一数据源；确定性输出）。
type SameModelSweepResult struct {
	Counts IdleLoadCounts // 装载计数（数据来源面）
	Sample SameModelSampleGate
	Grid   []SameModelPoint // 阈值升序网格（10..总结阈值 整数分钟）
	// Best 栅格最优（净节省 max，并列取更小 t——网格升序先到先得）。
	// 样本不足或网格空 = nil（不产出建议值，护栏④）。
	Best *SameModelPoint
	// Derived 建议值出口产物（DeriveSameModelForUpstream）；拒算 = nil，
	// DerivedErrKind 记分类（doctor/面板同款稳定字符串）。
	Derived        *policy.SameModelResult
	DerivedErrKind string
	// 三线之二（与 t 无关的历史面）：
	ActualFerryCost float64 // 实际发生：死事件 handoff 事实支出合计（可折算部分）
	ActualUnpriced  int     // 实际发生：不可折算 handoff 行数（照登不估算）
	DoNothingCost   float64 // 什么都不做：死事件参照全价 T 合计（版本化）
	// Uncomputable 评分层剔除分桶（kind → 事件数；no_pcache 等）。
	Uncomputable map[string]int
	// Warnings 上游级提示（no_gap/infeasible——成本仍可算、如实为负/不可行），
	// 首见序去重。
	Warnings []string
	// 现值（自 SameModelSweepOptions 拷入；报告 diff 列消费）。
	CurrentThresholdMin float64
	HasCurrent          bool
	// 建议投影观测（终局修复：跨票生效值链）——票07 校准输入源。
	// TTLObsEffMin = 本次扫参实际采用的 TTL 场景观测（缺省已归一为种子隐含
	// 中位 [25]，与内部同源：缺省扫参产出的建议不再携带空 TTL，apply 后
	// 运行时可现算）；IdleObsMin = 评分事件总体的闲置间隔样本（装载序，封顶
	// 最近 smCalibIdleCap 个；建议值 Derived 仍用全总体，此处为校准投影子集）。
	TTLObsEffMin []float64
	IdleObsMin   []float64
}

// SameModelSweep 同模型阈值网格评分主入口：同一事件集上逐阈值档三线记账。
// 全部循环按装载全序/网格升序固定，同输入两次运行逐字节一致。
func SameModelSweep(ds *IdleDataset, opts SameModelSweepOptions) (*SameModelSweepResult, error) {
	if opts.SummarizeMin <= 0 {
		return nil, errors.New("backtest: 总结阈值须 > 0（分钟）")
	}
	if opts.SummarizeMin < config.SameModelMinCeilMin {
		return nil, fmt.Errorf("backtest: 总结阈值 %g 分钟低于钳位下限 %g 分钟——同模型阈值网格域为空（[10, 总结阈值] 钳）",
			opts.SummarizeMin, config.SameModelMinCeilMin)
	}
	// 价格本定位（DeriveSameModelForUpstream 同口径，不另立映射规则）。
	var book *prices.PriceBook
	if opts.BookKey != "" {
		b, ok := opts.Books[opts.BookKey]
		if !ok {
			return nil, fmt.Errorf("backtest: 显式价格本键 %q 未命中 [prices.*]（显式映射不给单本回退）", opts.BookKey)
		}
		book = &b
	} else {
		book = prices.BookFor(opts.Books, opts.Upstream)
		if book == nil {
			return nil, fmt.Errorf("backtest: 上游 %q 无法定位价格本（[prices.*] 多本且无映射，或空表）——不猜", opts.Upstream)
		}
	}
	if len(book.Versions) == 0 {
		return nil, fmt.Errorf("backtest: 价格本 %q 无任何版本", book.Key)
	}
	ttlObs := opts.TTLObsMin
	if len(ttlObs) == 0 {
		ttlObs = []float64{config.SameModelSeedMin / policy.DefaultSafety} // 20/0.8 = 25（E0a 种子隐含中位）
	}
	for _, v := range ttlObs {
		if v <= 0 {
			return nil, errors.New("backtest: TTL 场景观测须全为正（分钟）——脏场景值拒算")
		}
	}
	out := opts.OutTokens
	if out <= 0 {
		out = smDefaultOutTokens
	}
	minEvents := opts.MinEvents
	if minEvents <= 0 {
		minEvents = config.TuningMinEvents
	}

	res := &SameModelSweepResult{Uncomputable: map[string]int{},
		CurrentThresholdMin: opts.CurrentThresholdMin, HasCurrent: opts.HasCurrent}
	res.TTLObsEffMin = append([]float64(nil), ttlObs...) // 缺省已归一，与内部同源
	if ds != nil {
		res.Counts = ds.Counts
	}
	res.Sample = SameModelSampleGate{
		WindowDays:  res.Counts.WindowDays,
		MinEvents:   minEvents,
		FerryEvents: res.Counts.FerryEventsInWindow,
		Sufficient:  res.Counts.FerryEventsInWindow >= minEvents,
	}

	// 评分总体：可算结局（returned/ferry）+ 前缀可定价；截断/零长/无前缀不入评
	//（截断的真实闲置只知下界——照登计数不入评，不造数）。
	type scored struct {
		ev   *IdleEvent
		c, t float64 // 逐事件版本化成本常数（公式出口）
	}
	var pool []scored
	var idles []float64
	if ds != nil {
		for i := range ds.Events {
			ev := &ds.Events[i]
			if ev.Outcome == IdleCensored || ev.PrefixTokens <= 0 || ev.IdleMin <= 0 {
				continue
			}
			pv := prices.BookVersionAt(book, ev.StartTS)
			if pv == nil || pv.PCache == nil {
				res.Uncomputable[policy.KindNoPCache]++ // 版本级缺 p_cache：无缓存经济红线
				continue
			}
			pool = append(pool, scored{ev: ev})
			idles = append(idles, ev.IdleMin)
		}
	}

	// 建议投影的闲置样本：装载序（StartTS 升序）封顶最近 smCalibIdleCap 个
	//（评分总体已完成，idles 不再变）。
	if n := len(idles); n > smCalibIdleCap {
		res.IdleObsMin = append([]float64(nil), idles[n-smCalibIdleCap:]...)
	} else {
		res.IdleObsMin = append([]float64(nil), idles...)
	}

	// 逐事件成本常数：调 policy.DeriveSameModelThreshold 现算（公式单源核心，
	// 版本按事件闲置起点取）。IdleObsMin 用全总体固定切片——事件在评分总体中
	// 的去留不改变分布输入，两次运行一致。infeasible/no_gap 的成本常数已随
	// 结果返回（gap≤0 时 C≥T，净节省如实为负——「不建议开启」的证据，照评）；
	// 其余拒算类别事件级剔除（前置校验下防御性路径，照登分桶）。
	warnSeen := map[string]bool{}
	noteWarn := func(err error) {
		kind := smErrKind(err)
		if kind != policy.KindInfeasible && kind != policy.KindNoGap {
			return
		}
		if !warnSeen[kind] {
			warnSeen[kind] = true
			res.Warnings = append(res.Warnings, kind+": "+err.Error())
		}
	}
	var active []scored
	for _, s := range pool {
		pv := prices.BookVersionAt(book, s.ev.StartTS)
		if pv == nil || pv.PCache == nil { // 防御：池构建后版本面不变，理论不可达
			res.Uncomputable[policy.KindNoPCache]++
			continue
		}
		d, err := policy.DeriveSameModelThreshold(*book, *pv, policy.SameModelObs{
			PrefixTokens: float64(s.ev.PrefixTokens), OutTokens: out,
			TTLObsMin: ttlObs, IdleObsMin: idles,
		}, opts.SummarizeMin)
		if err != nil {
			noteWarn(err)
			if kind := smErrKind(err); kind != policy.KindInfeasible && kind != policy.KindNoGap {
				res.Uncomputable[kind]++
				continue
			}
		}
		s.c, s.t = d.FerryCost, d.RefCost
		active = append(active, s)
	}

	// 场景 TTL（判热字面口径的缓存存活界）= 场景观测中位——统计量而非公式，
	// 本地小实现（推导本身仍在 policy，红线不动）。
	scenTTL := smMedian(ttlObs)

	// 阈值网格：[钳位下限, 总结阈值] 整数分钟升序（钳域即建议值合法域——
	// 扫参只改公式输入、不旁路公式）。无可算总体 = 空网格空推荐（宁可空表
	// 不造数，RunSweep 空集同惯例）。
	if len(active) > 0 {
		for i := 0; i < maxSMGridPoints; i++ {
			t := config.SameModelMinCeilMin + float64(i)
			if t > opts.SummarizeMin+1e-9 {
				break
			}
			pt := SameModelPoint{TMin: t, Events: len(active)}
			if t < scenTTL { // 字面口径：触发点不严格早于场景 TTL → 判冷，全体静默不触发
				for _, s := range active {
					if s.ev.IdleMin+1e-9 < t { // 会话在阈值前已回归
						continue
					}
					pt.Fired++
					pt.FerryCostTotal += s.c
					if s.ev.IdleMin > opts.SummarizeMin { // 死事件：叙事确会被用到，省参照全价
						pt.DeadSaved++
						pt.GrossSavings += s.t
					} else {
						pt.UselessWarm++ // 回归前白触发：花了 C 零兑现，单列不合并
					}
				}
			}
			pt.NetSavings = mathx.Round(pt.GrossSavings-pt.FerryCostTotal, 4)
			res.Grid = append(res.Grid, pt)
		}
	}

	// Best：样本门槛充足才产出（护栏④：不足不出建议值）；净节省 max，
	// 并列取更小 t（网格升序先到先得——确定性 tie-break）。
	if res.Sample.Sufficient {
		for i := range res.Grid {
			if res.Best == nil || res.Grid[i].NetSavings > res.Best.NetSavings {
				b := res.Grid[i]
				res.Best = &b
			}
		}
	}

	// 三线之二：历史面（与 t 无关）。实际发生 = 死事件中历史确有摆渡（ferry
	// 结局）的 handoff 事实支出合计（price_ver 不可折算行照登）；returned 的
	// 死事件历史上没有摆渡支出（晚归、无 handoff 行），不进实际线。
	// 什么都不做 = 死事件参照全价 T 合计（版本化）。
	for _, s := range active {
		if s.ev.IdleMin > opts.SummarizeMin {
			res.DoNothingCost += s.t
			if s.ev.Outcome == IdleFerry {
				if s.ev.ActualPriced {
					res.ActualFerryCost += s.ev.ActualFerryCost
				} else {
					res.ActualUnpriced++
				}
			}
		}
	}

	// 建议值出口（公式单源红线）：聚合观测走 DeriveSameModelForUpstream——
	// 代表前缀取可算总体均值，闲置分布取全总体，now 取数据末端（确定性，
	// 版本早于一切生效日回落末版——BookVersionAt 同口径）。
	agg := policy.SameModelObs{OutTokens: out, TTLObsMin: ttlObs, IdleObsMin: idles}
	if len(active) > 0 {
		sum := 0.0
		for _, s := range active {
			sum += float64(s.ev.PrefixTokens)
		}
		agg.PrefixTokens = math.Round(sum / float64(len(active)))
	}
	d, err := policy.DeriveSameModelForUpstream(opts.Books, opts.Upstream, opts.BookKey,
		agg, opts.SummarizeMin, res.Counts.HorizonTS)
	if err != nil {
		res.DerivedErrKind = smErrKind(err)
		noteWarn(err)
	} else {
		res.Derived = &d
	}
	return res, nil
}

// smErrKind 提取 SameModelError.Kind（非该类型 = 未知类别，照登不吞）。
func smErrKind(err error) string {
	var e *policy.SameModelError
	if errors.As(err, &e) {
		return e.Kind
	}
	return "unknown"
}

// smMedian 场景观测中位（只用于判热场景轴取值；入参拷贝后排序，不改调用方切片）。
func smMedian(xs []float64) float64 {
	s := slices.Clone(xs)
	sort.Float64s(s)
	n := len(s)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}
