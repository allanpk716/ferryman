# 票 04 · /stats glm_balance 行下线【paused】

## What to build
从守护 /stats 健康面移除 `glm_balance` 键及其取数函数(编码套餐 key 打 user/balance 端点永远"余额查询失败",是坏死重复面;悬浮窗余额已由 quota 端点承担)。连坐退役:仅被它消费的余额查询模块与其测试;配置侧余额端点字段保留为惰性(动它连坐配置迁移往返测试,另役)。**可单次提交回滚。**

## 验收标准
- [ ] /stats 响应不再含 glm_balance 键;健康面包测试绿
- [ ] 余额查询模块与其测试文件删除,grep 无残留引用(配置侧 BalanceURL 字段除外)
- [ ] `go test ./internal/daemon/ ./internal/dock/` 绿

## Blocked by
**paused(F3/D1)**:待用户拍板"删 vs 保留修复";同时请用户确认无仓外脚本消费该键(F7)。拍板"删"后本票可立即执行,内容已定。

## 涉及路径
- `internal/daemon/health.go`
- `internal/daemon/health_balance_test.go`(删)
- `internal/daemon/balance_active_wire_test.go`(删)
- `internal/daemon/dock_active_test.go`(删其中 TestHealthGLMBalanceFollowsActiveUpstream)
- `internal/dock/balance.go`(删)、`internal/dock/balance_test.go`(删)

## 副作用声明
`go test ./internal/daemon/ ./internal/dock/`

decision_refs: D1(待拍板)
review_blocks: F3(解除:D1 用户拍板), F7(随 D1 一并确认仓外消费面)
