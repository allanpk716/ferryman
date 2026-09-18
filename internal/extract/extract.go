// Package extract L0 提取器：CC 会话 jsonl → 确定性骨架 + 对话正文
// （摆渡执行器与评测共用的前半段。规格 ferryman/extract.py 1:1）。
//
// 设计依据 docs/DESIGN.md §6.4（三层漏斗之 L0 + 确定性骨架/模型叙事分工）：
//   - 骨架：文件改动、命令、时间线、标题、峰值上下文——程序化抽取，零经过模型；
//   - 正文：user/assistant 的 text（thinking、tool_result 输出一律丢弃，防注入面最小化）；
//   - token 估算：CJK 1 token/字、其余 chars/3.5（DESIGN §6.8 计量口径）。
//
// 防御纪律同 cctrans：坏行/缺字段静默跳过。行读一律 ReadBytes('\n') 无上限
// （Scanner 有内部行上限，禁用——同票 06 纪律，助手已收口 internal/jsonl）。
// 字符串长度/截断全按码点（mathx.RuneLen/RuneTrunc = Python len/s[:cap]）。
package extract

import (
	"sort"
	"strconv"
	"strings"

	"ferryman/internal/cctrans"
	"ferryman/internal/jsonl"
	"ferryman/internal/mathx"
)

const (
	ItemCharCap = 4000 // 单条正文截断（防超长粘贴撑爆材料）
	CmdCharCap  = 160  // 骨架里单条命令截断
	MaxCommands = 2000 // 收集上限（防病态内存；正常会话远不及，渲染时取尾部 20）
	MaxFiles    = 200

	freezeUserCap = 500  // 末段定格：用户末条截断（设计文档 §4.1）
	freezeAsstCap = 1500 // 末段定格：助手末条截断
)

// truncMark 截断尾标（extract.py _TRUNC_MARK 逐字）。
const truncMark = "（已截断，全文见会话文件）"

// TokenEstimate DESIGN §6.8 计量口径：CJK 1 token/字，其余按 3.5 字符/token
// （保守近似）。CJK 范围逐字平移 Python _CJK = [　-鿿＀-￯]，即
// [U+3000-U+9FFF U+FF00-U+FFEF]；计数与总长全按码点。
func TokenEstimate(text string) int {
	cjk := 0
	for _, r := range text {
		if (r >= 0x3000 && r <= 0x9FFF) || (r >= 0xFF00 && r <= 0xFFEF) {
			cjk++
		}
	}
	other := mathx.RuneLen(text) - cjk
	return cjk + int(float64(other)/3.5) + 1
}

// FileCount 涉及文件计数（路径, 次数），按次数降序。
type FileCount struct {
	Path  string
	Count int
}

// Choice AskUserQuestion 定格（问题一句话, [选项标签]）。
type Choice struct {
	Question string
	Labels   []string
}

// Facts 确定性骨架（零经过模型）。Title/Cwd/FirstTS/LastTS 的 "" ≡ Python None。
type Facts struct {
	Source   string
	Title    string
	Cwd      string
	FirstTS  string
	LastTS   string
	NTurns   int
	PeakCtx  int
	Files    []FileCount
	Commands []string // 按首次出现序

	TotalInputTokens int

	// 末段定格（§4.1）：最后一轮对话的程序化保留，INJECT 层第一段
	FreezeUser     string   // 末条用户文本（≤500 字）
	FreezeAsstText string   // 末条助手文本（≤1500 字）
	FreezeTools    []string // 末条助手的工具名
	FreezeChoice   *Choice  // nil ≡ Python None
}

