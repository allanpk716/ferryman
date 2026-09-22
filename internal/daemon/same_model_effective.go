package daemon

// same_model_effective.go — 票08:同模型"生效值"的进程内统一读法(票07 遗留
// 接线的装配单点)。守望触发(watcher.maybeSameModel)与面板端点
// (query_config_tuning.go)共用本函数——daemon 包内只此一处装配,公式本身
// 仍单源票05 policy.SameModelEffectiveThreshold(经 tuning.Store 出口)。
//
// 观测输入口径(终局修复后的装配契约,防误读为造数):
//   - 观测一律取运行侧基线(tuning.RuntimeBaselineObs,token 形状单源)——
//     TTL/闲置分布观测不存在运行时供给(离线扫参面的输入),运行时并进公式
//     输入的唯一路径是调参校准:Store.EffectiveThreshold 注入校准的 TTL+
//     闲置观测替代集(有校准以校准覆盖,无校准走基线种子路)。
//   - ts 非 nil:经 Store 出口三态现算。
//   - ts nil(裸构造/旧测试形态):等同无校准——直调票05 出口,与空校准的
//     Store 路径逐位一致(manual 档配置值即生效值;recommend/auto 档基线
//     无 TTL 观测 → 种子层)。不再视为现算失败。
// 三态语义、钳位与拒算全在票05 出口;ts 非 nil 路径的现算失败(缺闲置的
// 旧档位校准 bad_obs 等)由调用方处置(守望回落冷启动种子并告警一次)。
// 基线 token 形状对各种返回值的影响:manual 路径不进计算器;种子路径不
// 消费 token 规模(票05 出口只要求其为正);带校准路径的成本常数按基线
// 量级计(150k/1k,扫参黄金对拍口径)。

import (
	"errors"

	"ferryman/internal/config"
	"ferryman/internal/policy"
	"ferryman/internal/prices"
	"ferryman/internal/tuning"
)

// sameModelEffective 生效值现算:一律以运行侧基线观测经 tuning.Store.
// EffectiveThreshold(三态语义:manual 档配置值即生效值;recommend/auto 档
// 计算器在配置上限内现算)。
func sameModelEffective(c *config.Config, ts *tuning.Store,
	books map[string]prices.PriceBook, upstream string) (policy.SameModelResult, error) {
	if c == nil {
		return policy.SameModelResult{}, errors.New("配置未装配")
	}
	obs := tuning.RuntimeBaselineObs()
	if ts == nil { // 无库=无校准:直调票05 出口,与空校准 Store 路径一致
		return policy.SameModelEffectiveThreshold(c, books, upstream, obs)
	}
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
