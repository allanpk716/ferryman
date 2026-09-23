// same_model.go — 票05（同模型摆渡 · 参数供给）：按上游闭式推导同模型触发阈值。
//
// 决策依据：ADR-0015 决定二/决定四 + 夜链 D2/D9/D13；单位一律分钟（与
// [ferry.same_model] 配置一致；区别于心跳参数的秒制）。
//
// 公式（闭式，手算可复算；全部中间量入 SameModelResult）：
//
//	C     同模型单次摆渡成本（热缓存）= S/per·p_cache + OUT/per·p_out
//	T     参照成本（同一前缀全价读）= S/per·p_in
//	      （自家账本单本代理：这份前缀迟早要在某家按全价重读，取自家 p_in
//	        做保守参照——第三方摆渡的价格不在本计算器可见范围）
//	θ_cap 缓存安全点 = safety × median(TTL 观测)        ← TTL 分布定值
//	      （与心跳 τ = safety·TTL 同构，safety 复用 DefaultSafety=0.8：
//	        E0a 型分布下 0.8×中位 ≈ 冷启动种子 20min 的来历）
//	θ_econ 最早不亏点 = Q_idle(F(Sum) − D·(T−C)/C)      ← 间隔分布+价格定门槛
//	      D = 1 − F(Sum) = 闲置超过总结阈值的会话占比（≤Sum 计回归、>Sum 计死亡）
//	      推导：在 θ 触发的期望净收益 B(θ) = P_hot(θ)·[(T−C)·D − C·(F(Sum)−F(θ))]，
//	      P_hot 只缩放不改变符号，B(θ) ≥ 0 ⟺ F(θ) ≥ F(Sum) − D·(T−C)/C
//	θ_raw  = min(θ_cap, SummarizeMin)
//	建议值  = clamp(θ_raw, 10, SummarizeMin)（D10 护栏②：建议值只受 [10,总结] 钳）
//	可行性：θ_econ ≤ 建议值，否则拒算（任何合法触发点都净亏，不造数）
//	生效值  = clamp(θ_raw, 10, min(SummarizeMin, CeilingFor(上游)))（D13）
//
// 取值取向：θ_econ 只保证"不亏的最早点"，而期望收益 P_hot(θ)·B(θ) 在 E0a 型
// 分布下（回归流量 15 分钟内坍缩、缓存存活延伸到 25 分钟+）靠近安全带顶端取
// 最大——故取缓存安全点为值、经济点为可行性门槛；精确驻点无闭式，交扫参票
// 校准公式输入逼近，不旁路公式（红线）。
//
// 单源红线：本文件是同模型阈值公式全仓唯一实现，报表/面板/扫参/反跑一律
// 复用 DeriveSameModelForUpstream（建议值）与 SameModelEffectiveThreshold
// （生效值）出口，出现第二份实现即缺陷。
//
// 三层供给（D9）：无 TTL 观测 → 冷启动种子层（SameModelSeedMin，不拒算）；
// 有观测 → 本计算器闭式现算；扫参只校准输入。种子层同样检查 p_cache——
// 无缓存经济的上游连种子都不给（与既有"宁可不算不造数"红线一致）。
package policy

import (
	"fmt"
	"math"
	"slices"

	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/prices"
)

// SameModelError 同模型阈值拒算错误：Kind 稳定供调用方分支（doctor/面板），
// Msg 人话（带对拍值与修法指向）。
type SameModelError struct {
	Kind string
	Msg  string
}

func (e *SameModelError) Error() string { return e.Msg }

// 拒算分类（稳定字符串，doctor/面板按此分支）。
const (
	KindNoPriceBook = "no_price_book" // 上游→价格本映射失败（多本无映射/空表/显式键未命中）
	KindNoVersion   = "no_version"    // 命中的价格本无任何版本
	KindNoPCache    = "no_pcache"     // 现价缺 p_cache（无缓存经济红线）
	KindNoGap       = "no_gap"        // 全价读与摆渡成本无价差
	KindBadObs      = "bad_obs"       // 观测/输入脏值（缺失、非正）
	KindInfeasible  = "infeasible"    // 最早不亏点晚于可触发值（经济不可行）
	KindClampEmpty  = "clamp_empty"   // 钳位区间为空（配置矛盾，防御）
	KindBadMode     = "bad_mode"      // 未知调参档位（防御；config.Validate 应已拦截）
)

