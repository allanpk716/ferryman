// appendreplay.go — 票03：追加重放（HttpBeatSender 的姊妹形态，ADR-0015 决定一）。
//
// 与心跳重放（原样重放）的异同：
//   - 同：前缀源＝渡口内存主快照（SnapshotStore.Main）、头部＝快照白名单
//     头集＋占位令牌字面量＋accept-encoding 固定 identity、发往渡口入站口
//     （与真流量同路径同改写）、F1 零重试错误语义、绝不 panic；
//   - 异：体构造＝前缀字节一个不动、末尾追加一条摆渡指令 user 消息、
//     max_tokens 放开至封顶（心跳是改写→1）；响应解析取全文（text 增量＋
//     stop_reason＋usage，心跳只取 usage）；时限＝分钟级（交接生成慢）——
//     调用方在独立 goroutine 执行，不占守望线程（beat.go Sender 注释的
//     「秒级内必返回」时限铁律由调用方承载，本形态不适用）；
//   - 重放标记：请求带 x-ferryman-replay 头，渡口据此不入快照（追加体严格
//     大于主快照，照常 Capture 会顶替主快照造成追加雪球——dock/replayguard.go）；
//     改写模式出站剥离该头，不泄漏上游。
package beat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"ferryman/internal/dock"
)

// DefaultAppendMaxTokens max_tokens 封顶默认（保守值；ferry.SameModelMaxTokens
// 为可配缝，此处仅防零值计划回落）。
const DefaultAppendMaxTokens = 4096

// appendReplayTimeout 单次追加重放总超时：交接生成是分钟级（CJK ≤10K token
// 输出）。执行在调用方的独立 goroutine，超时只界定期限，不占守望线程。
const appendReplayTimeout = 300 * time.Second

// AppendReplayPlan 追加重放计划。
type AppendReplayPlan struct {
	SessionID   string
	Instruction string // 末尾追加的 user 消息内容（摆渡指令）
	MaxTokens   int    // 放开后的 max_tokens（封顶值；≤0 回落默认）
}

// AppendReplayResult 追加重放结果。Sent/OK 语义同 BeatResult（OK=false＝
// 一切传输/SSE 异常，类别见 Err）；OK=true 时 StopReason/Text/三列 token
// 可用。stop_reason=tool_use 与输出是否合交接 MD 结构的判定在执行器
// （票03 失败链）——sender 只报事实。
type AppendReplayResult struct {
	Sent            bool
	OK              bool
	StopReason      string
	Text            string
	InputTokens     int
	CacheReadTokens int
	OutputTokens    int
	Model           string
	Err             string
}

// AppendReplaySender 追加重放发送接口（watcher 注入缝；HttpBeatSender 实现）。
type AppendReplaySender interface {
	SendAppendReplay(p AppendReplayPlan) AppendReplayResult
}

// 编译期接口形状钉死。
var _ AppendReplaySender = (*HttpBeatSender)(nil)

// SendAppendReplay 单发追加重放（零重试——「同模型档不重试」由本语义承载）。
func (s *HttpBeatSender) SendAppendReplay(p AppendReplayPlan) AppendReplayResult {
	if p.MaxTokens <= 0 {
		p.MaxTokens = DefaultAppendMaxTokens
	}
	if s.store == nil {
		return AppendReplayResult{Sent: true, OK: false, Err: errSnapshotMissing}
	}
	snap, ok := s.store.Main(p.SessionID)
	if !ok {
		return AppendReplayResult{Sent: true, OK: false, Err: errSnapshotMissing}
	}
	body, model, err := AppendReplayBody(snap.Body, p.MaxTokens, p.Instruction)
	if err != nil {
		return AppendReplayResult{Sent: true, OK: false, Err: errBadBody}
	}
	timeout := s.appendTimeout
	if timeout <= 0 { // 零值构造防御：回落默认（正常构造恒正）
		timeout = appendReplayTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(s.dockURL, "/")+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return AppendReplayResult{Sent: true, OK: false, Err: errConn}
	}
	beatHeaders(req.Header, snap.Headers)
	req.Header.Set(dock.HeaderFerrymanReplay, "same_model") // 渡口识别：不入快照
	resp, err := s.client.Do(req)
	if err != nil {
		return AppendReplayResult{Sent: true, OK: false, Err: transportErrCategory(err)}
	}
	defer resp.Body.Close()
	switch { // 状态码语义（F1 不重试；与 Send 同款类别）
	case resp.StatusCode == http.StatusTooManyRequests:
		return AppendReplayResult{Sent: true, OK: false, Err: "http_429"}
	case resp.StatusCode >= 500:
		return AppendReplayResult{Sent: true, OK: false, Err: "http_5xx"}
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return AppendReplayResult{Sent: true, OK: false, Err: fmt.Sprintf("http_%d", resp.StatusCode)}
	}
	if resp.Header.Get("Content-Encoding") != "" {
		return AppendReplayResult{Sent: true, OK: false, Err: errContentEncoding}
	}
	msg, errCat := parseSSEMessage(resp.Body)
	if errCat != "" {
		return AppendReplayResult{Sent: true, OK: false, Err: errCat}
	}
	return AppendReplayResult{
		Sent:            true,
		OK:              true,
		StopReason:      msg.stopReason,
		Text:            strings.Join(msg.textParts, ""),
		InputTokens:     msg.usage.input,
		CacheReadTokens: msg.usage.cacheRead,
		OutputTokens:    msg.usage.output,
		Model:           model, // 快照体顶层 model——handoff 科目行的记账元数据
	}
}

