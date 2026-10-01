// codex_translate_stream.go — 票03：响应向流式翻译（Anthropic SSE →
// responses SSE，对照表 §2.2 事件流总表 + §2.5 异形态兜底）。
//
// 状态机移植参考实现 streaming_codex_anthropic.rs：output_index 按上游
// content block 到达顺序单调分配（非 Anthropic index 原值）；item id 按
// 块类型定形（message=resp_msg_N / reasoning=rs_resp_N / fc_|ctc_+call_id）；
// call_id 原样透传。SSE 帧统一 event:<名>\ndata:<JSON>\n\n。
package dock

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// ---- SSE 帧解析件 ----

// takeSSEBlock 取出缓冲里首个 SSE 事件块（\n\n 或 \r\n\r\n 分隔，先到者），
// 返回 (块, 剩余缓冲, 是否取到)。
func takeSSEBlock(buffer string) (string, string, bool) {
	best := -1
	bestLen := 0
	if i := strings.Index(buffer, "\r\n\r\n"); i >= 0 {
		best, bestLen = i, 4
	}
	if j := strings.Index(buffer, "\n\n"); j >= 0 && (best < 0 || j < best) {
		best, bestLen = j, 2
	}
	if best < 0 {
		return "", buffer, false
	}
	return buffer[:best], buffer[best+bestLen:], true
}

// parseSSEBlock 解析单个事件块：收集 event:/data: 行（data 多行以 \n 拼接，
// 字段名后空格可选）；data 非 JSON → data=nil（ping/注释等跳过）。事件名
// 取 data.type 优先、event: 名兜底（由调用方合成，这里都返回）。
func parseSSEBlock(block string) (eventName string, data map[string]any) {
	var dataParts []string
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if rest, ok := stripSSEField(line, "event"); ok {
			eventName = strings.TrimSpace(rest)
		}
		if rest, ok := stripSSEField(line, "data"); ok {
			dataParts = append(dataParts, rest)
		}
	}
	if len(dataParts) == 0 {
		return eventName, nil
	}
	v, ok := decodeJSONValue([]byte(strings.Join(dataParts, "\n")))
	if !ok {
		return eventName, nil
	}
	m, _ := v.(map[string]any)
	return eventName, m
}

// stripSSEField "field: value" / "field:value" 行拆解。
func stripSSEField(line, field string) (string, bool) {
	if strings.HasPrefix(line, field+": ") {
		return line[len(field)+2:], true
	}
	if strings.HasPrefix(line, field+":") {
		return line[len(field)+1:], true
	}
	return "", false
}

// ---- SSE 帧构造件（codex_responses_sse.rs 同形） ----

// sseFrame 统一帧格式。
func sseFrame(event string, data map[string]any) []byte {
	var b strings.Builder
	b.WriteString("event: ")
	b.WriteString(event)
	b.WriteString("\ndata: ")
	b.Write(encodeCompact(data))
	b.WriteString("\n\n")
	return []byte(b.String())
}

func sseLifecycleEvent(event, status string, response map[string]any) []byte {
	payload := map[string]any{"type": event, "response": response}
	return sseFrame(event, payload)
}

func outputItemEvent(event string, outputIndex int, item map[string]any) []byte {
	return sseFrame(event, map[string]any{
		"type": event, "output_index": outputIndex, "item": item,
	})
}

// messageItemAdded 文本块的 in_progress message 项 added。
func messageItemAdded(outputIndex int, itemID string) []byte {
	return outputItemEvent("response.output_item.added", outputIndex, map[string]any{
		"id": itemID, "type": "message", "status": "in_progress",
		"role": "assistant", "content": []any{},
	})
}

func messageContentPartAdded(outputIndex int, itemID string) []byte {
	return sseFrame("response.content_part.added", map[string]any{
		"type": "response.content_part.added", "item_id": itemID,
		"output_index": outputIndex, "content_index": 0,
		"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
	})
}

func outputTextDelta(outputIndex int, itemID, delta string) []byte {
	return sseFrame("response.output_text.delta", map[string]any{
		"type": "response.output_text.delta", "item_id": itemID,
		"output_index": outputIndex, "content_index": 0, "delta": delta,
	})
}

