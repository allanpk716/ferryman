package beat

// httpsender_test.go — 票03：HttpBeatSender（快照重放发送器）验收钉子。
//
// 覆盖（对齐票验收标准）：
//   - max_tokens 唯一改写（messages/system/tools 等逐字段不变）＋正常路径
//     OK＋三列 token（mock 上游 SSE 流式分块写）；
//   - message_delta usage 覆盖 message_start（Q14：start 恒 0，delta 才是真值）；
//   - 错误路径（429/500/超时/连接拒绝）单次请求即返回（F1：不重试）＋类别正确；
//   - snapshot_missing（无该会话快照，不发请求）；
//   - 头部重建：mock 收到的头＝快照头集＋占位令牌字面量＋identity，零新增头；
//   - 防御：响应带 content-encoding／SSE 缺 message_delta／data 非法 JSON。
//
// 全部走 httptest mock 上游，零真实网络、零消息内容断言（隐私铁律：只测
// 外部行为与元数据）。

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"ferryman/internal/dock"
)

// ---- 夹具 ----

// ccBodyFixture 仿真 CC /v1/messages 请求体：顶层键齐全（model/system/tools/
// messages/metadata/stream/max_tokens）——"唯一改写 max_tokens"断言的对象。
const ccBodyFixture = `{"model":"claude-sonnet-5[1m]","max_tokens":32000,"stream":true,` +
	`"system":[{"type":"text","text":"You are Claude Code.","cache_control":{"type":"ephemeral"}}],` +
	`"tools":[{"name":"Bash","description":"Runs a command","input_schema":{"type":"object",` +
	`"properties":{"command":{"type":"string"}},"required":["command"]}}],` +
	`"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]},` +
	`{"role":"assistant","content":[{"type":"text","text":"hi"}]},` +
	`{"role":"user","content":[{"type":"text","text":"go on"}]}],` +
	`"metadata":{"user_id":"user_abc","session_id":"sess-1"}}`

// snapHdrFixture 快照头集（票01 白名单六件；accept-encoding 是真 CC 的值——
// 发送侧必须以 identity 覆盖，不照抄）。
func snapHdrFixture() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Anthropic-Version", "2023-06-01")
	h.Set("Anthropic-Beta", "claude-code-20250219,context-1m-2025-08-07")
	h.Set("User-Agent", "claude-cli/2.0.0 (external, cli)")
	h.Set("Accept", "text/event-stream")
	h.Set("Accept-Encoding", "gzip, deflate")
	return h
}

// newStoreWithSession 建快照库并捕获一份会话（走 Capture＝与渡口捕获同路径，
// 白名单头提取一并被夹具化）。
func newStoreWithSession(sessionID string) *dock.SnapshotStore {
	st := dock.NewSnapshotStore()
	st.Capture(sessionID, []byte(ccBodyFixture), snapHdrFixture())
	return st
}

