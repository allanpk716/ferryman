// Package qwatch 提问潮检测器（规格 ferryman/qwatch.py 1:1，T51 票 01）。
//
// 给定 CC 会话 jsonl 路径，对末条带文本的 assistant 消息做紧档判定：
// 是否提问潮、问题单元数、判据构成，以及尾部悬空 tool_use 是否仅由
// AskUserQuestion 构成。判据与隐私不变量（问询守望 spec 决策 1/2）：
//   - 剥代码块后按行归桶计数（一行只计一桶）：标记行（❓ / **Qn**）计入；
//     问号行计入；编号/列表行仅当行内含问号或疑问词才计入——round-0 实验
//     153 条误报的形态就是无疑问信号的纯步骤/清单行，紧档为堵它们而来；
//   - 隐私铁律：Verdict 只含计数与布尔，任何字段不携带消息内容。
//
// 读取风格与 cctrans.HasDanglingToolUse 同源：只读尾部窗口、逐行
// 解析 jsonl、坏行/缺字段/读失败一律静默兜底，绝不向调用方抛错。
//
// 正则纪律（spec §文本）：RE2 化且字符类 Unicode 化——Python \d → \p{Nd}、
// Python \s → 显式 Unicode 空白集 pyWS（ASCII 空白 + U+001C-1F + NEL +
// NBSP + White_Space）；全角样本进测试（期望值以 Python _classify 实测钉死）。
package qwatch

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"ferryman/internal/beat"
	"ferryman/internal/jsonl"
)

// AskUserQuestion 该工具悬空＝正在等用户作答，恰是靶场景。
const AskUserQuestion = "AskUserQuestion"

// DefaultMinQuestions 提问潮阈值下限（spec 决策 9：min_questions）。
const DefaultMinQuestions = 5

// TailBytes 尾窗判定默认只读的尾部字节数。
const TailBytes = 262_144

// 漏检观测（票06，xcheck 附录第 10 条——observe 期粗粒度信号）口径常数：
const (
	MissIdleS     = 600.0  // 复活闲置门槛：GLM 实测 TTL 口径（spec D4 ~10 分钟）
	MissLookbackS = 1800.0 // 命中回看窗：此前 30 分钟内的末条疑似提问与复活关联
)

// pyWS Python \s（str 模式）的 Unicode 显式集——RE2 无 \s 的 Unicode 语义，
// 显式展开（ASCII 空白 + 文件/组分隔符 U+001C-1F + NEL + NBSP + White_Space）。
const pyWS = `[\t\n\v\f\r \x{1C}-\x{1F}\x{85}\x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}]`

// 包级 var 预编译正则（Python re.compile 1:1）：
//   - marker：**Q1** / **Q 2** 形态（\d → \p{Nd}，全角数字也认；\s → pyWS）；
//   - numbered：编号/列表行 1. 2、 3) 以及 - * • · 起头（**Qn** 粗体行由
//     标记桶先行接住）；
//   - codeFence：成对围栏剥到最近闭合，未闭合围栏剥到文末（都视作代码，
//     不计）——(?s) 点号跨行，Python \Z → RE2 \z（文末，非行末）。
var (
	markerRe    = regexp.MustCompile(`(?i)\*\*Q` + pyWS + `?\p{Nd}+`)
	numberedRe  = regexp.MustCompile(`^` + pyWS + `*(?:\p{Nd}{1,3}` + pyWS + `*[.、)]|[-*•·])` + pyWS + `*`)
	codeFenceRe = regexp.MustCompile("(?s)" + "```" + `.*?(?:` + "```" + `|\z)`)
)

// questionWords 疑问词 16 词逐字（中文逐字，英文小写作子串匹配）。
var questionWords = []string{"什么", "怎么", "为何", "如何", "哪个", "哪些", "是否",
	"能不能", "要不要", "还是不是", "还是说",
	"what", "how", "why", "which", "whether"}

// Breakdown 判据构成（行数，按行归桶、一行只计一桶，不重复计数）。
type Breakdown struct {
	MarkerLines            int // ❓ / **Qn** 标记行
	QmarkLines             int // 含 ？/? 的非编号行
	QualifiedNumberedLines int // 含问号或疑问词的编号/列表行
}

// Total 三桶合计＝问题单元数。
func (b Breakdown) Total() int {
	return b.MarkerLines + b.QmarkLines + b.QualifiedNumberedLines
}

// Verdict 提问潮判定结果。隐私铁律：只有计数与布尔，无任何消息内容。
type Verdict struct {
	IsSurge                 bool
	UnitCount               int
	BD                      Breakdown
	AskUserQuestionDangling bool // 悬空集 ⊆ {AskUserQuestion}；空集真空真
}

