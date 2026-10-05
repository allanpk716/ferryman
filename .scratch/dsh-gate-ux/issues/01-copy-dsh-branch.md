# 票 01 · P2 文案 dsh 化——四处用户可见文案按 agent 分支

## What to build

dsh 会话的用户可见文案不再出现 /clear，换成"新建会话"引导（或中性表述）；CC 文案逐字零变化。四处位点：

1. `internal/daemon/gate.go:230-238` 分支5 block——现"【推荐】/clear 换新会话…1. 输入 /clear 2. 随便发一个字…"；dsh 版："新建会话（桌面端点新建 / web 端 new session），开场自动收到：本会话的进度交接+你这条原话"。
2. `internal/daemon/gate.go:255-262` 分支6 block——现"【或 /clear 换新会话】…"；dsh 版按分支6语义改写（交接若已生成会一并带给新会话，此刻没好则新会话只带回原话）。
3. `internal/daemon/gate.go:392-405` warnCtx——现"建议 /clear 后开新会话（自动注入交接）。"；dsh 版："建议新建会话（桌面端点新建 / web 端 new session），开场自动注入交接。"
4. `internal/daemon/restore.go:50` 锚定归还·无交接分支——现"你 /clear 前被拦的那条消息没有丢…"；dsh 版中性："你被拦时输入的那条消息没有丢…"。

约束：强续前缀机制（gate.go:106 统一层）与各文案里强续段落原文不动；`fmt.Sprintf` 转义、`blockPreview`/`blockExample`/`h.Path` 参数原样；最小 diff（抽分支函数或 if/else 内联均可，不重构无关代码）。相关排除注释若提及文案口径同步更新。

## 验收标准

- [ ] dsh 的分支5/分支6/warnCtx/restore:50 输出不含 "/clear"，前三处含"新建会话"引导，第四处为中性表述
- [ ] cc 的四处输出与改动前**逐字一致**（表驱动测试：两 agent × 四位点断言）
- [ ] gate_test.go 现有文案断言（"随便发一个字"等）全绿不破
- [ ] 强续段落（分支5/6）两 agent 均保留原措辞
- [ ] `go vet ./internal/daemon/` 干净

## Blocked by

无，可立即开始。

## 涉及路径

- internal/daemon/gate.go
- internal/daemon/restore.go
- internal/daemon/gate_test.go
- internal/daemon/restore_test.go

## 副作用声明

- 独占验证命令：`go test ./internal/daemon/ -run 'TestBlock|TestWarn|TestGate|TestRestore' -count=1`，输出重定向到 `.scratch/dsh-gate-ux/logs/t01-test.log`
- 不跑 -race；不跑全仓测试（终局统一跑）

## decision_refs

D2（强续不动）、D3（文案分支+CC 逐字零变化）

## review_blocks

F6
