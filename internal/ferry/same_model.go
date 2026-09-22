// same_model.go — 票02:同模型判热的纯类型与纯逻辑(ADR-0015 决定一/决定二)。
//
// 调度本体挂在守望(daemon/watcher.go 的 maybeSameModel);本包只放判热
// 预测器与跳过原因编码——无 I/O、无消息内容(隐私铁律)。
//
// 口径(ADR-0015;夜链 decisions D2/D3/D6):
//   - 判据 = 判热时钟(距该会话最后一次上游请求、含体外心跳重放的时长)+ 该
//     上游 TTL 观测的闭式预判;
//   - 保守默认:无 TTL 观测(未实测)= 判冷——宁可落回第三方/骨架 25 分钟档,
//     不赌全价前缀重付(F5 保守 bootstrap 同款);
//   - 热当口 = 时钟 ≤ safety·TTL,即 policy 的 τ(「缓存必活带」右沿)。常数
//     单源取 policy.DefaultSafety,本包不出现第二份公式;
//   - 跳过三原因独立编码(F6):cold / whitelist_miss / not_enabled,稳定
//     字面量,防误统计。
package ferry

import "ferryman/internal/policy"

// 同模型跳过原因(独立编码,遥测事件的 reason 字段值;F6)。
const (
	SameModelSkipCold          = "cold"           // 判冷:缓存预判已死/观测不足
	SameModelSkipWhitelistMiss = "whitelist_miss" // 上游不在白名单(含无活动上游)
	SameModelSkipNotEnabled    = "not_enabled"    // 白名单预置但实跳臂结论未回写/未启用(D6)
)

// TTLObs 该上游的 TTL 观测(闭式预判的输入)。TTLS = 实测 TTL(秒);≤0 =
// 未实测/观测不足(预测器据此保守判冷)。当前单源 = [heartbeat].ttl_s
// (实测值);按上游分列待计算器票扩展,输入形状已按每上游预留。
type TTLObs struct {
	TTLS float64
}

// PredictHot 判热预测器:给定判热时钟时长(秒)与该上游 TTL 观测,判定
// 缓存预判是否仍热。
//
//	观测不足(TTLS ≤ 0)→ 冷(保守默认,绝不伪造热);
//	时钟 ≤ safety·TTL(缓存必活带)→ 热;
//	τ..T 的不确定带与超 T → 冷(miss=全价前缀重付,代价不对称取保守沿;
//	与 policy.TierFor 的「≤τ 缓存必活」同一条闭式)。
func PredictHot(clockS float64, obs TTLObs) bool {
	if obs.TTLS <= 0 {
		return false
	}
	return clockS <= policy.DefaultSafety*obs.TTLS
}
