// Package cctrans Claude Code 会话 jsonl 的防御式读取器（规格 ferryman/transcripts.py 1:1）。
//
// 行内格式属 CC 内部实现、版本间会变（官方明示不稳定）：本包只取需要的字段，
// 任何坏行/缺字段一律静默跳过，绝不向调用方抛错。台账闲置判定另有 mtime 兜底路径。
//
// 行读一律 ReadBytes('\n') 无上限：Scanner 有内部行上限
// （ErrTooLong 断流，超长行后的内容全部丢失），禁用。行读/解码/真值助手已
// 收口至 internal/jsonl（票 07 评审），本包只留 CC 行格式语义。
package cctrans

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"

	"ferryman/internal/jsonl"
)

// DefaultTailBytes 尾窗判定默认只读的尾部字节数（Python tail_bytes=262_144）。
const DefaultTailBytes = 262_144

// Turn 一条带 usage 的 assistant 轮次（时间已统一为 UTC epoch 秒）。
type Turn struct {
	TS            float64
	CacheRead     int
	CacheCreation int
	InputTokens   int
}

// CtxTokens 上下文占用 = 缓存读 + 缓存写 + 输入。
func (t Turn) CtxTokens() int {
	return t.CacheRead + t.CacheCreation + t.InputTokens
}

// tsLayouts ISO 时间戳解析布局序列（Python fromisoformat 语义：
// 秒/亚秒/偏移可缺；无时区按 UTC 处理，避免与 mtime 双时钟错位——
// Go 的 time.Parse 对无时区布局本就落 UTC，恰好同值）。
var tsLayouts = []string{
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05.999999999Z0700",
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04:05Z0700",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
}

// TSToEpoch 对应 Python _ts_to_epoch：ISO 串 → epoch 秒；非串/空/解析失败 → false。
func TSToEpoch(v any) (float64, bool) {
	s, ok := v.(string)
	if !ok || s == "" {
		return 0, false
	}
	for _, layout := range tsLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return float64(t.Unix()) + float64(t.Nanosecond())/1e9, true
		}
	}
	return 0, false
}

// ToInt 对应 Python int(u.get(k) or 0) 的宽松转换语义：
// nil/零值/空容器 → 0；bool → 1/0；数值截断；整数字符串可解析；
// 其余（非整数字符串、非空容器）→ false（Python 侧即 TypeError/ValueError → 跳行）。
func ToInt(v any) (int, bool) {
	switch x := v.(type) {
	case nil:
		return 0, true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case float64:
		return int(x), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		if err != nil {
			return 0, false
		}
		return n, true
	case []any:
		return 0, len(x) == 0
	case map[string]any:
		return 0, len(x) == 0
	default:
		return 0, false
	}
}

// truthy/decodeDict/readLines 已收口至 internal/jsonl（票 07 评审：三包共用
// 助手单源；本包经 jsonl.Truthy / jsonl.DecodeDict / jsonl.ReadLines 调用）。

// AssistantTurns 会话内全部 assistant usage 轮次，按时间升序（稳定排序）。
// 一切坏行/缺字段/类型不符静默跳过；读失败（OSError 语义）整单返回空。
func AssistantTurns(path string) []Turn {
	turns := []Turn{}
	err := jsonl.ReadLines(path, func(line string) bool {
		if !strings.Contains(line, `"usage"`) { // 子串预筛快速跳行
			return true
		}
		d, ok := jsonl.DecodeDict(line)
		if !ok {
			return true
		}
		if typ, _ := d["type"].(string); typ != "assistant" {
			return true
		}
		// Python (d.get("message") or {}).get("usage") or {}；message 非 dict 按
		// 缺失处理（防御纪律：绝不外抛，Python 侧此为潜在 AttributeError，Go 收紧）。
		var msg map[string]any
		if m, ok := d["message"].(map[string]any); ok {
			msg = m
		}
		var usage map[string]any
		if u, ok := msg["usage"].(map[string]any); ok {
			usage = u
		}
		cr, ok1 := ToInt(usage["cache_read_input_tokens"])
		cc, ok2 := ToInt(usage["cache_creation_input_tokens"])
		inp, ok3 := ToInt(usage["input_tokens"])
		if !ok1 || !ok2 || !ok3 {
			return true
		}
		ts, ok := TSToEpoch(d["timestamp"])
		if !ok || cr+cc+inp <= 0 {
			return true
		}
		turns = append(turns, Turn{TS: ts, CacheRead: cr, CacheCreation: cc, InputTokens: inp})
		return true
	})
	if err != nil {
		return []Turn{} // Python except OSError: return []——已收行数一并弃掉
	}
	sort.SliceStable(turns, func(i, j int) bool { return turns[i].TS < turns[j].TS })
	return turns
}

