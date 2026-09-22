# 票06 · 反跑扩总体与扫参网格纳同模型阈值

## What to build
评测机器扩展,打通端到端:internal/backtest 的评分总体从等待窗口扩到**闲置/摆渡事件**(数据源:E0a 型间隔分布+账本 usage/handoff 流水);扫参参数网格纳入同模型触发阈值,对历史闲置事件评分,三线对比("若当时阈值=t / 实际发生 / 什么都不做")。产出建议报告(markdown,含曲线、样本量、预期差价、与现值 diff)落状态目录;样本门槛:滚动窗口内摆渡事件 < min_events(票01 配置,默认 30)时报告标注"样本不足"、不产出建议值。

## 验收标准
- [ ] 闲置/摆渡事件总体装载:从账本/间隔数据可装载并有单测
- [ ] 阈值网格评分:同一事件集上不同 t 的三线对比可复算
- [ ] 报告落状态目录,含四要素(曲线/样本量/预期差价/diff)
- [ ] 样本不足路径:不出建议值、报告标注,单测覆盖
- [ ] 既有 backtest 测试照绿(等待窗口评分不回归)

## Blocked by
票05

## 涉及路径
internal/backtest/
internal/report/

## 副作用声明
go test ./internal/backtest/... ./internal/report/...;go build ./...

## decision_refs
D9

## review_blocks
无
