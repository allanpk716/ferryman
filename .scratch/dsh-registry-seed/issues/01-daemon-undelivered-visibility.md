# 票01 · 守护侧可见性：指令无人领取日志与一次性告警

## What to build

守护侧压缩指令"入槽后过期仍无人领取"不再静默：每次过期丢弃时打一行日志；同一会话连续 3 轮无人领取时经 gate.log 告警一次（每会话一次，防刷屏）；该会话出现成功压缩上报或新指令入槽后重置计数。用户价值：送达断链（如插件注册表空转）五分钟可定位，不再是四十分钟盲查。

## 验收标准（全自动化可判）

- [ ] 过期指令丢弃路径打一行 `[compact] dsh 指令过期无人领取（第 N 轮）：<sid16>`（sid 16 位截断，runeCap16 同款）；日志经包级可替换缝（appendLineBestEffort/类似 var seam 先例）可在测试中捕获断言
- [ ] 连续 3 轮无人领取 → gate.log 恰好一行 `mode=compact-undelivered` 告警（每会话一次：第 4、5 轮不再告警）
- [ ] 成功压缩上报（/dsh/compacted ok=true）或同会话新指令入槽后，计数重置——之后再连续 3 轮会再次告警（有界重复）
- [ ] 表驱动测试覆盖上述全部路径；既有 compact_test.go 用例零回归
- [ ] 告警与日志只在指令确实过期且未被领取时触发（poll 应答清槽的正常路径零日志）

## Blocked by

无，可立即开始。

## 涉及路径

internal/daemon/compact.go
internal/daemon/compact_test.go

## 副作用声明

默认只跑 `go test ./internal/daemon -run 'TestDshCompact|TestCompactUndelivered' -count=1`；不占端口、不碰生产。

decision_refs: D1、D6
review_blocks: 无
