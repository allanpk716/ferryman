// 票06：闲置/摆渡事件装载——从账本 usage/handoff 流水构造 E0a 型闲置间隔
// 事件总体（ADR-0015 决定四：反跑评分总体从等待窗口扩到闲置/摆渡事件）。
//
// 事件构造口径：
//   - 活动标记 = usage 科目主会话行（subagent=""）。子代理行不成标记——等待
//     窗内主会话停摆期间子代理流水照记，不代表会话回归。
//   - 闲置 span = 相邻两标记的间隙，结局三态：闭在下一标记 = returned（回归）；
//     闭在 span 内首条 handoff 行 = ferry（摆渡，历史在总结阈值处触发）；
//     末标记之后无回归且无 handoff = censored（截断——数据末端截尾，真实闲置
//     时长只知下界，不入评分总体，照登计数）。
//   - 代表性前缀：ferry span 取 handoff 行 prompt_tokens；returned span 取闭窗
//     标记的 input+cache_read+cache_creation（回归时刻的上下文规模）；censored
//     取开窗标记同口径。
//   - 事实摆渡金额：ferry 行按 price_ver 钉死折算（复用 report.HandoffCost，
//     无第二份折算式）；不可折算行 ActualPriced=false 照登不估算。
//   - TTL 观测账本历史数据不含——由扫参层以场景值供给（报告层如实标注），
//     本层不合成 TTL。
//
// 惯例对齐 load.go（等待窗装载）：文件名序遍历、坏行跳过、project 还原
// （D18）、glob 过滤（D16，unknown 不入正集）、全序排列确定性。
package backtest

import (
	"path/filepath"
	"sort"
	"time"

	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/prices"
	"ferryman/internal/report"
)

// 闲置事件结局三态（稳定字符串，报告/评分按此分支）。
const (
	IdleReturned = "returned" // 回归：闲置在下一活动标记处结束
	IdleFerry    = "ferry"    // 摆渡：闲置在 handoff 行处结束（历史在总结阈值触发）
	IdleCensored = "censored" // 截断：数据末端截尾，真实闲置时长未知（不入评）
)

// IdleEvent 一次闲置/摆渡事件（重放输入 + 历史事实金额）。
type IdleEvent struct {
	StartTS         float64 // 闲置起点（开窗侧活动标记 ts；价格版本按它取 BookVersionAt）
	EndTS           float64 // 闲置终点（回归/摆渡行/数据末端）
	IdleMin         float64 // 闲置时长（分钟）——扫参主输入
	Outcome         string  // returned | ferry | censored
	PrefixTokens    int     // 代表性前缀 S；0 = 无从定价（评分层剔除）
	Agent           string
	SessionID       string
	Project         string // 行载 project 或还原结果；未还原 = UnknownProject
	ProjectResolved bool   // false = unknown 桶成员（行 project 空且无可连接 usage 行）
	// 历史事实（仅 outcome=ferry）：handoff 行按 price_ver 折算的金额；
	// 不可折算 = ActualPriced=false 且金额 0（不造数）。
	ActualFerryCost float64
	ActualPriced    bool
}

// IdleLoadCounts 装载计数（口径注同 LoadCounts：过滤前/后分开登）。
type IdleLoadCounts struct {
	LoadedAt            float64 // 装载时点戳（epoch 秒）
	LoadedAtISO         string
	Sessions            int // 有任意 usage/handoff 行的会话数（过滤前）
	TotalUsageMarks     int // 主会话 usage 标记数（过滤前）
	TotalHandoffRows    int // handoff 行数（过滤前）
	TotalSpans          int // 过滤后 span 数（含 unknown 桶）
	ReturnedSpans       int
	FerrySpans          int
	CensoredSpans       int
	DegenerateSpans     int     // 零长 span 数（同刻双标记；过滤后）
	OrphanHandoffRows   int     // 无活动标记可依附的 handoff 行（过滤后；照登，gate 计入）
	UnknownSpans        int     // unknown 桶 span 数（过滤后留存）
	HorizonTS           float64 // 数据末端 = 全部行最大 ts（截断判定与滚动窗右端点）
	WindowDays          int     // 滚动观察窗（天；0 = config.TuningWindowDays）
	FerryEventsInWindow int     // 滚动窗内摆渡事件数（过滤后口径；护栏④样本门槛）
}

// IdleDataset 装载产物：事件集合 + unknown 桶 + 计数。
// Events/Unknown 均按 StartTS 升序全序排列（平局逐级决出），同输入两次装载
// 逐元素一致。
type IdleDataset struct {
	Events  []IdleEvent // 过滤幸存事件（含 censored——评分层再剔）
	Unknown []IdleEvent // unknown 桶（project 未还原留存）
	Counts  IdleLoadCounts
}

