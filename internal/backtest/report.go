// 票03：扫参报告——同一视图结构体的两投影（docs 惯例 markdown 实验报告 + --json）。
//
// BuildView 把 SweepResult（+Dataset 留出集两半）折叠成 *SweepView，
// RenderMarkdown / RenderJSON 均从该视图渲染——两投影数字一致由结构保证
// （同一结构体，无第二份算术）。渲染纯函数：无 wall-clock、无 map 遍历直出
// （close_reason 排序后输出），同输入两次渲染逐字节一致。
//
// 隐私面：输入类型（SweepResult/Window/Dataset）全部为账本元数据，结构上
// 不存在消息内容字段；本包只读时间戳/计数/金额，不触达 SessionID。
// 证据等级硬标与盲区声明为规格固定文案（「模型与保真」节），逐字不变。
//
// 票02 接缝：Scenarios/Baselines/Diagnostic/Best 由网格引擎填充；本包以
// types.go 现有字段渲染，引擎未产出时零值优雅呈现（「（引擎未产出）」占位），
// 不阻塞票04。场景档名文案归本票定（types.go Scenario 字段注释），导出常量
// 供票02 复用；引擎用了别的名字时会作为额外档附在规范三档之后，不丢数。
package backtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// 场景档名文案（规格「模型与保真」逐字；注意 − 为 U+2212）。
const (
	ScenarioNameConfig = "config TTL（无实测，采集日期 N/A）"
	ScenarioNameThird  = "−1/3"
	ScenarioNameHalf   = "−1/2"
)

// EvidenceLevel 证据等级标注（规格原文逐字）。
const EvidenceLevel = "**单位成本输入（价格表、prefix_tokens）为事实；跳数、时点、熔断触发与总成本均为反事实推断**"

// BlindSpots 盲区声明固定文案（规格「模型与保真」四条）。
var BlindSpots = []string{
	"单跳成本依赖 DefaultBeatOutTokens=300（policy.go:31）硬编码假设，真实 out-tokens 偏离将随跳数线性放大误差；第二遍以 wait_close.cost_actual 对账",
	"渡口限流/共享配额不可见",
	"GLM 端 TTL 策略漂移",
	"单机单用户样本外推性",
}

// expiredRatios 无效保温三档占比场景（D9 固定：真实样本无亏损事件，场景档是
// 唯一亏损侧证据来源）。
var expiredRatios = [3]float64{0, 0.10, 0.30}

// ---- 视图结构体（markdown 与 --json 的同一中间形；JSON 键 snake_case） ----

// ReasonCount close_reason 分布行（排序后输出，禁 map 直出）。
type ReasonCount struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// CountsView 数据集计数（头部节；口径注同 LoadCounts）。
type CountsView struct {
	TotalWindows       int           `json:"total_windows"`
	AfterFilterWindows int           `json:"after_filter_windows"`
	ReplayWindows      int           `json:"replay_windows"`
	UnknownCount       int           `json:"unknown_count"`
	UncomputableCount  int           `json:"uncomputable_count"`
	UnresolvedWindows  int           `json:"unresolved_windows"`
	MultiMatchSessions int           `json:"multi_match_sessions"`
	CloseReasonBefore  []ReasonCount `json:"close_reason_before"`
	CloseReasonAfter   []ReasonCount `json:"close_reason_after"`
}

// PerfView 一行可对比的性能数字（对照列 / 网格点通吃）。
// CapS 为指针：nil = +Inf（JSON 不支持 Inf，置 null）。
type PerfView struct {
	Scenario     string   `json:"scenario"`
	TTLS         float64  `json:"ttl_s"` // 点级有效 ttl_s′（场景档 × 乘数；对照列 ×1.0 即档值）
	TTLMult      float64  `json:"ttl_mult"`
	TauS         float64  `json:"tau_s"`
	FirstBeatS   float64  `json:"first_beat_s"`
	CapS         *float64 `json:"cap_s"`
	Windows      int      `json:"windows"`
	Beats        int      `json:"beats"`
	BeatCost     float64  `json:"beat_cost"`
	GrossSavings float64  `json:"gross_savings"`
	NetSavings   float64  `json:"net_savings"`
	BreakerTrips int      `json:"breaker_trips"`
	UselessWarm  int      `json:"useless_warm"`
}