// ---- 体构造：定点字节拼接（字节保真的机制根基） ----

// AppendReplayBody 追加重放体构造：前缀字节一个不动，在 messages 数组末尾
// 追加一条 user 消息，顶层 max_tokens 置为封顶值。返回（新体, 顶层 model）。
//
// 字节保真（票面验收）：全部编辑都是源字节区间上的定点拼接（先定位、后按
// 偏移降序替换），绝不反序列化重组——messages 既有条目、system/tools 等其余
// 键的值字节、顶层键序，全部与捕获快照逐字节一致；快照的原始序列化怪癖
// （转义形态/空白/键序）原样保留（缓存前缀＝内容序列化，保真优先）。
// max_tokens 数值恰等于封顶时该键零编辑，整体 diff＝仅末尾追加段；须改写时
// 仅该数值子区间受影响，「第一字节到追加消息之前」的前缀仍逐字节一致。
func AppendReplayBody(src []byte, maxTokens int, appendContent string) ([]byte, string, error) {
	dec := json.NewDecoder(bytes.NewReader(src))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, "", err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, "", errors.New("appendreplay: 快照体顶层非 JSON 对象")
	}
	type valueSpan struct {
		start, end int // 值字节区间 [start,end) 于 src 内
	}
	var msgs, mt *valueSpan
	var msgsRaw, mtRaw json.RawMessage
	model := ""
	for dec.More() {
		ktok, err := dec.Token()
		if err != nil {
			return nil, "", err
		}
		key, ok := ktok.(string)
		if !ok {
			return nil, "", errors.New("appendreplay: 快照体对象键非字符串")
		}
		before := dec.InputOffset()
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, "", err
		}
		after := dec.InputOffset()
		// raw 是 decoder 拷出的值字节；在 [before,after) 窗口（内容＝冒号+
		// 空白+值）内定位其绝对起点。
		rel := bytes.Index(src[before:after], raw)
		if rel < 0 {
			return nil, "", errors.New("appendreplay: 值区间定位失败（内部错误）")
		}
		start := int(before) + rel
		switch key {
		case "messages":
			msgsRaw = raw
			msgs = &valueSpan{start, start + len(raw)}
		case "max_tokens":
			mtRaw = raw
			mt = &valueSpan{start, start + len(raw)}
		case "model":
			_ = json.Unmarshal(raw, &model) // 非 string 型静默取空——记账降级不炸构造
		}
	}
	if _, err := dec.Token(); err != nil { // 收 '}'（尾随垃圾在此暴露）
		return nil, "", err
	}
	if msgs == nil {
		return nil, "", errors.New("appendreplay: 快照体缺 messages")
	}
	appendMsg, err := marshalAppendMsg(appendContent)
	if err != nil {
		return nil, "", err
	}
	// messages 值去尾空白后必以 ']' 收——插入点在该 ']' 前（区间只覆到 ']'，
	// 可能的值内尾空白留在替换段之后，JSON 仍合法）。
	trimmed := bytes.TrimRight(msgsRaw, " \t\r\n")
	if len(trimmed) < 2 || trimmed[0] != '[' || trimmed[len(trimmed)-1] != ']' {
		return nil, "", errors.New("appendreplay: messages 非 JSON 数组")
	}
	var newMsgs []byte
	if len(bytes.TrimSpace(trimmed[1:len(trimmed)-1])) == 0 { // 空数组：首元素
		newMsgs = append(appendMsg, ']')
		newMsgs = append([]byte("["), newMsgs...)
	} else {
		newMsgs = make([]byte, 0, len(trimmed)+len(appendMsg)+2)
		newMsgs = append(newMsgs, trimmed[:len(trimmed)-1]...)
		newMsgs = append(newMsgs, ',')
		newMsgs = append(newMsgs, appendMsg...)
		newMsgs = append(newMsgs, ']')
	}
	type edit struct {
		start, end int
		val        []byte
	}
	edits := []edit{{msgs.start, msgs.start + len(trimmed), newMsgs}}
	if mt != nil {
		n, err := strconv.ParseInt(strings.TrimSpace(string(mtRaw)), 10, 64)
		if err != nil {
			return nil, "", errors.New("appendreplay: max_tokens 非数值")
		}
		if n != int64(maxTokens) { // 恰等于封顶：零编辑（纯追加形态）
			edits = append(edits, edit{mt.start, mt.end, []byte(strconv.Itoa(maxTokens))})
		}
	} else {
		// 缺 max_tokens：对象闭括号前补键（追加在真末尾，前缀零扰动）。
		pos := -1
		for i := len(src) - 1; i >= 0; i-- {
			c := src[i]
			if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
				continue // 尾随空白跳过
			}
			if c == '}' {
				pos = i
			}
			break // 第一个非空白字节（顶层值已解析为对象，正常即 '}'）
		}
		if pos < 0 {
			return nil, "", errors.New("appendreplay: 对象闭括号定位失败")
		}
		edits = append(edits, edit{pos, pos, []byte(`,"max_tokens":` + strconv.Itoa(maxTokens))})
	}
	// 定点替换：按起点降序应用，偏移不失效；各编辑区间互不重叠。
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	out := src
	for _, e := range edits {
		nb := make([]byte, 0, e.start+len(e.val)+len(out)-e.end)
		nb = append(nb, out[:e.start]...)
		nb = append(nb, e.val...)
		nb = append(nb, out[e.end:]...)
		out = nb
	}
	return out, model, nil
}