// isPySpace Python str.strip 的空白集（= pyWS 同源，含 U+001C-1F——
// Go strings.TrimSpace 的 unicode.IsSpace 不含它们，故自持一份）。
func isPySpace(r rune) bool {
	switch {
	case r == '\t' || r == '\n' || r == '\v' || r == '\f' || r == '\r' || r == ' ':
		return true
	case r >= 0x1C && r <= 0x1F:
		return true
	case r == 0x85 || r == 0xA0 || r == 0x1680 || r == 0x2028 || r == 0x2029 ||
		r == 0x202F || r == 0x205F || r == 0x3000:
		return true
	case r >= 0x2000 && r <= 0x200A:
		return true
	}
	return false
}

// pyTrimSpace Python line.strip() 的 Go 形。
func pyTrimSpace(s string) string {
	return strings.TrimFunc(s, isPySpace)
}

// questionish 行内含问号或疑问词（英文不区分大小写）。
func questionish(line string) bool {
	low := strings.ToLower(line)
	if strings.Contains(low, "?") || strings.Contains(line, "？") {
		return true
	}
	for _, w := range questionWords {
		if strings.Contains(low, w) {
			return true
		}
	}
	return false
}

// classifyText 剥块后的正文逐行归桶：标记行 > 编号行 > 问号行，一行只进一桶。
func classifyText(text string) Breakdown {
	var marker, qmark, numbered int
	for _, raw := range strings.Split(text, "\n") {
		line := pyTrimSpace(raw)
		if line == "" {
			continue
		}
		switch {
		case strings.Contains(line, "❓") || markerRe.MatchString(line):
			marker++
		case numberedRe.MatchString(line):
			if questionish(line) { // 紧档：纯步骤/清单行不计
				numbered++
			}
		case strings.Contains(line, "?") || strings.Contains(line, "？"):
			qmark++ // 非编号行只认问号，疑问词不算
		}
	}
	return Breakdown{MarkerLines: marker, QmarkLines: qmark, QualifiedNumberedLines: numbered}
}

// Detect 转录尾部 → 提问潮判定（默认 256KB 尾窗）。
func Detect(path string, minQuestions int) Verdict {
	return DetectTail(path, minQuestions, TailBytes)
}

// DetectTail 转录尾部 tailBytes 字节 → 提问潮判定（测试注窗口变体）。
//
// 纯函数，除读文件外零副作用。只读尾部 tailBytes 字节（大 jsonl 不整读）；
// 末条带文本的 assistant 消息按 message id 聚合文本块（CC 流式分片同 id
// 多行）；悬空 tool_use 判定与 HasDanglingToolUse 同法做尾部窗口集合差，
// 另收集 name：悬空集 ⊆ {AskUserQuestion} 时 AskUserQuestionDangling=true
// ——空集按真空真记 true（语义＝「无非该工具悬空」，供命中谓词条件②直接用）。
// 缺文件/空文件/坏行：IsSurge=false 零计数兜底、读失败 AQD=true（真空真），
// 绝不抛错。
func DetectTail(path string, minQuestions int, tailBytes int64) Verdict {
	lines, ok := jsonl.TailWindow(path, tailBytes) // 票10 骑手：尾窗读法收口 jsonl 单源
	if !ok {                                       // 一切 OSError 语义 → 读失败兜底（真空真口径）
		return Verdict{IsSurge: false, AskUserQuestionDangling: true}
	}
	texts := map[string][]string{} // message id → 文本块（分片聚合）
	lastTextKey := ""              // 末条带文本的 assistant 消息（"" = 无）
	used := map[string]string{}    // tool_use id → name（缺名记 ""，不给豁免）
	served := map[string]bool{}    // 已回包的 tool_use id
	for i, line := range lines {
		d, ok := jsonl.DecodeDict(line)
		if !ok {
			continue
		}
		// Python (d.get("message") or {})：message 非 dict 按空 dict 处理。
		var msg map[string]any
		if m, ok := d["message"].(map[string]any); ok {
			msg = m
		}
		content := msg["content"]
		if typ, _ := d["type"].(string); typ == "assistant" {
			var parts []string
			switch c := content.(type) {
			case string:
				parts = append(parts, c)
			case []any:
				for _, bAny := range c {
					b, ok := bAny.(map[string]any)
					if !ok {
						continue
					}
					if bt, _ := b["type"].(string); bt == "text" {
						if s, ok := b["text"].(string); ok {
							parts = append(parts, s)
						}
					}
				}
			}
			kept := make([]string, 0, len(parts))
			for _, p := range parts {
				if pyTrimSpace(p) != "" {
					kept = append(kept, p)
				}
			}
			if len(kept) > 0 {
				key := fmt.Sprintf("#%d", i) // message id 非 str 的兜底键
				if mid, ok := msg["id"].(string); ok {
					key = mid
				}
				texts[key] = append(texts[key], kept...)
				lastTextKey = key
			}
		}
		if cl, ok := content.([]any); ok {
			for _, bAny := range cl {
				b, ok := bAny.(map[string]any)
				if !ok {
					continue
				}
				switch bt, _ := b["type"].(string); bt {
				case "tool_use":
					if id, ok := b["id"].(string); ok {
						name, _ := b["name"].(string)
						used[id] = name
					}
				case "tool_result":
					if tid, ok := b["tool_use_id"].(string); ok {
						served[tid] = true
					}
				}
			}
		}
	}
	// 集合差 used − served 的名字收集：悬空集 ⊆ {AskUserQuestion}（空集真空真）。
	aqDangling := true
	for tid := range used {
		if !served[tid] && used[tid] != AskUserQuestion {
			aqDangling = false
			break
		}
	}
	if lastTextKey == "" {
		return Verdict{IsSurge: false, AskUserQuestionDangling: aqDangling}
	}
	// 块边界视作换行边界：不同 text 块不共行，避免前后块首尾粘行漏计；
	// 先剥成对/未闭合代码围栏（都视作代码，不计）。
	body := codeFenceRe.ReplaceAllString(strings.Join(texts[lastTextKey], "\n"), "")
	bd := classifyText(body)
	return Verdict{IsSurge: bd.Total() >= minQuestions, UnitCount: bd.Total(), BD: bd,
		AskUserQuestionDangling: aqDangling}
}