// SameModelObs 一次同模型阈值推导的观测输入（接口层形状；单位分钟/token）。
// 真实观测数据的装载（TTL 来自心跳/渡口遥测流水，闲置间隔来自 E0a 型分布
// + 账本 usage/handoff 流水）由反跑票（票06）供给，本票只定形状。
type SameModelObs struct {
	PrefixTokens float64   // S：代表性前缀规模（观测均值口径）
	OutTokens    float64   // 追加重放的叙事输出预留（token）
	TTLObsMin    []float64 // 该上游缓存 TTL 观测（分钟）；空 = 种子回退
	IdleObsMin   []float64 // 会话闲置间隔观测（分钟）；≤SummarizeMin 计回归
}

// SameModelResult 推导产物：生效/建议值 + 整条复算链（闭式可解释）。
type SameModelResult struct {
	Upstream     string  `json:"upstream"`       // 上游名（=[dock.upstreams] 条目键）
	Mode         string  `json:"mode"`           // manual|recommend|auto（manual=配置值生效）
	BookKey      string  `json:"book_key"`       // 实际命中的价格本键（manual 档为空）
	SeedFallback bool    `json:"seed_fallback"`  // 无 TTL 观测 → 冷启动种子层
	RawMin       float64 `json:"raw_min"`        // 钳位前：min(缓存安全点, 总结阈值) 或种子
	SuggestMin   float64 `json:"suggest_min"`    // 建议值：clamp(Raw, 10, 总结阈值)，不受配置上限钳（D10 护栏②）
	ThresholdMin float64 `json:"threshold_min"`  // 生效值：manual=配置值；rec/auto=clamp(Raw, 10, min(总结, CeilingFor))（D13；仅 SameModelEffectiveThreshold 落笔）
	MedianTTLMin float64 `json:"median_ttl_min"` // TTL 观测中位（种子路径 0）
	CacheSafeMin float64 `json:"cache_safe_min"` // 缓存安全点 0.8×中位（种子路径 0）
	EconFloorMin float64 `json:"econ_floor_min"` // 最早不亏点（种子路径 0）
	DeadFrac     float64 `json:"dead_frac"`      // D = 闲置>总结阈值 占比（种子路径 0）
	HitTargetQ   float64 `json:"hit_target_q"`   // q_target = F(Sum) − D·(T−C)/C（种子路径 0）
	FerryCost    float64 `json:"ferry_cost"`     // C（价格本货币单位；种子路径 0）
	RefCost      float64 `json:"ref_cost"`       // T（同上）
}

