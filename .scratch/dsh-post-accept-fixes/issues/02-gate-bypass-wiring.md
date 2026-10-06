# 票02 · gate bypass 消耗接线 + 拦截账本 idle 显示修正

## What to build
dsh 用户发「强续」消息放行(bypass)的那一刻,该会话自己的现行交接被标记消耗(调票01 的 store 方法);CC/codex 的 bypass 分支行为逐字不变。顺手:block 分支记账的 idle_s 改用闸门自己的判定锚(闲置时长),不再用被顶新的台账 last_write——今晨 08:12:51 账本显示 16.5 秒而真实判定闲置 8.8 小时的脱节不再出现。

## 验收标准
- [ ] gate.go bypass 分支:agent=="dsh" 时按该会话 SessionID 调消耗标记(lineage 解析与分支内 stB 同源);cc/codex 路径零改动
- [ ] block/warn 记账条目 idle_s = 闸门判定锚算出的闲置时长(与判定用同源数据)
- [ ] 单测:dsh bypass 后其交接已消耗;cc bypass 后交接不消耗且 gate_test 既有断言逐字不变
- [ ] 单测:block 记账 idle 与判定锚一致(构造 last_write 被顶新的场景)

## Blocked by
票01。

## 涉及路径
internal/daemon/gate.go
internal/daemon/gate_test.go

## 副作用声明
只跑 internal/daemon 包内 gate 相关单测(go test ./internal/daemon/ -run 'Gate|Bypass|Block' 类选择器)。

## decision_refs: D2、D4、F1、F5、A5①
## review_blocks: 无
