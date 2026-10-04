# 05 · 歧义分支成本处置（paused——活动约束 F7）

## What to build

在 warm_cost.go 的 ambiguous 分支补成本处置：与 unpaired 同处理（价书推算并标注 ambiguous，不冒充实收），并补对应单测条款（歧义分支成本 = 价书推算额、标注为 ambiguous 非 paired）。

## 状态

**paused(F7，解除条件：用户确认"ambiguous 成本与 unpaired 同处理（价书推算并标注）"或下次修订落盘)**——rev1 合同对该分支只有计数披露、无成本处置，评审判阻断；无人值守自动修订预算已用，本票不自动实施。

## 验收标准

- [ ] ambiguous 分支成本 = prompt_tokens × p_in /per，结果带标注 ambiguous
- [ ] 单测覆盖歧义分支成本用例
- [ ] go test ./internal/report/ 全绿

## Blocked by

03（warm_cost.go 已存在）

## 涉及路径

internal/report/warm_cost.go
internal/report/warm_cost_test.go

## 副作用声明

同 03

decision_refs: D5
review_blocks: F7
