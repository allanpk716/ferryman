// replay_switch_boundary_e2e_test.go — 票04：切换边界实测（F4）——responses
// 车道全链路版（票02 的 TestResolverSwitchInflightHoldsOldUpstream 覆盖
// /v1/messages 车道；本钉子把同一边界压到 codex 车道的在途 SSE 流上）。
//
// 场景：假上游 A（anthropic 方言）故意拖住在途 SSE 流（首片已达客户端后压
// 流不放）→ 此刻热切换供应商（B，openai_responses 方言）→ 三断言：
//  1. 在途流在旧上游跑完不中断（全文首尾两片齐全、completed 收尾、A 只收
//     到过一次请求——无重派/无排水断流）；
//  2. 下一请求即刻走新供应商（不等 A 放行，B 即刻应答，条目整套视图＝
//     新方言/新真钥/新 codex 主模型键）；
//  3. 渡口进程不重启——同一 handler 实例（请求计数单调、无连接错误）持续
//     服务切换前后请求；在途连接全程未被剪断（重启必断流，断言 1 已含）。
//
// 切换通道用票02 的 UpstreamResolver 注桩（daemon /provider_switch 的同款
// 并发形状；票面三断言是行为断言，不规定切换通道）。
package dock

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/config"
)

// replayServeCounter handler 计数包装：同一实例持续服务＝进程未重启的行为
// 代理证据（重启＝新进程新 handler，计数必回零重计）。
type replayServeCounter struct {
	inner http.Handler
	n     atomic.Int64
}

func (c *replayServeCounter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.n.Add(1)
	c.inner.ServeHTTP(w, r)
}

// replayAnthropicFrame anthropic 方言 SSE 帧（测试内联构造）。
func replayAnthropicFrame(event string, data map[string]any) []byte {
	var b strings.Builder
	b.WriteString("event: ")
	b.WriteString(event)
	b.WriteString("\ndata: ")
	b.Write(encodeCompact(data))
	b.WriteString("\n\n")
	return []byte(b.String())
}