// messageItemDone 完成态 message 项。
func messageItemDone(itemID, text string) map[string]any {
	return map[string]any{
		"id": itemID, "type": "message", "status": "completed", "role": "assistant",
		"content": []any{
			map[string]any{"type": "output_text", "text": text, "annotations": []any{}},
		},
	}
}

// messageClose 文本块收口：output_text.done → content_part.done →
// output_item.done；返回事件序列与完成态项。
func messageClose(outputIndex int, itemID, text string) ([][]byte, map[string]any) {
	item := messageItemDone(itemID, text)
	return [][]byte{
		sseFrame("response.output_text.done", map[string]any{
			"type": "response.output_text.done", "item_id": itemID,
			"output_index": outputIndex, "content_index": 0, "text": text,
		}),
		sseFrame("response.content_part.done", map[string]any{
			"type": "response.content_part.done", "item_id": itemID,
			"output_index": outputIndex, "content_index": 0,
			"part": map[string]any{"type": "output_text", "text": text, "annotations": []any{}},
		}),
		outputItemEvent("response.output_item.done", outputIndex, item),
	}, item
}

func reasoningItemAdded(outputIndex int, itemID string) []byte {
	return outputItemEvent("response.output_item.added", outputIndex, map[string]any{
		"id": itemID, "type": "reasoning", "status": "in_progress", "summary": []any{},
	})
}

func reasoningSummaryPartAdded(outputIndex int, itemID string) []byte {
	return sseFrame("response.reasoning_summary_part.added", map[string]any{
		"type": "response.reasoning_summary_part.added", "item_id": itemID,
		"output_index": outputIndex, "summary_index": 0,
		"part": map[string]any{"type": "summary_text", "text": ""},
	})
}

func reasoningSummaryTextDelta(outputIndex int, itemID, delta string) []byte {
	return sseFrame("response.reasoning_summary_text.delta", map[string]any{
		"type": "response.reasoning_summary_text.delta", "item_id": itemID,
		"output_index": outputIndex, "summary_index": 0, "delta": delta,
	})
}

// reasoningCloseWithItem thinking 块收口（可见时先 done 两个 summary 事件；
// 完成态 reasoning 项无 status 字段——两转换器共同契约）。
func reasoningCloseWithItem(outputIndex int, itemID, text string, item map[string]any, visible bool) [][]byte {
	var events [][]byte
	if visible {
		events = append(events,
			sseFrame("response.reasoning_summary_text.done", map[string]any{
				"type": "response.reasoning_summary_text.done", "item_id": itemID,
				"output_index": outputIndex, "summary_index": 0, "text": text,
			}),
			sseFrame("response.reasoning_summary_part.done", map[string]any{
				"type": "response.reasoning_summary_part.done", "item_id": itemID,
				"output_index": outputIndex, "summary_index": 0,
				"part": map[string]any{"type": "summary_text", "text": text},
			}),
		)
	}
	return append(events, outputItemEvent("response.output_item.done", outputIndex, item))
}

func functionCallArgumentsDelta(outputIndex int, itemID, delta string) []byte {
	return sseFrame("response.function_call_arguments.delta", map[string]any{
		"type": "response.function_call_arguments.delta", "item_id": itemID,
		"output_index": outputIndex, "delta": delta,
	})
}

func functionCallArgumentsDone(outputIndex int, itemID, arguments string) []byte {
	return sseFrame("response.function_call_arguments.done", map[string]any{
		"type": "response.function_call_arguments.done", "item_id": itemID,
		"output_index": outputIndex, "arguments": arguments,
	})
}

func customToolCallInputDone(outputIndex int, itemID, input string) []byte {
	return sseFrame("response.custom_tool_call_input.done", map[string]any{
		"type": "response.custom_tool_call_input.done", "item_id": itemID,
		"output_index": outputIndex, "input": input,
	})
}

// ---- 状态机 ----

type streamBlockKind int

const (
	streamBlockText streamBlockKind = iota
	streamBlockTool
	streamBlockThinking
)

// streamBlock 单个上游 content block 的累计态。
type streamBlock struct {
	kind        streamBlockKind
	outputIndex int
	itemID      string
	callID      string
	name        string
	accum       string // 文本/参数增量累计
	startInput  string // tool_use：start 事件携带的完整 input（无增量网关兜底）
	sourceBlock map[string]any
	visible     bool // thinking：有可见 summary
	done        bool
}