// GapView 差距表：当前闭式配置 vs 主网格最优（同 TTL 档内对比另见 Tiers）。
type GapView struct {
	Baseline  *PerfView `json:"baseline"`  // 当前闭式配置（config 档对照 ×1.0）
	GridBest  *PerfView `json:"grid_best"` // 主网格最优（全局 Best 及其行数字）
	Delta     float64   `json:"delta"`     // 网格最优净节省 − 闭式对照净节省（两列齐备才计）
	Recommend string    `json:"recommend"` // ttl_s 校准建议（行动出口）
}

// TierView 单个 TTL 场景档：对照列、档内网格最优、逐行网格数字与翻转判定。
type TierView struct {
	Tier     string     `json:"tier"`     // 档名（规格逐字）
	TTLS     float64    `json:"ttl_s"`    // 档 TTL（对照列优先；无数据为 0）
	Baseline *PerfView  `json:"baseline"` // 该档闭式对照
	Best     *PerfView  `json:"best"`     // 该档网格最优（净节省 max，并列 CompareGridPoint 取小）
	Delta    float64    `json:"delta"`    // 档内差距（两列齐备才计）
	Flip     string     `json:"flip"`     // 与上一档的结论翻转判定（首档为空串）
	Rows     []PerfView `json:"rows"`     // 该档主网格逐行（引擎序）
}

// DiagView 诊断网格行（只读展示、非可部署，不进推荐）。
type DiagView struct {
	TauS         float64  `json:"tau_s"`
	FirstBeatS   float64  `json:"first_beat_s"`
	CapS         *float64 `json:"cap_s"`
	Windows      int      `json:"windows"`
	Beats        int      `json:"beats"`
	BeatCost     float64  `json:"beat_cost"`
	NetSavings   float64  `json:"net_savings"`
	BreakerTrips int      `json:"breaker_trips"`
	UselessWarm  int      `json:"useless_warm"`
}

// WarmRow 无效保温占比场景行（D9 线性外推口径）。
type WarmRow struct {
	RatioPct    int     `json:"ratio_pct"`
	Loss        float64 `json:"loss"`
	AdjustedNet float64 `json:"adjusted_net"`
}

// WarmView 无效保温单列（D2：单列不合并；D9：历史空表照登 + 三档场景）。
type WarmView struct {
	ExpiredAfter   int       `json:"expired_after"`   // 过滤后 expired 窗数（历史事实）
	HistoricalNote string    `json:"historical_note"` // 空表照登注明
	BasisDesc      string    `json:"basis_desc"`      // 外推基准（推荐参数组）
	Scenarios      []WarmRow `json:"scenarios"`       // expired 占比 0/10%/30%
}

// DoubleView 双计检查（D11）：心跳花费（推演）与摆渡成本（账本 handoff 科目
// 历史事实）各列各的，不互相抵扣。契约无摆渡成本输入字段 → FerryNote 占位。
type DoubleView struct {
	BeatCost  float64 `json:"beat_cost"`
	BeatDesc  string  `json:"beat_desc"`
	FerryNote string  `json:"ferry_note"`
	Policy    string  `json:"policy"`
}

// HoldoutView 留出集两栏（D17）：前半选参 / 后半验证；后半栏参数集与前半一致
// 是结构保证（SplitHalf 产出即保证），SameParams 恒真、不另算。
type HoldoutView struct {
	SelectN     int    `json:"select_n"`
	HoldoutN    int    `json:"holdout_n"`
	SelectSpan  string `json:"select_span"` // 开窗时间跨度（UTC ISO，元数据）
	HoldoutSpan string `json:"holdout_span"`
	ParamDesc   string `json:"param_desc"` // 两栏共用的参数组描述
	Note        string `json:"note"`
}

// SweepView 报告视图顶层（--json 载荷；九节顺序由 RenderMarkdown 固定）。
type SweepView struct {
	LoadedAtISO string      `json:"loaded_at_iso"`
	LoadedAt    float64     `json:"loaded_at"`
	Counts      CountsView  `json:"counts"`
	Gap         GapView     `json:"gap"`
	Tiers       []TierView  `json:"tiers"`
	Diagnostic  []DiagView  `json:"diagnostic"`
	Warm        WarmView    `json:"useless_warm"`
	DoubleCount DoubleView  `json:"double_count"`
	Holdout     HoldoutView `json:"holdout"`
	BlindSpots  []string    `json:"blind_spots"`
	Evidence    string      `json:"evidence_level"`
}

