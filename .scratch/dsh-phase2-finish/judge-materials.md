# 票06 判定记录 · 事件上报口可否提供「会话键＋快照所需材料」

> dsh phase2 票06（摆渡闭环接线，条件式规则）验收首项 · F6（判定时点＝本票验收首项）。
> 判定时间：2026-10-03（夜链）· 判定人：票06 实施泳道。
> 规则出处：`.scratch/dsh-phase2-finish/spec.md:36`（「摆渡闭环接线」节）＋ F1 解除条件
> （`.xcheck/20261003-124409/FINDINGS.md:14`——"P2-4 事件上报口可提供会话键＋快照所需材料 ⇒
> 役内接线…；不可提供 ⇒ 验收如实降级，声明「精华被渡」依赖晨间接法乙/直报决策"）。
> decision_refs：F1（摆渡闭环规则）、F6（判定时点）、P2-1 遗留注记③。

## 结论（一句话）

**材料不可得 ⇒ 本票走降级分支**：判据①（会话键）通过，判据②（材料足以按捕获-重放
前缀语义重建快照输入）不通过——事件口携带会话键但**零消息内容、零请求体**，而快照
输入的形状＝完整请求体字节＋白名单头集；不接线 `maybeEnqueue` dsh 面、不补 enrich
dsh 分支，代码面零改动（除本记录与票内验证日志）。二选一显式定案（F1 规则），无静默缺位。

## 判据逐条核对

判据（spec 定案原文）：*"session/event 携带会话键，且材料足以按捕获-重放前缀语义
（ADR-0007 同源）重建快照输入"*。拆两条独立核对。

### 判据① session/event 携带会话键（头行 id）——通过

| # | 事实 | 出处 |
|---|---|---|
| ①a | 插件取键＝会话头行 id，零换算：`sid = session.header.id`，body 即 `session_id: sid` | `plugin/ferryman-dsh/src/events.ts:246-254`（官方桥同款取法 `hooks-claude-code/src/index.ts:329`，P2-2 定案） |
| ①b | daemon 侧键必填：空键回 `skipped: empty-session-id`，绝不伪造 | `internal/daemon/dsh_receive.go:103-105` |
| ①c | 键形＝`session-<uuid>`（头行 id），插件测试钉死 `session_id === header.id` | `plugin/ferryman-dsh/test/events.test.ts:67,84`；Go 侧夹具 `internal/daemon/dsh_receive_test.go:33`、`dsh_wiring_test.go:27-28,56-58` |
| ①d | 事件口活动已喂台账供钟（可得分支的"触发信号"前置件本已就绪）：已知事件 `TouchFull("dsh", sid, …)` | `internal/daemon/dsh_receive.go:142-154` |

### 判据② 材料足以按捕获-重放前缀语义重建快照输入——不通过

**先钉目标形状**（"重建同形状快照输入"的"形状"是什么）：

- 快照单份＝`Snapshot{Body []byte, Headers map[string]string}`——**完整请求体字节**＋
  六键白名单头（content-type/anthropic-version/anthropic-beta/user-agent/accept/
  accept-encoding，auth 类一律不入）：`internal/dock/snapshot.go:43-46,31-38`。
- 捕获入口＝`Capture(sessionID, body, h)`，只收 POST `/v1/messages` 的真体
  （`ShouldCapture`，count_tokens 显式排除）：`snapshot.go:77-101,224-232`。
- 捕获-重放前缀语义（ADR-0007 同源）：心跳对捕获的**真实请求体原样重放、仅改
  max_tokens**；渡口改写/心跳重放的上下文＝快照体本身（同模型摆渡追加重放同源：
  `internal/ferry/append_replay.go:15-19`——"模型上下文＝捕获快照里的原会话"）。
  ADR-0007（`docs/adr/0007-heartbeat-prefix-source-capture-replay.md`）同时明文
  **否决了 jsonl 重构路线**：从会话转录拼装 wire 请求需约 6 簇与版本耦合的拼装规则，
  "CC 一升级即静默断链"——即捕获-重放语义下，唯一合法的"重建"＝**持有真实字节**，
  任何"从事件/转录再拼装"都属被否决路线。

**再对事件上报材料**（事件口实际携带什么）：

