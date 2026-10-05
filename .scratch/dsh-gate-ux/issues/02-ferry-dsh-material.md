# 票 02 · Ferry dsh 材料分支——渡口快照→交接（规则钉死）

## What to build

摆渡提取的 dsh 分支：材料＝`SnapshotStore.Main(sid)` 请求体（不是事件流、不把 zstd 会话文件当 CC jsonl 读），转 (facts, items) 进既有 L1/L2 流程。规则（不得偏离）：

1. **取哪份**：`dock.SnapshotStore.Main(sid).Body`——最大请求体＝最新主轮＝完整对话前缀（anthropic-messages 请求形：system+messages）；**禁止用 `Last()`**（仅诊断，可能是标题类小请求）。快照缺失（Main 未命中）→ 按现行骨架降级（skeleton handoff）返回，不报错、不 panic；可顺手留一行缺料原因日志（非阻断建议 F4）。
2. **键怎么来**：sid＝统一键（`session-<uuid>` 会话目录名）；从摆渡 item 的 `session_id` 直取，或从 `transcript_path` 推目录名——二选一，实施裁定。
3. **材料构造**：把请求体（system/messages）转成与 CC 提取器等价的 (facts, items)：facts.Title 可为空回落（台账现行标题在上游）；messages 逐条成 items（user/assistant 分角色）；**覆盖截止（facts.LastTS 等价物）＝入队时台账 lastWrite**——由 enqueue 闭包（`internal/daemon/serve.go:149-161`）在入队时捕获并随 item 携带（新增 item 字段，如 `covers_at`；worker 侧取用），**不得**在 worker 执行时才读台账（防取值时点漂移，F8）。CC/codex 的 item 构造与行为零变化（字段为新增，dsh 才消费）。
4. **送达方式**：DockSnap 引用如何到达提取点（Daemon 字段注入 worker/Ferry、闭包携带 resolver 等）实施裁定；CC/codex 路径零改动（`sessionMaterial` 的 cc/codex 分支行为与签名兼容不变）。
5. GLM thinking:disabled 属现行配置面，本票不碰；跨源去重（dsh_dedup.go）不改。

## 验收标准

- [ ] 快照替身测试：构造含多条 user/assistant 消息的请求体 → dsh 分支提取的材料**含近期对话内容**（语义级断言，非仅骨架/标题）
- [ ] Main 未命中 → 骨架降级返回（无 error、无 panic）
- [ ] 覆盖截止命中断言：用入队时刻 covers 值生成的交接，对同时刻 coversBar 判"覆盖"（ValidHandoff 命中形态）
- [ ] cc/codex 提取路径回归：现有 FerrySession/sessionMaterial 相关测试全绿
- [ ] `go vet ./internal/ferry/ ./internal/daemon/` 干净

## Blocked by

无，可立即开始。

## 涉及路径

- internal/ferry/ferry.go（或新文件 internal/ferry/dsh_material.go，同包）
- internal/ferry/dsh_material_test.go（新）
- internal/daemon/serve.go（enqueue item 增 covers 字段）
- internal/daemon/worker.go（仅当送达方式需要；dsh 材料注入点）
- internal/daemon/worker_test.go（仅当 worker.go 改动）

## 副作用声明

- 独占验证命令：`go test ./internal/ferry/ ./internal/daemon/ -run 'Ferry|Dsh|Material|Worker' -count=1`，输出重定向到 `.scratch/dsh-gate-ux/logs/t02-test.log`
- 不跑 -race；不跑全仓测试（终局统一跑）

## decision_refs

D4（材料源=渡口快照+规则钉死）、D7（fail-open）

## review_blocks

F1
