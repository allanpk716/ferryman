// Package codextrans Codex CLI rollout（jsonl）的防御式读取器
// （规格 ferryman/codex_transcripts.py 1:1）。
//
// 与 cctrans 同一防御纪律：坏行/缺字段静默跳过，绝不抛错。
//
// 语义（经 2026-06 会话实测校准）：token_count.last_token_usage 里
// input_tokens = 完整 prompt（**已包含** cached_input_tokens，OpenAI 惯例；
// input 随对话单调增长、cached ≤ input 恒成立），因此：
// 命中率 = cached ÷ input；上下文规模 ≈ input。
// 若未来版本语义变化（cached > input 出现即 disjoint/CC 式），调用方需切换公式。
//
// rollout 结构（2026-09-17 真机 2.8MB 样本校准）：
//   - 每行 {timestamp, type, payload}；type ∈ session_meta / response_item /
//     event_msg / turn_context / world_state / token_usage_record；
//   - 正文在 response_item.payload.type == "message"（role user/assistant，
//     content 块 input_text/output_text；developer=技能指令，防注入面丢弃）；
//   - 工具调用在 response_item.payload.type == "function_call"（name + arguments
//     JSON 串）；function_call_output/reasoning 丢弃（工具输出/思考不进材料）。
package codextrans

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode"

	"ferryman/internal/cctrans"
	"ferryman/internal/extract"
	"ferryman/internal/jsonl"
	"ferryman/internal/mathx"
)

// titleCharCap 标题（首条用户消息首行）截断（codex_transcripts.py _TITLE_CHAR_CAP）。
const titleCharCap = 60

// cmdArgKeys function_call.arguments 里视为"命令"的字段（exec_command 的 cmd 等）。
var cmdArgKeys = [...]string{"cmd", "command"}

// patchFilePrefixes apply_patch 补丁头里的文件操作行（codex 改文件的主通道）。
var patchFilePrefixes = [...]string{"*** Update File: ", "*** Add File: ", "*** Delete File: "}

// injectedUserPrefixes codex 伪装成 user 消息的环境注入前缀（2026-09-17 真机
// 实测：AGENTS.md 指令 1238 字节、<turn_aborted> 中断标记）。只枚举已知标记，
// 不泛匹配 "<"——真人粘贴 HTML/XML 片段是常见操作，误杀代价大于漏杀。
var injectedUserPrefixes = [...]string{
	"# AGENTS.md instructions",
	"<turn_aborted>",
	"<environment_context>",
	"<user_instructions>",
	"<skills_instructions>",
}

// XCTurn 一条 token_count 轮次（时间已统一为 UTC epoch 秒）。
// InputTokens = 完整 prompt（已含 cached，OpenAI 口径）。
type XCTurn struct {
	TS          float64
	InputTokens int
	Cached      int
	CacheWrite  int
}