// writeEvent 写一个 SSE 事件并冲刷——逐事件 Flush 钉死流式分块路径
// （渡口 FlushInterval:-1 透传下，beat 侧必须逐事件可解析）。
func writeEvent(w io.Writer, name, data string) {
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// writeNormalSSE 正常路径脚本：message_start（usage 全 0——GLM 实测）→
// 内容块事件（解析器必须跳过）→ message_delta（真 usage）→ message_stop。
func writeNormalSSE(w http.ResponseWriter) {
	writeEvent(w, "message_start",
		`{"type":"message_start","message":{"id":"msg_1",`+
			`"usage":{"input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}`)
	writeEvent(w, "content_block_start",
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	writeEvent(w, "content_block_delta",
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"."}}`)
	writeEvent(w, "message_delta",
		`{"type":"message_delta","delta":{"stop_reason":"max_tokens"},`+
			`"usage":{"input_tokens":120,"cache_read_input_tokens":880,"output_tokens":1}}`)
	writeEvent(w, "message_stop", `{"type":"message_stop"}`)
}

// sseUpstream mock 上游：互斥锁计数与留存收到的请求（头/体/路径），响应
// 脚本可注入（nil＝writeNormalSSE）。
type sseUpstream struct {
	mu       sync.Mutex
	requests int
	header   http.Header
	body     []byte
	path     string
	resp     func(w http.ResponseWriter, r *http.Request)
	srv      *httptest.Server
}

func newSSEUpstream(t *testing.T, resp func(w http.ResponseWriter, r *http.Request)) *sseUpstream {
	t.Helper()
	u := &sseUpstream{resp: resp}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.requests++
		u.header = r.Header.Clone()
		u.body = body
		u.path = r.URL.Path
		u.mu.Unlock()
		if u.resp != nil {
			u.resp(w, r)
			return
		}
		writeNormalSSE(w)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *sseUpstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.requests
}

func (u *sseUpstream) last() (http.Header, []byte, string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.header, u.body, u.path
}

// freeClosedPort 拿一个确定已关闭的本机端口（连接拒绝夹具）。
func freeClosedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return fmt.Sprintf("%d", port)
}

// ---- 正常路径：max_tokens 唯一改写＋头集重建＋delta usage ----

func TestSendNormalPathRewriteHeadersUsage(t *testing.T) {
	u := newSSEUpstream(t, nil)
	s := NewHttpBeatSender(u.srv.URL, newStoreWithSession("sess-1"))
	r := s.Send(BeatPlan{SessionID: "sess-1"})

	if !r.Sent || !r.OK {
		t.Fatalf("Send = %+v, want Sent=true OK=true", r)
	}
	if r.Err != "" {
		t.Errorf("Err = %q, want 空", r.Err)
	}
	if r.InputTokens != 120 || r.CacheReadTokens != 880 || r.OutputTokens != 1 {
		t.Errorf("token = in:%d cache:%d out:%d, want 120/880/1（message_delta 真值）",
			r.InputTokens, r.CacheReadTokens, r.OutputTokens)
	}
	if r.Model != "claude-sonnet-5[1m]" {
		t.Errorf("Model = %q, want 快照体顶层 model（记账元数据）", r.Model)
	}

	if n := u.count(); n != 1 {
		t.Fatalf("上游收到 %d 次请求, want 1（F1：不重试）", n)
	}
	hdr, body, path := u.last()
	if path != "/v1/messages" {
		t.Errorf("请求路径 = %q, want /v1/messages（与真流量同路径）", path)
	}

	// 体：唯一改写 max_tokens→1；其余键（含 messages/system/tools 嵌套）逐字段不变。
	var got, want map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("重放体非 JSON: %v (%q)", err, body)
	}
	if err := json.Unmarshal([]byte(ccBodyFixture), &want); err != nil {
		t.Fatal(err)
	}
	if got["max_tokens"] != float64(1) {
		t.Errorf("max_tokens = %v, want 1", got["max_tokens"])
	}
	delete(got, "max_tokens")
	delete(want, "max_tokens")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("重放体除 max_tokens 外逐字段不变被违反:\n got=%v\nwant=%v", got, want)
	}

	// 头：快照头集（五件内容头）＋auth 占位令牌字面量＋identity，零新增应用头。
	wantHdr := map[string]string{
		"Content-Type":      "application/json",
		"Anthropic-Version": "2023-06-01",
		"Anthropic-Beta":    "claude-code-20250219,context-1m-2025-08-07",
		"User-Agent":        "claude-cli/2.0.0 (external, cli)",
		"Accept":            "text/event-stream",
		"Accept-Encoding":   "identity", // 固定覆盖快照的 gzip 值
		"Authorization":     "Bearer PROXY_MANAGED",
		"X-Api-Key":         "PROXY_MANAGED",
	}
	// Content-Length＝Go client 随 body 自生的传输框架头（Host 同理不入 map），
	// 真实 CC 流量也携带——不算应用层新增头，白名单豁免。
	framing := map[string]bool{"Content-Length": true}
	for k := range hdr {
		if _, ok := wantHdr[k]; !ok && !framing[k] {
			t.Errorf("新增应用头 %s=%q, want 零新增", k, hdr.Get(k))
		}
	}
	for k, v := range wantHdr {
		if g := hdr.Get(k); g != v {
			t.Errorf("头 %s = %q, want %q", k, g, v)
		}
	}
}

// ---- message_delta 覆盖 message_start：start 带非零值也必须被 delta 盖掉 ----

func TestSendDeltaUsageOverridesStart(t *testing.T) {
	u := newSSEUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		writeEvent(w, "message_start",
			`{"type":"message_start","message":{"usage":{"input_tokens":999,`+
				`"cache_read_input_tokens":0,"output_tokens":0}}}`)
		writeEvent(w, "message_delta",
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},`+
				`"usage":{"input_tokens":120,"cache_read_input_tokens":880,"output_tokens":1}}`)
		writeEvent(w, "message_stop", `{"type":"message_stop"}`)
	})
	s := NewHttpBeatSender(u.srv.URL, newStoreWithSession("sess-1"))
	r := s.Send(BeatPlan{SessionID: "sess-1"})
	if !r.Sent || !r.OK {
		t.Fatalf("Send = %+v, want Sent=true OK=true", r)
	}
	// start 的 input=999 必须被 delta 的 120 覆盖（Q14：delta 是唯一可信口径）
	if r.InputTokens != 120 || r.CacheReadTokens != 880 || r.OutputTokens != 1 {
		t.Errorf("token = in:%d cache:%d out:%d, want 120/880/1（delta 覆盖 start）",
			r.InputTokens, r.CacheReadTokens, r.OutputTokens)
	}
}

