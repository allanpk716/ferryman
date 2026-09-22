package beat

// appendreplay_test.go — 票03：追加重放（HttpBeatSender 姊妹形态）验收钉子。
//
// 覆盖（对齐票验收标准）：
//   - 构造字节保真：与捕获快照 diff 仅末尾追加段（定点拼接，绝不重组——
//     messages 既有条目、其余键值字节、顶层键序逐字节一致）；快照的原始
//     序列化怪癖（转义形态/空白/键序）原样保留；
//   - max_tokens 封顶：数值恰等于封顶时零编辑（纯追加）；超封顶改写仅动
//     该数值子区间；缺失时末尾补键；
//   - 发送：占位令牌头集＋重放标记头（渡口据此不入快照）、上游收到
//     max_tokens=封顶、末条消息=指令、既有 messages 逐字段不变；
//   - SSE 完整解析：text 增量拼接＋stop_reason＋delta usage 覆盖 start；
//   - 错误路径单发即返回（零重试——同模型档不重试的发送侧形态）。
//
// 全部走 httptest mock 上游，零真实网络。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"ferryman/internal/dock"
)

// pureAppendBody messages 为末键、max_tokens 恰等于封顶的夹具：构造后整体
// diff 应=仅末尾追加段（字节保真验收的主证形态）。
const pureAppendBody = `{"model":"glm-5.3","system":"S","max_tokens":4096,` +
	`"messages":[{"role":"user","content":"q1"},{"role":"assistant","content":"a1"}]}`

func TestAppendReplayBodyPureAppendByteFidelity(t *testing.T) {
	out, model, err := AppendReplayBody([]byte(pureAppendBody), 4096, "写交接")
	if err != nil {
		t.Fatal(err)
	}
	if model != "glm-5.3" {
		t.Fatalf("model = %q", model)
	}
	// 唯一编辑＝在 messages 数组闭括号前拼接指令消息：前缀（第一字节到追加
	// 消息之前）与快照逐字节一致，diff 仅末尾追加段。
	want := pureAppendBody[:len(pureAppendBody)-2] +
		`,{"role":"user","content":"写交接"}]}`
	if string(out) != want {
		t.Fatalf("纯追加形态被违反:\n got=%s\nwant=%s", out, want)
	}
	// 前缀逐字节断言（diff 断言的显式形）。
	if string(out[:len(pureAppendBody)-2]) != pureAppendBody[:len(pureAppendBody)-2] {
		t.Fatal("前缀字节保真被违反")
	}
	var probe struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatalf("构造体非 JSON: %v", err)
	}
	if len(probe.Messages) != 3 || probe.Messages[2]["content"] != "写交接" {
		t.Fatalf("messages = %+v, want 原 2 条+追加 1 条", probe.Messages)
	}
}

// quirkyBody 带序列化怪癖的夹具：键序非字母序、messages 居中、消息内空白/
// 转义/非 ASCII 混排——构造必须原样保留这些字节（缓存前缀＝内容序列化）。
const quirkyBody = `{"max_tokens":32000,"stream":true,` +
	`"messages":[{"role":"user",  "content":"中文 中 ✨ '引号'\n"},{"role":"assistant","content":"a"}],` +
	`"model":"glm-5.3","metadata":{"session_id":"s1"}}`

// quirkyMsgs quirkyBody 的 messages 值原文（逐字节照抄）。
const quirkyMsgs = `[{"role":"user",  "content":"中文 中 ✨ '引号'\n"},{"role":"assistant","content":"a"}]`

func TestAppendReplayBodyCappedAndBytePreserving(t *testing.T) {
	out, model, err := AppendReplayBody([]byte(quirkyBody), 1024, "指令X")
	if err != nil {
		t.Fatal(err)
	}
	if model != "glm-5.3" {
		t.Fatalf("model = %q", model)
	}
	want := `{"max_tokens":1024,"stream":true,` +
		`"messages":` + quirkyMsgs[:len(quirkyMsgs)-1] + `,{"role":"user","content":"指令X"}],` +
		`"model":"glm-5.3","metadata":{"session_id":"s1"}}`
	if string(out) != want {
		t.Fatalf("封顶+保真形态被违反:\n got=%s\nwant=%s", out, want)
	}
	// 键序保持（定点拼接、绝不重组的实证）：首键仍为 max_tokens。
	if !strings.HasPrefix(string(out), `{"max_tokens":1024,`) {
		t.Fatal("顶层键序被重排（须定点拼接,不得反序列化重组）")
	}
}

