// Package harvest 用量采集：增量尾读会话文件，把每次助手记录的 token 用量落账
// （规格 ferryman/harvest.py 1:1，设计 §3.7）。
//
// 铁律（harvest.py docstring 逐字搬运）：
//   - 只取数字与类型，永不落消息内容（隐私不变量）；
//   - 增量：记住每文件已消费字节偏移，只解析新增的完整行（残行留待下轮）；
//   - 断点：偏移随 usage 流水入账，daemon 重启后从账本恢复——账本即唯一状态；
//     偏移先推进后返回，Record 中途失败时该批尾部行永久丢失（at-most-once，
//     故障隔离语义）；同 message.id 的重写行在内存中按首发去重（CC 会重复落
//     同一条助手消息）；
//   - 范围：CC 主会话＋子代理转录（票01/ADR-0008：子代理随父会话入账，守望
//     对 subagents 路径仅跳摆渡不跳采数；Codex 挂后续）。
package harvest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/jsonl"
)

// Row 一条 usage 出行（Python dict 键的定态形；Title/Project/Offset 由
// MaybeHarvest 落值，ParseUsageChunk 阶段为零值）。
type Row struct {
	// TS nil=坏行无 timestamp（Python None；调用方喂 NoteUsage 时按默认 0 语义处理）
	TS    *float64
	Model string
	MsgID string // 中间字段：MaybeHarvest 出行前 pop（去重后不带出）

	InputTokens         int
	CacheReadTokens     int
	CacheCreationTokens int
	OutputTokens        int

	Title   string
	Project string
	Offset  int64

	// 票01 子代理面：主转录行两字段恒零值（SessionID 由调用方按台账 sid 落账，
	// Subagent 落空串——白名单必填语义）；子代理行 Subagent=文件 stem
	// （agent-<agentId>）、SessionID=父 sid（行内 sessionId）。
	Subagent  string
	SessionID string
}

// harvestKey 状态键 (agent, 父sid, stem)（票01：由 (agent, stem) 扩为父域复合
// 键）。主会话 sid 成分恒空串，stem=文件名 stem（CC 为 UUID）——旧键 (agent,
// stem) 的「会话文件名唯一」假设仅对主会话成立，子代理文件名 agent-<hex> 实测
// 全局唯一，父域键是防御性投资（同名不串扰）且使断点恢复语义自明（恢复键全取
// 账本行内字段，零推导）。
type harvestKey struct {
	agent string
	sid   string // 父域 sid：主会话恒空串；子代理文件=行内 sessionId（父会话）
	stem  string
}

// tsLayouts ISO 时间戳解析布局序列（Python fromisoformat(ts.replace("Z","+00:00"))
// 同域：秒/亚秒/偏移可缺）。
var tsLayouts = []string{
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05.999999999Z0700",
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04:05Z0700",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02",
}

// tsOf 对应 _ts_of：timestamp → epoch 秒；非串/解析失败 → false（出行 TS=nil）。
// 无时区的 timestamp 按本地时区解释（CC 转录恒带 Z，现实无影响）——
// ParseInLocation 对带偏移串不改动、对朴素串落本地，与 Python .timestamp() 同值。
func tsOf(v any) (float64, bool) {
	s, ok := v.(string)
	if !ok {
		return 0, false
	}
	s = strings.ReplaceAll(s, "Z", "+00:00")
	for _, layout := range tsLayouts {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return float64(t.Unix()) + float64(t.Nanosecond())/1e9, true
		}
	}
	return 0, false
}