// tailWindow 已收口至 internal/jsonl.TailWindow（票 10 骑手：qwatch/cctrans
// 两份私有副本归一单源；统一 ToValidUTF8 语义即 Python decode(errors="replace")）。

// MaxLastTSWindowBytes LastTimestamp 尾窗上限（窗内找不到带时间戳行时的
// 翻倍扩张封顶）。真超限（>2MB 无时间戳尾段）按 not-ok 返回，调用方走
// mtime 兜底语义——宁可多摆渡一次，不误判内容未推进。
const MaxLastTSWindowBytes = 2 * 1024 * 1024

// LastTimestamp 转录内最后一条带顶层 timestamp 记录的 epoch 秒（内容时钟的
// 取值器，ADR-0013）。
//
// 与 extract 的 covers_until 同语义（extract.go：文件序最后一条含 timestamp 的
// 行，任意 type）——CC 的周期性状态块（last-prompt/ai-title/mode/…，无
// timestamp 字段）天然被跳过。CC 与 codex rollout 同形（顶层 timestamp），
// 两轨通用。防御纪律同包：任何坏行/读失败静默跳过；窗内无带时间戳行 →
// 窗口翻倍重试至 MaxLastTSWindowBytes，仍无 → false（fail-open 回 mtime 口径）。
func LastTimestamp(path string) (float64, bool) {
	for window := int64(DefaultTailBytes); ; window *= 2 {
		lines, ok := jsonl.TailWindow(path, window)
		if !ok {
			return 0, false
		}
		for i := len(lines) - 1; i >= 0; i-- {
			line := lines[i]
			if !strings.Contains(line, `"timestamp"`) { // 子串预筛快速跳行
				continue
			}
			d, ok := jsonl.DecodeDict(line)
			if !ok {
				continue
			}
			if ts, ok := TSToEpoch(d["timestamp"]); ok {
				return ts, true
			}
		}
		if window >= MaxLastTSWindowBytes {
			return 0, false
		}
	}
}

// HasDanglingToolUse 尾部悬空 tool_use 判定（默认 256KB 尾窗）。
func HasDanglingToolUse(path string) bool {
	return HasDanglingToolUseWindow(path, DefaultTailBytes)
}

// HasDanglingToolUseWindow 尾部悬空 tool_use 判定（测试注窗口变体）。
//
// True = 仍有工具/子代理在跑（会话按 mtime 看似闲置，实则运行中），守望应推迟摆渡。
// tool_result 恒在对应 tool_use 之后写入，故只需尾部窗口内做集合差：
// 出现过的 tool_use id − 出现过的 tool_result id ≠ ∅ 即悬空。
// 坏行/缺字段/无 assistant 行一律 False（宁可多摆渡不误判运行中；漏判由
// covers_until 兜住正确性）。
func HasDanglingToolUseWindow(path string, tailBytes int64) bool {
	lines, ok := jsonl.TailWindow(path, tailBytes) // 票10 骑手：尾窗读法收口 jsonl 单源
	if !ok {
		return false
	}
	used := map[string]bool{}
	served := map[string]bool{}
	for _, line := range lines {
		if !strings.Contains(line, `"tool_use"`) && !strings.Contains(line, `"tool_result"`) {
			continue // 子串预筛快速跳行
		}
		d, ok := jsonl.DecodeDict(line)
		if !ok {
			continue
		}
		// Python (d.get("message") or {}).get("content")；message 非 dict 按缺失处理。
		var msg map[string]any
		if m, ok := d["message"].(map[string]any); ok {
			msg = m
		}
		content, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for _, bAny := range content {
			b, ok := bAny.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := b["type"].(string)
			if typ == "tool_use" {
				if id, ok := b["id"].(string); ok {
					used[id] = true
				}
			} else if typ == "tool_result" {
				if id, ok := b["tool_use_id"].(string); ok {
					served[id] = true
				}
			}
		}
	}
	for id := range used { // 集合差 used − served ≠ ∅
		if !served[id] {
			return true
		}
	}
	return false
}

// AITitle 会话的自动生成标题（取最后一个 ai-title 行；该类行本身无 timestamp 字段）。
// 无 → ""（Python None 的 Go 形）。
func AITitle(path string) string {
	title := ""
	err := jsonl.ReadLines(path, func(line string) bool {
		if !strings.Contains(line, `"ai-title"`) { // 子串预筛快速跳行
			return true
		}
		d, ok := jsonl.DecodeDict(line)
		if !ok {
			return true
		}
		if t, ok := d["aiTitle"].(string); ok {
			if s := strings.TrimSpace(t); s != "" {
				title = s
			}
		}
		return true
	})
	if err != nil {
		return ""
	}
	return title
}