// ---- 视图构建（SweepResult+Dataset → *SweepView，两投影唯一数据源） ----

// BuildView 折叠扫参结果为报告视图。res/ds 均可 nil（零值优雅呈现）；
// 头部计数取 res.Dataset（引擎应已拷贝），其为零而 ds 在场时退回 ds.Counts。
func BuildView(res *SweepResult, ds *Dataset) *SweepView {
	if res == nil {
		res = &SweepResult{}
	}
	counts := res.Dataset
	if counts.TotalWindows == 0 && counts.LoadedAtISO == "" && ds != nil {
		counts = ds.Counts
	}
	v := &SweepView{
		LoadedAtISO: counts.LoadedAtISO,
		LoadedAt:    counts.LoadedAt,
		Counts:      buildCounts(counts),
		BlindSpots:  append([]string(nil), BlindSpots...),
		Evidence:    EvidenceLevel,
	}

	tiers := buildTiers(res)
	for i := range tiers {
		tiers[i].flip = flipTextOf(tiers, i)
	}
	for _, td := range tiers {
		tv := TierView{
			Tier:     td.name,
			Baseline: perfOf(td.baseline),
			Best:     perfOf(td.best),
			Flip:     td.flip,
		}
		switch {
		case tv.Baseline != nil:
			tv.TTLS = tv.Baseline.TTLS
		case tv.Best != nil:
			tv.TTLS = tv.Best.TTLS
		}
		if tv.Baseline != nil && tv.Best != nil {
			tv.Delta = tv.Best.NetSavings - tv.Baseline.NetSavings
		}
		for _, r := range td.rows {
			tv.Rows = append(tv.Rows, *perfOf(r))
		}
		v.Tiers = append(v.Tiers, tv)
	}

	bestPerf := perfOf(bestOf(res))
	v.Gap = GapView{
		Baseline:  perfOf(configBaseline(res)),
		GridBest:  bestPerf,
		Recommend: recommendText(res.Best),
	}
	if v.Gap.Baseline != nil && bestPerf != nil {
		v.Gap.Delta = bestPerf.NetSavings - v.Gap.Baseline.NetSavings
	}

	for i := range res.Diagnostic {
		v.Diagnostic = append(v.Diagnostic, diagOf(&res.Diagnostic[i]))
	}
	v.Warm = buildWarm(counts, bestPerf)
	v.DoubleCount = buildDouble(bestPerf)
	v.Holdout = buildHoldout(ds, bestPerf)
	return v
}

func buildCounts(c LoadCounts) CountsView {
	return CountsView{
		TotalWindows: c.TotalWindows, AfterFilterWindows: c.AfterFilterWindows,
		ReplayWindows: c.ReplayWindows, UnknownCount: c.UnknownCount,
		UncomputableCount: c.UncomputableCount, UnresolvedWindows: c.UnresolvedWindows,
		MultiMatchSessions: c.MultiMatchSessions,
		CloseReasonBefore:  sortedReasons(c.CloseReasonBefore),
		CloseReasonAfter:   sortedReasons(c.CloseReasonAfter),
	}
}

// sortedReasons 键字典序（"" 排最前，markdown 显示为「（未记录）」）。
func sortedReasons(m map[string]int) []ReasonCount {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]ReasonCount, 0, len(keys))
	for _, k := range keys {
		out = append(out, ReasonCount{Reason: k, Count: m[k]})
	}
	return out
}

// tierData 单档聚合（指针直指 res 切片元素，只读）。
type tierData struct {
	name     string
	baseline *ScenarioResult
	rows     []*ScenarioResult
	best     *ScenarioResult
	flip     string // 与上一档的结论翻转判定（首档空串）
}