// DeriveSameModelThreshold 核心闭式推导（纯函数，分钟制）。summarizeMin 为
// 总结阈值（全局不变量上限）。拒算：缺 p_cache / 脏观测 / 无价差 / 经济
// 不可行 / 钳位区间空——各自带 Kind。
func DeriveSameModelThreshold(b prices.PriceBook, pv prices.PriceVersion, obs SameModelObs, summarizeMin float64) (SameModelResult, error) {
	res := SameModelResult{}
	if pv.PCache == nil {
		return res, &SameModelError{KindNoPCache, fmt.Sprintf(
			`"%s"@%s 无 p_cache，拒绝推导同模型阈值（无缓存经济红线：前缀按缓存读计价的前提不存在）`,
			b.Key, pv.EffectiveFrom)}
	}
	if summarizeMin <= 0 {
		return res, &SameModelError{KindBadObs, "总结阈值须 > 0（分钟）"}
	}
	if obs.PrefixTokens <= 0 {
		return res, &SameModelError{KindBadObs, "prefix_tokens 须 > 0（代表性前缀规模，由观测侧供给）"}
	}
	if obs.OutTokens <= 0 {
		return res, &SameModelError{KindBadObs, "out_tokens 须 > 0（追加重放的叙事输出预留）"}
	}
	// 钳位区间健全性：建议值要落 [下限, 总结阈值]，区间倒挂 = 配置已坏，防御拒算。
	if summarizeMin < config.SameModelMinCeilMin {
		return res, &SameModelError{KindClampEmpty, fmt.Sprintf(
			"钳位区间为空：总结阈值 %g 分钟 < 下限 %g 分钟", summarizeMin, config.SameModelMinCeilMin)}
	}

	// 第一层：无 TTL 观测 → 冷启动种子（D2/D9；p_cache 已验，种子不拒）。
	if len(obs.TTLObsMin) == 0 {
		res.SeedFallback = true
		res.RawMin = math.Min(config.SameModelSeedMin, summarizeMin)
		res.SuggestMin = SameModelSeedThreshold(summarizeMin)
		return res, nil
	}

	for _, v := range obs.TTLObsMin {
		if v <= 0 {
			return res, &SameModelError{KindBadObs, fmt.Sprintf(
				"TTL 观测含非正值（%g 分钟）：脏数据，拒绝推导", v)}
		}
	}
	if len(obs.IdleObsMin) == 0 {
		return res, &SameModelError{KindBadObs,
			"观测不完整：有 TTL 观测但无闲置间隔观测——无法评估提前触发会浪费多少次摆渡，拒绝现算（观测齐全前用冷启动种子）"}
	}

	// 成本（价格本货币单位，按 per 归一；与心跳公式同族但独立成式——见文件头）。
	c, tCost, costErr := SameModelCosts(b, pv, obs.PrefixTokens, obs.OutTokens)
	res.FerryCost, res.RefCost = c, tCost
	if costErr != nil {
		return res, costErr
	}
	gap := tCost - c

	// TTL 侧：缓存安全点。
	median := medianOf(obs.TTLObsMin)
	cacheSafe := DefaultSafety * median
	raw := math.Min(cacheSafe, summarizeMin)
	suggest := math.Max(config.SameModelMinCeilMin, math.Min(raw, summarizeMin))
	res.MedianTTLMin, res.CacheSafeMin = median, cacheSafe
	res.RawMin, res.SuggestMin = raw, suggest

	// 间隔+价格侧：最早不亏点（可行性门槛）。
	idle := slices.Clone(obs.IdleObsMin)
	slices.Sort(idle)
	fSum := ecdfLE(idle, summarizeMin) // F(Sum)
	d := 1 - fSum                      // D：闲置 > 总结阈值
	res.DeadFrac = d
	ratio := math.Inf(1)
	if c > 0 { // C=0（免费缓存+免费输出）→ 比值取 +Inf；显式分支防 0×Inf=NaN
		ratio = gap / c
	}
	q := fSum - d*ratio // q_target（可 <0 / >1，分位前钳回 [0,1]）
	q = math.Max(0, math.Min(1, q))
	res.HitTargetQ = q
	econ := quantileOf(idle, q)
	res.EconFloorMin = econ
	if econ > suggest {
		return res, &SameModelError{KindInfeasible, fmt.Sprintf(
			"拒绝推导同模型阈值：最早不亏点 %g 分钟晚于可触发值 %g 分钟——按当前闲置间隔分布与价格，"+
				"缓存还热时任何提前触发都净亏（价差收益盖不住多触发的摆渡浪费）。建议维持关闭，或等扫参给出新输入",
			econ, suggest)}
	}
	return res, nil
}

