// truncation_test.go — 票02：干净 EOF 观测（错误契约热修 4）的验收钉子。
// 桩发若干 SSE 事件后 EOF（无 message_stop）→ 流水行带 truncated:true；
// 完整流（有 message_stop）不带；非 SSE 响应不带。不改转发字节（客户端
// 实收与桩所发逐字节一致）。
package dock

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// truncBackend 发完给定事件即 EOF 的假上游（无 message_stop 的截断形态）。
func truncBackend(t *testing.T, events string) *httptest.Server {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, events)
		w.(http.Flusher).Flush()
		// 直接返回：连接干净 EOF，无 message_stop
	}))
	t.Cleanup(backend.Close)
	return backend
}

// TestTruncatedStreamMarksRow 见过 SSE 事件但流尾无 message_stop → 行带
// truncated:true；转发字节不变。
func TestTruncatedStreamMarksRow(t *testing.T) {
	events := "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":0}}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","usage":{"input_tokens":10,"output_tokens":3}}` + "\n\n"
	backend := truncBackend(t, events)

	_, acc, front := newRecordingFront(t, backend.URL)
	resp, err := http.DefaultClient.Do(ccRequest(t, front+"/v1/messages", streamBody("trunc")))
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	// 转发字节不变：事件原样到达（观测纯旁路）
	if !strings.Contains(string(body), "message_delta") {
		t.Fatalf("截断观测改了转发字节: %q", body)
	}

	rows := waitDockRows(t, acc, 1)
	if rows[0]["truncated"] != true {
		t.Fatalf("截断流应带 truncated:true: %v", rows[0])
	}
	if rows[0]["status"].(float64) != 200 {
		t.Fatalf("行 status = %v, want 200", rows[0]["status"])
	}
}

// TestCompleteStreamNotMarked 有 message_stop 的完整流不带 truncated。
func TestCompleteStreamNotMarked(t *testing.T) {
	events := "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":0}}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","usage":{"input_tokens":10,"output_tokens":3}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	backend := truncBackend(t, events)

	_, acc, front := newRecordingFront(t, backend.URL)
	resp, err := http.DefaultClient.Do(ccRequest(t, front+"/v1/messages", streamBody("full")))
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	rows := waitDockRows(t, acc, 1)
	if _, ok := rows[0]["truncated"]; ok {
		t.Fatalf("完整流不应带 truncated: %v", rows[0])
	}
	// usage 解析照常（截断观测不碰既有扫描）
	if rows[0]["output_tokens"].(float64) != 3 {
		t.Fatalf("output_tokens = %v, want 3", rows[0]["output_tokens"])
	}
}

// TestNonSSEResponseNotMarked 非 SSE 响应（无事件可见）不标记。
func TestNonSSEResponseNotMarked(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(backend.Close)

	_, acc, front := newRecordingFront(t, backend.URL)
	resp, err := http.DefaultClient.Do(ccRequest(t, front+"/v1/messages",
		[]byte(`{"model":"claude-opus-5","metadata":{"session_id":"nosse"},"messages":[]}`)))
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	rows := waitDockRows(t, acc, 1)
	if _, ok := rows[0]["truncated"]; ok {
		t.Fatalf("非 SSE 响应不应带 truncated: %v", rows[0])
	}
}