// buildTiers 规范三档恒在（零值优雅呈现），其余档名按字典序续后；
// 档内最优 = 净节省 max，并列按 CompareGridPoint 取最小（spec tie-break）。
func buildTiers(res *SweepResult) []tierData {
	byName := map[string]*tierData{}
	var order []string
	ensure := func(name string) *tierData {
		if td, ok := byName[name]; ok {
			return td
		}
		td := &tierData{name: name}
		byName[name] = td
		order = append(order, name)
		return td
	}
	for _, name := range []string{ScenarioNameConfig, ScenarioNameThird, ScenarioNameHalf} {
		ensure(name)
	}
	for i := range res.Baselines {
		td := ensure(res.Baselines[i].Scenario)
		if td.baseline == nil {
			td.baseline = &res.Baselines[i]
		}
	}
	for i := range res.Scenarios {
		td := ensure(res.Scenarios[i].Scenario)
		td.rows = append(td.rows, &res.Scenarios[i])
	}
	rank := map[string]int{ScenarioNameConfig: 0, ScenarioNameThird: 1, ScenarioNameHalf: 2}
	sort.SliceStable(order, func(i, j int) bool {
		ri, oki := rank[order[i]]
		rj, okj := rank[order[j]]
		if oki && okj {
			return ri < rj
		}
		if oki != okj {
			return oki // 规范名优先
		}
		return order[i] < order[j]
	})
	out := make([]tierData, 0, len(order))
	for _, name := range order {
		td := byName[name]
		td.best = tierBest(td.rows)
		out = append(out, *td)
	}
	return out
}

func tierBest(rows []*ScenarioResult) *ScenarioResult {
	var best *ScenarioResult
	for _, r := range rows {
		if best == nil || r.NetSavings > best.NetSavings ||
			(r.NetSavings == best.NetSavings && CompareGridPoint(r.Point, best.Point) < 0) {
			best = r
		}
	}
	return best
}

// configBaseline 当前闭式配置 = config 档对照列（名字命中优先，退 ×1.0 档）。
func configBaseline(res *SweepResult) *ScenarioResult {
	var byMult *ScenarioResult
	for i := range res.Baselines {
		b := &res.Baselines[i]
		if b.Scenario == ScenarioNameConfig {
			return b
		}
		if byMult == nil && b.Point.TTLMult == 1.0 {
			byMult = b
		}
	}
	return byMult
}

// bestOf 全局 Best 及其在网格中的完整行数字（Point 全字段相等匹配）。
func bestOf(res *SweepResult) *ScenarioResult {
	if res.Best == nil {
		return nil
	}
	for i := range res.Scenarios {
		if res.Scenarios[i].Point == *res.Best {
			return &res.Scenarios[i]
		}
	}
	return nil
}

// flipTextOf 相邻档结论翻转判定：净节省符号变化 = 结论翻转；
// 符号不变而推荐参数组变化 = 如实记变化；全同 = 无翻转。数据不足照登。
func flipTextOf(tiers []tierData, i int) string {
	if i == 0 {
		return ""
	}
	prev, cur := tiers[i-1].best, tiers[i].best
	if prev == nil || cur == nil {
		return "数据不足，无法判定与上一档的翻转"
	}
	sp, sc := signOf(prev.NetSavings), signOf(cur.NetSavings)
	if sp != sc {
		return fmt.Sprintf("结论翻转：档内最优净节省符号由%s变%s（%.2f → %.2f）",
			signName(sp), signName(sc), prev.NetSavings, cur.NetSavings)
	}
	if prev.Point != cur.Point {
		return fmt.Sprintf("推荐参数组变化（净节省符号不变）：τ %.1f→%.1f s",
			prev.Point.TauS, cur.Point.TauS)
	}
	return "无翻转（净节省符号与推荐参数组均不变）"
}

func signOf(x float64) int {
	switch {
	case x > 0:
		return 1
	case x < 0:
		return -1
	default:
		return 0
	}
}

func signName(s int) string {
	switch s {
	case 1:
		return "正"
	case -1:
		return "负"
	default:
		return "零"
	}
}

// recommendText ttl_s 校准建议（行动出口；跨档点须先复核场景轴翻转）。
func recommendText(best *GridPoint) string {
	if best == nil {
		return "无主网格结果，无法给出 ttl_s 校准建议（引擎未产出）"
	}
	if best.TTLMult == 1.0 {
		return fmt.Sprintf("维持 config ttl_s 现值（主网格最优即当前闭式档位 ×%.2f，ttl_s′=%.1f s）",
			best.TTLMult, best.TTLS)
	}
	return fmt.Sprintf("建议将 config ttl_s 校准至 %.1f s（档位乘数 ×%.2f）；若该点来自 −1/3 / −1/2 档，先复核第 3 节结论翻转再行动",
		best.TTLS, best.TTLMult)
}