// tailWindow 已收口至 internal/jsonl.TailWindow（票 10 骑手：qwatch/cctrans
// 两份逐字等价私有副本归一单源；ToValidUTF8 即 Python decode(errors="replace")）。

// CorrelateMissSignals 漏检关联计数（票06，observe 期粗粒度信号）：
// 对账本行做纯计数关联。
//
// 一次「全量重付的闲置复活请求」（usage 行：cache_read_tokens == 0，且与该
// 会话上一条 usage 行间隔 ≥ idle_s——首条无前驱不算复活）若此前 lookback_s
// 内该会话有过「末条疑似提问」命中（qwatch_hit 事件）、且最近一次命中与其
// 之间无任何真跳保温（beat 行 outcome ≠ observe——observe 演练未真发、
// 不保温），计一次漏检信号：守望看见了提问潮、没能保温、用户最终全量重付。
// 其间有真跳出场（hit/miss/error）即检测与执行已尽职——其后重付归 TTL
// 漂移/死区取舍（熔断与 D4 遥测管辖），不计漏检。判据真漏检（检测器没
// 认出提问潮）无正文级证据可回溯，粗粒度信号不含它——回调判据靠 D8 命中
// 清单人工复核。纯函数、只读计数类字段、坏行缺字段一律跳过（隐私不变量：
// 计数与 bool，永不接触消息内容）。
func CorrelateMissSignals(rows []map[string]any) int {
	usage := map[string][][2]float64{}  // sid → [(ts, cache_read), ...]
	hits := map[string][]float64{}      // sid → [命中 ts, ...]
	realBeats := map[string][]float64{} // sid → [真跳 ts, ...]（observe 演练除外）
	for _, r := range rows {
		if r == nil {
			continue
		}
		sid, _ := r["session_id"].(string)
		ts, tsOK := numF(r["ts"])
		if sid == "" || !tsOK {
			continue
		}
		kind, _ := r["kind"].(string)
		switch {
		case kind == "usage":
			if cr, ok := numF(r["cache_read_tokens"]); ok {
				usage[sid] = append(usage[sid], [2]float64{ts, cr})
			}
		case kind == "qwatch_hit":
			hits[sid] = append(hits[sid], ts)
		case kind == "beat" && r["outcome"] != beat.OutObserve:
			realBeats[sid] = append(realBeats[sid], ts)
		}
	}
	n := 0
	for sid, turns := range usage {
		// Python turns.sort()：[ts, cr] 对逐元字典序（ts 同则按 cr）。
		sort.SliceStable(turns, func(i, j int) bool {
			if turns[i][0] != turns[j][0] {
				return turns[i][0] < turns[j][0]
			}
			return turns[i][1] < turns[j][1]
		})
		for k := 1; k < len(turns); k++ { // zip(turns, turns[1:])：相邻对
			prevTs, ts, cr := turns[k-1][0], turns[k][0], turns[k][1]
			if cr != 0.0 || ts-prevTs < MissIdleS {
				continue // 非闲置复活 / 非全量重付（首条无前驱天然出局）
			}
			var window []float64
			for _, t := range hits[sid] {
				if ts-MissLookbackS <= t && t < ts {
					window = append(window, t)
				}
			}
			if len(window) == 0 {
				continue // 此前 30 分钟内无末条疑似提问命中可关联
			}
			hitTs := window[0] // 取最近一次命中锚定真跳回看
			for _, t := range window {
				if t > hitTs {
					hitTs = t
				}
			}
			blocked := false
			for _, b := range realBeats[sid] {
				if hitTs <= b && b < ts {
					blocked = true // 其间有真跳保温——非漏检
					break
				}
			}
			if blocked {
				continue
			}
			n++
		}
	}
	return n
}

// numF Python isinstance(x, (int, float)) 的计数类字段宽松收数
// （JSON 解码值为 float64；测试/调用方可给 Go 整型）。
func numF(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}
