# 04 · SavingsV2 组装与 cost_report 保温盈亏节

## What to build

把 02（回合/四象限）与 03（成本腿）组装成成效账 v2 并挂进 cost_report：
- internal/report 新增 SavingsV2 组装（建议 savings_v2.go）：在 SavingsV1 输出之上追加保温节——{按月/按保温动作类型的: 兑现节省额、白保温无害计数、亏损计数与成本、命中率、支出明细(paired/ambiguous/zero_cost/unpaired 计数与金额)、净额}；formula 字段值 "v2"；**v1 段输出逐字段不变（金测锁定）**。
- internal/daemon/query_report.go 挂接：cost_report 响应在既有 savings/tokens/useless_warm 之外新增 warm 节（结构化透传，不洗字段）；useless_warm 语义不动。
- 语义红线：纯查询，不写任何账本行；不改 daemon 其他文件；v1 消费者零感知。

## 验收标准

- [ ] 单测（savings_v2_test.go + query_report_test.go 扩展）：合成账本 → v2 节字段齐全、按动作类型拆分正确、v1 段与改造前逐字段一致（金测）、useless_warm 不混入
- [ ] go test ./internal/report/ ./internal/daemon/ 全绿（输出落日志）
- [ ] 不触碰 internal/mcp/（若现有 MCP 工具面需要字段白名单才能透传新节，停下来回报 NEEDS_CONTEXT，不要自行改协议层）

## Blocked by

02（warm_episode.go 接口）、03（warm_cost.go 接口）

## 涉及路径

internal/report/savings_v2.go（新建）
internal/report/savings_v2_test.go（新建）
internal/daemon/query_report.go（修改）
internal/daemon/query_report_test.go（修改）

## 副作用声明

允许并要求运行：`go test ./internal/report/ ./internal/daemon/ > <repo>/.scratch/warm-attribution-v2/logs/t04-test.log 2>&1`（禁 -race）

decision_refs: D4, D5, D6, D7
review_blocks: 无
