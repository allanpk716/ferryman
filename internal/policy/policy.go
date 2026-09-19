// Package policy 移植策略计算器：价格表 + 实测 TTL → 心跳参数与分档
// （闭式推导，设计 §2）。规格：ferryman/policy.py（1:1）。
//
// 公式出处：docs/20260917_1630 实验报告 §4；金额单位 = 价格表自述单位（按 per 归一）。
// p_cache 缺省 → NoCachePriceError（宁可不算，不造数，Q16）。
// 全仓公式单源：报表、面板、反跑共用本包，出现第二份实现即缺陷。
package policy

import (
	"errors"
	"fmt"
	"math"

	"ferryman/internal/prices"
)

// ErrTTLUnset TTL 未实测/非法（文案与 Python ValueError 逐字一致）。
var ErrTTLUnset = errors.New("ttl_s 须 > 0（先跑 experiments/cache-ttl 套件实测）")

// NoCachePriceError 无 p_cache：无缓存经济，拒绝推导心跳参数。
type NoCachePriceError struct{ Key string }

func (e NoCachePriceError) Error() string {
	return fmt.Sprintf("%s 无 p_cache，拒绝推导心跳参数", e.Key)
}

// 调用方缺省：Go 无关键字缺省参数，Python 侧的 beat_out_tokens=300、safety=0.8、
// grace_s=300.0、min_prefix_tokens=30_000、compact_ratio=0.25 由调用方显式传入，
// 数值集中在此供取用。
const (
	DefaultBeatOutTokens   = 300.0
	DefaultSafety          = 0.8
	DefaultGraceS          = 300.0
	DefaultMinPrefixTokens = 30000
	DefaultCompactRatio    = 0.25
)

// HeartbeatPolicy 一次推导产出的心跳参数组。
type HeartbeatPolicy struct {
	TTLS            float64
	TauS            float64 // 0.8 × T
	PerBeatCost     float64 // 单次心跳成本
	ExpireCost      float64 // 放任过期代价
	WorthwhileCapS  float64 // 心跳划算的等待上限
	GraceS          float64
	MinPrefixTokens int
}

// Compute 对应 Python 的 heartbeat_policy：闭式推导心跳参数。
// ttl<=0 → ErrTTLUnset；PCache nil → NoCachePriceError。
func Compute(b prices.PriceBook, pv prices.PriceVersion, ttlS float64, prefixTokens int,
	beatOutTokens, safety, graceS float64, minPrefixTokens int) (HeartbeatPolicy, error) {
	if ttlS <= 0 {
		return HeartbeatPolicy{}, ErrTTLUnset
	}
	if pv.PCache == nil {
		return HeartbeatPolicy{}, NoCachePriceError{Key: b.Key}
	}
	pin := float64(prefixTokens) / float64(b.Per) * pv.PIn // expire = S/per·PIn
	pcache := float64(prefixTokens) / float64(b.Per) * *pv.PCache
	perBeat := pcache + float64(beatOutTokens)/float64(b.Per)*pv.POut // perBeat = S/per·pCache + beatOut/per·POut
	tau := safety * ttlS                                              // τ = safety·T
	capS := math.Inf(1)
	if perBeat > 0 {
		capS = tau * (pin - pcache) / perBeat // cap = τ·(PIn−PCache)/perBeat
	}
	return HeartbeatPolicy{TTLS: ttlS, TauS: tau, PerBeatCost: perBeat,
		ExpireCost: pin, WorthwhileCapS: capS,
		GraceS: graceS, MinPrefixTokens: minPrefixTokens}, nil
}

// TierFor 分档："none"（≤τ：缓存必活）| "beat"（τ..cap 划算）| "expire"（>cap）。
func TierFor(p HeartbeatPolicy, waitS float64) string {
	if waitS <= p.TauS {
		return "none"
	}
	if waitS <= p.WorthwhileCapS {
		return "beat"
	}
	return "expire"
}

// StrategyCosts 对应 Python 的 strategy_costs：缓存经济学三策略 + compact 变体；
// handoff 策略由 report 按账本均值另算（§3.4）。compact_ratio 为公式内常数
// （v1 取 0.25，复算附录可见）。
func StrategyCosts(b prices.PriceBook, pv prices.PriceVersion, ttlS, waitS float64,
	prefixTokens int, beatOutTokens, compactRatio float64) (map[string]float64, error) {
	pol, err := Compute(b, pv, ttlS, prefixTokens, beatOutTokens,
		DefaultSafety, DefaultGraceS, DefaultMinPrefixTokens)
	if err != nil {
		return nil, err
	}
	beats := 0.0
	if waitS > 0 {
		beats = math.Ceil(waitS / pol.TauS)
	}
	none := 0.0
	if waitS > ttlS {
		none = pol.ExpireCost
	}
	return map[string]float64{
		"none":           none,
		"beat":           beats * pol.PerBeatCost,
		"expire":         pol.ExpireCost,
		"expire_compact": float64(prefixTokens) * compactRatio / float64(b.Per) * pv.PIn,
	}, nil
}