// outputItemRecord 终态 response.output 数组的（output_index, item）对。
type outputItemRecord struct {
	index int
	item  map[string]any
}

// streamTranslator Anthropic SSE → responses SSE 状态机。零值不可用，一律
// newStreamTranslator；feed 喂上游字节（UTF-8 跨块安全），finish 收尾
// （残段兜底 + 截断判定）。事件以字节切片列表返回，调用方即写即冲。
type streamTranslator struct {
	tc *toolContext

	responseStarted bool
	completed       bool
	responseID      string
	model           string
	nextOutputIndex int
	blocks          map[int]*streamBlock
	outputItems     []outputItemRecord
	usage           map[string]any // Anthropic 原生 usage 键值累计（记账四列直取）
	stopReason      string
	stopReasonSet   bool
	streamTruncated bool

	buf           string // SSE 块缓冲
	utf8Remainder []byte // UTF-8 跨块残段
	jsonHeld      bool   // 整体 JSON 文档持有中（网关无视 stream:true 回 JSON）
}

func newStreamTranslator(tc *toolContext) *streamTranslator {
	return &streamTranslator{
		tc:         tc,
		responseID: "resp_ccswitch",
		blocks:     map[int]*streamBlock{},
		usage:      map[string]any{},
	}
}

// nextIndex 分配下一个 output_index（到达顺序单调递增）。
func (s *streamTranslator) nextIndex() int {
	i := s.nextOutputIndex
	s.nextOutputIndex++
	return i
}

// mergeUsage usage 列级合并（message_start 与 message_delta 的覆盖语义）。
func (s *streamTranslator) mergeUsage(usage any) {
	u, ok := asMap(usage)
	if !ok {
		return
	}
	for k, v := range u {
		if v == nil {
			continue
		}
		s.usage[k] = v
	}
}

// responsesUsage 状态机内的 responses usage 视图（终态 response 携带）。
func (s *streamTranslator) responsesUsage() map[string]any {
	if len(s.usage) == 0 {
		return zeroResponsesUsage()
	}
	return buildResponsesUsageFromAnthropic(s.usage)
}

// baseResponse 骨架 response 对象。
func (s *streamTranslator) baseResponse(status string, output []any) map[string]any {
	if output == nil {
		output = []any{}
	}
	return map[string]any{
		"id": s.responseID, "object": "response", "created_at": 0,
		"status": status, "model": s.model, "output": output,
		"usage": s.responsesUsage(),
	}
}

// ensureStarted 懒触发首两个事件（首个实质事件到达时才发，防半截流零事件）。
func (s *streamTranslator) ensureStarted() [][]byte {
	if s.responseStarted {
		return nil
	}
	s.responseStarted = true
	resp := s.baseResponse("in_progress", nil)
	return [][]byte{
		sseLifecycleEvent("response.created", "created", resp),
		sseLifecycleEvent("response.in_progress", "in_progress", resp),
	}
}

// handleEvent 单事件分发；failed=true 表示已产出终态失败（流终止）。
func (s *streamTranslator) handleEvent(eventName string, data map[string]any) (events [][]byte, failed bool) {
	dataType := mapStr(data, "type")
	if dataType == "" {
		dataType = eventName
	}
	switch dataType {
	case "message_start":
		if msg, ok := asMap(data["message"]); ok {
			if id := mapStr(msg, "id"); id != "" {
				if strings.HasPrefix(id, "resp_") {
					s.responseID = id
				} else {
					s.responseID = "resp_" + id
				}
			}
			if m := mapStr(msg, "model"); m != "" {
				s.model = m
			}
			if u, present := msg["usage"]; present {
				s.mergeUsage(u)
			}
		}
		return s.ensureStarted(), false
	case "content_block_start":
		return s.handleBlockStart(data), false
	case "content_block_delta":
		return s.handleBlockDelta(data), false
	case "content_block_stop":
		if idx, ok := asU64(data["index"]); ok {
			return s.closeBlock(int(idx)), false
		}
		return nil, false
	case "message_delta":
		if d, ok := asMap(data["delta"]); ok {
			if r := mapStr(d, "stop_reason"); r != "" {
				s.stopReason = r
				s.stopReasonSet = true
			}
		}
		if u, present := data["usage"]; present {
			s.mergeUsage(u)
		}
		return nil, false
	case "message_stop":
		return s.finalize(), false
	case "error":
		msg, errType := anthropicErrorEnvelope(data)
		if ev := s.failedEvent(msg, errType); ev != nil {
			return [][]byte{ev}, true
		}
		return nil, true // 终态已定，不再二发
	case "ping":
		return nil, false
	}
	return nil, false
}

