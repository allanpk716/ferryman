# 票05 · 策略计算器按上游推导同模型阈值

## What to build
参数供给的核心公式,打通端到端:internal/policy 扩展——按上游输入(TTL 观测分布、价格表、闲置间隔分布)闭式推导同模型触发阈值,输出钳位于 [10min, 总结阈值] ∩ 配置上限(全局单值,票01 已留按上游覆盖口)。manual 档:生效值=配置值(计算器不参与);recommend/auto 档:计算器值,配置值为上限。价格表缺 p_cache 的上游:计算器**拒绝推导**(与既有"无缓存经济"红线一致,返回明确错误)。公式单源:全仓唯一实现(公式单源红线),报表/面板后续复用本出口。

## 验收标准
- [ ] 给定 TTL 观测+价格+间隔分布,推导结果可复算(闭式可解释,单测对拍)
- [ ] 钳位:推导值被 [10,总结阈值] 与配置上限双向钳,单测覆盖越界用例
- [ ] manual=配置值、recommend/auto=计算器值受上限,三态语义单测
- [ ] 缺 p_cache 拒算并给人话错误;单测覆盖
- [ ] 既有 policy 测试照绿(心跳参数推导不回归)

## Blocked by
票01

## 涉及路径
internal/policy/
internal/prices/

## 副作用声明
go test ./internal/policy/... ./internal/prices/...;go build ./...

## decision_refs
D2 D9 D13

## review_blocks
F4(按上游分列的生效值出口)