// looseInt 宽松取整（_int 逐字）：null/字符串等取不动就 false，由调用方决定
// 整行跳过（审查 Nit）。
func looseInt(v any) (int, bool) {
	switch x := v.(type) {
	case nil:
		return 0, false // int(None) → TypeError
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(math.Trunc(x)), true // Python int(float) 向零截断
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

// getOrZero 对应 usage.get(k, 0)：缺键=0（可取整）；显式 null 传 nil → 取不动。
func getOrZero(m map[string]any, k string) any {
	if v, ok := m[k]; ok {
		return v
	}
	return 0
}

// pyStr 对应 str() 的宽松取串（现实输入恒为 string；非串防御性渲染与
// Python str() 同文案）。
func pyStr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e16 {
			return strconv.FormatFloat(x, 'f', -1, 64) + ".0"
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	default:
		return fmt.Sprintf("%v", x)
	}
}

// ParseUsageChunk 解析一段完整 JSONL 行 → (usage 行列表, 最新标题, 最新 cwd)。
//
// 只认两类记录出行/更新：ai-title（更新标题）、assistant 且 message.usage
// 非空（出行）；其余记录只可能补 cwd（首个带 cwd 的记录）。损坏行跳过不抛。
func ParseUsageChunk(text, title, cwd string) ([]Row, string, string) {
	rows := []Row{}
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		rec, ok := jsonl.DecodeDict(line)
		if !ok {
			continue
		}
		typ, _ := rec["type"].(string)
		if typ == "ai-title" {
			if t, ok := rec["aiTitle"]; ok && jsonl.Truthy(t) {
				title = pyStr(t)
			}
			continue
		}
		if cwd == "" {
			if v, ok := rec["cwd"]; ok && jsonl.Truthy(v) {
				cwd = pyStr(v)
			}
		}
		if typ != "assistant" {
			continue
		}
		msg, ok := rec["message"].(map[string]any)
		if !ok {
			continue
		}
		usage, ok := msg["usage"].(map[string]any)
		if !ok || len(usage) == 0 {
			continue
		}
		inputTokens, ok1 := looseInt(getOrZero(usage, "input_tokens"))
		cacheRead, ok2 := looseInt(getOrZero(usage, "cache_read_input_tokens"))
		cacheCreation, ok3 := looseInt(getOrZero(usage, "cache_creation_input_tokens"))
		outputTokens, ok4 := looseInt(getOrZero(usage, "output_tokens"))
		if !ok1 || !ok2 || !ok3 || !ok4 {
			continue // 数字取不动的行整行跳过，不落数字错误的账
		}
		var ts *float64
		if v, ok := tsOf(rec["timestamp"]); ok {
			ts = &v
		}
		model := ""
		if v, ok := msg["model"]; ok {
			model = pyStr(v)
		}
		msgID := ""
		if v, ok := msg["id"]; ok && jsonl.Truthy(v) { // msg.get("id") or ""
			msgID = pyStr(v)
		}
		rows = append(rows, Row{
			TS:                  ts,
			Model:               model,
			MsgID:               msgID,
			InputTokens:         inputTokens,
			CacheReadTokens:     cacheRead,
			CacheCreationTokens: cacheCreation,
			OutputTokens:        outputTokens,
		})
	}
	return rows, title, cwd
}

// HarvestState 每会话文件的采集偏移；daemon 重启后由账本恢复（usage 行自带 offset）。
//
// 病态场景（同路径文件被重写收缩）：偏移清零从头重采，可能与旧行重复——
// append-only 不改写旧账，report 层可按 (lineage_id, ts) 去重
// （重采行的 ts 取自转录原文，重复行 ts 相同）。
type HarvestState struct {
	accounts *accounts.Accounts
	offsets  map[harvestKey]int64
	titles   map[harvestKey]string
	cwds     map[harvestKey]string
	msgSeen  map[harvestKey]map[string]bool
	// subParents 子代理文件路径 → 已解析父 sid 的记忆（票01：每文件只窥探
	// 一次行内 sessionId；解析失败的文件不记忆，下轮重试——首行可能尚未落盘）。
	subParents map[string]string
}