// handleBlockStart content_block_start 三分支（text/tool_use/thinking）。
func (s *streamTranslator) handleBlockStart(data map[string]any) [][]byte {
	events := s.ensureStarted()
	idx, ok := asU64(data["index"])
	if !ok {
		return events
	}
	i := int(idx)
	block, _ := asMap(data["content_block"])
	if block == nil {
		block = map[string]any{}
	}
	switch mapStr(block, "type") {
	case "text":
		oi := s.nextIndex()
		itemID := s.responseID + "_msg_" + itoaU(oi)
		events = append(events, messageItemAdded(oi, itemID), messageContentPartAdded(oi, itemID))
		s.blocks[i] = &streamBlock{
			kind: streamBlockText, outputIndex: oi, itemID: itemID,
			accum: mapStr(block, "text"), sourceBlock: block,
		}
	case "tool_use":
		oi := s.nextIndex()
		callID := mapStr(block, "id")
		name := mapStr(block, "name")
		// 有些网关把完整 input 放 start 事件、不发 input_json_delta——兜底捕获。
		startInput := ""
		if in, ok := asMap(block["input"]); ok && len(in) > 0 {
			startInput = canonicalJSONString(in)
		}
		itemID := s.tc.responseToolCallItemID(callID, name)
		item := s.tc.responseToolCallItem(itemID, "in_progress", callID, name, "")
		events = append(events, outputItemEvent("response.output_item.added", oi, item))
		s.blocks[i] = &streamBlock{
			kind: streamBlockTool, outputIndex: oi, itemID: itemID,
			callID: callID, name: name, startInput: startInput, sourceBlock: block,
		}
	case "thinking", "redacted_thinking":
		oi := s.nextIndex()
		itemID := "rs_" + s.responseID + "_" + itoaU(oi)
		events = append(events, reasoningItemAdded(oi, itemID))
		visible := mapStr(block, "type") == "thinking"
		if visible {
			events = append(events, reasoningSummaryPartAdded(oi, itemID))
		}
		s.blocks[i] = &streamBlock{
			kind: streamBlockThinking, outputIndex: oi, itemID: itemID,
			accum: mapStr(block, "thinking"), sourceBlock: block, visible: visible,
		}
	}
	return events
}

// handleBlockDelta content_block_delta 四分支。
func (s *streamTranslator) handleBlockDelta(data map[string]any) [][]byte {
	idx, ok := asU64(data["index"])
	if !ok {
		return nil
	}
	b := s.blocks[int(idx)]
	if b == nil {
		return nil
	}
	delta, _ := asMap(data["delta"])
	switch mapStr(delta, "type") {
	case "text_delta":
		text := mapStr(delta, "text")
		b.accum += text
		return [][]byte{outputTextDelta(b.outputIndex, b.itemID, text)}
	case "input_json_delta":
		partial := mapStr(delta, "partial_json")
		b.accum += partial
		// Read/自定义工具不发增量（pages:"" 消毒留到收口）。
		if b.name == "Read" || s.tc.isCustomTool(b.name) {
			return nil
		}
		return [][]byte{functionCallArgumentsDelta(b.outputIndex, b.itemID, partial)}
	case "thinking_delta":
		text := mapStr(delta, "thinking")
		b.accum += text
		if b.sourceBlock == nil {
			b.sourceBlock = map[string]any{}
		}
		b.sourceBlock["thinking"] = b.accum
		return [][]byte{reasoningSummaryTextDelta(b.outputIndex, b.itemID, text)}
	case "signature_delta":
		if sig := mapStr(delta, "signature"); sig != "" && b.sourceBlock != nil {
			b.sourceBlock["signature"] = sig
		}
		return nil
	}
	return nil
}

