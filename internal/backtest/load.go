// 票01：账本装载——读窗、还原 project、glob 过滤、对半切、计数。
//
// 只读不经 accounts 包写路径（accounts.New 会 MkdirAll，读端不碰盘面）；
// 行结构本地定义 json.Unmarshal（最小耦合）。行为对齐 accounts.Read 惯例：
// 文件名序遍历、坏行 stderr 告警后跳过、stdout 保持机器可解析。
package backtest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"ferryman/internal/clock"
	"ferryman/internal/pathsx"
	"ferryman/internal/prices"
)

// LoadOptions 装载参数。DataDir 为账本根（读 <DataDir>/accounts/*.jsonl）；
// 显式路径由 CLI 层（票04）按 config 优先级解析后传入，本包不做环境变量解析。
// Books/EconKey 定"不可算"判定经济价格表：EconKey 命中则取之，否则仅一本时取
// 唯一本，再否则 nil（无可用品价格表——全部窗不可算，宁可不算不造数，
// 与 report 的 bookFor 同语义）。
type LoadOptions struct {
	DataDir  string
	Projects []string // 正集 glob（fnmatch 语义）；空 = 全量（unknown 永不被正集包含）
	Exclude  []string // 排除 glob（对 unknown 以字面 "unknown" 参与匹配）；排除恒压过正集
	Books    map[string]prices.PriceBook
	EconKey  string
	Now      func() float64 // 时点戳来源；nil = clock.Now()（测试注入）
}

// ledgerRow 账本行的本地读形：公共章 + window/usage 两科目的字段并集
// （JSON 数值统一 float64；字段白名单见 accounts.kindFields）。
type ledgerRow struct {
	V         int     `json:"v"`
	Kind      string  `json:"kind"`
	TS        float64 `json:"ts"`
	Agent     string  `json:"agent"`
	SessionID string  `json:"session_id"`
	Project   string  `json:"project"`
	// window 科目
	OpenedTS     float64 `json:"opened_ts"`
	ClosedTS     float64 `json:"closed_ts"`
	DurS         float64 `json:"dur_s"`
	PrefixTokens float64 `json:"prefix_tokens"`
	CloseReason  string  `json:"close_reason"`
	// usage 科目（还原候选：subagent="" 主会话行，非空 = 子代理行 stem）
	Subagent string `json:"subagent"`
	// handoff 科目（票06：摆渡事件事实金额的折算输入）
	PromptTokens     float64 `json:"prompt_tokens"`
	CompletionTokens float64 `json:"completion_tokens"`
	PriceVer         string  `json:"price_ver"`
	// usage 科目 token 规模（票06：闲置事件代表性前缀 S = input+cache_read+cache_creation）
	InputTokens         float64 `json:"input_tokens"`
	CacheReadTokens     float64 `json:"cache_read_tokens"`
	CacheCreationTokens float64 `json:"cache_creation_tokens"`
	// beat 科目（2026-09-23 管子一：TTL 观测收割的配对输入）
	Outcome  string `json:"outcome"`  // hit | miss | error | observe
	Provider string `json:"provider"` // 记账时的价格本键（归属分桶用）
	Lane     string `json:"lane"`     // qwatch | wait
}

// projCand 一条还原候选（usage 行的裁剪形）。
type projCand struct {
	ts      float64
	project string
	main    bool // subagent=="" 的主会话行
}