// SameModelCosts 单事件成本常数：C＝摆渡成本（热缓存，S/per·p_cache +
// OUT/per·p_out）、T＝参照成本（同一前缀全价读，S/per·p_in）——自
// DeriveSameModelThreshold 的成本段逐字抽出（2026-09-23 为修扫参逐事件
// 调用的平方级性能而开：算术仍只此一份，推导出口与扫参共用，红线不动）。
// 拒算口径与推导一致（p_cache 缺省 / prefix、out 非正 / 价差 ≤ 0）；
// no_gap 错误时 C/T 照样返回（推导同款——调用方可继续用成本记账）。
func SameModelCosts(b prices.PriceBook, pv prices.PriceVersion, prefixTokens, outTokens float64) (c, t float64, err error) {
	if pv.PCache == nil {
		return 0, 0, &SameModelError{KindNoPCache, fmt.Sprintf(
			`"%s"@%s 无 p_cache，拒绝推导同模型阈值（无缓存经济红线：前缀按缓存读计价的前提不存在）`,
			b.Key, pv.EffectiveFrom)}
	}
	if prefixTokens <= 0 {
		return 0, 0, &SameModelError{KindBadObs, "prefix_tokens 须 > 0（代表性前缀规模，由观测侧供给）"}
	}
	if outTokens <= 0 {
		return 0, 0, &SameModelError{KindBadObs, "out_tokens 须 > 0（追加重放的叙事输出预留）"}
	}
	per := float64(b.Per)
	if per <= 0 {
		per = 1
	}
	pcache := *pv.PCache
	c = prefixTokens/per*pcache + outTokens/per*pv.POut
	t = prefixTokens / per * pv.PIn
	if t-c <= 0 {
		return c, t, &SameModelError{KindNoGap, fmt.Sprintf(
			`"%s"@%s 价差 ≤ 0（全价读 T=%g，摆渡成本 C=%g）：同模型摆渡不比参照成本便宜，拒绝推导`,
			b.Key, pv.EffectiveFrom, t, c)}
	}
	return c, t, nil
}

// DeriveSameModelForUpstream 建议值出口（按上游分列，F4）：定位价格本 → 取
// 现价版本 → 核心推导。产物只到 SuggestMin（[10, 总结阈值] 钳位），不受
// 配置上限钳——上限只作用于生效值（SameModelEffectiveThreshold）。
//
// 映射口径（票01 评审备注钉死）：bookKey 非空 = 显式价格本键，必须精确命中
// （显式映射不给单本回退）；为空则上游名精确命中 → 仅一本回退该本 → 多本
// 拒算。配置暂无价格本键字段，字段落地后由调用方传入。
func DeriveSameModelForUpstream(books map[string]prices.PriceBook, upstream, bookKey string,
	obs SameModelObs, summarizeMin, nowS float64) (SameModelResult, error) {
	res := SameModelResult{}
	var book *prices.PriceBook
	if bookKey != "" {
		b, ok := books[bookKey]
		if !ok {
			keys := sortedBookKeys(books)
			return res, &SameModelError{KindNoPriceBook, fmt.Sprintf(
				`同模型阈值拒算：显式价格本键"%s"未命中 [prices.*]（现有 %d 本：%v）——`+
					"显式映射不给单本回退，请改对键或清空显式键", bookKey, len(books), keys)}
		}
		book = &b
	} else {
		book = prices.BookFor(books, upstream)
		if book == nil {
			if len(books) == 0 {
				return res, &SameModelError{KindNoPriceBook, fmt.Sprintf(
					`同模型阈值拒算：价格表未装配（[prices.*] 为空）——上游"%s"无从定价，`+
						"请先在 [prices.*] 登记该上游价格（p_cache 必填）", upstream)}
			}
			return res, &SameModelError{KindNoPriceBook, fmt.Sprintf(
				`同模型阈值拒算：上游"%s"无法定位价格本——[prices.*] 现有 %d 本（%v），`+
					"上游名与书键都无精确命中，且无显式价格本键映射。口径：优先显式键；"+
					"否则仅一本时回退该本；多本且无映射=拒算（不猜）", upstream, len(books), sortedBookKeys(books))}
		}
	}
	pv := prices.BookVersionAt(book, nowS)
	if pv == nil {
		return res, &SameModelError{KindNoVersion, fmt.Sprintf(
			`同模型阈值拒算：价格本"%s"没有任何生效版本（versions 为空）`, book.Key)}
	}
	res, err := DeriveSameModelThreshold(*book, *pv, obs, summarizeMin)
	if err != nil {
		return res, err
	}
	res.Upstream = upstream
	res.BookKey = book.Key
	return res, nil
}

