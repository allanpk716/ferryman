package config

// DshCompactCfg dsh 会话热缓存压缩（dsh-hot-compaction 票02，spec「架构与
// 契约」节）：[dsh_compact] 配置节。TTL 基准＝[heartbeat].ttl_s（实测 TTL，
// 同 cacheHot 单源）——比例键全部以它为分母。
type DshCompactCfg struct {
	// Enabled 总开关（默认 true；false＝指令不入槽、poll 空应答——全链路静默）。
	Enabled bool
	// TriggerRatio 触发线：闲置 ≥ trigger_ratio×TTL（默认 0.8＝缓存还热时压，
	// D1「拦你之前先救你」）。触发判定本体在 watcher 触发面（票03），本节供值。
	TriggerRatio float64
	// MinPeakTokens peak_ctx ≥ 此值才值得压（默认 20000；只管「值得压」——
	// 压缩红利要盖过冷重付才划算。放行线已解耦，勿再拿本键当闸门放行阈值）。
	MinPeakTokens int
	// PassFloorTokens 放行地板（v0.9.3 放行线解耦）：压后前缀 <
	// max(本值, pass_ratio×压前峰值) 时闸门放行（compacted-short-prefix）。
	// 默认 12000：glm-5.3 提示地板≈9.4K，压得干净的会话落 9~12K，在其上
	// 留余量。首单事故（2026-10-07，60562→20562 撞 20000 线）即本键动机。
	PassFloorTokens int
	// PassRatio 放行比例腿（默认 0.5）：压后 ≤ 压前峰值一半即放行——压掉
	// 一半以上＝剩余冷重付已是系统可达下限，再拦只逼「更贵强续」或「丢活
	// 上下文」二选一。压前峰值不可得（PrePeak=0）时本腿失效只剩地板。
	PassRatio float64
	// CommandTTLRatio 指令有效期＝command_ttl_ratio×TTL（N1：默认 0.2——指令
	// 过期即弃，插件收到也不执行）。
	CommandTTLRatio float64
	// PollHintS 下发给插件的建议轮询间隔（默认 30s；插件取 max(提示,10s)）。
	PollHintS float64
	// CompressedFlagTTLRatio compressed 标记有效期＝ratio×TTL（默认 2.0——
	// 覆盖一个完整冷窗仍留余量）。
	CompressedFlagTTLRatio float64
}
