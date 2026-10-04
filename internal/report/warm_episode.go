// warm_episode.go — 保温回合切分与归因双轴四象限（票02，收入腿）。
//
// 记帐合同逐条对齐（spec .scratch/warm-attribution-v2/spec.md rev1；决策 D2/D3）：
//   - 回合起点 = 该 lineage 末次真实 usage 行 ts（真实写入；重放结构性不落 usage
//     行，锚点干净）；保温动作行（handoff provider=智谱 且 lane=same_model、以及
//     kind=beat）落在 [起点, 终点) 内即属该回合。
//   - 终点三型（依时序先到先收口）：回归 = 起点后该 lineage 下一条 usage 行；
//     拦截 = block 行；过期 = 最后一个保温动作后自然 TTL 内无新事件（新事件 =
//     usage / block / 保温动作），过期时刻 = 末动作 ts + TTL。
//   - 回合前缀 = 回合内首个保温动作行 prefix（handoff.prompt_tokens /
//     beat.prefix_tokens）；不可得（缺字段或 ≤0——预派发失败行 prompt_tokens=0
//     即此形态）回落锚 usage 行四列之和并标 PrefixFallback。
//   - need = (回归ts − 起点) > TTL；hit = 回归首发 usage 行 cache_read_tokens
//     ≥ 前缀×HitRatio。判类：need∧hit→兑现（节省 = 前缀×(p_in−p_cache)/per，
//     econBook 按回归行时刻取版本，SavingsV1 同款传递）；¬need∧hit→白保温无害
//     （不计省）；¬hit→亏损。
//   - 拦截/过期回合无回归可判双轴：Need/Hit 留 nil，判类记亏损（CONTEXT.md
//     「无效保温」：回合内会话未回归、花费未兑现任何节省的亏损事件）。
//   - 跨月归属：回合收益计入回归所在月（Month=回归月键；非回归回合无收益留空）；
//     成本归发生方（票03/04 接，本模块只出回合清单）。
//   - 多跳回合收益一次结（一个回合一条输出，天然不逐跳重复）；bypass 行不参与
//     切分（用户强续后下一条 usage 即新回归锚点——本就由通用规则覆盖）。
//
// 纯函数：不读盘、不写账本行、SavingsV1 零触碰。

package report

import (
	"sort"
	"time"

	"ferryman/internal/mathx"
	"ferryman/internal/prices"
)

// 保温回合公式常数（D2：TTL 1800s 实测、hit 80%；可配性属实现细节）。
const (
	// WarmDefaultTTLS 自然 TTL（秒）：need 判据与过期终型共用。
	WarmDefaultTTLS = 1800.0
	// WarmDefaultHitRatio hit 判据的回合前缀占比阈值。
	WarmDefaultHitRatio = 0.80
	// warmProvider / warmLane 同模型保温动作的 handoff 判定值（记账合同原文）。
	warmProvider = "智谱"
	warmLane     = "same_model"
)

// 终型（EndKind）与判类（Class）取值，中文对齐报表词汇。
const (
	WarmEndRegression = "回归"
	WarmEndBlock      = "拦截"
	WarmEndExpire     = "过期"

	WarmClassRealized = "兑现"
	WarmClassBenign   = "白保温无害"
	WarmClassLoss     = "亏损"
)

// WarmEpisodeConfig 回合切分可配参数；零值回落默认（1800s / 0.80）。
type WarmEpisodeConfig struct {
	TTLS     float64 // 自然 TTL（秒）
	HitRatio float64 // hit 判据的前缀占比阈值
}

// DefaultWarmEpisodeConfig 默认参数（价书实测 TTL 1800s、hit 阈值 80%）。
func DefaultWarmEpisodeConfig() WarmEpisodeConfig {
	return WarmEpisodeConfig{TTLS: WarmDefaultTTLS, HitRatio: WarmDefaultHitRatio}
}

// normalized 非正值回落默认（零值结构体即默认配置）。
func (c WarmEpisodeConfig) normalized() WarmEpisodeConfig {
	if c.TTLS <= 0 {
		c.TTLS = WarmDefaultTTLS
	}
	if c.HitRatio <= 0 {
		c.HitRatio = WarmDefaultHitRatio
	}
	return c
}

