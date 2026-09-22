# 票07 · 调参三态应用引擎+护栏+调参流水+CLI

## What to build
建议从产出到落地的闭环,打通端到端:internal/tuning 新包——应用引擎按三态(manual 只出报告 / recommend 出报告+提醒,人工应用 / auto 护栏内自动应用);auto 五护栏:①只改公式输入不旁路计算器 ②建议值钳 [10,总结阈值] ③每上游每周至多一次生效 ④样本不足(读票06 的样本门槛)收敛为只提醒 ⑤生效后通报+一键回滚。永不自动升档(三态切换只认 config 手改)。调参流水:接受/拒绝/自动应用逐条 append-only 落状态目录(只记变更与依据,不进账本)。CLI 子命令:`ferryman tuning status / apply <id> / reject <id>`(唯一写路径);托盘气泡接线:有新建议待审、auto 已应用两类通知(经 internal/notify)。

## 验收标准
- [ ] 三态行为单测:manual 零应用、recommend 只提醒、auto 护栏内应用
- [ ] 五护栏各自单测(频控按上游按周、钳位、样本门槛、回滚)
- [ ] 调参流水 append-only,接受/拒绝/自动应用三事件落盘可回放
- [ ] CLI 三子命令可用,status 输出建议摘要与状态
- [ ] 托盘两类气泡通知路径接通(internal/notify 单测)
- [ ] 永不自动升档:代码路径不存在自动改 mode 的分支,单测断言

## Blocked by
票06

## 涉及路径
internal/tuning/
cmd/ferryman/main.go
internal/notify/

## 副作用声明
go test ./internal/tuning/... ./internal/notify/...;go build ./...

## decision_refs
D10 D11 D8

## review_blocks
无