// FirstUserMessageHash 首条 user 消息正文的 sha256（会话族系 lineage 指纹）。
//
// resume 若产生新文件且复制历史，此 hash 会与原会话相同——
// 这是 lineage 校准（E0a 附带项）的判定信号。无 → ""。
func FirstUserMessageHash(path string) string {
	hash := ""
	err := jsonl.ReadLines(path, func(line string) bool {
		if hash != "" { // Python 首条即 return，已命中则不再覆盖
			return false
		}
		if !strings.Contains(line, `"type":"user"`) { // 子串预筛快速跳行
			return true
		}
		d, ok := jsonl.DecodeDict(line)
		if !ok {
			return true
		}
		if typ, _ := d["type"].(string); typ != "user" {
			return true
		}
		var msg map[string]any
		if m, ok := d["message"].(map[string]any); ok {
			msg = m
		}
		var text string
		if cs, ok := msg["content"].(string); ok {
			text = cs
		} else if cl, ok := msg["content"].([]any); ok {
			var sb strings.Builder
			for _, bAny := range cl {
				b, ok := bAny.(map[string]any)
				if !ok {
					continue
				}
				if typ, _ := b["type"].(string); typ != "text" {
					continue
				}
				if ts, ok := b["text"].(string); ok {
					sb.WriteString(ts)
				}
			}
			text = sb.String()
		} else {
			return true
		}
		s := strings.TrimSpace(text)
		if s == "" {
			return true
		}
		sum := sha256.Sum256([]byte(s))
		hash = hex.EncodeToString(sum[:])
		return false
	})
	if err != nil {
		return ""
	}
	return hash
}

// HasAsyncLaunch 尾部异步派发判定（T48 票01）：最后一个 Task/Agent 派发是否为
// 后台/异步启动。
//
// True = 刚派出 async 子代理（工具调用秒回、真身仍在跑）——等待窗口应停表
// 停车（Stop 到达不闭窗），直到主会话恢复调用。判据（对最后一个 Task/Agent
// tool_use 取 OR）：input 的 run_in_background/background 为真；或其
// tool_result 首个 text 块以 "Async agent launched" 为前缀（严格前缀非子串
// ——评审#6：防同步 result 中段复读该文案被误停车 1h；2026-09-18 实机观测
// 文案，CC 改版可能失效——判不中一律 False，退回旧语义=Stop 即闭窗）。
// 交错派发（async A 在飞 + 再派 sync B）在此层面返回 False——async 等待的
// 保留由 server 侧 saw_async latch 兜住（round 0 e2 实验教训）。
// 坏行/缺字段/OSError 一律 False；message 非 dict 的行安全跳过（评审#11：
// 调用点 _park_or_close 无 try，直穿 /subagent 钩子）。
func HasAsyncLaunch(path string) bool {
	lines, ok := jsonl.TailWindow(path, DefaultTailBytes)
	if !ok {
		return false
	}
	var lastDispatch string // 尾窗内最后一个 Task/Agent tool_use id（"" = 无）
	isAsync := map[string]bool{}
	for _, line := range lines {
		if !strings.Contains(line, `"tool_use"`) && !strings.Contains(line, `"tool_result"`) {
			continue // 子串预筛快速跳行
		}
		d, ok := jsonl.DecodeDict(line)
		if !ok { // 顶层非 dict 的坏行同样静默跳过
			continue
		}
		m, ok := d["message"].(map[string]any)
		if !ok { // 评审#11：message 非 dict 安全跳过
			continue
		}
		content, ok := m["content"].([]any)
		if !ok {
			continue
		}
		for _, bAny := range content {
			b, ok := bAny.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := b["type"].(string)
			if typ == "tool_use" {
				name, _ := b["name"].(string)
				id, idOK := b["id"].(string)
				if (name == "Task" || name == "Agent") && idOK {
					lastDispatch = id
					inp, _ := b["input"].(map[string]any)
					isAsync[id] = jsonl.Truthy(inp["run_in_background"]) || jsonl.Truthy(inp["background"])
				}
			} else if typ == "tool_result" {
				id, ok := b["tool_use_id"].(string)
				if !ok {
					continue
				}
				var text string
				if c, ok := b["content"].(string); ok {
					text = c
				} else if cl, ok := b["content"].([]any); ok { // 评审#6：只取首个 text 块，严格前缀
					for _, xAny := range cl {
						x, ok := xAny.(map[string]any)
						if !ok {
							continue
						}
						if xt, _ := x["type"].(string); xt == "text" {
							if s, ok := x["text"].(string); ok {
								text = s
							}
							break // 只认首个 text 块（其 text 非 str 时同样止步）
						}
					}
				}
				if strings.HasPrefix(text, "Async agent launched") {
					isAsync[id] = true
				}
			}
		}
	}
	return lastDispatch != "" && isAsync[lastDispatch]
}