// SkeletonText 骨架中文文案逐字（extract.py:59-86）。末段定格三档互斥，
// 优先级：choice > 工具摘要 > 逐字文本（choice 时文本也带上）。
func (f *Facts) SkeletonText() string {
	lines := []string{"## 末段定格（最后一轮对话，程序化保留）"}
	lines = append(lines, "[user] "+orFallback(f.FreezeUser, "（无）"))
	if f.FreezeChoice != nil {
		lines = append(lines, "[assistant] "+orFallback(f.FreezeAsstText, "（无）"))
		lines = append(lines, "【上次停在选择】问题："+f.FreezeChoice.Question)
		for _, lb := range f.FreezeChoice.Labels {
			lines = append(lines, "  选项："+lb)
		}
		lines = append(lines, "（选择原文见会话文件，请在新会话中重述该选择）")
	} else if f.FreezeAsstText == "" && len(f.FreezeTools) > 0 {
		lines = append(lines, "[assistant] （末条为工具调用："+strings.Join(f.FreezeTools, "，")+"，无文字回复）")
	} else {
		lines = append(lines, "[assistant] "+orFallback(f.FreezeAsstText, "（无）"))
	}
	lines = append(lines,
		"## 确定性骨架（程序化抽取，未经模型）",
		"- 标题: "+orFallback(f.Title, "(无)"),
		"- 工作目录: "+orFallback(f.Cwd, "(未知)"),
		"- 起止: "+orFallback(f.FirstTS, "?")+" ~ "+orFallback(f.LastTS, "?")+
			"（"+strconv.Itoa(f.NTurns)+" 轮，峰值上下文 "+strconv.Itoa(f.PeakCtx)+" tokens）",
	)
	if len(f.Files) > 0 {
		top := f.Files
		if len(top) > 20 {
			top = top[:20]
		}
		parts := make([]string, len(top))
		for i, fc := range top {
			parts[i] = fc.Path + "(×" + strconv.Itoa(fc.Count) + ")"
		}
		lines = append(lines, "- 涉及文件（次数降序，前 20）: "+strings.Join(parts, ", "))
	}
	if len(f.Commands) > 0 {
		recent := f.Commands
		if len(recent) > 20 {
			recent = recent[len(recent)-20:]
		}
		lines = append(lines, "- 执行过的命令（去重后最近 "+strconv.Itoa(len(recent))+" 条，序）:")
		for _, c := range recent {
			lines = append(lines, "  - "+c)
		}
	}
	return strings.Join(lines, "\n")
}

