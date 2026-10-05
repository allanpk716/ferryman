# DSH 摆渡接线实施规格（夜链固化版 · 20261005-102141）

> 来源：`.xcheck/20261005-103320/proposal.md`（rev1，codex+kimi 双轮盲评收敛）+ 同环 decisions.md（D1-D8）/ FINDINGS.md（F1-F8）。评审对象的完整事实锚点见该 spec，本规格是它的实施投影，冲突时以评审对象+D/F 为准。

## Problem Statement

dsh（DeepSeek Harness 桌面端）用户闲置会话缓存过期后，Ferryman 不为 dsh 生成交接（被排除在摆渡入队之外），拦截文案还教用户输入 dsh 根本没有的 /clear 命令。用户换新会话后拿不回任何进度，与 CC 的体验不对等。

## Solution（用户视角）

- dsh 会话与 CC 同机制：闲置约 25 分钟时守护自动生成交接文档；约 35 分钟进拦窗时，用户看到"新建会话（桌面端点新建 / web 端 new session）"引导而非 /clear；照引导开新会话，开场自动收到进度交接+被拦时的原话；想留在原会话就用"强续"开头发消息。
- CC 用户一切照旧：所有文案与行为逐字不变。

## User Stories

1. 作为 dsh 用户，当我离开约 25 分钟后回来继续干活，我希望交接已经自动备好，以便拦窗到来时我不必从头描述进度。
2. 作为 dsh 用户，当我的会话闲置超时被拦，我希望文案告诉我桌面端/web 端怎么开新会话，以便照做就能拿到交接，而不是被告知输入一个不存在的 /clear 命令。
3. 作为 dsh 用户，当我照文案开了新会话并发第一条消息，我希望开场就收到上一会话的进度交接+我被拦时的原话，以便无缝续作。
4. 作为 dsh 用户，当我的会话被拦但交接恰好没生成，我希望新会话至少带回我的原话并用中性措辞告诉我交接缺失，以便不丢失输入。
5. 作为 CC 用户，我希望本战役的一切改动对我零感知，以便行为与文案与之前逐字一致。
6. 作为运维者，我希望 dsh 会话从没走过渡口（或守护重启丢了快照）时摆渡降级为骨架交接而不是报错，以便链路永不因缺料卡死。

## Implementation Decisions

- **文案分支（P2）**：四处用户可见文案按 agent 分支——gate.go 分支5 block（:230-238）、分支6 block（:255-262）、warnCtx（:392-405）、restore.go 锚定归还·无交接分支（:50）。dsh 引导语="新建会话（桌面端点新建 / web 端 new session）"；restore.go:50 用中性表述（"你被拦时输入的那条消息没有丢"）。强续前缀机制（gate.go:106 统一层）不动；CC 文案逐字零变化。
- **守望接线（P1）**：watcher_dsh.go:248 接 `maybeEnqueue(st)`（子代理已早退天然不进）；maybeEnqueue 内 dsh 跳过 `w.enrich`（其 else 分支是 codex 读取器，对 dsh 会清零峰值；dsh 的 title/peak 由 harvestDshUsage/事件面回写），跳过后 EnrichedWrite 版本章不盖、peak 门读已维护值；contentClock 对 dsh 走 fail-open（cctrans 读 zstd 必失败 → contentTS=0 放行＝旧行为），不造 dsh 内容钟。
- **闸门解锁（P1）**：gate.go:206（observe 分支入队）/:268（分支7 入队）/:190（判热豁免）三处 `agent != "dsh"` 解锁；:190 解锁后因 dsh 无钟数据行为零差异（防将来有钟误拦）；相关头注同步改写；machineWaiting dsh 判活道（:340-359）已就位不动。
- **摆渡材料（P1，规则钉死）**：dsh 分支材料＝`SnapshotStore.Main(sid)` 请求体（最大请求体＝最新主轮＝完整对话前缀，心跳重放同源；**禁用 Last()**——仅诊断、可能是小请求）；sid＝统一键（会话目录名，item 的 session_id 直取或 path 推导，实施裁定）；材料转 (facts, items) 进既有 L1/L2；**覆盖截止＝入队时台账 lastWrite**（与闸门 coversBar 同口径；实现上在入队闭包捕获并随 item 携带，避免 worker 执行时读晚了偏离口径——方向虽宽松仍以钉死为准）；快照缺失（未走渡口/守护重启）→ 骨架降级不报错；DockSnap 引用送达提取点的方式（注入 vs 携带）实施裁定，CC/codex 路径零改动；GLM thinking:disabled 属现行配置面不改；跨源去重（dsh_dedup.go）不改。
- **归还链**：不动（机制在位：`Restore(agent,cwd)` 锚定+旧 sid 钉死，/dsh/handoff 钉 dsh 整体复用）。
- **A3**：已核实闭环，无代码改动（双路径 peak 回写测试在案：dsh_wiring_test.go:120-122 / dsh_receive_test.go:214-238）。

## Testing Decisions

- 外部行为测试，不测实现细节；沿用仓库既有表驱动/替身注入先例（gate_test.go 文案断言、dsh_wiring_test.go 守望替身）。
- 文案：两 agent × 四处表驱动——dsh 无"/clear"、有"新建会话"引导或中性表述；cc 与改动前逐字一致（含 gate_test.go 现有断言不破）。
- 接线：三条触发路各有测试（gate observe :206、gate 分支7 :268、watcher pollDshSession :248→maybeEnqueue）；dsh 跳过 enrich 有测试（峰值不被清零）；contentClock fail-open 语义钉测试。
- 材料：语义级替身测试——快照体含完整对话（多条 user/assistant）时提取材料含近期对话内容（非仅骨架/标题）；覆盖截止命中断言（生成交接对同时刻 coversBar 判覆盖）；缺料骨架降级不报错。
- 全仓：`go test ./...` 全绿 + `go vet ./...` 干净（不跑 -race，本机工具链坏）；测试输出落 `.scratch/` 日志文件，不刷终端。

## Out of Scope

- P1.5 对话区选择框 UI（插件侧，先人工 spike 验三件事）
- P3 enforce 升档（observe 7 天观察后）
- dsh 心跳保温（等 CC 盈亏表拍板）
- plugin/ferryman-dsh/ 任何改动；通知文案与 cacheInfoCtx（已核两端通用）
- 新配置键；CC/dsh 共用代码重构（除非测试证明 CC 输出逐字不变）
- 真机闲置 25/35min 端到端两条路（强续放行一枪/新会话首枪收交接+原话）——人工验收，不属夜链

## Further Notes

- 代码基线 b097fea；行号若漂移按符号名对齐。
- 快照库事实：无时间 TTL；容量 LRU（非 pinned 上限 16、pinned 免逐出）；全内存不落盘（隐私红线）——守护重启=快照丢=该窗摆渡走骨架（已知降级）。
- 非阻断参考（不新增票，实施时顺带即可）：快照缺料降级路径可留一行缺料原因日志（F4 观测建议）；跨 sid 归还链可补端到端测试（F2，后续票候选）。
