// 票02：网格构造——主网格（可行域，唯一产生行动建议）/ 对照基线 / 诊断网格（非可部署）。
//
// 两条口径分离（票级评审 F1 seam）：
//   - 网格点是**绝对参数向量**：档位闭式曲线取经济价格表**最新生效版本** ×
//     数据集**参考前缀**（重放窗 prefix_tokens 均值四舍五入）一次 Compute 摊平——
//     与诊断网格的自由三参锚点同维可比，tie-break 字典序才有单一确义。
//   - **金额逐窗版本化**：单跳成本/避免重付按窗行时刻的生效版本现算
//     （引擎内调 policy.Compute，与 report.StrategyTable 同惯例）——
//     参数绝对、金额版本化，两不相混。
//
// 公式铁律：τ/首跳/上限/金额常数全部出自 internal/policy.Compute（公式单源），
// 本包不出现第二份价格算术。首跳与 τ 同源（规格「首跳时机对照语义」：
// 闭式侧首跳 = Compute 输出，即 policy.SimulateBeats 惯例的 T0+τ 首跳）。
package backtest

import (
	"math"

	"ferryman/internal/policy"
	"ferryman/internal/prices"
)

// TTLMultipliers 主网格 ttl_s 档位乘数表：config 当前值 × 乘数，12 档全集
// （对数均匀；规格「扫参网格」：档位表即全集，无开闭区间歧义）。
var TTLMultipliers = [...]float64{0.5, 0.6, 0.7, 0.8, 0.9, 1.0, 1.1, 1.2, 1.4, 1.6, 1.8, 2.0}

// TTL 场景档稳定键（规格 D3：config TTL / −1/3 / −1/2）。与报告规范档名
// （report.go ScenarioName* 常量）**同值单源**：引擎 Scenario 字段直接携带
// 规范档名，报告场景轴按名匹配不再出现占位行（78e8c50 首跑暴露的接缝瑕疵，
// 协调者修复 2026-09-21）；切片出场序即此处常量声明序。
const (
	ScenarioConfig     = ScenarioNameConfig
	ScenarioMinusThird = ScenarioNameThird
	ScenarioMinusHalf  = ScenarioNameHalf
)

// scenario 单个 TTL 场景档（缓存存活判定的 TTL 值 + 稳定键）。
type scenario struct {
	Name string
	TTLS float64
}

// scenariosOf TTL 场景三档（D3）：config / −1/3（=2/3·TTL）/ −1/2（=TTL/2）。
// 切片序固定 = 报告出场序，不排不抖。
func scenariosOf(configTTLS float64) []scenario {
	return []scenario{
		{Name: ScenarioConfig, TTLS: configTTLS},
		{Name: ScenarioMinusThird, TTLS: configTTLS * 2 / 3},
		{Name: ScenarioMinusHalf, TTLS: configTTLS * 0.5},
	}
}

// refVersion 网格闭式曲线的参考价版本：价格表最新生效版本（At(+Inf) =
// 最后一个可解析生效日）。对照列同上下文，差距表同档内可比。
func refVersion(book *prices.PriceBook) *prices.PriceVersion {
	return book.At(math.Inf(1))
}

// refPrefixTokens 数据集参考前缀：重放窗 prefix_tokens 均值四舍五入取整。
// 闭式 cap 随前缀弱变（perBeat 含固定 300 out-token 项，前缀不完全约去），
// 取数据集均值摊平；金额逐窗版本化，不受参考前缀影响。空集返回 0（调用方
// 空集短路，不会拿去 Compute）。
func refPrefixTokens(ds *Dataset) int {
	if len(ds.Windows) == 0 {
		return 0
	}
	sum := 0
	for _, w := range ds.Windows {
		sum += w.PrefixTokens
	}
	return int(math.Round(float64(sum) / float64(len(ds.Windows))))
}