// closeBlock 块收口三分支。
func (s *streamTranslator) closeBlock(i int) [][]byte {
	b := s.blocks[i]
	if b == nil || b.done {
		return nil
	}
	b.done = true
	switch b.kind {
	case streamBlockText:
		events, item := messageClose(b.outputIndex, b.itemID, b.accum)
		s.outputItems = append(s.outputItems, outputItemRecord{b.outputIndex, item})
		return events
	case streamBlockTool:
		// 优先增量累计；空则回退 start 兜底；仍空 → "{}"；Read 消毒+键序规范。
		raw := b.accum
		if strings.TrimSpace(raw) == "" {
			raw = b.startInput
		}
		var arguments string
		if strings.TrimSpace(raw) == "" {
			arguments = "{}"
		} else if b.name == "Read" {
			arguments = sanitizeReadArgumentsJSON(raw)
		} else {
			arguments = canonicalizeToolArgumentsStr(raw)
		}
		status := "completed"
		if s.streamTruncated {
			status = "incomplete"
		}
		item := s.tc.responseToolCallItem(b.itemID, status, b.callID, b.name, arguments)
		var events [][]byte
		if !s.streamTruncated {
			if s.tc.isCustomTool(b.name) {
				input, _ := item["input"].(string)
				events = append(events, customToolCallInputDone(b.outputIndex, b.itemID, input))
			} else {
				events = append(events, functionCallArgumentsDone(b.outputIndex, b.itemID, arguments))
			}
		}
		events = append(events, outputItemEvent("response.output_item.done", b.outputIndex, item))
		s.outputItems = append(s.outputItems, outputItemRecord{b.outputIndex, item})
		return events
	case streamBlockThinking:
		source := b.sourceBlock
		if source == nil {
			source = map[string]any{}
		}
		if mapStr(source, "type") == "thinking" {
			source["thinking"] = b.accum
		}
		item, ok := responsesReasoningItem(b.itemID, source)
		if !ok {
			// 无签名块产出不了信封 → 仅 output_item.done 都不发（块作废）。
			return nil
		}
		events := reasoningCloseWithItem(b.outputIndex, b.itemID, b.accum, item, b.visible)
		s.outputItems = append(s.outputItems, outputItemRecord{b.outputIndex, item})
		return events
	}
	return nil
}

// finalize 正常终态：关闭未收口块 + response.completed（全量 output 按
// output_index 排序 + status + usage）；终态只发一次。
func (s *streamTranslator) finalize() [][]byte {
	if s.completed {
		return nil
	}
	events := s.ensureStarted()
	// 按上游 index 顺序关闭未收口块（确定性）。
	var open []int
	for i, b := range s.blocks {
		if !b.done {
			open = append(open, i)
		}
	}
	sort.Ints(open)
	for _, i := range open {
		events = append(events, s.closeBlock(i)...)
	}
	status, incompleteReason := mapStopReason(s.stopReason)
	resp := s.baseResponse(status, s.sortedOutput())
	if incompleteReason != "" {
		resp["incomplete_details"] = map[string]any{"reason": incompleteReason}
	}
	events = append(events, sseLifecycleEvent("response.completed", "completed", resp))
	s.completed = true
	return events
}

// failedEvent 失败终态（message/type 逐字保留 + 已完成部分 output）；终态
// 只发一次，其后 error 不再重复发（返回 nil）。
func (s *streamTranslator) failedEvent(message, errType string) []byte {
	if s.completed {
		return nil
	}
	s.completed = true
	err := map[string]any{"message": message}
	if errType != "" {
		err["type"] = errType
	}
	resp := s.baseResponse("failed", s.sortedOutput())
	resp["error"] = err
	return sseLifecycleEvent("response.failed", "failed", resp)
}

// sortedOutput 全量 output 按 output_index 排序。
func (s *streamTranslator) sortedOutput() []any {
	sort.SliceStable(s.outputItems, func(a, b int) bool {
		return s.outputItems[a].index < s.outputItems[b].index
	})
	out := make([]any, 0, len(s.outputItems))
	for _, it := range s.outputItems {
		out = append(out, it.item)
	}
	return out
}

// hasSubstantiveOutput 是否已有部分产出（截断分级判定：有产出报 incomplete、
// 零产出报 failed）。
func (s *streamTranslator) hasSubstantiveOutput() bool {
	if len(s.outputItems) > 0 {
		return true
	}
	for _, b := range s.blocks {
		if strings.TrimSpace(b.accum) != "" || b.callID != "" || b.name != "" {
			return true
		}
	}
	return false
}

