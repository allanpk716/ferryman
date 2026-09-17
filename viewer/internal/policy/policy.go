// Package policy 移植心跳推导公式：由价格制+实测 TTL 推导保活跳点与各档成本。
package policy

import "errors"

// Params 是一次推导/反跑的输入。价格与 per 同单位制（如 GLM 积分系数 6.9/1.7/24，per=10000）。
type Params struct {
	PIn, PCache, POut float64
	Per               float64 // 单价对应的 token 块大小；<=0 视为 1
	PrefixTokens      float64 // S
	TTLS              float64 // 实测缓存寿命（秒）
	Safety            float64 // τ 系数，默认 0.8
	BeatOutTokens     float64 // 单跳预留输出 token，默认 300
	MaxWaitS          float64 // 手动上限；<=0 = auto
}

type Result struct {
	TauS      float64 `json:"tau_s"`
	CacheRead float64 `json:"cache_read_cost"` // S×P_cache
	PerBeat   float64 `json:"per_beat"`
	Expire    float64 `json:"expire"` // S×P_in
	CapS      float64 `json:"cap_s"`
}

func Derive(p Params) (Result, error) {
	if p.PCache <= 0 {
		return Result{}, errors.New("p_cache 缺省：无缓存经济，拒绝推导（宁可不算不造数）")
	}
	per := p.Per
	if per <= 0 {
		per = 1
	}
	safety := p.Safety
	if safety <= 0 {
		safety = 0.8
	}
	beatOut := p.BeatOutTokens
	if beatOut <= 0 {
		beatOut = 300
	}
	tau := safety * p.TTLS
	cr := p.PrefixTokens / per * p.PCache
	pb := cr + beatOut/per*p.POut
	ex := p.PrefixTokens / per * p.PIn
	cap := tau * (ex - cr) / pb
	if p.MaxWaitS > 0 && p.MaxWaitS < cap {
		cap = p.MaxWaitS
	} // 手动只能往下收
	return Result{TauS: tau, CacheRead: cr, PerBeat: pb, Expire: ex, CapS: cap}, nil
}

// SimulateBeats 按 §3.6 定案排跳：首跳 T0+τ，此后每 τ，停于 min(窗末, T0+cap)。
func SimulateBeats(t0, windowEnd float64, r Result) []float64 {
	var out []float64
	end := t0 + r.CapS
	if windowEnd < end {
		end = windowEnd
	}
	for t := t0 + r.TauS; t <= end+1e-9; t += r.TauS {
		out = append(out, t)
	}
	return out
}

// DoNothingCost 什么都不做的窗口成本：整窗 ≤TTL 存活为 0；否则过期一次全款。
func DoNothingCost(durS float64, ttlS float64, r Result) float64 {
	if durS <= ttlS {
		return 0
	}
	return r.Expire
}
