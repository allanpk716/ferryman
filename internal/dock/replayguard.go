// replayguard.go — 票03：渡口自产重放识别（追加重放的快照防污染）。
//
// 背景：追加重放体＝主快照＋末尾追加段，严格大于主快照——若照常走
// Capture，「主快照＝最大体」的擂台会被追加体顶替，后续重放将在追加体上
// 再追加（雪球），且压缩后的真实小请求永远打不回来（形态漂移+内容陈旧）。
// 心跳重放体（max_tokens→1）≤ 主快照虽不打擂，但快照语义本就只该记真流量。
//
// 识别标志＝专用标记头 x-ferryman-replay：占位令牌 PROXY_MANAGED 不可用作
// 标记（CC 真流量经 cc-switch 接管时本身就带它，2026-09-18 调研实证）。
// 只有 Ferryman 自产发送器（追加重放）携带本头；改写模式出站剥离
// （headers.go stripExact）不泄漏上游；透传模式按「头零处理」铁律原样透传
// ——同模型档只在白名单上游（＝改写模式）触发，实际不达。
package dock

import "net/http"

// HeaderFerrymanReplay 渡口自产重放标记头（小写即规范键形）。
const HeaderFerrymanReplay = "x-ferryman-replay"

// isReplayRequest 该请求是否 Ferryman 自产重放（标记头在场）。
// 只影响快照捕获与形态漂移观察（不入快照）；dock 科目照记——重放也是
// 真实经过渡口的上游请求，传输视图（token 四列/延迟/状态）保持完整。
func isReplayRequest(h http.Header) bool {
	return h.Get(HeaderFerrymanReplay) != ""
}