// ---- 错误路径：单次请求即返回（F1 不重试）＋类别正确 ----

func TestSendTransportErrorsSingleRequestNoRetry(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T) (url string, count func() int, shorten bool)
		wantErr string
	}{
		{
			name: "429",
			setup: func(t *testing.T) (string, func() int, bool) {
				u := newSSEUpstream(t, func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusTooManyRequests)
				})
				return u.srv.URL, u.count, false
			},
			wantErr: "http_429",
		},
		{
			name: "500",
			setup: func(t *testing.T) (string, func() int, bool) {
				u := newSSEUpstream(t, func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				})
				return u.srv.URL, u.count, false
			},
			wantErr: "http_5xx",
		},
		{
			name: "总超时（流挂起不收尾）",
			setup: func(t *testing.T) (string, func() int, bool) {
				u := newSSEUpstream(t, func(w http.ResponseWriter, r *http.Request) {
					writeEvent(w, "message_start",
						`{"type":"message_start","message":{"usage":{"input_tokens":0,`+
							`"cache_read_input_tokens":0,"output_tokens":0}}}`)
					<-r.Context().Done() // 挂死：客户端秒级总超时必须掐断
				})
				return u.srv.URL, u.count, true
			},
			wantErr: "timeout",
		},
		{
			name: "连接拒绝",
			setup: func(t *testing.T) (string, func() int, bool) {
				// 已关闭端口：无可达服务（无计数面），失败即返回＝不重试的可观测形
				return "http://127.0.0.1:" + freeClosedPort(t), nil, false
			},
			wantErr: "conn_error",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			url, count, shorten := c.setup(t)
			s := NewHttpBeatSender(url, newStoreWithSession("sess-1"))
			if shorten {
				s.timeout = 500 * time.Millisecond // 秒级铁律不动摇，测试缩短口径
			}
			r := s.Send(BeatPlan{SessionID: "sess-1"})
			if !r.Sent || r.OK {
				t.Fatalf("Send = %+v, want Sent=true OK=false", r)
			}
			if r.Err != c.wantErr {
				t.Fatalf("Err = %q, want %q", r.Err, c.wantErr)
			}
			if count != nil {
				if n := count(); n != 1 {
					t.Fatalf("上游收到 %d 次请求, want 1（F1：不重试）", n)
				}
			}
		})
	}
}