// buildWarm 无效保温：历史空表照登 + expired 占比三档线性外推
// （亏损侧 = 占比 × 推荐参数组心跳总花费；调整后净节省 = 推荐净节省 − 亏损）。
func buildWarm(counts LoadCounts, basis *PerfView) WarmView {
	w := WarmView{ExpiredAfter: counts.CloseReasonAfter["expired"]}
	if basis == nil {
		w.BasisDesc = "（无主网格结果，无法外推）"
	} else {
		w.BasisDesc = fmt.Sprintf("推荐参数组（净节省 %.2f / 心跳花费 %.2f）",
			basis.NetSavings, basis.BeatCost)
		for _, r := range expiredRatios {
			loss := r * basis.BeatCost
			w.Scenarios = append(w.Scenarios, WarmRow{
				RatioPct:    int(r*100 + 0.5),
				Loss:        loss,
				AdjustedNet: basis.NetSavings - loss,
			})
		}
	}
	if w.ExpiredAfter == 0 {
		w.HistoricalNote = "真实项目历史样本无 expired 窗——历史空表照登（评分口径：无效保温单列不合并）"
	} else {
		w.HistoricalNote = "历史存在 expired 窗——无效保温有真实亏损样本，下表为敏感性参考"
	}
	return w
}

// buildDouble 双计检查（D11）：契约无摆渡成本输入字段 → 占位说明，不造数。
func buildDouble(basis *PerfView) DoubleView {
	d := DoubleView{
		FerryNote: "本报告结构无摆渡成本输入字段——见账本 handoff 科目（历史事实）",
		Policy:    "两列各列各的、并排呈现，不互相抵扣；第 2/3 节净节省只抵扣心跳花费",
		BeatDesc:  "（无主网格结果，取 0）",
	}
	if basis != nil {
		d.BeatCost = basis.BeatCost
		d.BeatDesc = fmt.Sprintf("推荐参数组共 %d 跳 / %d 窗（反事实推断）", basis.Beats, basis.Windows)
	}
	return d
}

// buildHoldout 留出集两栏；ds 缺席时窗数 0 + 占位（评分分半字段契约暂无，
// 两栏先以窗数、时间跨度与共用参数组呈现）。
func buildHoldout(ds *Dataset, basis *PerfView) HoldoutView {
	h := HoldoutView{
		ParamDesc: paramDesc(basis),
		Note:      "验证栏参数不得来自后半数据（结构保证），后半栏参数集与前半一致",
	}
	if ds == nil {
		h.SelectSpan, h.HoldoutSpan = "（数据集未传入）", "（数据集未传入）"
		return h
	}
	h.SelectN, h.HoldoutN = len(ds.SelectHalf), len(ds.HoldoutHalf)
	h.SelectSpan, h.HoldoutSpan = spanISO(ds.SelectHalf), spanISO(ds.HoldoutHalf)
	return h
}

// spanISO 开窗时间跨度（UTC ISO——纯元数据；空半区照登「（空）」）。
func spanISO(ws []Window) string {
	if len(ws) == 0 {
		return "（空）"
	}
	lo, hi := ws[0].OpenedTS, ws[0].OpenedTS
	for _, w := range ws {
		if w.OpenedTS < lo {
			lo = w.OpenedTS
		}
		if w.OpenedTS > hi {
			hi = w.OpenedTS
		}
	}
	f := func(t float64) string { return time.Unix(int64(t), 0).UTC().Format("2006-01-02T15:04:05Z") }
	return f(lo) + ".." + f(hi)
}

// paramDesc 参数组一行描述（留出集两栏共用，体现「后半栏参数集与前半一致」）。
func paramDesc(p *PerfView) string {
	if p == nil {
		return "（无网格结果）"
	}
	return fmt.Sprintf("τ=%.1f s · 首跳=%.1f s · 等待上限=%s（ttl_s′=%.1f s ×%.2f）",
		p.TauS, p.FirstBeatS, fmtCap(p.CapS), p.TTLS, p.TTLMult)
}

