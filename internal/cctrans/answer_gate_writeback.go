package cctrans

// answer_gate_writeback.go — 拦截回写识别的单源判定处（spec #34 评审 F10）：
// 两族合一——打字族 IsHookBlockNotice（2026-10-11 案：UserPromptSubmit 拦截
// 提示行）与作答族（#37，ADR-0028「拦截回写不算会话内容」段：PostToolUse
// continue:false 拦下后的整段回写）。消费点（本包 LastTimestamp 尾窗 /
// extract.Extract 全文件）只调 WritebackSkips / WritebackSkipsFile，不再各自
// 双检两族。
//
// 2026-10-15 实测形状（notes/auq-blocked-transcript.jsonl）：作答被 PostToolUse
// continue:false 拦下后，CC 仍向转录追加带 timestamp 的一段——答案 tool_result、
// PostToolUse 的 hook_success 附件、hook_stopped_continuation 附件、
// prompt_snapshot 附件、turn_duration 系统行。这段是拦截回写、不是会话内容：
// 若计入内容时钟，交接被判过期白重摆一次，闸门闲置钟被归零（被拦后在原会话
// 直接打普通话即冷缓存静默放行）。跳过面＝内容时钟取值与交接覆盖截止提取
// （本包 LastTimestamp 与 extract.Extract，仅有的两个消费点，不另设平行判定）；
// 其余读转录的判定（悬空道、async 停车等）不受影响。
//
// 识别：锚＝attachment.type=hook_stopped_continuation ∧ hookName 以
// "PostToolUse:" 开头 ∧ message 带 AnswerGateBlockMarker（Ferryman 拦截文案；
// 别家钩子的同形回写不带标记、不跳过）。按锚的 toolUseID 认出整段：
//   - 该 id 的答案 tool_result 行；
//   - 该 id 的 PostToolUse hook_success 行（PreToolUse hook_success 同 id 但
//     先于作答、属正常写入，不跳过——拦截前最后真实内容常落在它身上）；
//   - 锚自身；
//   - 锚后至下一条真实内容（user 打字/assistant）之间的 prompt_snapshot 与
//     turn_duration（被拦回合的收尾；真实内容落地即闭窗，其后新回合照常计数）。
//
// 事实源只在转录，守护重启不丢。防御纪律同包：坏行/缺字段静默跳过；读失败
// 按无锚处理＝旧行为 fail-open（宁可白重摆一次，不误跳真实内容）。

import (
	"strings"

	"ferryman/internal/jsonl"
)

// answerGateAnchorSubstr 锚行子串预筛（快速跳行，无锚文件零解码开销）。
const answerGateAnchorSubstr = `"hook_stopped_continuation"`

// answerGateAnchorID 行是否为作答回写锚；是则一并返回其 toolUseID（可空：
// 缺 id 只跳锚自身，段无从认起）。只认 Ferryman：hookName 以 "PostToolUse:"
// 开头 ∧ message 带 AnswerGateBlockMarker（#36 拦截文案前缀）。
func answerGateAnchorID(d map[string]any) (string, bool) {
	if typ, _ := d["type"].(string); typ != "attachment" {
		return "", false
	}
	a, ok := d["attachment"].(map[string]any)
	if !ok {
		return "", false
	}
	if t, _ := a["type"].(string); t != "hook_stopped_continuation" {
		return "", false
	}
	hn, _ := a["hookName"].(string)
	if !strings.HasPrefix(hn, "PostToolUse:") {
		return "", false
	}
	msg, _ := a["message"].(string)
	if !strings.Contains(msg, AnswerGateBlockMarker) {
		return "", false
	}
	id, _ := a["toolUseID"].(string)
	return id, true
}

// answerGateAnchorIDs 第一遍：锚 toolUseID 集（空 id 不入集）。
func answerGateAnchorIDs(lines []string) map[string]bool {
	anchors := map[string]bool{}
	for _, line := range lines {
		if !strings.Contains(line, answerGateAnchorSubstr) {
			continue
		}
		if d, ok := jsonl.DecodeDict(line); ok {
			if id, is := answerGateAnchorID(d); is && id != "" {
				anchors[id] = true
			}
		}
	}
	return anchors
}

// answerGateSegRelevant 第二遍子串预筛：只有这些形状可能是段成员或闭窗器。
func answerGateSegRelevant(line string) bool {
	for _, sub := range [...]string{`"tool_result"`, `"hook_success"`,
		answerGateAnchorSubstr, `"prompt_snapshot"`, `"turn_duration"`,
		`"user"`, `"assistant"`} {
		if strings.Contains(line, sub) {
			return true
		}
	}
	return false
}

// answerGateBlockedToolResult user 行是否含锚 id 的答案 tool_result（回写段）。
func answerGateBlockedToolResult(d map[string]any, anchors map[string]bool) bool {
	if len(anchors) == 0 {
		return false
	}
	msg, ok := d["message"].(map[string]any)
	if !ok {
		return false
	}
	blocks, ok := msg["content"].([]any)
	if !ok {
		return false
	}
	for _, bAny := range blocks {
		b, ok := bAny.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := b["type"].(string); t == "tool_result" {
			if id, _ := b["tool_use_id"].(string); anchors[id] {
				return true
			}
		}
	}
	return false
}