// ---- snapshot_missing：无该会话快照（如 daemon 重启后窗口仍调度） ----

func TestSendSnapshotMissing(t *testing.T) {
	u := newSSEUpstream(t, nil)
	s := NewHttpBeatSender(u.srv.URL, dock.NewSnapshotStore()) // 空库：无 sess-x
	r := s.Send(BeatPlan{SessionID: "sess-x"})
	if !r.Sent || r.OK {
		t.Fatalf("Send = %+v, want Sent=true OK=false", r)
	}
	if r.Err != "snapshot_missing" {
		t.Fatalf("Err = %q, want snapshot_missing", r.Err)
	}
	if n := u.count(); n != 0 {
		t.Fatalf("无快照不得发请求, got %d", n)
	}
}

// ---- 防御路径 ----

func TestSendContentEncodingIsError(t *testing.T) {
	// 请求侧已固定 identity；响应仍带 content-encoding＝不可解析流，按错误处理
	u := newSSEUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		writeNormalSSE(w) // 明文体＋伪称 gzip
	})
	s := NewHttpBeatSender(u.srv.URL, newStoreWithSession("sess-1"))
	r := s.Send(BeatPlan{SessionID: "sess-1"})
	if !r.Sent || r.OK || r.Err != "content_encoding" {
		t.Fatalf("Send = %+v, want OK=false Err=content_encoding", r)
	}
}

func TestSendSSEIncompleteWithoutDeltaUsage(t *testing.T) {
	// 2xx 但流终（EOF）无 message_delta：usage 不可信，绝不以 start 的值记 OK
	u := newSSEUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		writeEvent(w, "message_start",
			`{"type":"message_start","message":{"usage":{"input_tokens":5,`+
				`"cache_read_input_tokens":0,"output_tokens":0}}}`)
	})
	s := NewHttpBeatSender(u.srv.URL, newStoreWithSession("sess-1"))
	r := s.Send(BeatPlan{SessionID: "sess-1"})
	if !r.Sent || r.OK || r.Err != "sse_incomplete" {
		t.Fatalf("Send = %+v, want OK=false Err=sse_incomplete", r)
	}
}

func TestSendSSEMalformedData(t *testing.T) {
	u := newSSEUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		writeEvent(w, "message_start", `{"type":"message_start","broken`)
	})
	s := NewHttpBeatSender(u.srv.URL, newStoreWithSession("sess-1"))
	r := s.Send(BeatPlan{SessionID: "sess-1"})
	if !r.Sent || r.OK || r.Err != "sse_error" {
		t.Fatalf("Send = %+v, want OK=false Err=sse_error", r)
	}
}

func TestSendBadSnapshotBody(t *testing.T) {
	u := newSSEUpstream(t, nil)
	st := dock.NewSnapshotStore()
	st.Capture("bad", []byte("not json"), snapHdrFixture())
	s := NewHttpBeatSender(u.srv.URL, st)
	r := s.Send(BeatPlan{SessionID: "bad"})
	if !r.Sent || r.OK || r.Err != "bad_snapshot_body" {
		t.Fatalf("Send = %+v, want OK=false Err=bad_snapshot_body", r)
	}
	if n := u.count(); n != 0 {
		t.Fatalf("快照体坏不得发请求, got %d", n)
	}
}

// ---- 构造默认值 ----

func TestNewHttpBeatSenderDefaults(t *testing.T) {
	s := NewHttpBeatSender("", dock.NewSnapshotStore())
	if s.dockURL != DefaultDockURL {
		t.Errorf("dockURL = %q, want 默认 %q", s.dockURL, DefaultDockURL)
	}
	if s.timeout != beatTimeout {
		t.Errorf("timeout = %v, want %v", s.timeout, beatTimeout)
	}
}