// perfOf ScenarioResult → 视图行（nil 安全）；CapS +Inf → nil（JSON null）。
func perfOf(r *ScenarioResult) *PerfView {
	if r == nil {
		return nil
	}
	p := &PerfView{
		Scenario: r.Scenario, TTLS: r.Point.TTLS, TTLMult: r.Point.TTLMult,
		TauS: r.Point.TauS, FirstBeatS: r.Point.FirstBeatS,
		Windows: r.Windows, Beats: r.Beats, BeatCost: r.BeatCost,
		GrossSavings: r.GrossSavings, NetSavings: r.NetSavings,
		BreakerTrips: r.BreakerTrips, UselessWarm: r.UselessWarm,
	}
	if !math.IsInf(r.Point.CapS, 1) {
		c := r.Point.CapS
		p.CapS = &c
	}
	return p
}

func diagOf(r *ScenarioResult) DiagView {
	d := DiagView{
		TauS: r.Point.TauS, FirstBeatS: r.Point.FirstBeatS,
		Windows: r.Windows, Beats: r.Beats, BeatCost: r.BeatCost,
		NetSavings: r.NetSavings, BreakerTrips: r.BreakerTrips, UselessWarm: r.UselessWarm,
	}
	if !math.IsInf(r.Point.CapS, 1) {
		c := r.Point.CapS
		d.CapS = &c
	}
	return d
}

// ---- 投影一：--json ----