// NewHarvestState 从 usage 流水恢复偏移/标题/项目（读失败 → 从零采，重复风险接受）。
//
// 恢复键零推导（票01）：复合键三成分全部取自账本行内字段——agent 公共字段、
// session_id（子代理行=父 sid）、subagent 标记字段（=stem）；旧账本无标记字段
// （或主会话行的空串）按主会话键恢复：stem 以 session_id 充（CC 主转录
// stem=UUID=session_id，历史语义向后兼容）。
func NewHarvestState(a *accounts.Accounts) *HarvestState {
	h := &HarvestState{
		accounts:   a,
		offsets:    map[harvestKey]int64{},
		titles:     map[harvestKey]string{},
		cwds:       map[harvestKey]string{},
		msgSeen:    map[harvestKey]map[string]bool{},
		subParents: map[string]string{},
	}
	var entries []map[string]any
	if a != nil {
		entries = a.Read(accounts.ReadOpts{Kind: "usage"}) // Read 吞错返空 ≡ except Exception
	}
	for _, e := range entries {
		agent, _ := e["agent"].(string)
		sid, _ := e["session_id"].(string)
		sub, _ := e["subagent"].(string) // 缺键/空串=主会话行（旧账本兼容）
		var key harvestKey
		if sub != "" {
			key = harvestKey{agent: agent, sid: sid, stem: sub}
		} else {
			key = harvestKey{agent: agent, stem: sid} // 主会话键：session_id 充 stem
		}
		off := ledgerOffset(e["offset"]) // int(e.get("offset") or 0)
		existing, ok := h.offsets[key]
		if !ok {
			existing = -1 // _offsets.get(key, -1)
		}
		if off > existing {
			h.offsets[key] = off
		}
		if t, ok := e["title"].(string); ok && t != "" {
			h.titles[key] = t
		}
		if p, ok := e["project"].(string); ok && p != "" {
			h.cwds[key] = p
		}
	}
	return h
}

// ledgerOffset 对应 int(e.get("offset") or 0)：None/零值 → 0；数值向零截断；
// 整数字符串可解析；其余 0（Python int() 会抛——账本毒行防御性收口为 0，
// 与「构造不抛」的毒行测试同向）。
func ledgerOffset(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(math.Trunc(x))
	case int64:
		return x
	case int:
		return int64(x)
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64); err == nil {
			return n
		}
	}
	return 0
}

// pathStem 对应 Python Path.stem：去最后一个后缀；前导点名文件（如 ".jsonl"）
// Python 视为无后缀，Go filepath.Ext 会整段吞掉——按 Python 收口。
func pathStem(p string) string {
	name := filepath.Base(p)
	ext := filepath.Ext(name)
	if ext == name {
		return name
	}
	return strings.TrimSuffix(name, ext)
}

// MaybeHarvest 有新增则尾读出 usage 行（带 title/project/offset）；无新增返回空。
//
// 残行（无换行结尾）整段留待下一轮；文件打不开返回空、不推进偏移；
// 偏移先推进后返回（at-most-once）。
func (h *HarvestState) MaybeHarvest(path string, size int64, agent string) []Row {
	return h.readNew(path, size, harvestKey{agent: agent, stem: pathStem(path)})
}

// MaybeHarvestSubagent 子代理转录采集（票01/ADR-0008）：父 sid 从行内 sessionId
// 取（勿只靠路径推导，peekSessionID 优先、路径剥离兜底），状态键=父域复合键
// (agent, 父sid, stem)；出行带 Subagent=stem、SessionID=父 sid。首见语义与主
// 转录一致：从 offset 0 全量回填，不跳尾。
func (h *HarvestState) MaybeHarvestSubagent(path string, size int64, agent string) []Row {
	stem := pathStem(path)
	parent := h.subagentParent(path)
	if parent == "" {
		return nil // 父 sid 解析不出（本轮不采，下轮重试）
	}
	rows := h.readNew(path, size, harvestKey{agent: agent, sid: parent, stem: stem})
	for i := range rows {
		rows[i].Subagent = stem
		rows[i].SessionID = parent
	}
	return rows
}