// IdleLoadOptions 装载参数。DataDir 为账本根（读 <DataDir>/accounts/*.jsonl）；
// Books 供 handoff 行事实金额折算（nil = 全部不可折算，如实标注）。
type IdleLoadOptions struct {
	DataDir    string
	Projects   []string // 正集 glob（fnmatch 语义）；空 = 全量（unknown 永不入正集）
	Exclude    []string // 排除 glob（unknown 以字面 "unknown" 参与匹配）；排除恒压过正集
	Books      map[string]prices.PriceBook
	WindowDays int            // 滚动观察窗（天）；0 = config.TuningWindowDays
	Now        func() float64 // 装载时点戳来源；nil = clock.Now()（测试注入）
}

// sessionRows 单会话的标记与摆渡行（装载中间形）。
type sessionRows struct {
	marks    []ledgerRow // usage 主会话行
	handoffs []ledgerRow
}

// LoadIdle 装载账本 usage/handoff 流水并产出闲置事件数据集。
// 步骤：读全部 *.jsonl → 按会话分桶 → span 构造（结局三态）→ project 还原
// → glob 过滤 → 事实金额折算 → 滚动窗 gate 计数 → 全序排列。
func LoadIdle(opts IdleLoadOptions) (*IdleDataset, error) {
	if opts.Now == nil {
		opts.Now = clock.Now
	}
	if opts.WindowDays <= 0 {
		opts.WindowDays = config.TuningWindowDays
	}
	rows, err := readLedger(filepath.Join(opts.DataDir, "accounts"))
	if err != nil {
		return nil, err
	}

	// 分桶：主会话标记 / handoff 行 / project 还原候选 / 数据末端。
	sessions := map[string]*sessionRows{}
	usageCands := map[string][]projCand{}
	horizon := 0.0
	totalHandoff := 0
	for _, r := range rows {
		if r.TS > horizon {
			horizon = r.TS
		}
		switch r.Kind {
		case "usage":
			if r.Project != "" {
				usageCands[r.SessionID] = append(usageCands[r.SessionID],
					projCand{ts: r.TS, project: r.Project, main: r.Subagent == ""})
			}
			if r.Subagent != "" {
				continue // 子代理行不成活动标记（等待窗内心跳/子代理流水照记）
			}
			key := r.Agent + "\x00" + r.SessionID
			s := sessions[key]
			if s == nil {
				s = &sessionRows{}
				sessions[key] = s
			}
			s.marks = append(s.marks, r)
		case "handoff":
			totalHandoff++
			key := r.Agent + "\x00" + r.SessionID
			s := sessions[key]
			if s == nil {
				s = &sessionRows{}
				sessions[key] = s
			}
			s.handoffs = append(s.handoffs, r)
		}
	}

	inc, exc := compileGlobs(opts.Projects), compileGlobs(opts.Exclude)
	keys := make([]string, 0, len(sessions))
	for k := range sessions {
		keys = append(keys, k)
	}
	sort.Strings(keys) // 会话字典序：跨会话构造序确定

	var events, unknown []IdleEvent
	var returnedN, ferryN, censoredN, degenN, orphanN, unknownN int
	totalMarks := 0
	ferryInWindow := 0
	windowStart := horizon - float64(opts.WindowDays)*86400
	for _, key := range keys {
		s := sessions[key]
		totalMarks += len(s.marks)
		marks := append([]ledgerRow(nil), s.marks...)
		handoffs := append([]ledgerRow(nil), s.handoffs...)
		sort.SliceStable(marks, func(i, j int) bool { return marks[i].TS < marks[j].TS })
		sort.SliceStable(handoffs, func(i, j int) bool { return handoffs[i].TS < handoffs[j].TS })

		hi := 0 // span 消费的 handoff 游标（两序列均升序，单趟扫过）
		emit := func(start, end float64, outcome string, prefix float64, h *ledgerRow, startRow ledgerRow) {
			d := end - start
			if d <= 0 {
				degenN++
				return
			}
			ev := IdleEvent{
				StartTS: start, EndTS: end, IdleMin: d / 60, Outcome: outcome,
				PrefixTokens: int(prefix),
				Agent:        startRow.Agent, SessionID: startRow.SessionID,
				Project: startRow.Project, ProjectResolved: startRow.Project != "",
			}
			if !ev.ProjectResolved { // D18 还原（同 load.go：主行 > 子行，新者优先）
				if proj, ok := restoreProject(usageCands[ev.SessionID]); ok {
					ev.Project, ev.ProjectResolved = proj, true
				} else {
					ev.Project = UnknownProject
				}
			}
			// glob 过滤（D16 同款）：unknown 永不入正集；排除恒压过正集。
			if len(inc) > 0 && (!ev.ProjectResolved || !matchAny(inc, ev.Project)) {
				return
			}
			if matchAny(exc, ev.Project) {
				return
			}
			if h != nil { // 事实摆渡金额（price_ver 钉死；不可折算如实标注）
				m := map[string]any{"price_ver": h.PriceVer,
					"prompt_tokens": h.PromptTokens, "completion_tokens": h.CompletionTokens}
				if c := report.HandoffCost(m, opts.Books); c != nil {
					ev.ActualFerryCost, ev.ActualPriced = *c, true
				}
			}
			if ev.ProjectResolved {
				events = append(events, ev)
			} else {
				unknown = append(unknown, ev)
				unknownN++
			}
			switch outcome {
			case IdleReturned:
				returnedN++
			case IdleFerry:
				ferryN++
			default:
				censoredN++
			}
		}
		for i := 0; i < len(marks); i++ {
			start := marks[i].TS
			var end, prefix float64
			var outcome string
			var h *ledgerRow
			// 消耗早于本 span 的 handoff（无 span 可依附 → 孤儿，gate 照计）。
			for hi < len(handoffs) && handoffs[hi].TS <= start {
				hi++
				orphanN++
			}
			if i+1 < len(marks) {
				next := marks[i+1]
				if hi < len(handoffs) && handoffs[hi].TS <= next.TS {
					h = &handoffs[hi]
					hi++ // 消费即推进：同一 handoff 行只收尾一个 span
					end, outcome, prefix = h.TS, IdleFerry, h.PromptTokens
				} else {
					end, outcome = next.TS, IdleReturned
					prefix = next.InputTokens + next.CacheReadTokens + next.CacheCreationTokens
				}
			} else { // 末标记：首个后续 handoff 收尾，否则截断至数据末端
				if hi < len(handoffs) {
					h = &handoffs[hi]
					hi++
					end, outcome, prefix = h.TS, IdleFerry, h.PromptTokens
				} else {
					end, outcome = horizon, IdleCensored
					prefix = marks[i].InputTokens + marks[i].CacheReadTokens + marks[i].CacheCreationTokens
				}
			}
			emit(start, end, outcome, prefix, h, marks[i])
		}
		// 末标记之后仍剩的 handoff（不可能：末 span 已无条件消费首个后续行）
		// ——防御兜底，照登孤儿。
		for ; hi < len(handoffs); hi++ {
			orphanN++
		}
		// 滚动窗 gate：handoff 行（还原后过滤幸存）落在窗内即计。
		for _, h := range handoffs {
			if h.TS < windowStart {
				continue
			}
			proj := h.Project
			resolved := proj != ""
			if !resolved {
				if p, ok := restoreProject(usageCands[h.SessionID]); ok {
					proj, resolved = p, true
				} else {
					proj = UnknownProject
				}
			}
			if len(inc) > 0 && (!resolved || !matchAny(inc, proj)) {
				continue
			}
			if matchAny(exc, proj) {
				continue
			}
			ferryInWindow++
		}
	}

	sortIdleEvents(events)
	sortIdleEvents(unknown)

	now := opts.Now()
	return &IdleDataset{
		Events:  events,
		Unknown: unknown,
		Counts: IdleLoadCounts{
			LoadedAt:            now,
			LoadedAtISO:         time.Unix(int64(now), 0).Format("2006-01-02T15:04:05-0700"),
			Sessions:            len(sessions),
			TotalUsageMarks:     totalMarks,
			TotalHandoffRows:    totalHandoff,
			TotalSpans:          returnedN + ferryN + censoredN + degenN, // 全部构造 span（含 unknown 桶——结局计数不分桶）
			ReturnedSpans:       returnedN,
			FerrySpans:          ferryN,
			CensoredSpans:       censoredN,
			DegenerateSpans:     degenN,
			OrphanHandoffRows:   orphanN,
			UnknownSpans:        unknownN,
			HorizonTS:           horizon,
			WindowDays:          opts.WindowDays,
			FerryEventsInWindow: ferryInWindow,
		},
	}, nil
}

// sortIdleEvents 全序：StartTS → SessionID → EndTS → PrefixTokens → Outcome →
// Agent 逐级决出（sortWindowSlice 同惯例）。
func sortIdleEvents(es []IdleEvent) {
	sort.Slice(es, func(i, j int) bool {
		a, b := es[i], es[j]
		if a.StartTS != b.StartTS {
			return a.StartTS < b.StartTS
		}
		if a.SessionID != b.SessionID {
			return a.SessionID < b.SessionID
		}
		if a.EndTS != b.EndTS {
			return a.EndTS < b.EndTS
		}
		if a.PrefixTokens != b.PrefixTokens {
			return a.PrefixTokens < b.PrefixTokens
		}
		if a.Outcome != b.Outcome {
			return a.Outcome < b.Outcome
		}
		return a.Agent < b.Agent
	})
}