// orFallback Python `x or fallback` 的字符串形（空串取回落）。
func orFallback(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// Item 正文条目（Role = "user" | "assistant"）。
type Item struct {
	Role string
	Text string
}

// contentText 取 message.content 里的 text 部分；其余类型
// （thinking/tool_use/tool_result）由调用方分流。
func contentText(content any) string {
	if s, ok := content.(string); ok {
		return s
	}
	blocks, ok := content.([]any)
	if !ok {
		return ""
	}
	var parts []string
	for _, bAny := range blocks {
		b, ok := bAny.(map[string]any)
		if !ok {
			continue
		}
		if typ, _ := b["type"].(string); typ == "text" {
			if t, ok := b["text"].(string); ok {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// truthy/decodeDict/readLines 已收口至 internal/jsonl（票 07 评审：三包共用
// 助手单源；本包经 jsonl.Truthy / jsonl.DecodeDict / jsonl.ReadLines 调用）。

// inputDict Python (b.get("input") or {})：缺失/非 dict 按 {}（防御收紧，
// 纪律同 cctrans 评审#11——message 非 dict 不外抛）。
func inputDict(b map[string]any) map[string]any {
	if m, ok := b["input"].(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// freezeFinalize 末段定格定稿：末条用户/助手文本（超长截断加尾标）+ 末条助手的
// 选择与工具名。
func freezeFinalize(facts *Facts, items []Item,
	asstText string, asstTools []string, asstChoice *Choice) {
	facts.FreezeUser = ""
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Role == "user" {
			facts.FreezeUser = items[i].Text
			break
		}
	}
	if mathx.RuneLen(facts.FreezeUser) > freezeUserCap {
		facts.FreezeUser = mathx.RuneTrunc(facts.FreezeUser, freezeUserCap) + truncMark
	}
	at := strings.TrimSpace(asstText)
	if mathx.RuneLen(at) > freezeAsstCap {
		facts.FreezeAsstText = mathx.RuneTrunc(at, freezeAsstCap) + truncMark
	} else {
		facts.FreezeAsstText = at
	}
	facts.FreezeTools = asstTools
	facts.FreezeChoice = asstChoice
}

// Extract 读取一个 CC 会话：返回 (骨架, 正文条目, usage 轮次)。单遍扫描。
// 打不开/读中断（OSError 语义）→ 已收部分 + 空 turns。
func Extract(path string) (Facts, []Item, []cctrans.Turn) {
	facts := Facts{Source: path, Files: []FileCount{}, Commands: []string{}}
	items := []Item{}
	fileCounts := map[string]int{}
	commands := []string{}
	cmdSeen := map[string]bool{}
	var firstISO, lastISO string
	haveFirst := false
	lastAsstText := "" // 末段定格跟踪（每条 assistant 覆盖）
	lastAsstTools := []string{}
	var lastAsstChoice *Choice

	scanErr := jsonl.ReadLines(path, func(line string) bool {
		if !strings.Contains(line, `"type"`) { // 子串预筛快速跳行
			return true
		}
		d, ok := jsonl.DecodeDict(line)
		if !ok {
			return true
		}
		typ, _ := d["type"].(string)

		if ts, ok := d["timestamp"].(string); ok {
			if !haveFirst {
				firstISO = ts
				haveFirst = true
			}
			lastISO = ts
		}
		if typ == "ai-title" {
			title, ok := d["aiTitle"].(string)
			if ok && strings.TrimSpace(title) != "" {
				facts.Title = strings.TrimSpace(title)
			}
			return true
		}
		if typ == "user" && facts.Cwd == "" {
			if cwd, ok := d["cwd"].(string); ok && cwd != "" {
				facts.Cwd = cwd
			}
		}

		if typ != "user" && typ != "assistant" {
			return true
		}
		var msg map[string]any
		if m, ok := d["message"].(map[string]any); ok { // Python d.get("message") or {}
			msg = m
		}
		content := msg["content"]

		if typ == "assistant" {
			// 末段定格跟踪：每条 assistant 先清空再记录，循环自然只留最后一条
			lastAsstText = contentText(content)
			lastAsstTools = []string{}
			lastAsstChoice = nil
			// tool_use → 骨架（file_path / command）
			if blocks, ok := content.([]any); ok {
				for _, bAny := range blocks {
					b, ok := bAny.(map[string]any)
					if !ok {
						continue
					}
					if btyp, _ := b["type"].(string); btyp != "tool_use" {
						continue
					}
					name, _ := b["name"].(string)
					if name != "" {
						lastAsstTools = append(lastAsstTools, name)
					}
					if name == "AskUserQuestion" {
						// 多问只存第一问（v1 简化，其余问丢弃——原文见会话文件）
						qs, _ := inputDict(b)["questions"].([]any)
						if len(qs) > 0 {
							if q0, ok := qs[0].(map[string]any); ok {
								if q, ok := q0["question"].(string); ok && q != "" {
									labels := []string{}
									if opts, ok := q0["options"].([]any); ok {
										for _, oAny := range opts {
											o, ok := oAny.(map[string]any)
											if !ok {
												continue
											}
											if lb, ok := o["label"].(string); ok {
												labels = append(labels, lb)
											}
										}
									}
									lastAsstChoice = &Choice{Question: q, Labels: labels}
								}
							}
						}
					}
					inp := inputDict(b)
					// Python inp.get("file_path") or inp.get("notebook_path")
					fp := inp["file_path"]
					if !jsonl.Truthy(fp) {
						fp = inp["notebook_path"]
					}
					if s, ok := fp.(string); ok && s != "" {
						fileCounts[s] = fileCounts[s] + 1
					}
					if cmd, ok := inp["command"].(string); ok && strings.TrimSpace(cmd) != "" {
						key := strings.TrimSpace(cmd)
						if !cmdSeen[key] && len(commands) < MaxCommands {
							cmdSeen[key] = true
							commands = append(commands, mathx.RuneTrunc(key, CmdCharCap))
						}
					}
				}
			}
			if s := strings.TrimSpace(lastAsstText); s != "" {
				items = append(items, Item{Role: "assistant", Text: mathx.RuneTrunc(s, ItemCharCap)})
			}
		} else { // user：只要 text，tool_result（工具输出）整块丢弃
			if blocks, ok := content.([]any); ok {
				hasResult := false
				for _, bAny := range blocks {
					b, ok := bAny.(map[string]any)
					if !ok {
						continue
					}
					if btyp, _ := b["type"].(string); btyp == "tool_result" {
						hasResult = true
						break
					}
				}
				if hasResult {
					return true
				}
			}
			if s := strings.TrimSpace(contentText(content)); s != "" {
				items = append(items, Item{Role: "user", Text: mathx.RuneTrunc(s, ItemCharCap)})
			}
		}
		return true
	})
	if scanErr != nil {
		// Python except OSError：已收部分定格，turns 全弃
		freezeFinalize(&facts, items, lastAsstText, lastAsstTools, lastAsstChoice)
		return facts, items, []cctrans.Turn{}
	}

	turns := cctrans.AssistantTurns(path)
	facts.FirstTS, facts.LastTS = firstISO, lastISO
	facts.NTurns = len(turns)
	facts.PeakCtx = 0
	total := 0
	for _, t := range turns {
		if t.CtxTokens() > facts.PeakCtx {
			facts.PeakCtx = t.CtxTokens()
		}
		total += t.CtxTokens()
	}
	facts.TotalInputTokens = total
	files := make([]FileCount, 0, len(fileCounts))
	for p, c := range fileCounts {
		files = append(files, FileCount{Path: p, Count: c})
	}
	// Python sorted(key=(-count, path))：次数降序、路径升序（码点序 = UTF-8 字节序）
	sort.Slice(files, func(i, j int) bool {
		if files[i].Count != files[j].Count {
			return files[i].Count > files[j].Count
		}
		return files[i].Path < files[j].Path
	})
	if len(files) > MaxFiles {
		files = files[:MaxFiles]
	}
	facts.Files = files
	facts.Commands = commands
	freezeFinalize(&facts, items, lastAsstText, lastAsstTools, lastAsstChoice)
	if facts.Title == "" {
		facts.Title = cctrans.AITitle(path)
	}
	return facts, items, turns
}

// MaterialText L1 输入材料 = 骨架 + 正文流。
func MaterialText(f Facts, items []Item) string {
	parts := []string{f.SkeletonText(), "", "## 会话正文（user/assistant 文本，工具输出已省略）"}
	for _, it := range items {
		parts = append(parts, "["+it.Role+"] "+it.Text)
	}
	return strings.Join(parts, "\n")
}

// ChunkItems L2 分块：按 token 预算切正文条目（骨架不参与分块，进 reduce 阶段）。
// 每条计 +8 token 条目开销。
func ChunkItems(items []Item, budgetTokens int) [][]Item {
	chunks := [][]Item{}
	cur := []Item{}
	curTokens := 0
	for _, it := range items {
		n := TokenEstimate(it.Text) + 8
		if len(cur) > 0 && curTokens+n > budgetTokens {
			chunks = append(chunks, cur)
			cur, curTokens = []Item{}, 0
		}
		cur = append(cur, it)
		curTokens += n
	}
	if len(cur) > 0 {
		chunks = append(chunks, cur)
	}
	return chunks
}
