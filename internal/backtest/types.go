// Package backtest 等待窗扫参（ferryman backtest）的数据地基与契约类型。
//
// 票01（本包）：从 <data_dir>/accounts/*.jsonl 装载 kind=window 记录窗
// （规格 D4 数据源①，纯元数据），project 归属还原（D18）、项目 glob 过滤
// （D16）、时间对半切留出集（D17）、provider 缺 P_cache 不可算桶（D19）。
// 票02（网格引擎）与票03（报告）只依赖本包类型，不互相依赖；
// 全部窗计数实跑现算并带装载时点戳（review_blocks F4）。
package backtest

// UnknownProject 未还原窗的 project 占位值（unknown 桶成员）。
const UnknownProject = "unknown"

// Window 一个等待窗的重放输入（账本 window 行纯元数据 + 还原后归属）。
type Window struct {
	RowTS           float64 // 账本行盖章 ts（≈关窗时刻；价格版本按它取 PriceBook.At）
	OpenedTS        float64 // 开窗时刻（留出集切分与窗排序键）
	ClosedTS        float64 // 关窗时刻
	DurS            float64 // 等待时长——重放主输入（spec：不依赖 close_reason）
	PrefixTokens    int     // 前缀 token——重放主输入
	CloseReason     string  // 关窗原因（只进报告口径分布，重放不用）
	Agent           string  // agent（cc|codex）
	SessionID       string  // 会话 ID
	Project         string  // 行载 project 或还原结果；未还原 = UnknownProject
	ProjectResolved bool    // false = unknown 桶成员（行 project 空且无可连接 usage 行）
}

// Dataset 装载产物：窗集合 + 两半切分 + 各桶 + 全套计数。
// 三个切片（Windows/Unknown/Uncomputable）均按全序排列（OpenedTS 升序，
// 平局由 SessionID 等逐级决出），同输入两次装载逐元素一致。
type Dataset struct {
	Windows      []Window   // 可重放窗：项目过滤幸存且价格可算（引擎重放输入）
	SelectHalf   []Window   // 留出集前半：选参（窗数 ceil(n/2)）
	HoldoutHalf  []Window   // 留出集后半：只验证（验证栏参数不得来自后半——结构保证）
	Unknown      []Window   // unknown 桶：project 未还原（照登；不被正集包含、受 exclude 约束）
	Uncomputable []Window   // 不可算桶：provider 缺 P_cache（照登不剔除不估算，D19）
	Counts       LoadCounts // 全套计数 + 装载时点戳
}

// LoadCounts 装载计数（F4：全部实跑现算；规格文本数字仅时点快照，永不进代码）。
// 口径注：未还原数/多值匹配数为过滤前口径（数据质量面）；
// AfterFilter/Unknown/Uncomputable/Replay 为过滤后口径。
type LoadCounts struct {
	LoadedAt           float64        // 装载时点戳（epoch 秒；数据活体增长，报告以此为准）
	LoadedAtISO        string         // 时点戳 ISO 形（与账本行盖章同格式）
	TotalWindows       int            // 过滤前：读到的 kind=window 总行数
	AfterFilterWindows int            // 过滤后：项目过滤幸存窗数 = Replay + Uncomputable + Unknown 留存
	ReplayWindows      int            // 可重放窗数（过滤后且价格可算）
	UnresolvedWindows  int            // 未还原数：project 空且无可连接 usage 行（过滤前）
	MultiMatchSessions int            // 多值匹配 session 数：还原候选含 ≥2 个不同 project（主/子代理行 cwd 差异所致）
	UnknownCount       int            // unknown 桶数（过滤后留存）
	UncomputableCount  int            // 不可算桶数（过滤后口径）
	CloseReasonBefore  map[string]int // close_reason 分布——过滤前口径（全部窗）
	CloseReasonAfter   map[string]int // close_reason 分布——过滤后口径（项目过滤幸存，含不可算）
}

// GridPoint 一组心跳参数（τ 间隔 / 首跳时机 / 等待上限）。
// 主网格：由 policy.Compute(ttl_s′) 闭式生成，TTLMult 记档位乘数；
// 诊断网格：自由三参锚点枚举（CapS 允许 +Inf = ∞），TTLMult 记 0（不适用）。
type GridPoint struct {
	TTLMult    float64 // ttl_s 档位乘数（主网格 0.5..2.0；诊断网格 0 = 不适用）
	TTLS       float64 // 档位 ttl_s′
	TauS       float64 // τ 心跳间隔（闭式 = safety × TTL′）
	FirstBeatS float64 // 首跳时机（开窗后秒数）
	CapS       float64 // 等待上限（秒；+Inf = ∞，仅诊断网格档）
}

// CompareGridPoint 确定性并列裁决（spec tie-break）：τ 升序 → 首跳时机升序 →
// 等待上限升序，返回 -1/0/1。一切排序键并列时按此取最小，保证同输入两次
// 运行逐字节一致。
func CompareGridPoint(a, b GridPoint) int {
	if c := cmpFloat(a.TauS, b.TauS); c != 0 {
		return c
	}
	if c := cmpFloat(a.FirstBeatS, b.FirstBeatS); c != 0 {
		return c
	}
	return cmpFloat(a.CapS, b.CapS)
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// ScenarioResult 单场景 × 单参数组的重放结果。
// 证据等级（报告须硬标）：Windows 数为事实；Beats/成本/熔断/无效保温均为
// 反事实推断（第一遍无任何 cost_actual 可对账）。
type ScenarioResult struct {
	Scenario     string    // 场景档名（票03 定文案：config / −1/3 / −1/2）
	TTLS         float64   // 场景 TTL（秒）
	Point        GridPoint // 本结果对应的参数组
	Windows      int       // 参与重放的窗数
	Beats        int       // 心跳总跳数（反事实推断）
	BeatCost     float64   // 心跳总花费（反事实推断）
	GrossSavings float64   // Σ 避免重付（反事实推断）
	NetSavings   float64   // 净节省 = GrossSavings − BeatCost（主指标）
	BreakerTrips int       // 熔断触发窗数（连续 miss ≥ 2 停跳，beat.Breaker 语义）
	UselessWarm  int       // 无效保温窗数（单列不合并，spec 评分口径）
}

// SweepResult 全场景 × 网格聚合产物（--json 载荷的顶层结构，票03 消费）。
// 差距表 = Baselines（对照列）与 Scenarios（主网格）同档内对比，不跨档错位。
type SweepResult struct {
	Dataset    LoadCounts       // 数据集计数（含装载时点戳）
	Scenarios  []ScenarioResult // 主网格：可行域网格（唯一产生行动建议）
	Baselines  []ScenarioResult // 对照列：各 TTL 场景档 policy.Compute 闭式现算
	Best       *GridPoint       // 网格最优参数组（并列按 CompareGridPoint 取最小）
	BestNet    float64          // 最优净节省
	Diagnostic []ScenarioResult // 诊断网格：自由三参扫描——只读展示、非可部署，单列不进推荐
}
