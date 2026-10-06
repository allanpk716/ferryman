# N2 前置查证：compactNow 在途与新用户消息的并发语义

> 票01 · 夜链 20261006-233527。问题：DSH `compactNow` 压缩进行中（摘要调用在途）时用户发新消息，实际行为是什么——排队等待？消息报错？会话状态错乱？
>
> 查证方式：源码双证（机制代码 + 接口契约 + 子系统文档 + 上游自带测试四层互证）。**沙箱实测未做**（理由与降档说明见 §6），结论置信度如实标注见 §7。

## 结论（三选一）：**排队**

压缩在途期间新用户消息**被接受并入持久收件箱排队，压缩收尾后自动开新 turn 处理**。不报错、不丢消息、无状态错乱。且这不是操作系统级数据竞争：Node 单线程事件循环下，"压缩在途 + 消息到达"是 await 点上的**确定性交错**，行为由 Phase 状态机完全决定——源码推演本身就是证明。

## 1. 机制层证据（agent-loop，ReactLoopAgent Phase 状态机）

克隆路径：`C:/Users/allan716/AppData/Local/Temp/dsh-research/`（下称 `<clone>`）。

### 1.1 新消息到达时的路径（压缩在途 = phase.kind === 'maintenance'）

- `<clone>/packages/core/agent-loop/src/agent.ts:42-50` — Phase 联合类型：`idle | maintenance{abort,lastTurn,wakeRequested} | running{...}`。`compactNow` 跑在 `maintenance` phase 里。
- `agent.ts:154-161`（`send()`）— 消息先 `this.inbox.splice(...)` **持久入队**，再 `wakeDriver()`。注意 `wakingAfterAbort` 只在 signal 已 abort 时改道 next-turn，正常压缩在途不触发。
- `agent.ts:214-235`（`wakeDriver()`）— phase 非 idle 时：maintenance 分支把 wake **锁存**为 `this.phase.wakeRequested = true`（:220-222）后直接 return，不启动 driver。
- 消息落盘：`<clone>/packages/core/agent-loop/src/inbox.ts:227-235` — splice 落为持久事件 `agent/inbox/spliced`（`session.append`），由 inbox 投影折叠（:31-56）。**消息在 send 时只进 inbox 投影，不碰 surface**；`user/message` 要到 step 执行时才 append 到 surface（`agent.ts:420-423`）。

### 1.2 压缩收尾时的汇聚（wake 重放）

- `agent.ts:183-204`（`runMaintenance`）— `finally` 块（:197-203）：先回 idle，再判 `maintenance.wakeRequested && this.inbox.hasPending` → `this.wakeDriver()` 重放锁存的 wake。**无论 job 成功还是抛错，finally 都执行**——压缩失败也不吞消息。
- `agent.ts:252-265`（`kick`）— 新 driver 起来后 `turn()` → `preStep()`（:271 `inbox.claim('next-turn', turn)`）领取排队消息，开新 turn。
- 时序闭合：finally 块从 `setPhase(idle)` 到 `wakeDriver()` 之间全同步、无 await，单线程下不存在"消息插进来但 wake 丢了"的窗口。
- 对外状态语义：`agent.ts:140-142` — maintenance 期间 `agent.status` 上报 `'idle'`。宿主看到 idle 照常收消息，无拒绝路径。

### 1.3 反方向（已知事实，复核一致）

`compactNow`（`<clone>/packages/compaction/compaction-basic/src/index.ts:383-435`）经 `agent.runMaintenance`（:390）执行；agent 非 idle（含已在 maintenance）时 `runMaintenance` 同步 throw（`agent.ts:184`），被 compactNow 的 catch 包成 `ManualCompactionError('busy')`（:428-434）。即**压缩在途时第二次 compactNow = 干净报错**，与票面已查证事实一致。

## 2. 契约层证据（compaction 包 docstring，上游自己声明的语义）

- `<clone>/packages/compaction/compaction/src/index.ts:77-86`（`ManualCompactAgentContext.runMaintenance` 契约）：*"Run a non-turn maintenance operation only while the agent is idle, **withholding later waking input until it settles**."*
- `compaction/src/index.ts:142-161`（abstract `compactNow` 契约）关键三句：
  - :146-148 — *"append a standalone `compaction/start` before summarization. That durable marker is the compaction lock until one `compaction/end` attempt."*
  - :148-150 — *"**Later waking prompts remain accepted in FIFO order and start only after** the optional durability checkpoint and idle-task settlement."*
  - :149-151 — *"Context injected while the summary runs may sit between the marker pair; only the selected span must remain stable."*（摘要运行中到达的上下文落在 marker 对之间是**契约明确允许**的形态）
