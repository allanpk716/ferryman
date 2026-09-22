// headers.go — 票06：出站头卫生（改写五件之第五件；透传模式零处理——票01
// 保真语义由 server_test.go 钉子看住）。
//
// 清单（spec「改写五件·出站头卫生」）：
//   - host 换上游：不在本文件（ReverseProxy 的 pr.Out.Host 已做，两模式同）；
//   - 删追踪/CDN 头：x-forwarded-* 前缀、cdn-* 前缀、x-real-ip、
//     traceparent/tracestate/baggage（W3C TraceContext/OpenTelemetry）、
//     sentry-trace；
//   - 认证替换：删入站 Authorization 与 x-api-key，出站
//     Authorization: Bearer <[dock].api_key>。真钥只进出站头，永不入日志/
//     账本/错误（T39）；
//   - anthropic-beta 重建：保留客户端全部标记＋确保含 claude-code-20250219
//     （大小写不敏感判重，有则不加、无则尾部追加）；
//   - anthropic-version 透传、UA 保留：即白名单外其余头不动——只做减法与
//     auth/beta 两处改写。
//
// 只动 pr.Out（ReverseProxy 的出站克隆）；入站头不受影响。
package dock

import (
	"net/http"
	"strings"
)

// mustBetaFlag 出站必含的 beta 标记（红线条款：出站头必含 claude-code-20250219）。
const mustBetaFlag = "claude-code-20250219"

// stripExact / stripPrefixes 出站删除清单（键小写比对）。
var (
	stripExact = map[string]bool{
		"x-real-ip":    true,
		"traceparent":  true,
		"tracestate":   true,
		"baggage":      true,
		"sentry-trace": true,
		// 票03：自产重放标记头出站剥离（渡口内部识别用，不泄漏上游）。
		HeaderFerrymanReplay: true,
	}
	stripPrefixes = []string{"x-forwarded-", "cdn-"}
)

// sanitizeOutboundHeaders 就地清洗出站头。
func sanitizeOutboundHeaders(h http.Header, apiKey string) {
	for _, k := range collectStripped(h) {
		h.Del(k)
	}
	// 认证替换：入站两类认证头一律删，出站只认渡口的真钥
	h.Del("X-Api-Key")
	h.Set("Authorization", "Bearer "+apiKey)
	rebuildBeta(h)
}

// collectStripped 先收集后删：map 边遍历边删会跳过迭代中未到达的键。
func collectStripped(h http.Header) []string {
	var kill []string
	for k := range h {
		lk := strings.ToLower(k)
		if stripExact[lk] {
			kill = append(kill, k)
			continue
		}
		for _, p := range stripPrefixes {
			if strings.HasPrefix(lk, p) {
				kill = append(kill, k)
				break
			}
		}
	}
	return kill
}

// rebuildBeta 保留客户端全部 beta 标记（多行并一），确保含必需标记：有则不
// 加（EqualFold 判重防双写），无则尾部追加。
func rebuildBeta(h http.Header) {
	var flags []string
	for _, raw := range h.Values("Anthropic-Beta") {
		for _, f := range strings.Split(raw, ",") {
			if f = strings.TrimSpace(f); f != "" {
				flags = append(flags, f)
			}
		}
	}
	for _, f := range flags {
		if strings.EqualFold(f, mustBetaFlag) {
			h.Set("Anthropic-Beta", strings.Join(flags, ","))
			return
		}
	}
	flags = append(flags, mustBetaFlag)
	h.Set("Anthropic-Beta", strings.Join(flags, ","))
}