// computeAt 参考上下文一跳闭式（τ/首跳/上限的唯一来源——公式单源接线）。
func computeAt(book *prices.PriceBook, pv *prices.PriceVersion, ttlS float64,
	prefixTokens int) (policy.HeartbeatPolicy, error) {
	return policy.Compute(*book, *pv, ttlS, prefixTokens,
		policy.DefaultBeatOutTokens, policy.DefaultSafety,
		policy.DefaultGraceS, policy.DefaultMinPrefixTokens)
}

// closedPoint 闭式参数组 → 网格点（首跳 = τ，与 τ 同源）。
func closedPoint(mult float64, ttls float64, pol policy.HeartbeatPolicy) GridPoint {
	return GridPoint{
		TTLMult: mult, TTLS: ttls,
		TauS: pol.TauS, FirstBeatS: pol.TauS, CapS: pol.WorthwhileCapS,
	}
}

// BuildMainGrid 主网格（可行域，唯一产生行动建议）：12 乘数档全集，每档
// policy.Compute(ttl_s′) 闭式生成 (τ, 首跳, 上限)。档序 = 乘数表序（升序），
// 保证最优点必落在公式可生成的参数曲线上，可直接映射回 ttl_s 校准建议。
func BuildMainGrid(book *prices.PriceBook, pv *prices.PriceVersion, configTTLS float64,
	prefixTokens int) ([]GridPoint, error) {
	pts := make([]GridPoint, 0, len(TTLMultipliers))
	for _, m := range TTLMultipliers {
		pol, err := computeAt(book, pv, configTTLS*m, prefixTokens)
		if err != nil {
			return nil, err
		}
		pts = append(pts, closedPoint(m, configTTLS*m, pol))
	}
	return pts, nil
}

// BuildBaseline 对照基线：TTL 场景档 TTL′ 的闭式参数组（τ=safety×TTL′ 同步
// 变化）——差距表永远同档内对比，不跨档错位。TTLMult 记 1.0（场景 TTL 自身）。
func BuildBaseline(book *prices.PriceBook, pv *prices.PriceVersion, scenarioTTLS float64,
	prefixTokens int) (GridPoint, error) {
	pol, err := computeAt(book, pv, scenarioTTLS, prefixTokens)
	if err != nil {
		return GridPoint{}, err
	}
	return closedPoint(1.0, scenarioTTLS, pol), nil
}

// diagCapMults 诊断 cap 锚：闭式 cap ×{0.5, ×1.0, ×2.0, ∞}（∞ = +Inf 不设上限）。
var diagCapMults = [...]float64{0.5, 1.0, 2.0, math.Inf(1)}

// BuildDiagnosticGrid 诊断网格（只读展示、非可部署、不产生行动建议）：
// τ 锚 = 场景档闭式 τ × 主网格同表乘数（12，全集枚举）× 首跳锚 {开窗即跳,
// τ/2, τ} × cap 锚 4 = 每场景档 144 点，三场景共 432。TTLMult 记 0
// （types.go 契约：0 = 档位乘数不适用 = 「非可部署」机器标记；诊断点
// 永不进 Scenarios/Best，结果经 SweepResult.Diagnostic 单列输出）。
// 枚举序：τ 乘数 → 首跳锚 → cap 锚，全部固定表序——确定性锚点。
func BuildDiagnosticGrid(book *prices.PriceBook, pv *prices.PriceVersion,
	scenarioTTLS float64, prefixTokens int) ([]GridPoint, error) {
	pol, err := computeAt(book, pv, scenarioTTLS, prefixTokens)
	if err != nil {
		return nil, err
	}
	pts := make([]GridPoint, 0, len(TTLMultipliers)*3*len(diagCapMults))
	for _, m := range TTLMultipliers {
		tau := pol.TauS * m
		for _, fb := range [...]float64{0, tau / 2, tau} { // 开窗即跳 / τ/2 / τ
			for _, cm := range diagCapMults {
				pts = append(pts, GridPoint{
					TTLMult: 0, TTLS: scenarioTTLS,
					TauS: tau, FirstBeatS: fb,
					CapS: pol.WorthwhileCapS * cm, // 有限正数 × Inf = +Inf
				})
			}
		}
	}
	return pts, nil
}