// SameModelEffectiveThreshold 生效值出口（三态语义，D13；CeilingFor 为钳位
// 上限单源——票01 internal/config/same_model.go，本函数不另立第二份上限逻辑）：
//
//	manual        生效值 = CeilingFor(上游)（配置值即生效值，计算器不参与）
//	recommend/auto 生效值 = clamp(计算器 raw, 10, min(总结阈值, CeilingFor))
//
// 两种档位共用同一推导（谁来"应用"建议是调参票的事，本出口只给值）。
func SameModelEffectiveThreshold(c *config.Config, books map[string]prices.PriceBook,
	upstream string, obs SameModelObs) (SameModelResult, error) {
	switch mode := c.Tuning.Mode; mode {
	case "manual":
		ceil := c.SameModel.CeilingFor(upstream)
		return SameModelResult{
			Upstream:     upstream,
			Mode:         mode,
			RawMin:       ceil,
			ThresholdMin: ceil, // 配置值即生效值；SuggestMin 留 0（建议值另行调 DeriveSameModelForUpstream）
		}, nil
	case "recommend", "auto":
		sumMin := c.Thresholds.SummarizeS / 60.0
		res, err := DeriveSameModelForUpstream(books, upstream, "", obs, sumMin, clock.Now())
		if err != nil {
			return res, err
		}
		res.Mode = mode
		ceil := c.SameModel.CeilingFor(upstream)
		hi := math.Min(sumMin, ceil)
		if hi < config.SameModelMinCeilMin {
			return res, &SameModelError{KindClampEmpty, fmt.Sprintf(
				"钳位区间为空：配置上限 %g 分钟（CeilingFor）与总结阈值 %g 分钟的较小者低于下限 %g 分钟"+
					"——[ferry.same_model] 配置矛盾，请先修正（doctor same_model_clamp 同款检查）",
				ceil, sumMin, config.SameModelMinCeilMin)}
		}
		res.ThresholdMin = math.Max(config.SameModelMinCeilMin, math.Min(res.RawMin, hi))
		return res, nil
	default:
		return SameModelResult{}, &SameModelError{KindBadMode, fmt.Sprintf(
			"未知调参档位 %q（合法：manual/recommend/auto）——config.Validate 应已拦截，此处防御", mode)}
	}
}

// SameModelSeedThreshold 冷启动种子层取值（D9 三层供给第一层，单源）：
// clamp(min(SameModelSeedMin, 总结阈值), 钳位下限, 总结阈值)。种子路径的
// 生效值与运行侧现算失败的回落共用此出口——CeilingFor 是上限不是缺省，
// 终局修复后不再充当回落值。入参总结阈值须 ≥ 下限（前置钳位检查保证；
// 越界输入按下限夹取，防御不拒）。
func SameModelSeedThreshold(summarizeMin float64) float64 {
	raw := math.Min(config.SameModelSeedMin, summarizeMin)
	return math.Max(config.SameModelMinCeilMin, math.Min(raw, summarizeMin))
}

// medianOf 样本中位（不改动入参切片）。
func medianOf(xs []float64) float64 {
	s := slices.Clone(xs)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// ecdfLE 经验 CDF：xs 须升序；返回 #{v ≤ x}/n（≤ 计回归侧）。
func ecdfLE(xs []float64, x float64) float64 {
	le := 0
	for _, v := range xs {
		if v > x {
			break
		}
		le++
	}
	return float64(le) / float64(len(xs))
}

// quantileOf 经验分位（xs 须升序）：最小 xs[i] 使 (i+1)/n ≥ q。q 已由调用方
// 钳回 [0,1]；q≤0 → 首样本，q≥1 → 末样本。升序输入下闭式可手算。
func quantileOf(xs []float64, q float64) float64 {
	idx := int(math.Ceil(q*float64(len(xs)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx > len(xs)-1 {
		idx = len(xs) - 1
	}
	return xs[idx]
}

// sortedBookKeys 错误文案里的书键列表（排序保确定性）。
func sortedBookKeys(books map[string]prices.PriceBook) []string {
	keys := make([]string, 0, len(books))
	for k := range books {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