- `<clone>/docs/subsystems/compaction.md:84` — *"compactNow() runs as agent maintenance between turns … **flushes a closed attempt before later queued prompts may derive from the new surface**"*——排队消息在新 turn 里从**压缩后的新 surface**（摘要检查点替换后）派生请求。

## 3. surface 一侧的安全性（压缩中 surface 会不会被新消息弄脏）

- 消息到达只写 `agent/inbox/spliced`，不写 surface（§1.1）→ 摘要在途期间 surface 根本不变。
- 即便有其他来源在 span 外 append（如 inject 的上下文），compactNow 用 `stability: 'selected-span'` + `owner: null`（compaction-basic/index.ts:406-409），`assertSelectedSpanStable`（`<clone>/packages/compaction/compaction-basic/src/region.ts:447-468`）只要求**被选 span**不变；:443-445 注释明说 *"Nodes added outside it remain visible and do not invalidate the summary"*。
- 替换落盘是单事件原子操作：`region.ts:506-509` 一次 `session.append('user/message', checkpoint, { surfaceOp: { op: 'replace', startSeq, endSeq } })`，surface 折叠同步应用（`<clone>/packages/core/session/src/surface.ts:255-257` 的 replaceGeneration/contentGeneration 单调计数）。JS 单线程下 commit 段无 torn state。
- 压缩对压缩的互斥另有 durable 锁：`compaction/start` 先于摘要落盘（region.ts:160-163, :210），重复进入被 `assertCompactionInactive` 拒为 `ManualCompactionError('busy')`（region.ts:308-320）。

## 4. 宿主 API 层（web 实例发消息走哪条入口）

- `<clone>/packages/api/session-controller/src/commands.ts:364-365` — 用户 prompt 派发：`request.mode === 'steer' ? agent.steer(message) : agent.followup(message)`。
- `steer`/`followup` 都汇入 `send(..., wakeup=true)`（`agent.ts:163-169`）→ §1.1 的"入队 + 锁存"路径，maintenance 期间行为一致。
- 准入路径（commands.ts:330-376）**没有针对压缩状态的拒绝分支**——消息照常被接受。
- Ferryman 侧消费链（本仓，已落地代码）：`plugin/ferryman-dsh/src/compact.ts` catch `ManualCompactionError code 'busy'` → 上报 `{ok:false, reason:"busy"}` 不执行；daemon 指令槽过期即弃（票02）。**压缩在途时轮询循环二次触发 = 干净 busy 上报，零副作用，无堆积。**

## 5. 上游自带测试（本场景的权威断言，本环境未执行）

`<clone>/packages/core/agent-loop/tests/loop.spec.ts` 有本票场景的专属测试组：

| 测试 | 行号 | 场景与断言 |
|---|---|---|
| `replays a wake latched behind maintenance at convergence` | :401-423 | **就是本票场景**：runMaintenance 在途时 `send(agent, 'wake behind maintenance')` → maintenance 结束 → `whenIdle()` 后断言消息被处理（`userTexts` 命中、1 次模型请求）。 |
| `cancels queued wakeup work together with an active maintenance task` | :349-378 | 压缩在途收到消息后 cancel：队列与锁存同弃，maintenance 中止；cancel 后新消息重新锁存、汇聚时重放。 |
| `suppresses the replay when a latched maintenance wake is removed` | :425-450 | 锁存期间消息被从 inbox 移除 → 汇聚时不重放、零 turn。 |
| `rejects concurrent maintenance` / `rejects a second maintenance task` | :330-347, :380-399 | 压缩在途时第二次 runMaintenance 同步 throw（compactNow busy 的机制根）。 |

克隆缺 node_modules 且票面禁装依赖，这组测试在本环境**未执行**；作为上游自己声明的行为断言引用（若票08 需要可在装好依赖的环境跑 `pnpm vitest` 于 agent-loop 包复验）。

## 6. 沙箱实测：未做 + 可执行配方

**未做理由**：实测需"触发压缩在途 + 秒级窗口内经 web UI 发第二条消息"的双端竞态编排（灌上下文→触发 compact→窗口内发消息→双消息归宿观测），属票08 E2E 基建量级；而本场景是单线程状态机的确定性交错（§结论），四层源码互证已闭合，边际置信度收益低。按票面许可（"源码证据链完整+推演明确标注未实测也可达验收"）降档处理。

**留档配方**（票08 如需实弹验证可照此做）：