// jsonDocumentCandidate 缓冲首字节是 { 或 [（剥 BOM/空白）→ 疑似整体 JSON
// 文档（网关无视 stream:true），持有至 EOF 不当 SSE 切。
func jsonDocumentCandidate(buffer string) bool {
	t := strings.TrimLeft(buffer, " \t\r\n\ufeff")
	return strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")
}

// feed 喂上游字节块，返回应下发的 responses SSE 字节（UTF-8 跨块安全拼接）。
// failed=true 表示流已进终态（调用方停止喂）。
func (s *streamTranslator) feed(p []byte) (out []byte, failed bool) {
	// UTF-8 残段安全拼接：不完整尾序列留待下一块。
	combined := p
	if len(s.utf8Remainder) > 0 {
		combined = append(s.utf8Remainder, p...)
		s.utf8Remainder = nil
	}
	valid := combined
	if i := utf8Boundary(valid); i < len(valid) {
		valid, s.utf8Remainder = valid[:i], append([]byte(nil), valid[i:]...)
	}
	s.buf += string(valid)

	// 整体 JSON 文档：持有不动（含美化打印的空行），finish 时统一转事件。
	if !s.jsonHeld && jsonDocumentCandidate(s.buf) {
		s.jsonHeld = true
	}
	if s.jsonHeld {
		return nil, false
	}

	var events [][]byte
	for {
		block, rest, ok := takeSSEBlock(s.buf)
		if !ok {
			break
		}
		s.buf = rest
		evs, fail := s.processBlock(block)
		events = append(events, evs...)
		if fail {
			return joinBytes(events), true
		}
	}
	return joinBytes(events), false
}

// utf8Boundary 尾部不完整 UTF-8 序列的起始偏移（无残段=len）。
func utf8Boundary(b []byte) int {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if b[i]&0xC0 != 0x80 { // 找到序列头
			r, size := utf8.DecodeRune(b[i:])
			if r == utf8.RuneError && size <= 1 {
				return i // 头之后不完整
			}
			return len(b)
		}
	}
	return len(b)
}

// processBlock 单 SSE 块处理。
func (s *streamTranslator) processBlock(block string) (events [][]byte, failed bool) {
	if strings.TrimSpace(block) == "" {
		return nil, false
	}
	eventName, data := parseSSEBlock(block)
	if data == nil {
		return nil, false
	}
	evs, fail := s.handleEvent(eventName, data)
	return evs, fail
}

// finish 流收尾（EOF）：残段兜底 + 截断/半截判定 + 终态产出。
func (s *streamTranslator) finish() []byte {
	var events [][]byte
	if strings.TrimSpace(s.buf) != "" {
		if !s.responseStarted && s.jsonHeld {
			// 整体 JSON 文档收尾：合成完整生命周期事件序列（非对象体 →
			// invalid_response，不 panic——网关无视 stream:true 回数组/标量）。
			if v, ok := decodeJSONValue([]byte(s.buf)); ok {
				if body, isObj := asMap(v); isObj {
					// 带状态变体：usage 回写本状态机（记账侧从 s.usage 取四列，
					// 无状态版会落进一次性翻译器 → 漏账）。
					out, _ := responsesSSEFromAnthropicMessageWithState(body, s.tc, s)
					return out
				}
				if ev := s.failedEvent("upstream returned a non-object Anthropic message body",
					"invalid_response"); ev != nil {
					return ev
				}
				return nil
			}
		}
		if !s.completed {
			evs, fail := s.processBlock(s.buf)
			events = append(events, evs...)
			if fail {
				return joinBytes(events)
			}
		}
		s.buf = ""
	}
	if !s.completed {
		switch {
		case s.stopReasonSet:
			// message_delta 已到、message_stop 没到：语义已完整，正常 finalize。
			events = append(events, s.finalize()...)
		case s.hasSubstantiveOutput():
			// 流截断但有部分产出：强制 stop_reason=max_tokens → incomplete；
			// 未收口工具项标 incomplete 且不发 arguments.done（closeBlock 内判）。
			s.stopReason = "max_tokens"
			s.streamTruncated = true
			events = append(events, s.finalize()...)
		default:
			// 一个字都没产出：response.failed(stream_truncated)。
			if ev := s.failedEvent("Upstream Anthropic stream ended before message_stop",
				"stream_truncated"); ev != nil {
				events = append(events, ev)
			}
		}
	}
	return joinBytes(events)
}