// marshalAppendMsg 追加消息的确定性序列化（键序 role,content；关 HTML 转义
// ——非 ASCII 直出，与账本 compactJSON 同口径）。
func marshalAppendMsg(content string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{"user", content}); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// ---- SSE 完整解析（text 增量＋stop_reason＋usage） ----

// appendSSE 追加重放需要的完整 SSE 消息累积。
type appendSSE struct {
	usage      sseUsage
	stopReason string
	textParts  []string
	sawDelta   bool
}

// appendDeltaJSON content_block_delta 的 delta 与 message_delta 的 delta 合体
// （字段按事件类型出现，互不干扰）。
type appendDeltaJSON struct {
	Type       string `json:"type"`
	Text       string `json:"text"`
	StopReason string `json:"stop_reason"`
}

type appendSSEEventJSON struct {
	Type    string `json:"type"`
	Message *struct {
		Usage *usageJSON `json:"usage"`
	} `json:"message"`
	Delta *appendDeltaJSON `json:"delta"`
	Usage *usageJSON       `json:"usage"`
}

// parseSSEMessage 逐事件读 SSE 至 EOF，返回完整消息累积。完成判据＝流读完且
// 见过 message_delta（stop_reason 所在事件——与 parseSSEUsage「缺 delta 视为
// 截断流」同纪律，绝不以 start 的值记 OK）。
func parseSSEMessage(r io.Reader) (appendSSE, string) {
	var acc appendSSE
	apply := func(u *usageJSON) {
		if u == nil {
			return
		}
		if u.InputTokens != nil {
			acc.usage.input = *u.InputTokens
		}
		if u.CacheReadInputTokens != nil {
			acc.usage.cacheRead = *u.CacheReadInputTokens
		}
		if u.OutputTokens != nil {
			acc.usage.output = *u.OutputTokens
		}
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // parseSSEUsage 同款上限
	var data []string
	flushEvent := func() string {
		if len(data) == 0 {
			return ""
		}
		payload := strings.Join(data, "\n")
		data = data[:0]
		var ev appendSSEEventJSON
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			return errSSE
		}
		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				apply(ev.Message.Usage)
			}
		case "content_block_delta":
			if ev.Delta != nil && ev.Delta.Type == "text_delta" {
				acc.textParts = append(acc.textParts, ev.Delta.Text)
			}
		case "message_delta":
			acc.sawDelta = true
			if ev.Delta != nil && ev.Delta.StopReason != "" {
				acc.stopReason = ev.Delta.StopReason
			}
			apply(ev.Usage)
		}
		return ""
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if cat := flushEvent(); cat != "" {
				return acc, cat
			}
		case strings.HasPrefix(line, "data:"):
			v := strings.TrimPrefix(line, "data:")
			v = strings.TrimPrefix(v, " ")
			data = append(data, v)
		}
	}
	if cat := flushEvent(); cat != "" {
		return acc, cat
	}
	if err := sc.Err(); err != nil {
		return acc, transportErrCategory(err)
	}
	if !acc.sawDelta {
		return acc, errSSEIncomplete
	}
	return acc, ""
}