// RenderJSON 结构化投影（ticket04 的 --json 字节；缩进 2 空格，确定性）。
func RenderJSON(res *SweepResult, ds *Dataset) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(BuildView(res, ds)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---- 投影二：markdown（docs 实验报告惯例：中文标题 + 表格） ----

// RenderMarkdown 九节实验报告（节序即规格：头部/差距表/场景轴/诊断网格/
// 无效保温/双计检查/留出集/盲区/证据等级）。
func RenderMarkdown(res *SweepResult, ds *Dataset) string {
	v := BuildView(res, ds)
	L := []string{"# 等待窗扫参实验报告", ""}

	// 1. 头部：装载时点戳 + 数据集双口径 + 桶计数。
	iso := v.LoadedAtISO
	if iso == "" {
		iso = "（未装载）"
	}
	L = append(L, "## 1. 头部：装载时点戳与数据集双口径", "",
		fmt.Sprintf("- 装载时点戳：%s（epoch %.0f）——数据活体增长，结论以此时点为准", iso, v.LoadedAt),
		"- 性质：离线反事实扫参（第一遍，零真跳时代纯推演；第二遍开闸后以 wait_close.cost_actual 对账）", "",
		"| 口径 | 窗数 |", "|---|---:|",
		fmt.Sprintf("| 全部窗（过滤前） | %d |", v.Counts.TotalWindows),
		fmt.Sprintf("| 真实项目过滤后 | %d |", v.Counts.AfterFilterWindows),
		fmt.Sprintf("| 其中可重放（价格可算） | %d |", v.Counts.ReplayWindows),
		fmt.Sprintf("| unknown 桶（未还原留存） | %d |", v.Counts.UnknownCount),
		fmt.Sprintf("| 不可算桶（缺 P_cache） | %d |", v.Counts.UncomputableCount), "",
		fmt.Sprintf("- 未还原窗（过滤前口径）：%d", v.Counts.UnresolvedWindows),
		fmt.Sprintf("- 多值匹配 session 数：%d", v.Counts.MultiMatchSessions),
		fmt.Sprintf("- close_reason 分布（过滤前，全部窗）：%s", reasonLine(v.Counts.CloseReasonBefore)),
		fmt.Sprintf("- close_reason 分布（过滤后，项目过滤幸存、含不可算）：%s", reasonLine(v.Counts.CloseReasonAfter)), "")

	// 2. 差距表：当前闭式配置 vs 主网格最优 + ttl_s 校准建议 + 同档内对比。
	L = append(L, "## 2. 差距表：当前闭式配置 vs 主网格最优", "",
		"| 对比项 | ttl_s′ | τ（间隔） | 首跳时机 | 等待上限 | 心跳花费 | 净节省 |",
		"|---|---:|---:|---:|---:|---:|---:|",
		perfRow("当前闭式配置（config 档对照 ×1.0）", v.Gap.Baseline),
		gridBestRow(v.Gap.GridBest), "")
	delta := "—"
	if v.Gap.Baseline != nil && v.Gap.GridBest != nil {
		delta = fmt.Sprintf("**%.2f**", v.Gap.Delta)
	}
	L = append(L,
		fmt.Sprintf("- 差距（主网格最优 − 当前闭式配置）：%s", delta),
		fmt.Sprintf("- **推荐（ttl_s 校准建议）**：%s", v.Gap.Recommend), "",
		"| 档 | 闭式对照净节省 | 档内网格最优净节省 | 差距 |", "|---|---:|---:|---:|")
	for _, tv := range v.Tiers {
		baseNet, bestNet, gapCell := "—", "—", "—"
		if tv.Baseline != nil {
			baseNet = fmt.Sprintf("%.2f", tv.Baseline.NetSavings)
		}
		if tv.Best != nil {
			bestNet = fmt.Sprintf("%.2f", tv.Best.NetSavings)
		}
		if tv.Baseline != nil && tv.Best != nil {
			gapCell = fmt.Sprintf("%.2f", tv.Delta)
		}
		L = append(L, fmt.Sprintf("| %s | %s | %s | %s |", tv.Tier, baseNet, bestNet, gapCell))
	}
	L = append(L, "")

	// 3. TTL 三档场景轴 + 结论翻转点。
	L = append(L, "## 3. TTL 三档场景轴与结论翻转点", "",
		"| 档名 | ttl_s | 闭式对照净节省 | 档内网格最优净节省 | 档内最优跳数 | 档内最优心跳花费 | 熔断触发窗 | 无效保温窗 |",
		"|---|---:|---:|---:|---:|---:|---:|---:|")
	for _, tv := range v.Tiers {
		L = append(L, tierRow(tv))
	}
	L = append(L, "")
	for _, tv := range v.Tiers {
		if tv.Flip != "" {
			L = append(L, fmt.Sprintf("- 档「%s」与上一档对比：%s", tv.Tier, tv.Flip))
		}
	}
	L = append(L, "- 全网格逐行数值见 --json（tiers[].rows）。", "")

	// 4. 诊断网格（单列，标注非可部署）。
	L = append(L, "## 4. 诊断网格（诊断用，非可部署）", "",
		"> 只读展示、不产生行动建议；结论不得直接输出 ttl_s 建议（规格治理节）。",
		"",
		"| τ | 首跳 | 等待上限 | 窗数 | 跳数 | 净节省 | 无效保温窗 |",
		"|---|---:|---:|---:|---:|---:|---:|")
	if len(v.Diagnostic) == 0 {
		L = append(L, "（引擎未产出诊断网格）")
	}
	for _, d := range v.Diagnostic {
		L = append(L, fmt.Sprintf("| %.1f | %.1f | %s | %d | %d | %.2f | %d |",
			d.TauS, d.FirstBeatS, fmtCap(d.CapS), d.Windows, d.Beats, d.NetSavings, d.UselessWarm))
	}
	L = append(L, "")

	// 5. 无效保温单列（历史空表照登 + expired 占比三档场景）。
	L = append(L, "## 5. 无效保温（单列）", "",
		fmt.Sprintf("- 历史样本（过滤后口径）：expired 窗 = %d。%s", v.Warm.ExpiredAfter, v.Warm.HistoricalNote),
		"- 无效保温单列不合并进净节省（D2 语义）。",
		"",
		"| expired 占比 | 亏损侧（心跳花费口径） | 调整后净节省 |", "|---|---:|---:|")
	if len(v.Warm.Scenarios) == 0 {
		L = append(L, "| 0% / 10% / 30% | — | — |")
	}
	for _, r := range v.Warm.Scenarios {
		L = append(L, fmt.Sprintf("| %d%% | %.2f | %.2f |", r.RatioPct, r.Loss, r.AdjustedNet))
	}
	L = append(L, "",
		fmt.Sprintf("- 口径（D9 线性外推）：亏损侧 = expired 占比 × 推荐参数组心跳总花费；调整后净节省 = 推荐净节省 − 亏损。基准：%s",
			v.Warm.BasisDesc), "")

	// 6. 双计检查：心跳推演与摆渡历史各列各的，不互相抵扣。
	L = append(L, "## 6. 双计检查", "",
		"| 项目 | 金额 | 口径 |", "|---|---:|---|",
		fmt.Sprintf("| 心跳花费（本扫参推演） | %.2f | %s |", v.DoubleCount.BeatCost, v.DoubleCount.BeatDesc),
		fmt.Sprintf("| 摆渡成本（背景数据，历史事实） | — | %s |", v.DoubleCount.FerryNote),
		"",
		fmt.Sprintf("- %s。", v.DoubleCount.Policy), "")

	// 7. 留出集两栏：前半选参 / 后半验证（参数集与前半一致）。
	L = append(L, "## 7. 留出集：前半选参 / 后半验证", "",
		"| 栏 | 角色 | 窗数 | 开窗跨度（UTC） | 参数组 |", "|---|---|---:|---|---|",
		fmt.Sprintf("| 前半 | 选参 | %d | %s | %s |", v.Holdout.SelectN, v.Holdout.SelectSpan, v.Holdout.ParamDesc),
		fmt.Sprintf("| 后半 | 只验证 | %d | %s | 参数集与前半一致（结构保证） |", v.Holdout.HoldoutN, v.Holdout.HoldoutSpan),
		"", fmt.Sprintf("- %s", v.Holdout.Note), "")

	// 8. 盲区声明（固定文案）。
	L = append(L, "## 8. 盲区声明", "")
	for i, b := range v.BlindSpots {
		L = append(L, fmt.Sprintf("%d. %s", i+1, b))
	}
	L = append(L, "")

	// 9. 证据等级（规格原文逐字）。
	L = append(L, "## 9. 证据等级", "", v.Evidence, "")
	return strings.Join(L, "\n")
}

// ---- markdown 行助手（格式锚点：金额 2 位、token 整数、秒 1 位） ----

// fmtCap 等待上限（nil = +Inf → ∞）。
func fmtCap(capP *float64) string {
	if capP == nil {
		return "∞"
	}
	return fmt.Sprintf("%.1f", *capP)
}

func reasonLine(rs []ReasonCount) string {
	if len(rs) == 0 {
		return "（无）"
	}
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		name := r.Reason
		if name == "" {
			name = "（未记录）"
		}
		parts = append(parts, fmt.Sprintf("%s %d", name, r.Count))
	}
	return strings.Join(parts, " · ")
}

