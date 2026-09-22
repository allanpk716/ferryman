package daemon

// same_model_effective.go — 票08:同模型"生效值"的进程内统一读法(票07 遗留
// 接线的装配单点)。守望触发(watcher.maybeSameModel)与面板端点
// (query_config_tuning.go)共用本函数——daemon 包内只此一处装配,公式本身
// 仍单源票05 policy.SameModelEffectiveThreshold(经 tuning.Store 出口)。
//
// 观测输入口径(重要,防误读为造数):运行侧传基线前缀/输出(见常量),无闲置
// 观测——闲置/TTL 分布是离线扫参面(票06)的输入;运行时唯一会并进公式输入的
// 是调参校准(Store.EffectiveThreshold 内注入校准投影的 TTL 观测替代集)。
// 由此本函数的返回值只有两种来历,且基线值对两者都无影响:
//   - manual 档:出口直回配置值,观测根本不进计算器;
//   - 无校准的 recommend/auto 档:走种子路径——该路径不消费前缀/输出规模
//     (成本常数只在非种子路径算),票05 出口只要求其为正;
//   - 有校准的 recommend/auto 档:TTL 观测被校准替换后缺闲置观测,必 bad_obs
//     拒算——调用方回落(守望)或如实展示(端点),不会带着基线值算出数字。
// 现算失败(无价格本/钳位矛盾/bad_obs 等)由调用方处置。

import (
	"errors"

	"ferryman/internal/config"
	"ferryman/internal/policy"
	"ferryman/internal/prices"
	"ferryman/internal/tuning"
)

// 运行侧基线观测(过票05 出口的正值守卫;取值论证见文件头——种子路径与
// manual 路径都不消费它们,带校准的路径必拒算,故对任何返回值无影响)。
// 量级与扫参黄金对拍口径一致:前缀 150k token、追加重放叙事输出 1k token。
const (
	smBaselinePrefixTokens = 150000.0
	smBaselineOutTokens    = 1000.0
)

// sameModelEffective 生效值现算:一律经 tuning.Store.EffectiveThreshold(三态
// 语义:manual 档配置值即生效值;recommend/auto 档计算器在配置上限内现算)。
// ts nil = 调参库未装配(裸构造/旧测试形态)→ 报错,由调用方回落。
func sameModelEffective(c *config.Config, ts *tuning.Store,
	books map[string]prices.PriceBook, upstream string) (policy.SameModelResult, error) {
	if c == nil {
		return policy.SameModelResult{}, errors.New("配置未装配")
	}
	if ts == nil {
		return policy.SameModelResult{}, errors.New("调参状态库未装配")
	}
	obs := policy.SameModelObs{PrefixTokens: smBaselinePrefixTokens,
		OutTokens: smBaselineOutTokens}
	return ts.EffectiveThreshold(c, books, upstream, obs)
}

// ctErrKind 拒算类别提取(*policy.SameModelError.Kind;其余归 error)。
// 稳定字符串与票05 拒算分类同源,面板按此展示拒算原因。
func ctErrKind(err error) string {
	var e *policy.SameModelError
	if errors.As(err, &e) {
		return e.Kind
	}
	return "error"
}

// ctStatusText 建议状态人话(待审/已接受/已拒绝/已自动应用;未知原样透出)。
func ctStatusText(status string) string {
	switch status {
	case tuning.StPending:
		return "待审"
	case tuning.StAccepted:
		return "已接受"
	case tuning.StRejected:
		return "已拒绝"
	case tuning.StAutoApplied:
		return "已自动应用"
	}
	return status
}