// Load 装载账本 window 行并产出数据集与全套计数。
// 步骤：读全部 *.jsonl → 还原空 project（D18 确定性优先级）→ 项目 glob 过滤
// （D16，unknown 桶特殊语义）→ 经济价格可算性分桶（D19）→ 时间对半切留出集
// （D17）→ 计数与装载时点戳。
func Load(opts LoadOptions) (*Dataset, error) {
	if opts.Now == nil {
		opts.Now = clock.Now
	}
	rows, err := readLedger(filepath.Join(opts.DataDir, "accounts"))
	if err != nil {
		return nil, err
	}

	// 收集窗行与 usage 还原候选（usage 行 project 空者不带归属信息，不收）。
	var windowRows []ledgerRow
	usage := map[string][]projCand{}
	for _, r := range rows {
		switch r.Kind {
		case "window":
			windowRows = append(windowRows, r)
		case "usage":
			if r.Project != "" {
				usage[r.SessionID] = append(usage[r.SessionID],
					projCand{ts: r.TS, project: r.Project, main: r.Subagent == ""})
			}
		}
	}

	// project 还原（D18）：主会话行（subagent=""）> 子代理行；同级取时间最新，
	// 平局字典序取最小（与主网格 tie-break 同向）。无可连接行 → unknown 桶。
	now := opts.Now()
	var windows []Window
	unresolved, multiMatch := 0, map[string]bool{}
	for _, r := range windowRows {
		w := Window{
			RowTS: r.TS, OpenedTS: r.OpenedTS, ClosedTS: r.ClosedTS,
			DurS: r.DurS, PrefixTokens: int(r.PrefixTokens),
			CloseReason: r.CloseReason, Agent: r.Agent, SessionID: r.SessionID,
			Project: r.Project, ProjectResolved: r.Project != "",
		}
		if !w.ProjectResolved {
			cands := usage[r.SessionID]
			if proj, ok := restoreProject(cands); ok {
				w.Project = proj
				w.ProjectResolved = true
				if distinctProjects(cands) >= 2 {
					multiMatch[r.SessionID] = true
				}
			} else {
				w.Project = UnknownProject
				unresolved++
			}
		}
		windows = append(windows, w)
	}

	// 过滤前口径：close_reason 分布（全部窗，缺失原因以 "" 键如实计数）。
	before := map[string]int{}
	for _, w := range windows {
		before[w.CloseReason]++
	}

	// 项目 glob 过滤（D16）：正集空 = 全量；unknown 永不被正集包含；
	// exclude 对 unknown 以字面 "unknown" 匹配，且恒压过正集。
	inc, exc := compileGlobs(opts.Projects), compileGlobs(opts.Exclude)
	var kept, unknownKept []Window
	for _, w := range windows {
		if len(inc) > 0 && (!w.ProjectResolved || !matchAny(inc, w.Project)) {
			continue
		}
		if matchAny(exc, w.Project) {
			continue
		}
		if w.ProjectResolved {
			kept = append(kept, w)
		} else {
			unknownKept = append(unknownKept, w)
		}
	}
	after := map[string]int{}
	for _, w := range kept {
		after[w.CloseReason]++
	}
	for _, w := range unknownKept {
		after[w.CloseReason]++
	}

	// 经济价格可算性分桶（D19）：书 nil / 行时刻无生效版本 / 版本缺 P_cache
	// → 不可算桶，照登不剔除不估算。
	book := resolveEconBook(opts.Books, opts.EconKey)
	var replay, uncomputable []Window
	for _, w := range kept {
		if computable(book, w) {
			replay = append(replay, w)
		} else {
			uncomputable = append(uncomputable, w)
		}
	}

	// 全序排列（确定性锚点）：OpenedTS 升序逐级决出，同输入两次装载一致。
	sortWindowSlice(replay)
	sortWindowSlice(unknownKept)
	sortWindowSlice(uncomputable)

	// 时间对半切留出集（D17）：前半选参 ceil(n/2)、后半只验证；
	// 两半窗数差 ≤1，切分边界确定。半区显式拷贝——下游重排 Windows 不得波及。
	mid := (len(replay) + 1) / 2
	selectHalf := append([]Window(nil), replay[:mid]...)
	holdoutHalf := append([]Window(nil), replay[mid:]...)

	return &Dataset{
		Windows:      replay,
		SelectHalf:   selectHalf,
		HoldoutHalf:  holdoutHalf,
		Unknown:      unknownKept,
		Uncomputable: uncomputable,
		Counts: LoadCounts{
			LoadedAt:           now,
			LoadedAtISO:        time.Unix(int64(now), 0).Format("2006-01-02T15:04:05-0700"),
			TotalWindows:       len(windowRows),
			AfterFilterWindows: len(kept) + len(unknownKept),
			ReplayWindows:      len(replay),
			UnresolvedWindows:  unresolved,
			MultiMatchSessions: len(multiMatch),
			UnknownCount:       len(unknownKept),
			UncomputableCount:  len(uncomputable),
			CloseReasonBefore:  before,
			CloseReasonAfter:   after,
		},
	}, nil
}

// restoreProject 从同 session_id 的 usage 候选还原 project：
// 有主会话行则只在主行里选，否则在子代理行里选；取时间最新一条，
// 平局按 project 字典序取最小。
func restoreProject(cands []projCand) (string, bool) {
	if len(cands) == 0 {
		return "", false
	}
	hasMain := false
	for _, c := range cands {
		if c.main {
			hasMain = true
			break
		}
	}
	best := projCand{}
	found := false
	for _, c := range cands {
		if hasMain && !c.main {
			continue
		}
		if !found || c.ts > best.ts || (c.ts == best.ts && c.project < best.project) {
			best, found = c, true
		}
	}
	return best.project, found
}

// distinctProjects 候选里不同 project 的个数（多值匹配判定）。
func distinctProjects(cands []projCand) int {
	seen := map[string]bool{}
	for _, c := range cands {
		seen[c.project] = true
	}
	return len(seen)
}

// computable 行时刻在价格表上有生效版本且版本带 P_cache。
func computable(book *prices.PriceBook, w Window) bool {
	if book == nil {
		return false
	}
	pv := book.At(w.RowTS)
	return pv != nil && pv.PCache != nil
}