func TestAppendReplayBodyMaxTokensAbsentAppendedAtEnd(t *testing.T) {
	src := `{"model":"m","messages":[{"role":"user","content":"q"}]}`
	out, _, err := AppendReplayBody([]byte(src), 4096, "I")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"m","messages":[{"role":"user","content":"q"},{"role":"user","content":"I"}],` +
		`"max_tokens":4096}`
	if string(out) != want {
		t.Fatalf("缺 max_tokens 补键形态被违反:\n got=%s\nwant=%s", out, want)
	}
}

func TestAppendReplayBodyEmptyMessages(t *testing.T) {
	out, _, err := AppendReplayBody([]byte(`{"messages":[],"max_tokens":100}`), 100, "I")
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"messages":[{"role":"user","content":"I"}],"max_tokens":100}`; string(out) != want {
		t.Fatalf("空数组追加形态被违反:\n got=%s\nwant=%s", out, want)
	}
}

func TestAppendReplayBodyErrors(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"坏JSON", "not json"},
		{"顶层非对象", `[1,2]`},
		{"缺messages", `{"model":"m","max_tokens":1}`},
		{"messages非数组", `{"messages":{},"max_tokens":1}`},
		{"max_tokens非数值", `{"messages":[],"max_tokens":"x"}`},
	}
	for _, tc := range cases {
		if _, _, err := AppendReplayBody([]byte(tc.src), 4096, "I"); err == nil {
			t.Fatalf("%s: 应返回错误", tc.name)
		}
	}
}

// ---- 发送：正常路径（头集+标记头+体形状+SSE 完整解析） ----

func writeHandoffSSE(w http.ResponseWriter, _ *http.Request) {
	writeEvent(w, "message_start",
		`{"type":"message_start","message":{"usage":{"input_tokens":0,`+
			`"cache_read_input_tokens":0,"output_tokens":0}}}`)
	writeEvent(w, "content_block_start",
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	writeEvent(w, "content_block_delta",
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"第一段"}}`)
	writeEvent(w, "content_block_delta",
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"第二段"}}`)
	writeEvent(w, "message_delta",
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},`+
			`"usage":{"input_tokens":120,"cache_read_input_tokens":880,"output_tokens":9}}`)
	writeEvent(w, "message_stop", `{"type":"message_stop"}`)
}

func TestSendAppendReplayHappyPath(t *testing.T) {
	u := newSSEUpstream(t, writeHandoffSSE)
	s := NewHttpBeatSender(u.srv.URL, newStoreWithSession("sess-1"))
	r := s.SendAppendReplay(AppendReplayPlan{
		SessionID: "sess-1", Instruction: "写交接", MaxTokens: 777,
	})
	if !r.Sent || !r.OK || r.Err != "" {
		t.Fatalf("SendAppendReplay = %+v, want Sent=true OK=true", r)
	}
	if r.Text != "第一段第二段" {
		t.Fatalf("Text = %q, want 文本增量拼接", r.Text)
	}
	if r.StopReason != "end_turn" {
		t.Fatalf("StopReason = %q, want end_turn", r.StopReason)
	}
	if r.InputTokens != 120 || r.CacheReadTokens != 880 || r.OutputTokens != 9 {
		t.Fatalf("token = in:%d cache:%d out:%d, want 120/880/9（delta 真值）",
			r.InputTokens, r.CacheReadTokens, r.OutputTokens)
	}
	if r.Model != "claude-sonnet-5[1m]" {
		t.Fatalf("Model = %q", r.Model)
	}
	if n := u.count(); n != 1 {
		t.Fatalf("上游收到 %d 次请求, want 1（零重试）", n)
	}
	hdr, body, path := u.last()
	if path != "/v1/messages" {
		t.Fatalf("路径 = %q, want /v1/messages（与真流量同路径）", path)
	}
	// 头：占位令牌头集（与心跳共用发送通道）＋重放标记头。
	if g := hdr.Get("Authorization"); g != "Bearer "+placeholderToken {
		t.Fatalf("Authorization = %q", g)
	}
	if g := hdr.Get("Accept-Encoding"); g != "identity" {
		t.Fatalf("Accept-Encoding = %q", g)
	}
	if g := hdr.Get("X-Ferryman-Replay"); g != "same_model" {
		t.Fatalf("重放标记头 = %q, want same_model（渡口据此不入快照）", g)
	}
	// 体：max_tokens=封顶；既有 messages 逐字段不变；末条=指令 user 消息。
	var got struct {
		MaxTokens int               `json:"max_tokens"`
		Messages  []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("重放体非 JSON: %v", err)
	}
	if got.MaxTokens != 777 {
		t.Fatalf("max_tokens = %d, want 777（放开至封顶）", got.MaxTokens)
	}
	if len(got.Messages) != 4 {
		t.Fatalf("messages = %d 条, want 3+1", len(got.Messages))
	}
	var last struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(got.Messages[3], &last); err != nil {
		t.Fatal(err)
	}
	if last.Role != "user" || last.Content != "写交接" {
		t.Fatalf("末条消息 = %+v, want 追加的指令 user 消息", last)
	}
	var want struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal([]byte(ccBodyFixture), &want); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if string(got.Messages[i]) != string(want.Messages[i]) {
			t.Fatalf("既有消息 %d 字节被改动（前缀保真违反）:\n got=%s\nwant=%s",
				i, got.Messages[i], want.Messages[i])
		}
	}
}

// stop_reason=tool_use：sender 如实上报（判该档失败在执行器——票03 失败链）。
func TestSendAppendReplayReportsToolUseStop(t *testing.T) {
	u := newSSEUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		writeEvent(w, "message_start",
			`{"type":"message_start","message":{"usage":{"input_tokens":0,`+
				`"cache_read_input_tokens":0,"output_tokens":0}}}`)
		writeEvent(w, "content_block_start",
			`{"type":"content_block_start","index":0,`+
				`"content_block":{"type":"tool_use","id":"t1","name":"Bash"}}`)
		writeEvent(w, "message_delta",
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},`+
				`"usage":{"input_tokens":120,"cache_read_input_tokens":880,"output_tokens":3}}`)
		writeEvent(w, "message_stop", `{"type":"message_stop"}`)
	})
	s := NewHttpBeatSender(u.srv.URL, newStoreWithSession("sess-1"))
	r := s.SendAppendReplay(AppendReplayPlan{SessionID: "sess-1", Instruction: "x", MaxTokens: 64})
	if !r.Sent || !r.OK {
		t.Fatalf("tool_use 是上游结论不是传输错误: %+v", r)
	}
	if r.StopReason != "tool_use" {
		t.Fatalf("StopReason = %q, want tool_use", r.StopReason)
	}
	if r.Text != "" {
		t.Fatalf("tool_use 流无文本增量, Text = %q", r.Text)
	}
}

