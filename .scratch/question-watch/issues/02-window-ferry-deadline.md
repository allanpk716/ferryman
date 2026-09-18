# 票 02 · 等答复窗口：台账态＋摆渡推迟＋死线

## What to build

 命中之后台账与摆渡侧的端到端行为：提问潮会话在台账挂"等答复窗口"（含计划字段），窗口期间摆渡入队被推迟、最迟 `block_s − ferry_deadline_lead_s` 强制入队，模型失败降级既有骨架交接；窗口与异步等待窗口（停车窗）互斥（先开者赢）；任何新写入关窗；关窗后末条又是提问潮重开新窗。命中谓词四条件接线（提问潮＋悬空⊆{AskUserQuestion}＋子代理在飞=0＋前缀≥min_ctx_tokens）。配置校验：lead 夹取与 `summarize_s + lead ≤ block_s` 违例拒绝。

## 验收标准

- [ ] SessionState 增窗口字段；开/关/重开状态迁移正确
- [ ] 命中谓词四条件真值表单测（任一假不开窗；悬空含非 AskUserQuestion 工具不开窗）
- [ ] 窗口期间摆渡推迟：`_maybe_enqueue` 路径对窗口会话跳过常规入队
- [ ] 死线：窗口会话闲置达 `block_s − lead` 强制入队；摆渡失败/超时落骨架交接（复用既有降级路径）
- [ ] 与停车窗互斥：停车窗已开则不开等答复窗口，反之亦然
- [ ] 新写入关窗＋恢复常规摆渡调度；末条又提问潮重开（重新计时）
- [ ] 配置校验：`ferry_deadline_lead_s` 夹取 `[60, block_s−summarize_s]`，summarize_s+lead>block_s 拒绝并告警
- [ ] 闸门语义零改动（gate 相关测试不动仍绿）
- [ ] 全量 pytest 绿

## Blocked by

票 01（检测器 Verdict 是谓词输入）。
