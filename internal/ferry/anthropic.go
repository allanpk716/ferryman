// anthropic.go — 票03：Anthropic Messages 协议适配器（链执行器的 anthropic 档）。
//
// 与 Chat（OpenAI 兼容 /chat/completions）同签名同纪律的单发调用：
//   - 非流式 POST <base>/v1/messages，认证头 x-api-key + anthropic-version:
//     2023-06-01（cc-switch forwarder 同款版本头；端点拼接防双后缀见
//     anthropicEndpoint）；
//   - 回包 content 块序列里只拼 type=="text" 的 text 字段，thinking 块忽略
//     （extended thinking 机型混排不影响正文）；
//   - usage 映射：input_tokens→prompt_tokens、output_tokens→completion_tokens、
//     total=input+output（Anthropic 无 total 键，计算值）；缺失补 0；
//   - ExtraBody 逐键并入请求体，键冲突以透传为准（票02 裁决在此消费）；
//   - HTTP≥400 文案与 Chat 同款 `HTTP <code> from <name>: <body前500字>`；
//   - 超时语义与 Chat 相同：请求 ctx 按 timeoutS 限时（云端级沿用既有 context
//     总时限语义，票03）。
package ferry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ferryman/internal/mathx"
)

// anthropicVersion Messages API 版本头（cc-switch forwarder:2252 同值）。
const anthropicVersion = "2023-06-01"

// AnthropicChat 一次 Anthropic Messages 调用，返回 (reply, usage, error)。
// 签名/错误纪律与 Chat 同形；执行器经 dispatchCall 按 Provider.Protocol 分派。
func AnthropicChat(pr Provider, system, user string, timeoutS float64, maxTokens int) (string, map[string]any, error) {
	return chatAnthropic(http.DefaultClient, pr, system, user, timeoutS, maxTokens)
}

// chatAnthropic client 注入形（链执行器本地级拨号限时共用；Chat 的
// chatOpenAI 同构）。
func chatAnthropic(client *http.Client, pr Provider, system, user string,
	timeoutS float64, maxTokens int) (string, map[string]any, error) {
	payload := map[string]any{
		"model":       pr.Model,
		"max_tokens":  maxTokens,
		"system":      system,
		"messages":    []map[string]string{{"role": "user", "content": user}},
		"temperature": 0.2,
	}
	for k, v := range pr.ExtraBody { // 逐键并入；冲突以透传为准
		payload[k] = v
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(timeoutS*float64(time.Second)))
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		anthropicEndpoint(pr.BaseURL), bytes.NewReader(body))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if pr.APIKey != "" {
		req.Header.Set("x-api-key", pr.APIKey)
	}
	req.Header.Set("anthropic-version", anthropicVersion)
	t0 := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, err
	}
	if resp.StatusCode >= 400 { // Chat 同款 HTTPError 文案（body 按码点截 500）
		msg := mathx.RuneTrunc(strings.ToValidUTF8(string(raw), "�"), 500)
		return "", nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, pr.Name, msg)
	}
	wall := time.Since(t0).Seconds()
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return "", nil, err
	}
	reply := "" // content 块序列拼接 text 块（thinking 等其余类型忽略）
	if blocks, ok := data["content"].([]any); ok {
		var b strings.Builder
		for _, blk := range blocks {
			if m, ok := blk.(map[string]any); ok && m["type"] == "text" {
				if s, ok := m["text"].(string); ok {
					b.WriteString(s)
				}
			}
		}
		reply = b.String()
	}
	usageSrc, _ := data["usage"].(map[string]any)
	in := numOr0(usageSrc["input_tokens"])
	out := numOr0(usageSrc["output_tokens"])
	usage := map[string]any{
		"prompt_tokens":     in,
		"completion_tokens": out,
		"total_tokens":      in + out,
		"wall_s":            mathx.Round(wall, 1),
	}
	return reply, usage, nil
}

// anthropicEndpoint Messages 端点拼接三形归一：base 已指到 /v1/messages →
// 原样（cc-switch forwarder 的双后缀防线：+ /v1/messages 会得
// .../v1/messages/v1/messages 的确定性 400）；尾 /v1 → 补 /messages（与
// OpenAI 档 base_url 含 /v1 的既有约定共存）；其余（裸 host/网关前缀）→
// 补 /v1/messages。
func anthropicEndpoint(baseURL string) string {
	base := strings.TrimRight(baseURL, "/")
	switch {
	case strings.HasSuffix(base, "/v1/messages"):
		return base
	case strings.HasSuffix(base, "/v1"):
		return base + "/messages"
	default:
		return base + "/v1/messages"
	}
}