- 白名单三族事件 turn/start、assistant/message（带 usage）、compaction/* 的上报体
  字段全集＝`session_id / event / time / cwd / title / parent_session_id? / model? /
  usage?{input_tokens, cache_read_tokens, cache_creation_tokens, output_tokens}`：
  `plugin/ferryman-dsh/src/events.ts:253-271`；daemon 收形同集
  （`internal/daemon/dsh_receive.go:102-188`）。**无任何消息文本、无 system、无
  tools、无请求体**——连重构的原料都不在场，遑论拼装。

**四条独立依据，任一条即足判不通过**：

- **(a) 材料缺席**：事件口字段全集（上行）里不存在请求体或消息内容；"重建快照输入"
  无米下锅。
- **(b) 路线被否决**：即便放宽到"事件＋dsh 会话转录文件"合力重构 wire 体，也是
  ADR-0007 明文否决的 jsonl 重构路线（版本耦合、静默断链）；对 dsh 还需额外复刻
  pi-ai 的 anthropic-messages 序列化（dsh 请求体形状由适配器铸出，会话日志里没有
  wire 形），耦合只多不少。且缓存命中要求前缀字节保真——重构体打不中缓存，捕获-
  重放的经济性前提（前缀同源）直接落空。
- **(c) 键与体不在同一通道**：唯一见到 dsh 真实请求体的通道＝渡口入站口；当前接管
  路由 llm-pi-ai **不上线任何会话键**（体无 metadata.session_id、affinity 头被
  catalog 'withhold' 不可开——P2-2 四源钉死，`internal/dock/snapshot.go:244-261`
  注记），捕获归因恒 skipped（`snapshot.go:74-82` 只记跳过计数）。渡口已备好
  `x-deepseek-harness-session-id` 第三回落（`snapshot.go:261-266,313-322`），但只有
  llm-deepseek 路由（接法乙）会发它。**事件口有键无体，渡口有体无键**——事件上报
  无法补渡口的体，渡口归因也无法借事件的键。
- **(d) 链内独立同结论**：票02 接法乙 spike（只读评估，本链产物）一句话结论已钉
  "P2-4 插件直报给不了请求体捕获，心跳重放必须有快照体"、接法乙是 dsh 拿到
  「渡口捕获→心跳保温→摆渡→闸门」全套同权的唯一路径：
  `.scratch/dsh-phase2-finish/spike/jiefayi-report.md` §0/§4.2。

**判据②核对结果：不通过。** 判据①②须同时成立（spec 判据为"且"），故整体判定＝
**材料不可得**。

## 降级定案（二选一显式，F1 规则）

- **不接线**：`maybeEnqueue` dsh 面维持现状（`internal/daemon/watcher_dsh.go:226`
  "不 maybeEnqueue（见文件头 P2-1 边界注）"原样保留）；不补 enrich dsh 分支
  （P2-1 遗留注记③的"采集顺带回写"替身继续服役：`watcher_dsh.go:297-308`，事件口
  峰值回写同款 `dsh_receive.go:148-154`）。
- **代码面零改动**：本票产物＝本判定记录＋票内验证日志（`logs/t06-*.log`），
  `internal/daemon/`、`internal/enrich/`（该包不存在——enrich 入口＝
  `Watcher.enrichImpl`，`internal/daemon/watcher.go:1632`，已在 daemon 包内）零改动。
- **「精华被渡」缺口如实声明**：dsh 会话当前无摆渡触发路径——闲置拦截（桥+插件
  问询）与归还播种（插件 `agent/created`）已交付，唯"渡"缺役内闭环；依赖晨间
  接法乙/直报决策（见下）。

## 缺口（缺什么才可得）

1. **请求体捕获归因**（判据②(c) 的缺口本体）：dsh 流量需带会话键过渡口入站口，
   快照库才能收到"键＋体"成对的 `Capture`。当前 pi-ai 路无线（P2-2 钉死），事件口
   无体（本判定②(a)）。
2. **（若走同模型摆渡档）快照体即可，无提取器需求**；**（若走第三方/骨架档）另缺
   dsh 转录提取器分支**：worker 的 `extractFacts`/`saveSkeleton` 只有 cc/codex 分流
   （`internal/daemon/worker.go:405-412`），dsh 会话文件走 CC 提取器得空骨架——此为
   判定判据之外的实施缺口，如实记入，供晨间决策估算接线票范围时一并考虑。

## 依赖项（解锁条件，按 spike §4.2/§4.3）

- **接法乙（主路）**：dsh 接管路由从 llm-pi-ai 换 llm-deepseek 适配器 → 每请求带
  `x-deepseek-harness-session-id`（＝头行 id，spike H2，`adapter.ts:129`）→ 渡口
  第三回落生效、快照捕获归因成立 → 心跳保温与同模型摆渡（追加重放）面拿到前缀源。
  代价量级与前置条件见 `spike/jiefayi-report.md` §4.1-4.3（≈1~1.5 人日，含渡口两件
  剥除先行＋三项真机验证）；回退＝`provider apply` 重铸回 pi-ai 形态。
- **直报扩展（备选，今日不存在）**：插件事件面（session/event）扩展上报请求体——
  插件 API 面拿不到 wire 请求体（spike §4.2 已钉），需 dsh 侧新机制，且落全量报文
  与隐私不变量（台账/账本永不落消息内容）冲突，属新决策不在本役。

## 晨间决策项（预告，供晨报引用）

1. **接法乙条件切与否**（spike §4.2 两造论据齐备）：切 ⇒ dsh 拿全套渡口机械
   （捕获→保温→摆渡→闸门），摆渡接线票随即具备"可得"前提；不切 ⇒ dsh 维持
   "台账活动＋闸门＋归还"三件（桥+插件已覆盖），「精华被渡」明确放弃或另立
   dsh 专用摆渡通道（如第三方档＋提取器分支，不依赖快照，见缺口 2——需单独立票
   决策，本役不预设）。
2. 若切：渡口两件剥除先行合入＋生成器 v2＋V1/V2/V3 真机验证（晨间人操作清单：
   spike §4.3 五步）。
3. P2-5 重放头排期（spike R7：心跳重放行归因缺 dsh 头）随接法乙联动确认。

## 附：验收第4项核对（台账/守望键维持头行 id，不另造键）

- 本票代码零改动 ⇒ 键定义零变动（构造性满足"不另造键"）。
- 既有测试断言在位锁死键＝头行 id：daemon 收面 `dsh_receive_test.go:145-188`
  （Touch/入账键＝上报 `session-<uuid>` 原串）、守望面 `dsh_wiring_test.go:110-123`
  （台账键＝头行 id `session-1111…`，title/peak 采集回写）、子会话随父入账分流
  `dsh_receive_test.go:264-277`、插件侧 `plugin/ferryman-dsh/test/events.test.ts:67,84`。
  票内验证日志（下节）实证上述测试现行绿。

## 留痕（判定过程读了什么）

- 规则与背景：`spec.md:36`（条件式规则原文）；`.xcheck/20261003-124409/FINDINGS.md`
  F1（:4-16）与 F6（:90）；票06 任务书「判定要读的关键事实」。
- 前缀语义源：`docs/adr/0007-heartbeat-prefix-source-capture-replay.md`（全文）。
- 快照捕获面：`internal/dock/snapshot.go`（Snapshot 形状/Capture/ShouldCapture/
  P2-2 键注记/第三回落）。
- 事件面：`internal/daemon/dsh_receive.go`（DshEvent 全文）；`plugin/ferryman-dsh/
  src/events.ts`（buildEventBody 字段全集）＋ `test/events.test.ts`（键断言）。
- 摆渡面现状：`internal/daemon/watcher.go`（maybeEnqueue :479-566、enrichImpl
  :1632、装配 :148-160 之 serve.go）、`internal/daemon/worker.go`（do/doChain/
  saveSkeleton 的 agent 分流）、`internal/ferry/same_model.go`＋`append_replay.go`
  （同模型档快照依赖与 34 连败注记）、`internal/daemon/watcher_dsh.go`
  （P2-1 边界注 :28-36 与"不 maybeEnqueue"现状 :226）。
- 链内先验：`.scratch/dsh-phase2-finish/spike/jiefayi-report.md`（票02，§0/§4）。

## 票内验证（副作用声明项）

- `go test ./internal/daemon/`（零改动 sanity＋键断言现行绿实证；enrich 无独立包，
  入口在 daemon 包内已随跑）→ `.scratch/dsh-phase2-finish/logs/t06-daemon-green.log`。
- 不联网、不装依赖、真机夹具不涉及（降级分支无新夹具）。
