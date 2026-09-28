# 票 02 · 渡口包核心：优雅排水 + 自产错误形状 + 首包闸门 + 截断记账

## What to build
在 internal/dock 包实现错误契约的四件核心行为（对客户端可见）：
1. **优雅排水**：`Server` 新增 `Shutdown(ctx)`——停收新连接、等在途请求自然结束；排水上限从配置来（`[dock]` 节新键，解析层默认 180 秒）；到期对流已建立的在途请求逐个合成 Anthropic 流内错误事件（SSE `event: error`，负载 `{"type":"error","error":{"type":"api_error","message":...}}`）后 `Close()` 硬收；首字节未写出的请求回 504＋Anthropic 错误体。
2. **拨号失败错误体**：ReverseProxy 补 `ErrorHandler`——出站错误（拨号失败等，此时客户端首字节未写出）回 502＋Anthropic 错误体（含一句人话原因）＋`Retry-After: 5`；替换 Go 默认空体 502。
3. **首包闸门**：仅流式请求（/v1/messages POST 且体 `stream:true`）——上游 200 响应头到达后 withhold 客户端响应，等首个流字节到达才放行（首字节续传，不额外缓冲）；静默超 60 秒判失败→回 504＋错误体＋Retry-After。非流式不闸。请求体已在 ServeHTTP 读过（capture/record 路径），stream 判定从中来；纯透传不记账路径不闸（daemon 生产接线恒记账）。
4. **截断记账**：既有 SSE usage 扫描器顺带记 `message_stop` 是否出现；流 EOF 时见过至少一个 SSE 事件但无 `message_stop` → dock 科目流水行加 `"truncated": true` 字段＋日志一行。非 SSE 响应不标记；不改转发字节。

配置：internal/config 的 `[dock]` 节解析新增排水上限键（缺省 180 秒，秒数），沿用 parseDockSection 单源纪律。

红线（不许触碰）：改写五件语义、快照捕获、心跳重放路径、`FlushInterval: -1`、`ResponseHeaderTimeout` 50 分钟、请求侧任何行为；透传模式（无记账无改写）的既有字节行为不变。

## 验收标准
- [ ] Shutdown：在途流进行中调用（httptest 桩＋短排水上限注入）→ 在途请求跑完、客户端收完整响应；到期未完 → 客户端收到 `event: error` 流内事件后连接关闭
- [ ] Shutdown 到期·首字节未写出的请求 → 收 502/504＋Anthropic 错误体
- [ ] ErrorHandler：上游不可达 → 客户端收 502＋`{"type":"error","error":{"type":"api_error",...}}`＋`Retry-After` 头
- [ ] 首包闸门：桩回 200 头后不吐体（短超时注入）→ 60 秒语义路径回 504＋错误体；正常流首字节无额外延迟；非流式慢响应不被闸
- [ ] 截断记账：桩发若干 SSE 事件后 EOF（无 message_stop）→ 流水行带 `truncated: true`；完整流（有 message_stop）不带；非 SSE 响应不带
- [ ] 上游非 200 透传不回归：桩回 429（带 retry-after 头＋JSON 体）→ 客户端实收状态码/体/头三者一致（既有行为锁定测试）
- [ ] 排水上限配置键解析：缺省 180；显式值生效；非法值报解析错误（对齐既有键纪律）
- [ ] `go test ./internal/dock/... ./internal/config/...` 全绿（含既有测试零回归）

## Blocked by
无，可立即开始

## 涉及路径
- internal/dock/（dock.go、server.go、可新建 shutdown.go/errorshape.go 等＋测试）
- internal/config/config.go（[dock] 节新键＋测试）
- internal/accounts/accounts.go（**协调者实施中裁定并入**：dock 科目白名单加 truncated＋kindOptional 可选放行——账本拒未知字段，不并入则验收项「流水行带 truncated」不可达；改动 2 处带注释）

## 副作用声明
测试跑 `go test ./internal/dock/... ./internal/config/...`（票内独占）；构建产物落 GOCACHE，不入仓。

## decision_refs: D2, D3, D9, D10, D11, D12, D13
## review_blocks: 无