// resolveEconBook report.bookFor 同语义：key 命中取之；仅一本取唯一本；否则 nil。
func resolveEconBook(books map[string]prices.PriceBook, key string) *prices.PriceBook {
	if len(books) == 0 {
		return nil
	}
	if key != "" {
		if b, ok := books[key]; ok {
			return &b
		}
	}
	if len(books) == 1 {
		for _, b := range books {
			return &b
		}
	}
	return nil
}

// sortWindowSlice 全序：OpenedTS → SessionID → ClosedTS → PrefixTokens →
// CloseReason → Agent 逐级决出，平局不可达即全序确定。
func sortWindowSlice(ws []Window) {
	sort.Slice(ws, func(i, j int) bool {
		a, b := ws[i], ws[j]
		if a.OpenedTS != b.OpenedTS {
			return a.OpenedTS < b.OpenedTS
		}
		if a.SessionID != b.SessionID {
			return a.SessionID < b.SessionID
		}
		if a.ClosedTS != b.ClosedTS {
			return a.ClosedTS < b.ClosedTS
		}
		if a.PrefixTokens != b.PrefixTokens {
			return a.PrefixTokens < b.PrefixTokens
		}
		if a.CloseReason != b.CloseReason {
			return a.CloseReason < b.CloseReason
		}
		return a.Agent < b.Agent
	})
}

// ---- glob（Python fnmatch 语义，归一后匹配） ----

// compileGlobs 把 glob 表编译成正则表；空表返回 nil（不过滤）。
// 项目串与模式先经 pathsx.NormPath 归一（反斜杠→正斜杠 + 小写）——
// Windows 反斜杠路径与大小写差异不影响匹配（lineage 键同款归一惯例）。
func compileGlobs(patterns []string) []*regexp.Regexp {
	if len(patterns) == 0 {
		return nil
	}
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		out = append(out, globRegexp(pathsx.NormPath(p)))
	}
	return out
}

// matchAny 项目串（归一后）命中任一模式。
func matchAny(res []*regexp.Regexp, s string) bool {
	n := pathsx.NormPath(s)
	for _, re := range res {
		if re.MatchString(n) {
			return true
		}
	}
	return false
}

// globRegexp glob 译正则，Python fnmatch.translate 语义：
// `*` 匹配任意字符（含路径分隔符，跨段）、`?` 单字符、`[seq]`/`[!seq]`
// 字符类，其余字面；无闭括号的 `[` 按字面。
func globRegexp(pattern string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString(`^(?s)`)
	for i := 0; i < len(pattern); {
		switch c := pattern[i]; c {
		case '*':
			b.WriteString(`.*`)
			i++
		case '?':
			b.WriteByte('.')
			i++
		case '[':
			j := i + 1
			neg := false
			if j < len(pattern) && (pattern[j] == '!' || pattern[j] == '^') {
				neg = true
				j++
			}
			if j < len(pattern) && pattern[j] == ']' {
				j++ // 首位 ] 为字面成员（fnmatch 语义）
			}
			for j < len(pattern) && pattern[j] != ']' {
				j++
			}
			if j >= len(pattern) { // 无闭括号：'[' 字面
				b.WriteString(regexp.QuoteMeta("["))
				i++
				continue
			}
			b.WriteByte('[')
			if neg {
				b.WriteByte('^')
			}
			b.WriteString(strings.ReplaceAll(pattern[i+1:j], `\`, `\\`))
			b.WriteByte(']')
			i = j + 1
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
			i++
		}
	}
	b.WriteString(`$`)
	return regexp.MustCompile(b.String())
}

// ---- 账本目录只读遍历（对齐 accounts.Read 惯例） ----

// readLedger 文件名序遍历 <dir>/*.jsonl；目录缺失报错（显式路径由 CLI 层解析，
// 读不到数据是硬错误而非空集）。
func readLedger(dir string) ([]ledgerRow, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读账本目录失败: %w", err)
	}
	var rows []ledgerRow
	for _, de := range entries { // ReadDir 已按文件名排序（月序即时序）
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".jsonl") {
			continue
		}
		if err := readLedgerFile(filepath.Join(dir, de.Name()), de.Name(), &rows); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

// readLedgerFile 单文件逐行：ReadBytes 循环（禁 Scanner 行上限，accounts 同款）；
// 空行跳过；坏行 stderr 告警后跳过（stdout 保持机器可解析）。
func readLedgerFile(path, base string, out *[]ledgerRow) error {
	fh, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	r := bufio.NewReader(fh)
	for i := 1; ; i++ {
		raw, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(raw)) > 0 {
			var e ledgerRow
			if json.Unmarshal(bytes.TrimSpace(raw), &e) != nil {
				fmt.Fprintf(os.Stderr, "[backtest] 跳过损坏行 %s:%d\n", base, i)
			} else {
				*out = append(*out, e)
			}
		}
		if err != nil {
			return nil // io.EOF 即正常收尾
		}
	}
}
