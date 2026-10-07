# 票01 · 守护进程：新会话无锚零注入（R1 核心）

## What to build

DSH 新建会话向守护进程问询交接时（POST /dsh/handoff），若目标会话转录无机器产出事件（=新会话）且该目录线上**没有**被拦待领原话，守护进程回 `{"context": null, "continuation": true}`——不再自动注入最新交接全文、不再注入候选清单。有线内被拦待领原话时照旧走锚定归还（锚会话交接+原话，行为逐字不变）。续用档（转录有产出）与 CC/Codex 的 Restore 显式命令路径零变化。

实现要点：改动收在 `DshHandoff`（internal/daemon/dsh_receive.go）——先判转录产出（既有），无产出时先查 `Store.LatestPendingFor("dsh", cwd)`：有 → 走既有 `d.Restore`（其内部自然落锚定分支）；无 → 回 `{"context": null, "continuation": true}`。`restoreNewest` 本体不动（CC/Codex /restore 仍用）。**禁止**回无 continuation 键的 `{context:null}`——插件会当"材料未到"记欠账、每条用户消息重问。头注释写明 continuation 键扩注语义（"续用档或新会话无料=零注入终态，清欠账止问"，旧插件 v0.9.7 兼容）。

## 验收标准

- [ ] 新会话+无锚：`DshHandoff` 回 `context:null` 且 `continuation:true`；账本**无** kind=inject 行（表驱动断言）
- [ ] 新会话+有锚（线内 2h 待领原话）：锚定归还照旧——锚会话交接+原话+inject 记账，与现状逐字一致
- [ ] 续用档（转录有机器产出）：走 restoreContinue，行为与 v0.9.7 逐字一致
- [ ] 多候选形态下新会话：零注入（候选清单也不再注入）
- [ ] CC/Codex `Restore` 公共路径零改动（现有用例零回归）
- [ ] `go test ./internal/... `全绿；`go vet ./...` 干净

## Blocked by

无，可立即开始。

## 涉及路径

- internal/daemon/dsh_receive.go
- internal/daemon/dsh_receive_test.go

## 副作用声明

只跑 `go test ./internal/daemon/... -count=1` 与 `go vet ./internal/daemon/...`；不跑全仓套件（终局统一跑）。

decision_refs: D1、D4、D7
review_blocks: F4（清单零注入随本票落地）