// WarmEpisode 一个保温回合（结算单元，D3）：末次真实写入起，到回归/拦截/放任过期止。
type WarmEpisode struct {
	Lineage        string  // 族系键（lineageKeyOf 同款：lineage_id 空回落 session_id）
	StartTS        float64 // 回合起点 = 锚定 usage 行 ts（末次真实写入）
	EndKind        string  // 终型：WarmEndRegression / WarmEndBlock / WarmEndExpire
	EndTS          float64 // 终点 ts（回归/拦截 = 事件行 ts；过期 = 末保温动作 ts+TTL）
	Prefix         float64 // 回合前缀（回合内首个保温动作行 prefix；fallback = 锚 usage 四列和）
	PrefixFallback bool    // true = 前缀走了 usage 四列回落
	Need           *bool   // 归因双轴 need：回归距起点 > TTL；拦截/过期无回归 → nil
	Hit            *bool   // 归因双轴 hit：回归首发 cache_read ≥ 前缀×HitRatio；拦截/过期 → nil
	Class          string  // 判类：兑现 / 白保温无害 / 亏损（拦截/过期 = 无效保温记亏损）
	Savings        float64 // 兑现节省额（仅兑现类；价书缺席或无 p_cache 记 0 不造数）
	Month          string  // 归属月键 "YYYY-MM"（回归所在月，本地时区；非回归回合留空）
	Hops           int     // 回合内保温动作数（多跳一次结；计数供报表按动作类型拆分）
}

// WarmEpisodes 从已解析账本行切出全部保温回合并按双轴判类，供 SavingsV2 组装。
// 行形态与 SavingsV1 同入口（[]map[string]any）；输出按 lineage 首现序、
// 回合时间升序，确定纯函数。
func WarmEpisodes(entries []map[string]any, econBook *prices.PriceBook,
	cfg WarmEpisodeConfig) []WarmEpisode {
	cfg = cfg.normalized()
	var order []string // Python dict 插入序的 Go 形：lineage 首现序
	buckets := map[string][]map[string]any{}
	for _, e := range entries {
		lid := lineageKeyOf(e)
		if _, ok := buckets[lid]; !ok {
			order = append(order, lid)
		}
		buckets[lid] = append(buckets[lid], e)
	}
	var eps []WarmEpisode
	for _, lid := range order {
		rows := buckets[lid]
		// 账本本就追加序 = 时间序；排序兜底乱序输入（同 ts 保持原相对序）。
		sort.SliceStable(rows, func(i, j int) bool {
			return numOr(rows[i], "ts") < numOr(rows[j], "ts")
		})
		eps = append(eps, walkLineage(lid, rows, econBook, cfg)...)
	}
	return eps
}

// walkLineage 单 lineage 内切回合：锚 = 最近一条 usage 行；保温动作挂当前锚；
// usage/block 依时序收口（回归/拦截）；动作或事件距末动作超 TTL 先收口过期
// （过期后同锚可再开新回合——其间无新 usage 则起点不变，属合同正常形态）。
func walkLineage(lid string, rows []map[string]any, econBook *prices.PriceBook,
	cfg WarmEpisodeConfig) []WarmEpisode {
	var (
		eps     []WarmEpisode
		anchor  map[string]any   // 锚 = 末次真实 usage 行（nil = 该族系尚无真实写入）
		pending []map[string]any // 当前未收口回合的保温动作（ts 升序追加）
	)
	// expireIfStale 直至 untilTS 无新事件 → 末动作后已过 TTL 的回合先收口过期。
	expireIfStale := func(untilTS float64) {
		if anchor == nil || len(pending) == 0 {
			return
		}
		if last := numOr(pending[len(pending)-1], "ts"); untilTS > last+cfg.TTLS {
			eps = append(eps, closeEpisode(lid, anchor, pending, WarmEndExpire,
				last+cfg.TTLS, nil, econBook, cfg))
			pending = nil
		}
	}
	for _, e := range rows {
		switch strOr(e, "kind") {
		case "usage":
			expireIfStale(numOr(e, "ts")) // 回归晚于过期时刻 → 回合先过期、本行转新锚
			if len(pending) > 0 {
				eps = append(eps, closeEpisode(lid, anchor, pending, WarmEndRegression,
					numOr(e, "ts"), e, econBook, cfg))
			}
			anchor, pending = e, nil
		case "block":
			expireIfStale(numOr(e, "ts")) // 拦截晚于过期时刻 → 回合已过期，block 不再收口
			if len(pending) > 0 {
				eps = append(eps, closeEpisode(lid, anchor, pending, WarmEndBlock,
					numOr(e, "ts"), nil, econBook, cfg))
			}
			pending = nil // 拦截收口；锚保留（后续保温动作仍挂该锚开新回合）
		default:
			if !isWarmAction(e) {
				continue // bypass/inject/window/dock/非保温 handoff：不参与回合切分
			}
			if anchor == nil {
				continue // 无真实写入锚的保温动作无从起回合，丢弃（如月切片边界残留 beat）
			}
			expireIfStale(numOr(e, "ts")) // 距上跳超 TTL 无事件 → 先过期再挂新回合
			pending = append(pending, e)
		}
	}
	if len(pending) > 0 { // 数据末尾仍开着的回合：按放任过期收口
		last := numOr(pending[len(pending)-1], "ts")
		eps = append(eps, closeEpisode(lid, anchor, pending, WarmEndExpire,
			last+cfg.TTLS, nil, econBook, cfg))
	}
	return eps
}

