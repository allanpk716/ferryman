// headers_test.go — 票06：出站头卫生验收钉子（仅改写模式启用；透传零处理由
// server_test.go 的保真钉子间接钉死）。
package dock

import (
	"net/http"
	"strings"
	"testing"
)

// clientStyleHeaders CC 全头夹具（server_test.go ccRequest 同款＋追踪/CDN 补充）。
func clientStyleHeaders() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Anthropic-Version", "2023-06-01")
	h.Set("Anthropic-Beta", "oauth-2025-04-20,claude-code-20250219,interleaved-thinking-2025-05-14")
	h.Set("User-Agent", "claude-cli/2.0.0 (external)")
	h.Set("Accept", "application/json")
	h.Set("Accept-Encoding", "gzip, deflate")
	h.Set("Authorization", "Bearer placeholder-token")
	h.Set("X-Api-Key", "sk-ant-placeholder")
	h.Set("X-Forwarded-For", "203.0.113.9")
	h.Set("X-Forwarded-Proto", "https")
	h.Set("X-Real-Ip", "203.0.113.9")
	h.Set("Cdn-Loop", "cloudflare")
	h.Set("Traceparent", "00-4bf92f3605-trace")
	h.Set("Tracestate", "vendor=1")
	h.Set("Baggage", "trace-id=abc")
	h.Set("Sentry-Trace", "abc-parent")
	h.Set("X-Custom-Passthrough", "keep-me")
	return h
}

func TestSanitizeOutboundHeadersReplacesAuthAndStrips(t *testing.T) {
	h := clientStyleHeaders()
	sanitizeOutboundHeaders(h, "sk-real-key")

	if got := h.Get("Authorization"); got != "Bearer sk-real-key" {
		t.Fatalf("Authorization = %q, want 真钥 Bearer", got)
	}
	if h.Get("X-Api-Key") != "" {
		t.Fatal("x-api-key 未删")
	}
	for _, k := range []string{"X-Forwarded-For", "X-Forwarded-Proto", "X-Real-Ip",
		"Cdn-Loop", "Traceparent", "Tracestate", "Baggage", "Sentry-Trace"} {
		if len(h.Values(k)) != 0 {
			t.Fatalf("追踪/CDN 头 %s 未删", k)
		}
	}
	// 保留面：UA / version / 自定义业务头
	if h.Get("User-Agent") != "claude-cli/2.0.0 (external)" {
		t.Fatal("UA 须保留")
	}
	if h.Get("Anthropic-Version") != "2023-06-01" {
		t.Fatal("anthropic-version 须透传")
	}
	if h.Get("X-Custom-Passthrough") != "keep-me" {
		t.Fatal("无关自定义头须保留（只做减法＋auth/beta 改写）")
	}
}

func TestSanitizeBetaKeepsClientFlags(t *testing.T) {
	h := clientStyleHeaders() // 已含 claude-code-20250219
	sanitizeOutboundHeaders(h, "k")
	got := h.Get("Anthropic-Beta")
	for _, want := range []string{"oauth-2025-04-20", "claude-code-20250219", "interleaved-thinking-2025-05-14"} {
		if !strings.Contains(got, want) {
			t.Fatalf("beta 须保留客户端全部标记: %q 缺 %q", got, want)
		}
	}
	if strings.Count(got, "claude-code-20250219") != 1 {
		t.Fatalf("已有标记不重复追加: %q", got)
	}
}

func TestSanitizeBetaAppendsWhenMissing(t *testing.T) {
	h := http.Header{}
	h.Set("Anthropic-Beta", "oauth-2025-04-20")
	sanitizeOutboundHeaders(h, "k")
	if got := h.Get("Anthropic-Beta"); got != "oauth-2025-04-20,claude-code-20250219" {
		t.Fatalf("beta = %q, want 尾部追加必需标记", got)
	}
}

func TestSanitizeBetaAddsWhenAbsent(t *testing.T) {
	h := http.Header{} // 客户端没带 beta
	sanitizeOutboundHeaders(h, "k")
	if got := h.Get("Anthropic-Beta"); got != "claude-code-20250219" {
		t.Fatalf("beta = %q, want 仅含必需标记", got)
	}
}

func TestSanitizeBetaCaseInsensitiveDedup(t *testing.T) {
	h := http.Header{}
	h.Set("Anthropic-Beta", "Claude-Code-20250219") // 大小写变体＝已有
	sanitizeOutboundHeaders(h, "k")
	if got := h.Get("Anthropic-Beta"); got != "Claude-Code-20250219" {
		t.Fatalf("大小写变体视为已含，不追加: %q", got)
	}
}
