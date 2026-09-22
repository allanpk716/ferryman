// 票06：同模型扫参报告——SameModelSweepResult 的 markdown 投影（docs 实验
// 报告惯例，等待窗报告同款结构与文风）。
//
// 四要素（票面验收）：曲线（净节省 t→Net 文本条形）/ 样本量（滚动窗摆渡
// 事件 vs 门槛）/ 预期差价（三线对比 vs 实际发生）/ 与现值 diff。
// 样本不足路径（护栏④）：报告大字标注「样本不足」、建议值与 Best 不产出。
// 渲染纯函数：无 wall-clock、无 map 直出（Uncomputable 键排序后输出），
// 同输入两次渲染逐字节一致。
package backtest

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// smReportFileName 报告落盘固定名（状态目录内最新报告覆盖旧文件）。
const smReportFileName = "same-model-sweep-report.md"

// smCurveWidth 曲线条形最大宽（字符）。
const smCurveWidth = 40

// RenderSameModelMarkdown 渲染同模型扫参实验报告（确定性纯函数）。
// ds 未直接消费（装载计数经 SweepResult.Counts 携带）——参数占位保持与等待窗
// 报告 RenderMarkdown(res, ds) 同形签名。
func RenderSameModelMarkdown(res *SameModelSweepResult, ds *IdleDataset) string {
	_ = ds
	if res == nil {
		res = &SameModelSweepResult{}
	}
	L := []string{"# 同模型阈值扫参实验报告（闲置/摆渡事件总体）", ""}

	// 1. 头部：数据来源、事件总体与样本量。
	iso := res.Counts.LoadedAtISO
	if iso == "" {
		iso = "（未装载）"
	}
	L = append(L, "## 1. 头部：数据来源与样本量", "",
		fmt.Sprintf("- 装载时点戳：%s（epoch %.0f）——数据活体增长，结论以此时点为准", iso, res.Counts.LoadedAt),
		"- 数据来源：账本 usage 科目（主会话标记间隙 = E0a 型闲置间隔）+ handoff 科目（摆渡事件）",
		fmt.Sprintf("- 事件总体：span %d（返回 %d / 摆渡 %d / 截断 %d）+ 孤儿摆渡行 %d；评分入池 %d（截断与无前缀剔除）",
			res.Counts.TotalSpans, res.Counts.ReturnedSpans, res.Counts.FerrySpans,
			res.Counts.CensoredSpans, res.Counts.OrphanHandoffRows, poolSize(res)),
		fmt.Sprintf("- **样本量**：滚动 %d 天窗内摆渡事件 %d 个（门槛 %d）——%s",
			res.Sample.WindowDays, res.Sample.FerryEvents, res.Sample.MinEvents,
			sampleVerdict(res)),
		"", "")

	// 2. 样本门槛判定（护栏④）。
	if res.Sample.Sufficient {
		L = append(L, "## 2. 样本门槛判定", "",
			fmt.Sprintf("- 样本量充足（%d ≥ %d）：三线对比与建议值照常产出。",
				res.Sample.FerryEvents, res.Sample.MinEvents), "")
	} else {
		L = append(L, "## 2. 样本门槛判定", "",
			fmt.Sprintf("- **样本不足（窗内摆渡事件 %d < 门槛 %d）：不产出建议值**，仅呈现实验数据（护栏④）。",
				res.Sample.FerryEvents, res.Sample.MinEvents), "")
	}

	// 3. 三线对比：若当时阈值=t / 实际发生 / 什么都不做。
	L = append(L, "## 3. 三线对比（若当时阈值=t / 实际发生 / 什么都不做）", "",
		"| 阈值 t（分钟） | 触发 | 兑现 | 浪费 | 净额（若阈值=t） | 预期差价（vs 实际发生） |",
		"|---:|---:|---:|---:|---:|---:|")
	for i := range res.Grid {
		p := &res.Grid[i]
		L = append(L, fmt.Sprintf("| %.0f | %d | %d | %d | %.2f | %.2f |",
			p.TMin, p.Fired, p.DeadSaved, p.UselessWarm, p.NetSavings,
			p.NetSavings+res.ActualFerryCost))
	}
	if len(res.Grid) == 0 {
		L = append(L, "（无可算事件或引擎未产出——空网格）")
	}
	L = append(L, "",
		fmt.Sprintf("- 实际发生净额：**%.2f**（死事件 handoff 事实支出合计，price_ver 钉死折算；不可折算行 %d 行照登不估算）",
			-res.ActualFerryCost, res.ActualUnpriced),
		fmt.Sprintf("- 什么都不做净额：**%.2f**（死事件参照全价 T 合计，逐事件版本化）",
			-res.DoNothingCost),
		"- 口径注：净额 = 兑现收益 − 触发支出；「预期差价」= 若阈值=t 净额 − 实际发生净额，正数=同模型更省。",
		"- 触发而未死（回归前白触发）计入「浪费」单列，已含在净额内。", "")

	// 4. 净节省曲线。
	L = append(L, "## 4. 净节省曲线（t → Net）", "", "```")
	maxAbs := 0.0
	for i := range res.Grid {
		if a := math.Abs(res.Grid[i].NetSavings); a > maxAbs {
			maxAbs = a
		}
	}
	for i := range res.Grid {
		p := &res.Grid[i]
		bar := ""
		if maxAbs > 0 {
			n := int(math.Round(math.Abs(p.NetSavings) / maxAbs * smCurveWidth))
			bar = strings.Repeat("#", n)
		}
		L = append(L, fmt.Sprintf("t=%-4.0f |%-40s| %.2f", p.TMin, bar, p.NetSavings))
	}
	if len(res.Grid) == 0 {
		L = append(L, "（无网格数据）")
	}
	L = append(L, "```", "")

	// 5. 建议值与现值 diff。
	L = append(L, "## 5. 建议值与现值 diff", "")
	switch {
	case !res.Sample.Sufficient:
		L = append(L, fmt.Sprintf("- 建议值不出：样本不足（窗内摆渡事件 %d < 门槛 %d，护栏④）。",
			res.Sample.FerryEvents, res.Sample.MinEvents))
	case res.Derived != nil:
		d := res.Derived
		desc := fmt.Sprintf("- 建议值：%.0f 分钟（公式出口 policy.DeriveSameModelForUpstream；缓存安全点 0.8×median %.0f 分钟，最早不亏点 %.0f 分钟）",
			d.SuggestMin, d.MedianTTLMin, d.EconFloorMin)
		if d.SeedFallback {
			desc = "- 建议值：冷启动种子层（无 TTL 观测回退）"
		}
		L = append(L, desc)
	default:
		L = append(L, fmt.Sprintf("- 建议值拒算（%s）：不出建议值——按当前分布与价格，任何合法触发点净亏或输入不可算（宁可不算不造数）。",
			res.DerivedErrKind))
	}
	if !res.HasCurrent {
		L = append(L, "- 现值：（未供给——CLI 层从 config 生效口径取值传入）")
	} else {
		L = append(L, fmt.Sprintf("- 现值：%.1f 分钟", res.CurrentThresholdMin))
	}
	switch {
	case res.Best != nil && res.HasCurrent:
		L = append(L, fmt.Sprintf("- 扫参最优阈值：%.0f 分钟（净额 %.2f）", res.Best.TMin, res.Best.NetSavings),
			fmt.Sprintf("- **diff（扫参最优 − 现值）：%.1f 分钟**", res.Best.TMin-res.CurrentThresholdMin))
	case res.Best != nil:
		L = append(L, fmt.Sprintf("- 扫参最优阈值：%.0f 分钟（净额 %.2f）；现值未供给，diff 不可计",
			res.Best.TMin, res.Best.NetSavings))
	default:
		L = append(L, "- 扫参最优阈值：—（样本不足或空网格，不出最优）")
	}
	L = append(L, "")

	// 6. 口径与盲区。
	L = append(L, "## 6. 口径与盲区", "",
		"- 闲置间隔来自 usage 主会话标记间隙：等待窗内主会话停摆不产生标记，长等待会计为闲置段（口径局限，如实标注）。",
		"- TTL 判热为字面场景口径（触发点 < 场景 TTL）：账本历史无逐会话 TTL 观测，场景值由调用方供给，非历史事实。",
		"- 截断事件（数据末端未见回归/摆渡）真实闲置只知下界——不入评分总体，计数照登。",
		"- 参照全价 T 取自家 p_in（单本代理保守口径）；第三方摆渡价格不在本计算器可见范围。",
		"- 实际发生线仅计 price_ver 可折算的 handoff 行；不可折算行如实单列，不掺代理值。")
	if n := len(res.Uncomputable); n > 0 {
		keys := make([]string, 0, n)
		for k := range res.Uncomputable {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, n)
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s ×%d", k, res.Uncomputable[k]))
		}
		L = append(L, "- 评分剔除分桶："+strings.Join(parts, "、")+"。")
	}
	for _, w := range res.Warnings {
		L = append(L, "- 警告："+w)
	}
	L = append(L, "")

	// 7. 证据等级（规格原文硬标，等待窗报告同款）。
	L = append(L, "## 7. 证据等级", "", EvidenceLevel, "")
	return strings.Join(L, "\n")
}

// poolSize 评分入池数（网格行 Events 恒定，取首行；空网格 0）。
func poolSize(res *SameModelSweepResult) int {
	if len(res.Grid) == 0 {
		return 0
	}
	return res.Grid[0].Events
}

// sampleVerdict 样本量判定文案。
func sampleVerdict(res *SameModelSweepResult) string {
	if res.Sample.Sufficient {
		return "充足"
	}
	return "**不足**"
}

// WriteSameModelReport 报告落状态目录（固定名覆盖 = 最新报告）；返回落盘路径。
func WriteSameModelReport(dir string, res *SameModelSweepResult, ds *IdleDataset) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, smReportFileName)
	if err := os.WriteFile(path, []byte(RenderSameModelMarkdown(res, ds)), 0o644); err != nil {
		return "", err
	}
	return path, nil
}