// streamError 传输层错误（读体失败等）→ response.failed(stream_error)。
func (s *streamTranslator) streamError(err error) []byte {
	if ev := s.failedEvent("Stream error: "+err.Error(), "stream_error"); ev != nil {
		return ev
	}
	return nil
}

// joinBytes 字节序列拼接。
func joinBytes(parts [][]byte) []byte {
	if len(parts) == 0 {
		return nil
	}
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// responsesSSEFromAnthropicMessage 完整 JSON message（或错误信封）→ 合成
// responses SSE 生命周期（请求流式但上游 200 回 JSON 的分支，§2.5 表行 1）。
func responsesSSEFromAnthropicMessage(body map[string]any, tc *toolContext) []byte {
	out, _ := responsesSSEFromAnthropicMessageWithState(body, tc, newStreamTranslator(tc))
	return out
}

// responsesSSEFromAnthropicMessageWithState 同上，但调用方持有状态机（读
// 记账 usage）。
func responsesSSEFromAnthropicMessageWithState(body map[string]any, tc *toolContext, st *streamTranslator) ([]byte, bool) {
	// 顶层错误信封 → response.failed（不当 stream_truncated 报）。
	if t, _ := asStr(body["type"]); t == "error" {
		msg, errType := anthropicErrorEnvelope(body)
		if ev := st.failedEvent(msg, errType); ev != nil {
			return ev, false
		}
		return nil, true
	}
	if _, present := body["error"]; present {
		msg, errType := anthropicErrorEnvelope(body)
		if ev := st.failedEvent(msg, errType); ev != nil {
			return ev, false
		}
		return nil, true
	}

	messageStart := make(map[string]any, len(body))
	for k, v := range body {
		messageStart[k] = v
	}
	messageStart["content"] = []any{}
	var events [][]byte
	emit := func(evs [][]byte, _ bool) {
		events = append(events, evs...)
	}
	emit(st.handleEvent("message_start", map[string]any{
		"type": "message_start", "message": messageStart,
	}))
	complete := true

	if content, ok := asSlice(body["content"]); ok {
		for i, blockAny := range content {
			block, ok := asMap(blockAny)
			if !ok {
				continue
			}
			startBlock := make(map[string]any, len(block))
			for k, v := range block {
				startBlock[k] = v
			}
			blockType := mapStr(block, "type")
			switch blockType {
			case "text":
				startBlock["text"] = ""
			case "thinking":
				startBlock["thinking"] = ""
			}
			emit(st.handleEvent("content_block_start", map[string]any{
				"type": "content_block_start", "index": i, "content_block": startBlock,
			}))
			switch blockType {
			case "text":
				if t, ok := asStr(block["text"]); ok {
					emit(st.handleEvent("content_block_delta", map[string]any{
						"type": "content_block_delta", "index": i,
						"delta": map[string]any{"type": "text_delta", "text": t},
					}))
				}
			case "thinking":
				if t, ok := asStr(block["thinking"]); ok {
					emit(st.handleEvent("content_block_delta", map[string]any{
						"type": "content_block_delta", "index": i,
						"delta": map[string]any{"type": "thinking_delta", "thinking": t},
					}))
				}
			}
			emit(st.handleEvent("content_block_stop", map[string]any{
				"type": "content_block_stop", "index": i,
			}))
		}
	}

	var stopReason any
	if r, present := body["stop_reason"]; present {
		stopReason = r
	}
	emit(st.handleEvent("message_delta", map[string]any{
		"type": "message_delta", "delta": map[string]any{"stop_reason": stopReason},
	}))
	events = append(events, st.finalize()...)
	return joinBytes(events), complete
}

// sanitizeReadArgumentsJSON Read 工具流式参数收口（剥 pages:"" 后规范化）。
func sanitizeReadArgumentsJSON(raw string) string {
	v, ok := decodeJSONValue([]byte(raw))
	if !ok {
		return raw
	}
	if m, isMap := asMap(v); isMap {
		if pages, isStr := m["pages"].(string); isStr && pages == "" {
			delete(m, "pages")
		}
		return canonicalJSONString(m)
	}
	return canonicalJSONString(v)
}

func itoaU(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
