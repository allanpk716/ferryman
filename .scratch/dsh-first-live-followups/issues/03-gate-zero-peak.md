# 票03 · 强续横幅与 bypass 记账 0 tokens：闸门同步兜底 + 降级文案 + 记账时序

## What to build

dsh 会话强续（auto-continue）时横幅报真实的全价冷重付额度、bypass 记账行记真实 prefix_tokens；闸门时刻 PeakCtx=0 且拿不到任何补值来源时，横幅降级为"重付额度未知"，绝不显示"约 0 tokens"。

背景机制（夜链预查证已钉死一半）：
- 现行取值链：`gate.go dshAutoContinue(sessionID, transcriptPath, st, snap.peak, idle)` → 横幅 `autoContinueCtx(idle, peak)` 与 bypass 记账 `prefix_tokens: peak` 同源
- PeakCtx=0 根因一半已钉：`dsh_boot_replay.go` 按 session_id/lineage_id 从账本**月文件**（accounts/202610.jsonl）回放峰值，但 10-03 接法乙之前的历史流量无归因键（5167d69a 的行全部从 15:41 起），回放无料
- 生产实证：15:41:18 横幅"约 0 tokens"实际 17,693；bypass 行 prefix_tokens=0 四行（e042332b×2/bb5d5e37/13069db2/5167d69a）vs 长跑 daemon 的 62,090 一行

## 验收标准

- [ ] 构造"daemon 重启后、静置、账本无行但转录非空"的 dsh 会话触发强续：闸门路径同步补值（优先账本回放；无行按转录粗估 token 量级），横幅报非零、bypass 行 prefix_tokens=同源非零
- [ ] **时序断言**：兜底补值先于 bypass 记账取值（同函数内先补后记；用例显式断言补值后才记账）
- [ ] 无任何 usage 史且转录无可解析事件的会话：横幅显示"重付额度未知"降级文案，不出现"约 0"字样
- [ ] PeakCtx 已非零的常规强续：横幅/记账行为与现行一致（取现值，不重算）
- [ ] cc/codex 闸门路径零变化（autoContinueCtx 仅 dsh 调用面）

## Blocked by

无，可立即开始

## 涉及路径

- internal/daemon/gate.go
- internal/daemon/dsh_boot_replay.go（兜底复用回放/粗估，不新建回放通道）
- internal/daemon/gate_test.go
- internal/daemon/adr0013_content_clock_test.go（如判据触及内容钟；预期不动）

## 副作用声明

- `go test ./internal/daemon/ -run 'Gate|DshAutoContinue|BootReplay'`（scoped）
- 不安装依赖、不联网、不动 .xcheck/

## decision_refs

D1（范围）

## review_blocks

无（F5 时序前提已并入本票验收）
