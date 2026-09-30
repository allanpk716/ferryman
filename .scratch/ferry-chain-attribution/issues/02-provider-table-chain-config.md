# 票 02 · 供应商表协议扩展与链配置解析

## What to build

供应商表条目新增可选键：`protocol`（`openai` 默认 | `anthropic`）与 `extra_body`（逐键并入请求体的透传字典）。摆渡主配置从单 `provider` 升级为顺位链 `chain = [name, ...]`，保留 `provider` 单键向后兼容（等价单元素链）。解析与校验：链引用的名字必须存在于供应商表（缺名→配置错误按既有坏 TOML 同款语义上抛）；空链/未配置=既有降级骨架语义不变。本票只做配置层与数据结构，不改运行时调用行为。

## 验收标准

- [ ] `[ferry] provider = "x"` 旧配置解析行为与现状完全一致（回归钉死）
- [ ] `chain` 多元素解析出有序链；`provider` 与 `chain` 并存时以 chain 为准并留一行警告日志
- [ ] protocol/extra_body 两键解析正确，缺省 openai/空
- [ ] 链引用缺名/空链的行为有单测钉死（上抛或既有降级语义，与现状对齐）
- [ ] 全仓 go test 相关包全绿 + go vet 净

## Blocked by

无，可立即开始。

## 涉及路径

- internal/ferry/ferry.go
- internal/ferry/ferry_test.go
- internal/config/config.go（若 [ferry] 段解析在此；以实际代码为准，只改解析不动其他段）

## 副作用声明

只跑 `go test ./internal/ferry/... ./internal/config/...` 与 `go vet`；无网络、无构建产物。

## decision_refs

D1、D2、D3

## review_blocks

无