// perfRow 差距表行（p 缺失 → 全「—」占位）。
func perfRow(label string, p *PerfView) string {
	if p == nil {
		return fmt.Sprintf("| %s | — | — | — | — | — | — |", label)
	}
	return fmt.Sprintf("| %s | %.1f | %.1f | %.1f | %s | %.2f | %.2f |",
		label, p.TTLS, p.TauS, p.FirstBeatS, fmtCap(p.CapS), p.BeatCost, p.NetSavings)
}

func gridBestRow(p *PerfView) string {
	if p == nil {
		return perfRow("主网格最优（引擎未产出）", nil)
	}
	return perfRow(fmt.Sprintf("主网格最优（档位乘数 ×%.2f）", p.TTLMult), p)
}

// tierRow 场景轴行（对照/档内最优缺任一侧 → 该侧「—」）。
func tierRow(tv TierView) string {
	ttl, baseNet, bestNet, beats, cost, trips, warm := "—", "—", "—", "—", "—", "—", "—"
	switch {
	case tv.Baseline != nil:
		ttl = fmt.Sprintf("%.1f", tv.Baseline.TTLS)
	case tv.Best != nil:
		ttl = fmt.Sprintf("%.1f", tv.Best.TTLS)
	}
	if tv.Baseline != nil {
		baseNet = fmt.Sprintf("%.2f", tv.Baseline.NetSavings)
	}
	if b := tv.Best; b != nil {
		bestNet = fmt.Sprintf("%.2f", b.NetSavings)
		beats = fmt.Sprintf("%d", b.Beats)
		cost = fmt.Sprintf("%.2f", b.BeatCost)
		trips = fmt.Sprintf("%d", b.BreakerTrips)
		warm = fmt.Sprintf("%d", b.UselessWarm)
	}
	return fmt.Sprintf("| %s | %s | %s | %s | %s | %s | %s | %s |",
		tv.Tier, ttl, baseNet, bestNet, beats, cost, trips, warm)
}
