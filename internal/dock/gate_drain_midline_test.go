// gate_drain_midline_test.go — 票02 回炉（R1）：排水注入的前导空行验收钉子。
// 排水可能命中 SSE 事件中段（已放行字节停在无换行的半行 data 上）：注入
// 负载若不带前导空行，"event: error" 会并入半行、被客户端 SSE 解析器当
// data 内容吞掉。此处钉住：半行被空行终结、错误事件以独立 event: error
// 事件可见（字节流可被标准 SSE 规则解析出名为 error 的事件）。
package dock

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// sseEvt 解析出的单条 SSE 事件（本测试自用的最小解析形状）。
type sseEvt struct {
	name string
	data []string
}

// parseSSEEvents 按标准 SSE 规则切分字节流：空行派发事件，"event: "/"data: "
// 行各自归位。出现不认识的行（比如 "event: error" 并进了半行 data）直接
// FailFast——那正是本钉子要抓的形状。
func parseSSEEvents(t *testing.T, raw string) []sseEvt {
	t.Helper()
	var evts []sseEvt
	for _, block := range strings.Split(raw, "\n\n") {
		if strings.TrimSpace(block) == "" {
			continue
		}
		var ev sseEvt
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				ev.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				ev.data = append(ev.data, strings.TrimPrefix(line, "data: "))
			case line == "":
			default:
				t.Fatalf("SSE 字节流出现无法归位的行: %q", line)
			}
		}
		evts = append(evts, ev)
	}
	return evts
}

// TestDrainExpiryMidEventInjectsStandaloneErrorEvent 已放行字节停在事件中段
// （半行 data 无换行）时排水到期：注入的错误事件必须是独立 SSE 事件——
// 半行被前导空行终结、"event: error" 行完整可见、负载为 Anthropic 标准形状。
func TestDrainExpiryMidEventInjectsStandaloneErrorEvent(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	// 首个完整事件 + 挂起的半行（无结尾换行），随后按住不再发字节。
	halfLine := `data: {"type":"content_block_delta"`
	backend := sseBackend(t,
		"event: message_start\ndata: {\"type\":\"message_start\"}\n\n"+halfLine,
		block, "")

	srv, acc, front := newRecordingFront(t, backend.URL)
	done := doStreamIn(t, front+"/v1/messages")

	time.Sleep(150 * time.Millisecond) // 半行已放行在途

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if err := srv.Shutdown(ctx); err == nil {
		t.Fatal("排水到期 Shutdown 应返回非 nil")
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("客户端收尾失败: %v", r.err)
		}
		body := string(r.body)
		// 字节级钉子：半行后紧跟空行，空行后 "event: error" 独立成行。
		if !strings.Contains(body, halfLine+"\n\nevent: error\n") {
			t.Fatalf("半行未被空行终结（event: error 并入半行）: %q", body)
		}
		// 解析级钉子：整条流按 SSE 规则切得出名为 error 的独立事件。
		evts := parseSSEEvents(t, body)
		var errEvt *sseEvt
		for i := range evts {
			if evts[i].name == "error" {
				errEvt = &evts[i]
			}
		}
		if errEvt == nil {
			t.Fatalf("未见名为 error 的独立事件（事件数 %d）: %q", len(evts), body)
		}
		if len(errEvt.data) != 1 {
			t.Fatalf("error 事件应有且仅有一行 data: %+v", *errEvt)
		}
		var payload struct {
			Type  string `json:"type"`
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(errEvt.data[0]), &payload); err != nil {
			t.Fatalf("error 事件负载非合法 JSON: %v: %q", err, errEvt.data[0])
		}
		if payload.Type != "error" || payload.Error.Type != "api_error" || payload.Error.Message == "" {
			t.Fatalf("error 事件负载非 Anthropic 标准形状: %q", errEvt.data[0])
		}
		// 半行数据保留在自己的事件里（未被吞、未被拼坏）。
		foundHalf := false
		for _, ev := range evts {
			if ev.name == "error" {
				continue
			}
			for _, d := range ev.data {
				if strings.Contains(d, "content_block_delta") {
					foundHalf = true
				}
			}
		}
		if !foundHalf {
			t.Fatalf("挂起半行内容丢失: %q", body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("3s 内连接未收尾")
	}

	rows := waitDockRows(t, acc, 1)
	if rows[0]["truncated"] != true {
		t.Fatalf("排水掐流行应带 truncated: %v", rows[0])
	}
}