// subagentParent 解析子代理文件的父会话 sid：行内 sessionId 优先，路径剥离
// 兜底；解析结果按路径记忆。失败返回空串。
func (h *HarvestState) subagentParent(path string) string {
	if parent, ok := h.subParents[path]; ok {
		return parent
	}
	parent := peekSessionID(path)
	if parent == "" {
		parent = parentSidFromPath(path)
	}
	if parent == "" {
		return ""
	}
	h.subParents[path] = parent
	return parent
}

// peekSessionID 读文件头部找第一条带 sessionId 的记录（子代理转录首行即含父
// 会话 sid——t32 实测样例；64KB 覆盖富首行绰绰有余）。打不开/无 → 空串。
func peekSessionID(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 64*1024)
	n, rerr := f.ReadAt(head, 0)
	if rerr != nil && !errors.Is(rerr, io.EOF) {
		return ""
	}
	for _, line := range strings.Split(string(head[:n]), "\n") {
		rec, ok := jsonl.DecodeDict(line)
		if !ok {
			continue
		}
		if v, ok := rec["sessionId"].(string); ok && jsonl.Truthy(v) {
			return v
		}
	}
	return ""
}

// parentSidFromPath 路径剥离兜底：.../<父sid>/subagents/<stem>.jsonl → <父sid>
// （父转录为同目录兄弟文件 <父sid>.jsonl 的实测布局）。
func parentSidFromPath(path string) string {
	if filepath.Base(filepath.Dir(path)) != "subagents" {
		return ""
	}
	return filepath.Base(filepath.Dir(filepath.Dir(path)))
}

// readNew 键就位后的增量尾读（MaybeHarvest/MaybeHarvestSubagent 共用体）：
// 偏移推进、残行扣留、去重与 title/project 携带全在此。
func (h *HarvestState) readNew(path string, size int64, key harvestKey) []Row {
	offset, ok := h.offsets[key]
	if !ok {
		offset = 0
	}
	if size < offset { // 重写/收缩 → 从头重采
		offset = 0
	}
	if size == offset {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil // OSError：文件打不开返回空、不推进偏移
	}
	defer f.Close()
	raw := make([]byte, size-offset)
	n, rerr := f.ReadAt(raw, offset)
	if rerr != nil && !errors.Is(rerr, io.EOF) {
		return nil
	}
	raw = raw[:n]
	if len(raw) == 0 {
		return nil
	}
	end := len(raw)
	if raw[len(raw)-1] != '\n' { // 残行：截到最后一个 \n，整段留待下一轮
		nl := bytes.LastIndexByte(raw, '\n')
		if nl < 0 {
			return nil // 一整段没有完整行
		}
		end = nl + 1
	}
	// decode("utf-8", errors="replace")：非法 UTF-8 以 U+FFFD 替换
	chunk := strings.ToValidUTF8(string(raw[:end]), "�")
	rows, title, cwd := ParseUsageChunk(chunk, h.titles[key], h.cwds[key])
	seen := h.msgSeen[key]
	if seen == nil {
		seen = map[string]bool{}
		h.msgSeen[key] = seen
	}
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		mid := r.MsgID
		r.MsgID = "" // pop("msg_id")：出行不带 id
		if mid != "" && seen[mid] {
			continue // CC 重写同一条消息（同 id）——只记首发
		}
		if mid != "" {
			seen[mid] = true
		}
		out = append(out, r)
	}
	newOffset := offset + int64(end)
	for i := range out {
		out[i].Title = title
		out[i].Project = cwd
		out[i].Offset = newOffset
	}
	if title != "" {
		h.titles[key] = title
	}
	if cwd != "" {
		h.cwds[key] = cwd
	}
	h.offsets[key] = newOffset // 偏移先推进后返回
	return out
}
