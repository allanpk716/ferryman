# 02 · 心跳头分流（形状分类器导出＋两发送点）

## What to build

让心跳发给 dsh 会话时携带 dsh 归因头（`x-deepseek-harness-session-id`）而非 CC 头——渡口归因提取对 CC 头做 UUID36 校验，dsh 键（`session-` 前缀）塞 CC 头会被拒收（记账行归因空）。形状判定从 dock 单源导出，两个发送点（快照重放 Send、追加重放 SendAppendReplay）按形状分流。完成即可独立验收：发送器对 dsh 形键发对头、CC 键行为与今日逐字节一致。

规格依据：`.scratch/dsh-heartbeat/spec.md`「心跳头分流」节。

## 验收标准

- [ ] dock 导出会话键形状分类器（单源；或导出两谓词），beat 包消费、不复制第二份形状规则
- [ ] Send：dsh 形键 → 发 `dock.HeaderDeepSeekHarnessSessionID` 且 CC 头不发；UUID36 键 → CC 头（现状不变）；其他非两形键 → CC 头（保守维持今日行为）
- [ ] SendAppendReplay：同款分流（含既有 x-ferryman-replay 标记头不动）
- [ ] 测试双向钉死：httptest 断言两种键各自发出的头集合（dsh 键无 CC 头、CC 键无 dsh 头、其余头集不变）
- [ ] scoped 测试绿；既有测试零回归（CC 键路径逐字节不变）

## Blocked by

无，可立即开始

## 涉及路径

- internal/dock/snapshot.go
- internal/dock/snapshot_test.go
- internal/beat/httpsender.go
- internal/beat/httpsender_test.go
- internal/beat/appendreplay.go
- internal/beat/appendreplay_test.go

## 副作用声明

- 独占验证命令：`go test ./internal/dock/... ./internal/beat/...`（输出落 `.xcheck/20261003-183301/t02-test.log`，跑一次不重跑）

## decision_refs

D5（CC 零回归——形状互斥是该约束的实现面）

## review_blocks

无
