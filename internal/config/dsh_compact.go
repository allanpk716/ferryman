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
	// MinPeakTokens peak_ctx ≥ 此值才值得压（默认 20000；gate 的 min_ctx_tokens
	// 同量纲——压缩红利要盖过冷重付才划算）。
	MinPeakTokens int
	// CommandTTLRatio 指令有效期＝command_ttl_ratio×TTL（N1：默认 0.2——指令
	// 过期即弃，插件收到也不执行）。
	CommandTTLRatio float64
	// PollHintS 下发给插件的建议轮询间隔（默认 30s；插件取 max(提示,10s)）。
	PollHintS float64
	// CompressedFlagTTLRatio compressed 标记有效期＝ratio×TTL（默认 2.0——
	// 覆盖一个完整冷窗仍留余量）。
	CompressedFlagTTLRatio float64
}
