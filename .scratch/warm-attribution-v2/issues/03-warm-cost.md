# 03 · 配对合同与保温成本腿（支出腿）

## What to build

internal/report 新增保温成本模块（建议 warm_cost.go）：对每条智谱档保温动作行（handoff provider=智谱）配对其重放实付，供 SavingsV2 组装。端到端行为：给定账本行集合，产出每个保温动作的 {lineage, ts, 配对结果(paired/ambiguous/zero_cost/unpaired), 实付四列 或 回落推算额, 标注(实收/价书回落/unpaired)}。

规则（照规格配对合同，逐条实现）：
- 候选 dock 行：session_id 相等 ∧ dock.ts ∈ [handoff.ts−2, handoff.ts+wall_s+90]。
- 命中签名：|dock.(input_tokens+cache_read_tokens) − handoff.prompt_tokens| ≤ max(512, 0.02×prompt_tokens) ∧ dock.output_tokens ≤ 8192。
- 恰一候选命中 → paired，成本 = 四列 × [prices.glm] 实价（input 全价列、cache_read 1.7/万、output 24/万；按行时刻取价书版本）。
- 多候选 → ambiguous：**只计数与披露，不做成本处置**（歧义分支的成本规则是活动约束 F7，票 05 落；本票不得自行发明规则——成本记 0 并带 pending=true 标注，注释指向 F7）。
- 零候选 ∧ handoff.wall_s=0（预派发失败）→ zero_cost，成本 0。
- 零候选 ∧ wall_s>0 → unpaired，成本回落价书推算（prompt_tokens×p_in 或按 p_cache 视缓存态不可知时保守 p_in，取 p_in）并标注 unpaired。
- provider=local（本地 Qwen 档）→ 零成本。
- beat 行成本：beat 行自带 cost_actual/cache_read 字段，直接取用（当前等待窗零跳、qwatch observe 零成本，仍按此口径支持将来）。

## 验收标准

- [ ] 表驱动单测（warm_cost_test.go）：四分支各自用例 + **并发窗用例（同窗既有重放 dock 行又有真实流量 dock 行时，签名判据不误配；真实流量行 input+cache_read 不匹配 prompt_tokens 或 output 超 8192 时落 ambiguous 而非错配）**
- [ ] 纯函数、不读盘、不改既有文件
- [ ] go test ./internal/report/ 全绿（输出落日志）

## Blocked by

无，可立即开始

## 涉及路径

internal/report/warm_cost.go（新建）
internal/report/warm_cost_test.go（新建）

## 副作用声明

允许并要求运行：`go test ./internal/report/ > <repo>/.scratch/warm-attribution-v2/logs/t03-test.log 2>&1`（先建 logs 目录；禁 -race）

decision_refs: D4, D5
review_blocks: 无（歧义分支成本处置刻意留给票 05）