// closeEpisode 收口一个回合并落双轴判类：前缀取回合内首个保温动作行；双轴与
// 节省只在回归终型可判；拦截/过期 = 无效保温（Need/Hit nil、判亏损、不计省）。
func closeEpisode(lid string, anchor map[string]any, acts []map[string]any,
	endKind string, endTS float64, regLine map[string]any,
	econBook *prices.PriceBook, cfg WarmEpisodeConfig) WarmEpisode {
	ep := WarmEpisode{
		Lineage: lid,
		StartTS: numOr(anchor, "ts"),
		EndKind: endKind,
		EndTS:   endTS,
		Hops:    len(acts),
	}
	// 前缀口径：回合内首个保温动作行；缺字段或 ≤0（预派发失败 prompt_tokens=0）
	// 视为不可得，回落锚 usage 行四列之和（cache_read+cache_creation+input+output）。
	if p := warmActionPrefix(acts[0]); p > 0 {
		ep.Prefix = p
	} else {
		ep.Prefix = usageFourCol(anchor)
		ep.PrefixFallback = true
	}
	if regLine == nil { // 拦截/过期：无回归可判双轴
		ep.Class = WarmClassLoss
		return ep
	}
	need := numOr(regLine, "ts")-numOr(anchor, "ts") > cfg.TTLS
	hit := numOr(regLine, "cache_read_tokens") >= ep.Prefix*cfg.HitRatio
	ep.Need, ep.Hit = &need, &hit
	ep.Month = time.Unix(int64(numOr(regLine, "ts")), 0).Local().Format("2006-01")
	switch {
	case !hit:
		ep.Class = WarmClassLoss // ¬hit→亏损（need 与否皆亏，成本照计）
	case need:
		ep.Class = WarmClassRealized
		if econBook != nil { // SavingsV1 同款：按行时刻取版本；无价/无 p_cache 不造数
			if pv := econBook.At(numOr(regLine, "ts")); pv != nil && pv.PCache != nil {
				ep.Savings = mathx.Round(
					ep.Prefix/float64(econBook.Per)*(pv.PIn-*pv.PCache), 4)
			}
		}
	default:
		ep.Class = WarmClassBenign // ¬need∧hit：白保温无害，单列不计省
	}
	return ep
}

// isWarmAction 记账合同：保温动作 = handoff(provider=智谱 ∧ lane=same_model) ∨ beat。
func isWarmAction(e map[string]any) bool {
	if strOr(e, "kind") == "beat" {
		return true
	}
	return strOr(e, "kind") == "handoff" &&
		strOr(e, "provider") == warmProvider && strOr(e, "lane") == warmLane
}

// warmActionPrefix 保温动作行的 prefix：handoff.prompt_tokens / beat.prefix_tokens。
func warmActionPrefix(e map[string]any) float64 {
	k := "prefix_tokens"
	if strOr(e, "kind") == "handoff" {
		k = "prompt_tokens"
	}
	return numOr(e, k)
}

// usageFourCol 末次真实 usage 行四列之和（前缀回落口径，记账合同原文）。
func usageFourCol(e map[string]any) float64 {
	return numOr(e, "input_tokens") + numOr(e, "output_tokens") +
		numOr(e, "cache_creation_tokens") + numOr(e, "cache_read_tokens")
}