// TestReplaySwitchBoundaryInflightSSECompletesOnOldUpstream F4 主线：在途
// SSE 流旧上游跑完 + 下一请求新供应商 + 进程不重启。
func TestReplaySwitchBoundaryInflightSSECompletesOnOldUpstream(t *testing.T) {
	releaseA := make(chan struct{})
	aRequests := atomic.Int32{}
	aGot := make(chan replayCapture, 4)

	// 上游 A（anthropic 方言）：首片即达后压流不放，放行后补完尾流。
	upA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		aGot <- replayCapture{
			path: r.URL.Path,
			auth: r.Header.Get("Authorization"),
			body: b,
		}
		aRequests.Add(1)
		fl, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(replayAnthropicFrame("message_start", map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id": "msg_old", "type": "message", "role": "assistant", "model": "glm-a-stream",
				"content": []any{}, "stop_reason": nil,
				"usage": map[string]any{"input_tokens": 70, "output_tokens": 1},
			},
		}))
		_, _ = w.Write(replayAnthropicFrame("content_block_start", map[string]any{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "text", "text": ""},
		}))
		_, _ = w.Write(replayAnthropicFrame("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": "旧流首片"},
		}))
		fl.Flush()
		<-releaseA // 在途窗口：流压住，直到测试放行
		_, _ = w.Write(replayAnthropicFrame("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": "旧流尾片"},
		}))
		_, _ = w.Write(replayAnthropicFrame("content_block_stop", map[string]any{
			"type": "content_block_stop", "index": 0,
		}))
		_, _ = w.Write(replayAnthropicFrame("message_delta", map[string]any{
			"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"},
			"usage": map[string]any{"output_tokens": 20},
		}))
		_, _ = w.Write(replayAnthropicFrame("message_stop", map[string]any{"type": "message_stop"}))
		fl.Flush()
	}))
	t.Cleanup(upA.Close)

	// 上游 B（openai_responses 方言）：完整小流，即刻应答。
	upB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		aGot <- replayCapture{path: r.URL.Path, auth: r.Header.Get("Authorization"), body: b}
		fl, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, frame := range [][]byte{
			replayAnthropicFrame("response.created", map[string]any{
				"type": "response.created",
				"response": map[string]any{"id": "resp_b", "object": "response",
					"created_at": 0, "status": "in_progress", "output": []any{}},
			}),
			replayAnthropicFrame("response.output_text.delta", map[string]any{
				"type": "response.output_text.delta", "item_id": "m0", "output_index": 0,
				"content_index": 0, "delta": "新上游首包即达",
			}),
			replayAnthropicFrame("response.completed", map[string]any{
				"type": "response.completed",
				"response": map[string]any{"id": "resp_b", "object": "response", "created_at": 0,
					"status": "completed", "output": []any{},
					"usage": map[string]any{"input_tokens": 30, "output_tokens": 5, "total_tokens": 35}},
			}),
		} {
			_, _ = w.Write(frame)
			fl.Flush()
		}
	}))
	t.Cleanup(upB.Close)

	// 渡口：resolver 注桩（票02 seam），A 活跃；B 条目＝原生 responses 方言。
	res := &stubResolver{}
	entryA := config.DockUpstream{
		BaseURL:  upA.URL,
		APIKey:   "sk-test-switch-a",
		ModelMap: map[string]string{"default": "glm-a", "codex": "glm-a-stream"},
		Dialect:  config.DialectAnthropic,
	}
	entryB := config.DockUpstream{
		BaseURL:  upB.URL,
		APIKey:   "sk-test-switch-b",
		ModelMap: map[string]string{"default": "glm-b", "codex": "glm-b-stream"},
		Dialect:  config.DialectOpenAIResponses,
	}
	res.store("a", map[string]config.DockUpstream{"a": entryA, "b": entryB})

	acc, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewWithOptions("127.0.0.1:15722", upA.URL, Options{
		Upstream: &entryA, Resolver: res, Accounts: acc,
	})
	if err != nil {
		t.Fatal(err)
	}
	counter := &replayServeCounter{inner: srv}
	front := httptest.NewServer(counter)
	t.Cleanup(front.Close)
	frontURL := front.URL

	// 请求 1 → A（翻译分支），增量收集。
	body1, _ := json.Marshal(map[string]any{
		"model":             "gpt-9-fx", // 未知名 → 改写为 A 的 codex 主模型键
		"max_output_tokens": 256,
		"stream":            true,
		"input":             []any{map[string]any{"role": "user", "content": "讲个长故事"}},
	})
	req1, _ := http.NewRequest("POST", frontURL+"/responses", bytes.NewReader(body1))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("Session_id", "sw-inflight")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp1.Body.Close()
	if ct := resp1.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("请求 1 CT = %q", ct)
	}

	type evOut struct {
		name string
		data map[string]any
	}
	events1 := make(chan evOut, 64)
	finished1 := make(chan struct{})
	go func() {
		defer close(finished1)
		sc := bufio.NewScanner(resp1.Body)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		var block strings.Builder
		for sc.Scan() {
			line := sc.Text()
			block.WriteString(line)
			block.WriteString("\n")
			if line == "" {
				name, data := parseSSEBlock(strings.TrimSuffix(block.String(), "\n"))
				block.Reset()
				if data != nil {
					events1 <- evOut{name: name, data: data}
				}
			}
		}
	}()

	// 等在途确立：客户端已收到首片（流穿渡口到达客户端且上游压住中）。
	waitEvent1 := func(want string) evOut {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for {
			select {
			case ev := <-events1:
				if ev.name == want {
					return ev
				}
			case <-deadline:
				t.Fatalf("等待事件 %s 超时", want)
				return evOut{}
			}
		}
	}
	first := waitEvent1("response.output_text.delta")
	if mapStr(first.data, "delta") != "旧流首片" {
		t.Fatalf("首片 = %q, want 旧流首片", mapStr(first.data, "delta"))
	}

	// 上游 A 观测：在途请求持旧条目整套视图（真钥 + codex 主模型键改写）。
	var capA1 replayCapture
	select {
	case capA1 = <-aGot:
	case <-time.After(5 * time.Second):
		t.Fatal("上游 A 未收到在途请求")
	}
	if capA1.auth != "Bearer sk-test-switch-a" || !strings.Contains(string(capA1.body), `"model":"glm-a-stream"`) {
		t.Fatalf("在途请求出站视图 = %q / %s", capA1.auth, capA1.body)
	}
	nAtInflight := counter.n.Load()

	// 此刻热切换：active a → b。
	res.store("b", res.cur.Load().Upstreams)

	// 请求 2 → 必须即刻走 B（原生透传分支），不等 A 放行。
	body2, _ := json.Marshal(map[string]any{
		"model":             "gpt-9-fx",
		"max_output_tokens": 256,
		"stream":            true,
		"input":             []any{map[string]any{"role": "user", "content": "换上游试试"}},
	})
	req2, _ := http.NewRequest("POST", frontURL+"/responses", bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Session_id", "sw-next")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("切换后新请求失败（疑似重启空窗/排水）: %v", err)
	}
	raw2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("请求 2 状态 = %d, body=%s", resp2.StatusCode, raw2)
	}
	if !strings.Contains(string(raw2), "event: response.created") ||
		!strings.Contains(string(raw2), "新上游首包即达") ||
		!strings.Contains(string(raw2), "event: response.completed") {
		t.Fatalf("请求 2 应为 B 原生流逐字节中继: %s", raw2)
	}

	// B 观测：新条目整套视图（新方言+新真钥+新 codex 键改写）。
	select {
	case capB := <-aGot:
		if capB.path != "/v1/responses" {
			t.Fatalf("B 上游路径 = %q, want /v1/responses（原生分支）", capB.path)
		}
		if capB.auth != "Bearer sk-test-switch-b" || !strings.Contains(string(capB.body), `"model":"glm-b-stream"`) {
			t.Fatalf("新请求出站视图 = %q / %s", capB.auth, capB.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("上游 B 未收到切换后请求")
	}

	// 放行 A：在途流在旧上游跑完，不中断不重派。逐事件顺序消费（增量拼
	// 全文，直到 completed 收尾）。
	close(releaseA)
	text := mapStr(first.data, "delta")
	var last evOut
	deadline := time.After(10 * time.Second)
collect:
	for {
		select {
		case ev, ok := <-events1:
			if !ok {
				t.Fatal("事件流提前关闭")
			}
			switch ev.name {
			case "response.output_text.delta":
				text += mapStr(ev.data, "delta")
			case "response.completed":
				last = ev
				break collect
			}
		case <-deadline:
			t.Fatal("等待在途流收尾超时")
		}
	}
	if text != "旧流首片旧流尾片" {
		t.Fatalf("在途流全文 = %q, want 首尾两片齐全（不截断不中断）", text)
	}
	r := asMapT(t, last.data["response"])
	if mapStr(r, "id") != "resp_msg_old" || mapStr(r, "status") != "completed" ||
		mapStr(r, "model") != "glm-a-stream" {
		t.Fatalf("在途流终态 = %v（应旧上游整套视图跑完）", r)
	}
	select {
	case <-finished1:
	case <-time.After(5 * time.Second):
		t.Fatal("在途流未走到 EOF")
	}
	if n := aRequests.Load(); n != 1 {
		t.Fatalf("上游 A 收到 %d 次请求, want 1（在途流不重派）", n)
	}

	// 断言 3：同一 handler 实例持续服务（计数只增不减、两请求都落在同一实
	// 例上、无连接错误）——进程未重启的行为证据；叠加断言 1（在途连接未被
	// 剪断，重启必断流）。
	if nEnd := counter.n.Load(); nEnd != nAtInflight+1 {
		t.Fatalf("handler 计数 = %d, want %d（切换后新请求与在途请求同实例服务，计数无回零）",
			nEnd, nAtInflight+1)
	}
	if front.URL != frontURL {
		t.Fatalf("前门监听地址漂移: %q → %q", frontURL, front.URL)
	}

	// 记账连续性：两请求各记一行，mode 带 lane 标注（同实例同账本）。
	rows := waitDockRows(t, acc, 2)
	bySession := map[string]map[string]any{}
	for _, row := range rows {
		bySession[row["session_id"].(string)] = row
	}
	row1, ok := bySession["sw-inflight"]
	if !ok || row1["mode"] != "codex_translate" {
		t.Fatalf("在途请求记账行缺失/ mode 错: %v", row1)
	}
	row2, ok := bySession["sw-next"]
	if !ok || row2["mode"] != "codex_native" {
		t.Fatalf("切换后请求记账行缺失/ mode 错: %v", row2)
	}
	if row1["model_out"] != "glm-a-stream" || row2["model_out"] != "glm-b-stream" {
		t.Fatalf("两行 model_out = %v / %v（应各持各条目视图）", row1["model_out"], row2["model_out"])
	}
}
