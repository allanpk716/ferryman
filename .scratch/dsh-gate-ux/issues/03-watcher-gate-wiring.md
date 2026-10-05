# 票 03 · 守望/闸门接线——maybeEnqueue + enrich 跳过 + 三处解锁

## What to build

把 dsh 会话接进时间驱动的主动摆渡（CC 已有机制，dsh 缺的只是接线）：

1. **watcher_dsh.go:248 接线**：现 `// 不 maybeEnqueue（见文件头 P2-1 边界注）。` → `w.maybeEnqueue(st)`（st 为 :233 TouchFull 返回的主会话态；子代理 :222-232 已早退天然不进）。文件头 P2-1 边界注（:30-38）同步改写。
2. **maybeEnqueue 内 dsh 跳过 enrich**（watcher.go:559 `w.enrich(st)`）：enrichImpl 的 else 分支是 codextrans 读取器，对 dsh 会清零峰值。加 `agent=="dsh"` 跳过（跳过点在 maybeEnqueue 或 enrichImpl 内，实施裁定）；跳过后 `EnrichedWrite` 版本章**不盖**、peak 门（:564）读 harvestDshUsage/事件面已维护的 `st.PeakCtx`。
3. **gate.go 三处 `agent != "dsh"` 解锁**：:206（observe 分支入队）、:268（分支7 入队）、:190（判热豁免——解锁后 dsh 无钟数据恒判冷、行为零差异，注释写明"为将来有钟不误拦"）。三处相关 dsh 排除注释（"终局修复2"等）同步改写为接法乙后已解锁口径；:188-189 头注同步。
4. **contentClock fail-open**（watcher.go:534）：cctrans.LastTimestamp 对 dsh zstd 必失败 → contentTS=0 → 内容守卫放行＝旧行为；写测试钉死该语义（不造 dsh 内容钟）。
5. **不动**：machineWaiting dsh 判活道（gate.go:340-359）；悬空道已限定 cc；停车窗/子代理/qwatch 死线/去重/入队各道 agent 无关直接复用。

## 验收标准

- [ ] 三条触发路各有测试：gate observe 分支（:206）、gate 分支7（:268）、watcher pollDshSession→maybeEnqueue（:248）——dsh 满足入队条件（观察窗内/闲置≥SummarizeS/peak≥MinCtxTokens/交接未覆盖）时 Enqueue 被调用
- [ ] dsh 跳过 enrich 有测试：构造 peak>0 的 dsh 会话过 maybeEnqueue，峰值不被清零、不触发 codextrans 读盘
- [ ] contentClock 对 dsh 恒 fail-open（contentTS=0 放行）有测试
- [ ] 端到端（替身）：dsh 会话满足条件 → 入队 → 票02 的 dsh 材料分支（或替身）产出交接 → 落库 ValidHandoff 命中（依赖票02 在位）
- [ ] CC 回归：现有 watcher/gate 测试全绿（cc 的 maybeEnqueue/enrich/悬空道行为零变化）
- [ ] `go vet ./internal/daemon/` 干净

## Blocked by

票 02（端到端验收需 dsh 材料分支在位；前四条不依赖，可与票02完成前的窗口做其余部分——但涉及路径与票01的 gate.go 相交，见下）。

## 涉及路径

- internal/daemon/watcher_dsh.go
- internal/daemon/watcher.go
- internal/daemon/gate.go
- internal/daemon/dsh_wiring_test.go（接线/端到端测试落点，或同目录新测试文件 internal/daemon/dsh_enqueue_test.go）

## 副作用声明

- 独占验证命令：`go test ./internal/daemon/ -run 'Dsh|Enqueue|Gate|Watcher|Poll' -count=1`，输出重定向到 `.scratch/dsh-gate-ux/logs/t03-test.log`
- gate.go 与票01 相交：两票不得同时在跑（调度互斥），先后皆可
- 不跑 -race；不跑全仓测试（终局统一跑）

## decision_refs

D1（目标交互）、D4（接线技术形态）、D7（contentClock fail-open）

## review_blocks

F1（端到端覆盖命中项）