func TestSendAppendReplayZeroMaxTokensFallsBack(t *testing.T) {
	u := newSSEUpstream(t, writeHandoffSSE)
	s := NewHttpBeatSender(u.srv.URL, newStoreWithSession("sess-1"))
	r := s.SendAppendReplay(AppendReplayPlan{SessionID: "sess-1", Instruction: "x"})
	if !r.Sent || !r.OK {
		t.Fatalf("零值计划应回落默认封顶: %+v", r)
	}
	_, body, _ := u.last()
	if !strings.Contains(string(body), `"max_tokens":4096`) {
		t.Fatalf("零值未回落默认封顶 4096: %.120s", body)
	}
}

func TestSendAppendReplayErrorPaths(t *testing.T) {
	cases := []struct {
		name    string
		sid     string
		setup   func(t *testing.T) (*HttpBeatSender, func() int)
		wantErr string
	}{
		{"snapshot_missing", "sess-x", func(t *testing.T) (*HttpBeatSender, func() int) {
			u := newSSEUpstream(t, nil)
			return NewHttpBeatSender(u.srv.URL, dock.NewSnapshotStore()), u.count
		}, "snapshot_missing"},
		{"bad_snapshot_body", "bad", func(t *testing.T) (*HttpBeatSender, func() int) {
			u := newSSEUpstream(t, nil)
			st := dock.NewSnapshotStore()
			st.Capture("bad", []byte("not json"), snapHdrFixture())
			return NewHttpBeatSender(u.srv.URL, st), u.count
		}, "bad_snapshot_body"},
		{"http_429", "sess-1", func(t *testing.T) (*HttpBeatSender, func() int) {
			u := newSSEUpstream(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusTooManyRequests)
			})
			return NewHttpBeatSender(u.srv.URL, newStoreWithSession("sess-1")), u.count
		}, "http_429"},
		{"sse_incomplete", "sess-1", func(t *testing.T) (*HttpBeatSender, func() int) {
			u := newSSEUpstream(t, func(w http.ResponseWriter, r *http.Request) {
				writeEvent(w, "message_start",
					`{"type":"message_start","message":{"usage":{"input_tokens":5,`+
						`"cache_read_input_tokens":0,"output_tokens":0}}}`)
			})
			return NewHttpBeatSender(u.srv.URL, newStoreWithSession("sess-1")), u.count
		}, "sse_incomplete"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, count := c.setup(t)
			r := s.SendAppendReplay(AppendReplayPlan{SessionID: c.sid, Instruction: "x", MaxTokens: 64})
			if !r.Sent || r.OK || r.Err != c.wantErr {
				t.Fatalf("SendAppendReplay = %+v, want OK=false Err=%q", r, c.wantErr)
			}
			if c.wantErr == "snapshot_missing" || c.wantErr == "bad_snapshot_body" {
				if n := count(); n != 0 {
					t.Fatalf("不该发请求, got %d", n)
				}
			} else if n := count(); n != 1 {
				t.Fatalf("上游收到 %d 次, want 1（零重试）", n)
			}
		})
	}
}
