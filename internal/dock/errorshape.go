// errorshape.go — 票02：渡口自产错误的标准形状（错误契约热修 2 的形状单源）。
//
// 契约（ADR-0017）：渡口自身产生的错误永不裸断连，一律输出 CC 已认识的
// Anthropic 形状，按流状态分流投递——
//   - 首字节未写出（响应头尚未发给 CC）→ HTTP 502/504 ＋ Anthropic 错误体
//     （{"type":"error","error":{"type":"api_error",...}}）＋ Retry-After 短值；
//   - 流已建立（200 已写、SSE 进行中）→ 先合成一个 Anthropic 流内错误事件
//     （SSE event: error）再断连（drainBody，见 gate.go）。
//
// 上游非 200 不在此列：状态码/体/头零改写透传（响应保真红线，server_test 与
// errorshape_test 钉子看住）。
package dock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

// retryAfterShort 渡口自产错误的退避提示（spec：短值约 5 秒——CC 认这个头，
// 自动重试不风暴）。
const retryAfterShort = "5"

// 出站失败来源哨兵（经 RoundTrip 返回给反向代理 ErrorHandler，由
// proxyErrorHandler 统一分流成标准形状；不直接面向客户端）。
var (
	// errFirstByteSilent 首包闸门静默超时（热修 3）。
	errFirstByteSilent = errors.New("上游 200 后首包静默超时")
	// errDrainExpired 排水到期收尾（热修 1）。
	errDrainExpired = errors.New("排水到期收尾")
)

// anthropicErrorBody / anthropicErrorDetail CC 认识的错误体形状（Messages API
// 错误响应）。自产错误细类恒 api_error：不替上游 4xx 细类冒名。
type anthropicErrorBody struct {
	Type  string               `json:"type"` // 恒 "error"
	Error anthropicErrorDetail `json:"error"`
}

type anthropicErrorDetail struct {
	Type    string `json:"type"` // 恒 "api_error"
	Message string `json:"message"`
}

func marshalAnthropicError(message string) []byte {
	payload, err := json.Marshal(anthropicErrorBody{
		Type:  "error",
		Error: anthropicErrorDetail{Type: "api_error", Message: message},
	})
	if err != nil { // 静态形状不会失败；兜底保底可解析体
		return []byte(`{"type":"error","error":{"type":"api_error","message":"渡口内部错误"}}`)
	}
	return payload
}

// writeDockErrorResponse 首字节未写出路径的标准形状：HTTP 状态＋Anthropic
// 错误体（含一句人话原因）＋Retry-After。只在响应头未写出时调用
// （RoundTrip 失败/闸门失败/排水取消——反向代理此时尚未写给客户端任何字节）。
func writeDockErrorResponse(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", retryAfterShort)
	w.WriteHeader(status)
	_, _ = w.Write(marshalAnthropicError(message))
}

// sseErrorEvent 流已建立路径的标准形状：Anthropic 流内错误事件（SSE
// event: error，负载同错误体）。写进转发体后连接随即收尾——CC 见事件知
// 错误，不裸断连。
func sseErrorEvent(message string) []byte {
	var b bytes.Buffer
	b.WriteString("event: error\ndata: ")
	b.Write(marshalAnthropicError(message))
	b.WriteString("\n\n")
	return b.Bytes()
}

// proxyErrorHandler 反向代理 ErrorHandler（热修 2）：出站失败的形状分流。
// 此时客户端首字节必未写出（RoundTrip 失败则代理尚未写任何响应），可安全
// 换头。区分来源：
//   - 闸门/排水哨兵 → 504＋错误体＋Retry-After；
//   - context.Canceled 且排水已到期（expireDrain 主动取消出站）→ 504 排水
//     收尾形状；
//   - context.Canceled 未排水（客户端先断/其取消）→ 静默返回（对端已不在，
//     不写无意义的 502）；
//   - 其余（拨号失败等出站错误）→ 502＋错误体（含一句人话原因）＋
//     Retry-After，替换 Go 默认空体 502。
func (s *Server) proxyErrorHandler(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errDrainExpired):
		writeDockErrorResponse(w, http.StatusGatewayTimeout,
			"渡口正在关停（排水到期收尾），请求未获上游响应即被结束；请稍后重试")
	case errors.Is(err, errFirstByteSilent):
		writeDockErrorResponse(w, http.StatusGatewayTimeout,
			"上游已受理但迟迟未吐出任何流数据（首包静默超时）；请重试")
	case errors.Is(err, context.Canceled) && s.isDrained():
		writeDockErrorResponse(w, http.StatusGatewayTimeout,
			"渡口正在关停（排水到期收尾），请求未获上游响应即被结束；请稍后重试")
	case errors.Is(err, context.Canceled):
		// 客户端先断（或其取消）：对端已不在，写亦无人收——不造无意义的 502
	default:
		writeDockErrorResponse(w, http.StatusBadGateway,
			"渡口连不上上游（"+err.Error()+"）；请稍后重试")
	}
}
