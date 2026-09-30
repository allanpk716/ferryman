# 票 01 · 渡口归因头回落与防御

## What to build

渡口会话提取支持 HTTP 头回落：请求体 `metadata.session_id` 第一优先，缺失时读 `X-Claude-Code-Session-Id` 头。头值须过 UUID 格式校验（不合格式=缺失）；头体并存以体为准、冲突留一行日志；都缺维持现有空语义（绝不伪造 ID）。两个消费点同缝生效：快照捕获与渡口记账归因。另：Ferryman 自发的重放/追加重放请求由发送侧带上 `X-Claude-Code-Session-Id` 头（值=目标会话 ID），使其渡口记账行可按会话归集。判热时钟零改动。

## 验收标准

- [ ] 归因矩阵单测全绿：体有/体无头有(合法UUID)/都无/头格式坏/头体冲突(以体为准+留痕)
- [ ] 无头无体请求维持空会话语义（不伪造、不误归因）
- [ ] 快照捕获与记账归因两个调用点走同一提取逻辑（一处实现）
- [ ] 心跳重放与追加重放发送侧带头（值=会话 ID），单测断言请求头
- [ ] 全仓 go test 相关包全绿 + go vet 净

## Blocked by

无，可立即开始。

## 涉及路径

- internal/dock/snapshot.go
- internal/dock/snapshot_test.go
- internal/dock/server.go
- internal/dock/server_test.go（如已有；新增用例落 snapshot_test.go 亦可）
- internal/beat/httpsender.go
- internal/beat/appendreplay.go
- internal/beat/httpsender_test.go
- internal/beat/appendreplay_test.go

## 副作用声明

只跑 `go test ./internal/dock/... ./internal/beat/...` 与 `go vet`；无端口、无网络、无构建产物目录。

## decision_refs

D4

## review_blocks

无