// answerGateRealContent 真实内容行（闭窗器）：带打字正文的 user 行（content 为
// 非空字符串，或含非 tool_result 块——纯 tool_result 的 user 行是工具回写不是
// 打字，不闭窗）。assistant 行由调用方直闭（模型被调＝回合已续）。
func answerGateRealContent(d map[string]any) bool {
	if typ, _ := d["type"].(string); typ != "user" {
		return false
	}
	msg, ok := d["message"].(map[string]any)
	if !ok {
		return false
	}
	switch c := msg["content"].(type) {
	case string:
		return c != ""
	case []any:
		for _, bAny := range c {
			if b, ok := bAny.(map[string]any); ok {
				if t, _ := b["type"].(string); t != "tool_result" {
					return true
				}
			}
		}
	}
	return false
}

// answerGateSegMarker 第二遍：按锚集（文件序）标出回写段行号。
type answerGateSegMarker struct {
	anchors map[string]bool
	skips   map[int]bool
	inTail  bool // 锚后、未见真实内容（被拦回合的 prompt_snapshot/turn_duration 落在此窗）
}

// feed 文件序喂一行（i 为行号）。
func (m *answerGateSegMarker) feed(i int, line string) {
	if !answerGateSegRelevant(line) {
		return // 子串预筛快速跳行
	}
	d, ok := jsonl.DecodeDict(line)
	if !ok {
		return
	}
	switch typ, _ := d["type"].(string); typ {
	case "attachment":
		a, _ := d["attachment"].(map[string]any)
		switch at, _ := a["type"].(string); at {
		case "hook_stopped_continuation":
			if _, is := answerGateAnchorID(d); is {
				m.skips[i] = true
				m.inTail = true
			}
		case "hook_success":
			hn, _ := a["hookName"].(string)
			id, _ := a["toolUseID"].(string)
			if strings.HasPrefix(hn, "PostToolUse:") && m.anchors[id] {
				m.skips[i] = true // 拦截钩子的回显（PreToolUse 同 id 不带该前缀，不跳）
			}
		case "prompt_snapshot":
			if m.inTail {
				m.skips[i] = true // 被拦回合的收尾快照
			}
		}
	case "system":
		if sub, _ := d["subtype"].(string); sub == "turn_duration" && m.inTail {
			m.skips[i] = true // 被拦回合的时长行
		}
	case "user":
		if answerGateBlockedToolResult(d, m.anchors) {
			m.skips[i] = true // 答案 tool_result＝拦截回写
			return
		}
		if answerGateRealContent(d) {
			m.inTail = false // 真实内容落地：闭窗（其后新回合照常计数）
		}
	case "assistant":
		m.inTail = false
	}
}

// isTypingWriteback 打字族行级判定（IsHookBlockNotice 的行形态：子串预筛＋
// 解码＋判定；两族单源的打字腿——2026-10-11 拦截回写案）。
func isTypingWriteback(line string) bool {
	if !strings.Contains(line, `"preventContinuation"`) {
		return false
	}
	d, ok := jsonl.DecodeDict(line)
	return ok && IsHookBlockNotice(d)
}

// WritebackSkips 一批文件序行的拦截回写行号集（两族合一的单源判定处，
// spec #34 评审 F10）：打字族逐行判定＋作答族两小遍（先集锚、再标段）。
// LastTimestamp 尾窗用它。无回写（常态）返回 nil，近零解码开销。
func WritebackSkips(lines []string) map[int]bool {
	m := &answerGateSegMarker{anchors: answerGateAnchorIDs(lines), skips: map[int]bool{}}
	for i, line := range lines {
		if isTypingWriteback(line) {
			m.skips[i] = true
		}
		m.feed(i, line)
	}
	if len(m.skips) == 0 {
		return nil
	}
	return m.skips
}

// WritebackSkipsFile 整文件预扫的拦截回写行号集（两族合一；交接覆盖截止提取
// 用）。行号＝文件序 0 起，与 jsonl.ReadLines 的交付序一致——调用方按同一
// 行序计数即可对齐（extract.Extract 的主扫描即此读法）。无作答锚（常态）
// 单遍即出（打字族逐行判定）；有锚才走第二遍标段。读失败：作答族按无锚＝
// 旧行为 fail-open（打字族保留已扫部分）。空集归一为 nil（同旧契约）。
func WritebackSkipsFile(path string) map[int]bool {
	var anchors map[string]bool
	skips := map[int]bool{}
	idx := 0
	err := jsonl.ReadLines(path, func(line string) bool {
		i := idx
		idx++
		if strings.Contains(line, answerGateAnchorSubstr) {
			if d, ok := jsonl.DecodeDict(line); ok {
				if id, is := answerGateAnchorID(d); is && id != "" {
					if anchors == nil {
						anchors = map[string]bool{}
					}
					anchors[id] = true
				}
			}
		}
		if isTypingWriteback(line) {
			skips[i] = true
		}
		return true
	})
	if err != nil || len(anchors) == 0 {
		if len(skips) == 0 {
			return nil
		}
		return skips // 无作答锚（或首遍读失败）：打字族单遍结果
	}
	m := &answerGateSegMarker{anchors: anchors, skips: skips}
	idx = 0
	if err := jsonl.ReadLines(path, func(line string) bool {
		m.feed(idx, line)
		idx++
		return true
	}); err != nil {
		return nil // 第二遍读失败：按旧行为整集作废（fail-open）
	}
	if len(skips) == 0 {
		return nil
	}
	return skips
}