// isInjectedUserText 对应 Python _is_injected_user_text：lstrip 后按已知前缀过滤。
func isInjectedUserText(text string) bool {
	t := strings.TrimLeftFunc(text, unicode.IsSpace) // Python lstrip()
	for _, p := range injectedUserPrefixes {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// TokenCountTurns 会话内全部 token_count 轮次（last_token_usage 口径），按时间升序。
func TokenCountTurns(path string) []XCTurn {
	turns := []XCTurn{}
	err := jsonl.ReadLines(path, func(line string) bool {
		if !strings.Contains(line, `"token_count"`) { // 子串预筛快速跳行
			return true
		}
		d, ok := jsonl.DecodeDict(line)
		if !ok {
			return true
		}
		if typ, _ := d["type"].(string); typ != "event_msg" {
			return true
		}
		// Python (d.get("payload") or {}).get(...) 链；payload/info 非 dict 按
		// 缺失处理（防御收紧，纪律同 cctrans 评审#11——Python 侧为潜在
		// AttributeError，Go 绝不外抛）。
		var payload map[string]any
		if p, ok := d["payload"].(map[string]any); ok {
			payload = p
		}
		if pt, _ := payload["type"].(string); pt != "token_count" {
			return true
		}
		var info, usage map[string]any
		if i, ok := payload["info"].(map[string]any); ok {
			info = i
		}
		if u, ok := info["last_token_usage"].(map[string]any); ok {
			usage = u
		}
		inp, ok1 := cctrans.ToInt(usage["input_tokens"])
		cached, ok2 := cctrans.ToInt(usage["cached_input_tokens"])
		w, ok3 := cctrans.ToInt(usage["cache_write_input_tokens"])
		if !ok1 || !ok2 || !ok3 {
			return true
		}
		ts, ok := cctrans.TSToEpoch(d["timestamp"])
		if !ok || inp <= 0 {
			return true
		}
		turns = append(turns, XCTurn{TS: ts, InputTokens: inp, Cached: cached, CacheWrite: w})
		return true
	})
	if err != nil {
		return []XCTurn{} // Python except OSError: return []——已收轮次一并弃掉
	}
	sort.SliceStable(turns, func(i, j int) bool { return turns[i].TS < turns[j].TS })
	return turns
}

// SessionCwd 首条 session_meta 的 payload.cwd（防御式：缺失/坏行/超头部未见过
// 返回空，Python None/"" 的 Go 形均为 ""）。
//
// session_meta 恒为 rollout 首行（2026-06 实测）；只扫头部 10 行防大开文件。
// 行计数含坏行/不匹配行（Python enumerate(f) 语义）。
func SessionCwd(path string) string {
	cwd := ""
	seen := 0
	err := jsonl.ReadLines(path, func(line string) bool {
		if seen >= 10 { // 只扫头部 10 行
			return false
		}
		seen++
		if !strings.Contains(line, `"session_meta"`) { // 子串预筛快速跳行
			return true
		}
		d, ok := jsonl.DecodeDict(line)
		if !ok {
			return true
		}
		if typ, _ := d["type"].(string); typ == "session_meta" {
			if p, ok := d["payload"].(map[string]any); ok {
				// Python str(p.get("cwd") or "")；cwd 实测恒为字符串，
				// 非串值按 "" 收窄（str() 的非串形态在 rollout 不可达）。
				if s, ok := p["cwd"].(string); ok {
					cwd = s
				}
				return false
			}
		}
		return true
	})
	if err != nil {
		return ""
	}
	return cwd
}

// messageText message 的 content 里 input_text/output_text 块的文本
// （其余类型丢弃）；无块结构时极简容错直接取串。
func messageText(payload map[string]any) string {
	var parts []string
	switch c := payload["content"].(type) {
	case []any:
		for _, bAny := range c {
			b, ok := bAny.(map[string]any)
			if !ok {
				continue
			}
			t, ok := b["text"].(string)
			if !ok {
				continue
			}
			if typ, _ := b["type"].(string); typ == "input_text" || typ == "output_text" {
				parts = append(parts, t)
			}
		}
	case string:
		parts = append(parts, c)
	}
	return strings.Join(parts, "\n")
}

// skeletonFromCall 一个 function_call → 骨架增量（命令 + 文件）。防御式：坏参数
// 全跳过。commands 经指针追改（Python 可变列表传参的 Go 形）。
func skeletonFromCall(name, arguments any,
	fileCounts map[string]int, commands *[]string, cmdSeen map[string]bool) {
	args := map[string]any{}
	if s, ok := arguments.(string); ok && strings.TrimSpace(s) != "" {
		var a any
		if json.Unmarshal([]byte(s), &a) == nil {
			if m, ok := a.(map[string]any); ok {
				args = m
			}
		}
	} else if m, ok := arguments.(map[string]any); ok {
		args = m
	}

	tool, _ := name.(string) // Python str(name or "")；name 实测恒为字符串，非串按 ""
	for _, key := range cmdArgKeys {
		cmd, ok := args[key].(string)
		if !ok || strings.TrimSpace(cmd) == "" {
			continue // 该键无非空串命令 → 试下一键（Python 无 break 落到此处）
		}
		k := strings.TrimSpace(cmd)
		if !cmdSeen[k] && len(*commands) < extract.MaxCommands {
			cmdSeen[k] = true
			*commands = append(*commands, mathx.RuneTrunc(k, extract.CmdCharCap))
		}
		break // 命中首个非空串命令键即止（Python break 在 if 块内，语义同）
	}

	if tool == "apply_patch" {
		// Python patch = args.get("input") or args.get("patch") or ""
		var patch any = args["input"]
		if !jsonl.Truthy(patch) {
			patch = args["patch"]
		}
		if !jsonl.Truthy(patch) {
			patch = ""
		}
		if ps, ok := patch.(string); ok {
			// Python splitlines() 的行集在此按 '\n' 切（apply_patch 载荷恒 \n
			// 分行；行尾 \r 由 strip 吸收，\r-only/unicode 分隔不可达不设防）。
			for _, raw := range strings.Split(ps, "\n") {
				line := strings.TrimSpace(raw)
				for _, prefix := range patchFilePrefixes {
					if strings.HasPrefix(line, prefix) {
						fp := strings.TrimSpace(line[len(prefix):])
						if fp != "" {
							fileCounts[fp] = fileCounts[fp] + 1
						}
					}
				}
			}
		}
	}
	// Python fp = args.get("file_path") or args.get("notebook_path")
	fp := args["file_path"]
	if !jsonl.Truthy(fp) {
		fp = args["notebook_path"]
	}
	if s, ok := fp.(string); ok && s != "" {
		fileCounts[s] = fileCounts[s] + 1
	}
}

// ExtractCodex 读取一个 codex rollout：返回 (骨架, 正文条目)。单遍扫描，防御式。
//
// 2026-09-17 11:17 事故的修复主体：此前 ferry_session 只会 CC 格式，
// codex 会话 0 正文进模型 → "无实际开发活动"垃圾交接。
func ExtractCodex(path string) (extract.Facts, []extract.Item) {
	facts := extract.Facts{Source: path, Files: []extract.FileCount{}, Commands: []string{}}
	items := []extract.Item{}
	fileCounts := map[string]int{}
	commands := []string{}
	cmdSeen := map[string]bool{}
	var firstISO, lastISO string
	haveFirst := false

	scanErr := jsonl.ReadLines(path, func(line string) bool {
		if !strings.Contains(line, `"type"`) { // 子串预筛快速跳行
			return true
		}
		d, ok := jsonl.DecodeDict(line)
		if !ok {
			return true
		}
		if ts, ok := d["timestamp"].(string); ok {
			if !haveFirst {
				firstISO = ts
				haveFirst = true
			}
			lastISO = ts
		}
		typ, _ := d["type"].(string)
		if typ == "session_meta" {
			if p, ok := d["payload"].(map[string]any); ok && facts.Cwd == "" {
				if cwd, ok := p["cwd"].(string); ok && cwd != "" {
					facts.Cwd = cwd
				}
			}
			return true
		}
		if typ != "response_item" {
			return true
		}
		p, ok := d["payload"].(map[string]any)
		if !ok {
			return true
		}
		switch pt, _ := p["type"].(string); pt {
		case "message":
			role, _ := p["role"].(string)
			if role != "user" && role != "assistant" {
				return true // developer/system = 指令注入面，不进材料
			}
			text := strings.TrimSpace(messageText(p))
			if role == "user" && isInjectedUserText(text) {
				return true // codex 环境注入伪装的 user 消息
			}
			if text != "" {
				items = append(items, extract.Item{Role: role,
					Text: mathx.RuneTrunc(text, extract.ItemCharCap)})
			}
		case "function_call":
			skeletonFromCall(p["name"], p["arguments"], fileCounts, &commands, cmdSeen)
		}
		// reasoning / function_call_output：不进骨架也不进正文
		return true
	})
	if scanErr != nil {
		// Python except OSError：已收部分原样返回（不做骨架定稿）
		return facts, items
	}

	turns := TokenCountTurns(path)
	facts.FirstTS, facts.LastTS = firstISO, lastISO
	facts.NTurns = len(turns)
	facts.PeakCtx = 0
	total := 0
	for _, t := range turns {
		if t.InputTokens > facts.PeakCtx {
			facts.PeakCtx = t.InputTokens
		}
		total += t.InputTokens
	}
	facts.TotalInputTokens = total
	files := make([]extract.FileCount, 0, len(fileCounts))
	for p, c := range fileCounts {
		files = append(files, extract.FileCount{Path: p, Count: c})
	}
	// Python sorted(key=(-count, path))：次数降序、路径升序（码点序 = UTF-8 字节序）
	sort.Slice(files, func(i, j int) bool {
		if files[i].Count != files[j].Count {
			return files[i].Count > files[j].Count
		}
		return files[i].Path < files[j].Path
	})
	if len(files) > extract.MaxFiles {
		files = files[:extract.MaxFiles]
	}
	facts.Files = files
	facts.Commands = commands
	for _, it := range items { // 标题：首条用户消息首行（rollout 无 ai-title）
		if it.Role == "user" {
			firstLine := it.Text
			if idx := strings.IndexByte(it.Text, '\n'); idx >= 0 {
				firstLine = it.Text[:idx] // Python splitlines()[0]；\r 由 strip 吸收
			}
			firstLine = strings.TrimSpace(firstLine)
			if firstLine != "" {
				facts.Title = mathx.RuneTrunc(firstLine, titleCharCap)
			}
			break
		}
	}
	return facts, items
}
