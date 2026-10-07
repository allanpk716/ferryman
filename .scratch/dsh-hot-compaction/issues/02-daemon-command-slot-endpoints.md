# 票02 · daemon 指令槽与 poll/compacted 端点（含 N1 有效期）

## What to build
daemon 新增 per-session 压缩指令槽（内存态）与两个 HTTP 端点：POST /dsh/poll（插件轮询取指令，应答即清槽，过期指令丢弃不派发）与 POST /dsh/compacted（结果上报，落账本 kind=compacted 事件，ok 时更新 gate 会话状态 compressed 标记+prefix 覆盖）。配置节 [dsh_compact]（enabled/trigger_ratio/min_peak_tokens/command_ttl_ratio/poll_hint_s/compressed_flag_ttl_ratio）。契约见 spec"架构与契约"节，逐字遵守。

## 验收标准
- [ ] 表驱动 Go 测试绿：过期指令在 poll 应答时被丢弃且不派发；poll 应答即清槽（同指令不二次派发）；compacted ok=true 落账本事件（字段齐全）并设置 compressed 标记（带 expires）；ok=false 只落账本不设标记；标记有效期与流量作废语义可测
- [ ] 端点鉴权与 /dsh/gate 同源同法（token）
- [ ] go vet 过；涉及包测试全绿

## Blocked by
无，可立即开始

## 涉及路径
- internal/daemon/（新增 compact.go 或并入既有文件 + 对应 _test.go；server 路由注册处）
- internal/config/（[dsh_compact] 节 + Validate + config_test）

## 副作用声明
默认只跑 go test 指定包（internal/daemon internal/config）与 go vet

decision_refs: D1 D6 D8（N1 语义）
review_blocks: F1 F2 N1