1. `bash tools/e2e_dsh/start.sh` 起沙箱栈（daemon 25900 / web 25902，铁闸 25xxx，生产口零接触）。
2. 经 web UI（Playwright）建会话，发 2-3 条消息确保有可压缩 span（`selectCompactableRange` retainTokens=0 时 ≥3 个 surface 节点即有 range）。
3. 触发压缩在途：优先走 daemon 沙箱口 `/dsh/poll` 拿 compact 指令槽（票02/票05 链路），或 web UI 手动 `/compact`；摘要经真实 LLM 数秒，窗口足够。
4. 窗口内经 UI 发第二条消息。
5. 观测断言：① 第二条消息不被拒、UI 不报错；② 压缩完成后出现摘要检查点 + 新 turn 回答第二条消息；③ 会话转录（jsonl）序：`compaction/start` → `agent/inbox/spliced`（夹在 marker 对之间，契约允许）→ `compaction/summary`+replace → `compaction/end` → 新 `turn/start` → `user/message`；④ 压缩失败注入版（断网/坏 key）：compaction/end 带 error，消息仍被新 turn 处理，surface 未替换。
6. 生产零接触自查：`netstat` 确认仅 25900-25902。

## 7. 结论置信度与降级链吸收语义

**置信度：高（源码推演确定性成立），未实测降档标注**。机制非时序敏感（单线程 await 点确定性交错），实测风险主要在"配方编排"而非"行为不确定"。

**降级链吸收语义（一行）**：压缩在途期间新消息由上游（agent-loop inbox + wake 锁存）保证有序排队、压缩收尾后自动开新 turn，降级链无需新增处理，只需容忍压缩后新 turn 的请求前缀含摘要检查点（前缀变化 → 缓存失效一次，属预期成本）。

**对票08 的输入**：
- 结论是排队而非状态错乱 → **并发竞态断言不必跳过/waiting**；可安全断言：竞态窗口内消息被接受（无 error 反馈）、压缩收尾后新 turn 处理该消息、jsonl 序符合 §6.5-③。
- 双触发方向：压缩在途时 compactNow = 干净 `busy`（插件已 catch 上报 `{ok:false, reason:"busy"}`），E2E 可断言该错误路径不产生任何 surface/turn 痕。
- 唯一不建议在 E2E 竞态断言的：摘要窗口的**精确时长**依赖真实 LLM 延迟，断言只认事件序不认时长。

## 附：关键 file:line 索引（克隆根 = `<clone>`）

| 证据 | 位置 |
|---|---|
| Phase 状态机（maintenance/wakeRequested） | `<clone>/packages/core/agent-loop/src/agent.ts:42-50` |
| send 入队 + wakeDriver 锁存 | agent.ts:154-161, 214-235（锁存 :220-222） |
| runMaintenance 汇聚重放（finally） | agent.ts:183-204（重放 :200） |
| maintenance 对外上报 'idle' | agent.ts:140-142 |
| user/message 仅 step 时上 surface | agent.ts:420-423 |
| inbox 持久 splice 事件 | `<clone>/packages/core/agent-loop/src/inbox.ts:227-235` |
| compactNow 全流程 + busy/cancelled 包装 | `<clone>/packages/compaction/compaction-basic/src/index.ts:383-435`（busy :428-434） |
| compactNow 锁 + 稳定性选项 | 同上 :406-409；锁落盘 region.ts:210 |
| selected-span 稳定性（span 外新增不失效） | `<clone>/packages/compaction/compaction-basic/src/region.ts:447-468`（注释 :443-445） |
| 替换单事件原子落盘 | region.ts:506-509；surface 计数 `<clone>/packages/core/session/src/surface.ts:255-257` |
| 压缩 durable 锁互斥 | region.ts:160-163, 308-320 |
| runMaintenance 契约（withhold waking input） | `<clone>/packages/compaction/compaction/src/index.ts:77-86` |
| compactNow 契约（FIFO + 收尾后开跑） | compaction/src/index.ts:142-161（:148-151） |
| 子系统文档（排队派生新 surface） | `<clone>/docs/subsystems/compaction.md:84` |
| 宿主 prompt 派发 steer/followup | `<clone>/packages/api/session-controller/src/commands.ts:364-365` |
| 本场景上游测试 | `<clone>/packages/core/agent-loop/tests/loop.spec.ts:401-423`（另 :330-399, 425-450） |
| Ferryman busy 上报链 | 本仓 `plugin/ferryman-dsh/src/compact.ts`（code 'busy' → `{ok:false, reason:"busy"}`） |
