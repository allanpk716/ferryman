# 01 · /stats 暴露渡口在途与最后请求时刻

## What to build
守护的 /stats（GET，Bearer）新增两字段：`dock_inflight`（渡口 15722 代理面当前在途请求数，int）与 `last_request_ts`（最后一个代理面请求完成的 Unix 秒，int64；守护启动以来无代理面请求则恒 0）。只统计代理面请求；管理口 15700 流量（mcp/心跳/widget 轮询/闸门钩子）一律不计入。零值如实暴露，"零请求视为静默成立"由消费方（票 02）判定。

## 验收标准
- [ ] /stats 响应含 dock_inflight 与 last_request_ts
- [ ] 有在途代理面请求时 dock_inflight 与实际并发数一致，完成后回落
- [ ] 代理面请求完成后 last_request_ts 更新为完成时刻（秒级）
- [ ] 管理面请求（/stats 自身、闸门问询）不改变两字段
- [ ] 刚启动无代理面请求：dock_inflight=0 且 last_request_ts=0
- [ ] 渡口未启用（无 [dock] 配置）时两字段 0/0 且不报错

## Blocked by
无，可立即开始

## 涉及路径
internal/dock/server.go
internal/dock/server_test.go
internal/daemon/daemon.go
internal/daemon/daemon_test.go

## 副作用声明
go test ./internal/dock/ ./internal/daemon/ -run 相关子集允许

## decision_refs
D1、D10

## review_blocks
无
