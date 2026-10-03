# P2-5 设计文档 · dsh 判活信号重选 ＋ 等待窗口/问询守望适配 ＋ beat 头决策点

> 票 07（spec「P2-5 设计文档」节，`.scratch/dsh-phase2-finish/spec.md:37`）。
> 读者＝晨会拍板者与后续实施票：自包含，每条主张带出处。出处体例：
> - 无前缀 `file:line` ＝ Ferryman 本仓实现（路径自仓库根起）；
> - 〔dsh 克隆 `file:line`〕＝ dsh 调研克隆只读事实（deepseek-ai/deepseek-harness master@639ed015，`C:/Users/allan716/AppData/Local/Temp/dsh-research/`）。
>
> **本票不改任何代码**（decision_refs D2＝P2-5 是设计文档；proposal rev1 第 4 件口径＝决策点不实施）。
> §3 beat 头是决策点：改动点/风险/前置条件齐备，实施另立票且受本设计结论（§4 DC-5）阻塞。

## 0. 背景速览（自包含所需最小集）

- **会话键**：P2-2 定案，dsh 全链路统一键＝会话头行 id `session-<uuid>`（spec.md:25；守望/台账现行键，零换算，watcher_dsh.go:20-26）。CC 键＝裸 UUID36（转录文件名，claude-cli 2.1.273 实证，internal/dock/snapshot.go:234-236）。**两键形状不同是 §3 的根源。**
- **票 05 已落地的事件面**（本设计的地基）：dsh 插件把会话事件直报 daemon 管理口 `/dsh/event`；daemon 白名单＝`turn/start`、`assistant/message`、`compaction/*`——已知事件 Touch 活动登记＋usage 四列入账；`agent/status`、`agent/disposed` 插件侧**已送达**但 daemon 白名单外静默收窄 `skipped=unknown-event`（internal/daemon/dsh_receive.go:107-110；插件侧注记明言「消费方=P2-5 判活设计，届时 daemon 侧白名单/语义随设计落票——本侧先把信号送达事件口，通道不再改」，plugin/ferryman-dsh/src/events.ts:294-302）。**§1 的落点就是决定这两个信号的接收语义。**
- **跨源去重**（票 05）：事件口与 pollDsh 文件守望面双记风险已用「事件接管单源化」消除——会话一旦有插件事件流量（fed 表），文件面 usage 采集让位、Touch/观察窗照常（internal/daemon/dsh_dedup.go:3-26；watcher_dsh.go:217-225）。
- **术语**（词汇表 CONTEXT.md）：守望＝轮询会话目录登记活动；台账＝会话状态登记表；闸门＝闲置超阈值的提交拦截；等待窗口＝主会话等子代理的区间（机器等机器）；问询窗＝提问潮等答复窗口（心跳保温＋死线强制入队摆渡）；摆渡＝闲置时自动生成交接文档；心跳（beat）＝对主快照的低成本重放保温。

---

## 1. dsh 判活信号重选

### 1.1 「判活」是三件事，不是一件事

| 消费面 | 回答的问题 | CC 现行信号源 | dsh 现状 |
|---|---|---|---|
| ① 活动/闲置 | 会话最近有没有动静（闸门闲置钟） | 转录 mtime Touch（watcher.go:336）＋内容时钟（gate.go:140-149，幻影写免疫） | **已闭环**：pollDsh mtime Touch（watcher_dsh.go:214-216）＋事件 Touch（dsh_receive.go:142-155），票 05 单源化收口（dsh_dedup.go） |
| ② 机器等机器 | 会话看似闲置实则机器在跑（闸门豁免道） | 三道 OR：子代理计数／停车窗／悬空 tool_use（gate.go:304-327） | **三道全空转**（§1.3 逐道核） |
| ③ 保温排程 | 等待/等答复期间要不要排心跳 | 窗口表＋双泳道（watcher.go:340-342、993-1062） | cc-only，dsh 未接（watcher_dsh.go:34-36 边界注） |

①不重选——票 05 已把最好的信号（事件直报）落了。本票的重选对象是②的信号源与③的接法。

### 1.2 CC 现行判活基线（对照物，含 async 文案判据定位）

机器等机器豁免 `machineWaiting` 三道 OR（gate.go:316-327），上界与兜底关系如实声明在 gate.go:307-315：

1. **子代理计数道**：`/subagent` 钩子 start/stop 事件驱动的台账计数（windows.go:57-130；HTTP 路径 `/subagent`，httpapi.go:169），1h 泄漏保护（SubagentEventLeakS，internal/ledger/ledger.go:28）。
2. **停车窗道**：计数归零但 async 子代理真身在跑时停表挂起（`parkOrCloseLocked`，windows.go:141-159）；豁免谓词 `WindowWait` 带 1h 懒过期（windows.go:306-320；ParkExpireS=3600，daemon.go:38-43）。
3. **悬空 tool_use 道**（计数丢失的兜底）：CC 转录尾部 256KB 滑窗内 tool_use id 集合减 tool_result id 集合非空（`HasDanglingToolUse`，internal/cctrans/cctrans.go:189-240；尾窗 DefaultTailBytes=262_144，cctrans.go:23）。

**async 文案判据**（本票要求定位并明确不移植的对照件）：`HasAsyncLaunch`（cctrans.go:346-412）——读 CC 转录尾窗，取最后一个 Task/Agent tool_use，判其是否 async：input 的 `run_in_background`/`background` 为真（cctrans.go:381），**或**对应 tool_result 文本以 `"Async agent launched"` 开头（cctrans.go:405-407）。消费点两处：停车判定（windows.go:151-152）与 start 时尾判锁存（windows.go:122-123、171-180）。

### 1.3 候选逐一评估

#### 候选 A：`agent/status` → idle（以及 running）

协议事实：`AgentStatus = 'idle' | 'running'`（〔dsh 克隆 packages/core/agent/src/runtime-types.ts:109〕）；事件 `agent/status` @mode emit、fire-and-forget（runtime-types.ts:271-280；监听器失败只记日志，插件侧钉点 events.ts:8-10 引 core/agent/src/index.ts:513-525）。**语义锚点：`idle` ＝「没有任何 driver 在调度或执行」（"idle means no driver remains scheduled or active"，runtime-types.ts:272-274）；status 是转入态（transition destination）。**

- **可靠性**：结构性状态机信号，不从文案反推——这是 dsh 相对 CC 的代际优势。转发链尽力而为（插件 forwardLifecycle fire-and-forget、失败静默，events.ts:302-317）。缺点：插件离线/dsh 未装/HTTP 终败＝信号整体缺失（与 CC 钩子离线同款盲区）。
- **时延**：状态翻转即发，实时（emit 位，runtime-types.ts:278）。远优于文件面轮询周期。
- **盲区**：①插件部署前的存量会话无信号（文件面 Touch 照旧，活动面不受影响）；②信号丢失时运行态可能滞留「在跑」——需超时失效兜底（§1.5 I2）；③`running` 是否覆盖「父会话委派子代理期间」未在本次钉死〔dsh 克隆 subagent 包 child-agent.ts:139-157 只有子会话铸造面，父等待语义未读〕——但推荐组合对该不确定性鲁棒（§1.5：子会话信号兜底）。

#### 候选 B：`agent/disposed`

协议事实：`agent/disposed` @mode emit；语义＝「agent 离开 registry；在 driver 静默与 scoped-registration 收尾之后、session 脱附之前发出」（runtime-types.ts:262-270）。

- **可靠性**：唯一明确的**会话终结**信号——会话在 dsh 侧关闭/清理。emit fire-and-forget，同样依赖插件在位。
- **时延**：即时。
- **盲区**：①dsh 进程被杀/崩溃不发 disposed——**非可靠终止语义**，不能把「没收到 disposed」当「活着」；②dispose 后同键可 resume（`agent/created` 的 source 含 resume/clear/compaction，runtime-types.ts:256-261），终结态必须可清除。
- **用途边界**：它回答「死了没有」，不回答「活着没」——负信号，只用于关账（停排心跳、剔摆渡队、拦截文案声明），不参与活动钟。

#### 候选 C：子女目录写入

现状事实：pollDsh 已扫 `~/.dsh/sessions/<项目>/<会话>` 两层（watcher_dsh.go:164-187）；子代理会话＝独立目录、头行 `origin=subagent`＋`parentSession`＝父键（watcher_dsh.go:204-212；〔dsh 克隆 packages/subagent/subagent/src/child-agent.ts:139-157 `childSessionMeta` 铸造 `origin:'subagent'`、`parentSession: parentHeader.id`〕；父会话事件流内有持久 `subagent/catalog` 目录事件记录每个孩子〔dsh 克隆 packages/subagent/subagent/src/catalog.ts:28-45〕）。今日子目录只被 stat＋采集/让位，不产生任何「子代理在跑」状态（watcher_dsh.go:204-213）。

- **可靠性**：文件面信号，与插件无关——插件离线时唯一的子代理活动证据。但**只能证「有过写入」，不能证「还在跑」**：子代理卡在长工具调用期间代文件无写入，与「已死」不可区分——dsh 事件日志没有 CC 那样的 tool_use/tool_result 配对可供悬空判定（§1.4）。
- **时延**：守望轮询周期粒度，劣于事件面。
- **盲区**：①长静默子代理；②事件接管会话的子目录文件面让位（dshIsFed 分流，watcher_dsh.go:208-210）——stat 仍在、语义仍在，但若依赖文件解析则与单源化冲突。
- **定位**：候选 A 的兜底道（插件离线降级），不是主信号。

### 1.4 CC async 文案判据：明确不移植

三条理由，按分量排序：

1. **有结构化替代，无需从文案反推**。CC 判活靠读转录文案是因为 CC 钩子面没有「子代理还在跑」的结构信号；dsh 事件面有（`agent/status` running/idle，§1.3 候选 A）。用文案判据是开倒车。
2. **判据本体是 CC 转录形状＋CC CLI 英文文案，dsh 侧零对应物**。`"Async agent launched"` 是 CC CLI 写进 tool_result 的固定前缀（cctrans.go:405），`run_in_background` 是 CC Task/Agent 工具的 input 字段（cctrans.go:378-381）；dsh 事件日志的行形是 `{type, data, ...}` 事件封套（internal/dshtrans/dshtrans.go:114-140 解析面；插件 SessionEventRef 同形，events.ts:107-114），既无这两个字段也无该文案。移植＝对 dsh 事件流做形状猜测，违背「协议事实钉源码」纪律。
3. **CC 自己把文案判据定位为最后手段**：判不中一律退回旧语义即闭窗——「宁可少记一个真窗，不误停一个假窗」（windows.go:132-141 注释原文）。这个哲学本身值得继承（§1.5 I4），实现不移植。

### 1.5 推荐组合与不变量

**推荐组合**：`agent/status`（运行面，主信号）＋ `agent/disposed`（终结面，负信号）＋ Touch 活动钟（活动面，现状保留）；子女目录写入**不接线**，仅作为「插件离线时运行面缺失」的已记录降级事实（不建状态，避免第二份真相）。

信号优先级：**终结态 ＞ 运行态 ＞ 活动钟**。

不变量表述（判定表＋四条不变量）：

| 判定 | 条件（信号组合） |
|---|---|
| **在跑**（机器等机器，闸门豁免） | 收到过 `status=running` 且未收到配对 `status=idle`，且距该 running 未超失效上界（I2） |
| **死了**（终结） | 收到 `agent/disposed` 且其后未收到该键的 `agent/created(resume)`（I3） |
| **活着**（不拦） | 活动钟新鲜（`now − coversBar < BlockS`，阈值现值 35min，config.go:57、171-173）**或**在跑 |
| **凉了**（闸门正常路径） | 活动钟超阈 且 不在跑 且 未终结 |

- **I1（活动钟单源）**：dsh 会话闲置钟只由 Touch 推进；Touch 源单调——事件接管会话只认 `/dsh/event`，未接管只认 pollDsh mtime（票 05 已落，dsh_dedup.go:16-23；本设计不改）。
- **I2（运行态有界）**：运行态按会话内到达序配对（running 置位、idle 复位）；插件离线丢事件时运行态可能滞留——置位时记时间戳，超失效上界自动失效。上界建议与计数道泄漏保护同值（SubagentEventLeakS=3600s，ledger.go:28；独立命名留单一改点，ParkExpireS 注释同款先例 daemon.go:38-40）。失效后果＝豁免丢失回正常闸门路径，与 CC 计数道泄漏同哲学（gate.go:307-308「守护重启丢内存计数→悬空道兜底」——dsh 无第三道，兜底即失效，见 I4）。
- **I3（终结优先且可逆）**：终结态置位后该会话不排心跳、不入摆渡队；被 resume 清除。同键 disposed 之后又到 running（异常序）以先到为准并留一行告警。
- **I4（豁免尽力而为）**：dsh 的机器等机器豁免＝运行态在效（I2）；**没有第三道兜底**（悬空道对 dsh 代文件恒 False，论据见下）。信号缺失时回正常闸门路径——「宁可走正常闸门路径，不误豁免」（gate.go:313-315 原哲学）。盲区后果如实声明：插件离线＋长跑会话可能被误拦，用户可用强续前缀自解（gate.go:104-121 bypass 道对全 agent 生效）。

**悬空道对 dsh 恒 False 的论据**（I4 的依据）：CC 判据读行**顶层** `message.content[].type` 的 tool_use/tool_result（cctrans.go:216-240）；dsh 代文件行顶层是事件封套 `{type, data, ...}`（dshtrans.go:132 起按顶层 `type` 分派），顶层无 `message` 键 → 逐行 continue → 集合差恒空 → False（「无 assistant 行一律 False」的既定语义，cctrans.go:198-199）。即：现闸门对 dsh 的第三道不是「误判风险」而是「恒不命中但每次问询白读一次 256KB 尾窗」（cctrans.go:23）——列入 §4 DC-6 微优化。

### 1.6 `agent/status` / `agent/disposed` 接收语义（本设计定案，票面要求）

今日两事件在 daemon 白名单外收窄 `skipped=unknown-event`（dsh_receive.go:107-110）。定案：

1. **白名单收编**：`DshEvent` 事件白名单从 `{turn/start, assistant/message, compaction/*}` 扩为再加 `agent/status`、`agent/disposed`（改点唯一：dsh_receive.go:108 的白名单条件）。插件侧零改动（通道已送达，events.ts:294-302 预留即为此）。
2. **`agent/status`**：①照主会话路径 Touch 活动登记（dsh_receive.go:142-147 同路）——机器在跑是真活动，防「长 turn 中段无事件落盘 → 闲置钟虚走 → pre-step 问闸被拦」：dsh 的闸门问询发生在 pre-step（插件 onPreStep，events.ts:160-184），排队输入的问闸时刻可能距上一次落盘很远，此时 running 是唯一能证明会话没凉的信号；②维护运行态（I2 置位/复位）；③不入账（无 usage）。`status` 字段收形只认 `running`/`idle`（〔dsh 克隆 runtime-types.ts:109〕两值闭集），他值按 unknown-event 收窄。
3. **`agent/disposed`**：**不 Touch**（死亡不是活动，不刷新闲置钟）；置终结态（I3）。
4. **子会话事件**（`parent_session_id` 非空，events.ts:260-262）：沿用今日分流——不 Touch 父（dsh_receive.go:140-142）；运行态记子键，**族系在跑＝父运行态 OR 任一已知子键运行态**（已知子键来源＝账本 subagent 列/fed 表回种同源，dsh_dedup.go:82-84）。若实施时钉死「父委派期间父自身保持 running」（§1.3 候选 A 盲区③），子键道即冗余保险，保留。

落点形态建议（供实施票，不预铺代码）：运行/终结态落台账 SessionState 新字段（时间戳形），**不复用窗口表**——窗口表的语义束（子代理计数、停车过期、两窗互斥）是 CC 等待窗专用，dsh 运行态没有「停表等恢复」语义，套进来只会引入无关不变量；闸门侧 `machineWaiting` 加 agent 分支读台账（gate.go:316-327 骨架不动，与 SubagentActive 同型）。

---

## 2. 等待窗口 / 问询守望对 dsh 的适配设计

### 2.1 既有机制现状（CC 形，语义锚点）

- **等待窗口状态机**（windows.go:27-53 头注「语义+注释逐字搬运」）：`/subagent` start 开窗/计数、归零即闭或停车（windows.go:74-128）；停车＝async 真身在跑的停表挂起（windows.go:141-159）；主会话恢复（NoteUsage，windows.go:288-296）与 prompt（NoteGatePrompt，windows.go:376-383）是两条闭窗道；`WindowWait` 是豁免谓词（windows.go:306-320）。
- **等待窗心跳泳道**（票 04）：窗开着＋主会话闲置满 τ＋前缀够 → 排跳保温（maybeWaitBeats，watcher.go:993-1062）；**cc-only**（watcher.go:999-1001「心跳前缀源＝渡口 CC 流量，仅 cc」）。
- **问询窗**（T51）：四条件开窗——①提问潮检测（CC jsonl 形 detect）②悬空集 ⊆ {AskUserQuestion} ③子代理不在飞 ④前缀 ≥ min_ctx_tokens（maybeQwatch，watcher.go:610-725；**仅 cc**，watcher.go:625-627）；开窗即排心跳计划（beatPlan，watcher.go:767-778）；**死线＝block_s − ferry_deadline_lead_s**（qwatchDeadline，watcher.go:595-606；lead 默认 480s＝拦截阈值前 8 分钟，config.go:192）到点强制入队摆渡；关窗只由新写入（Touch）触发（watcher.go:635-636；pollCC:338）。一键停＋熔断降级齐备（QWatchStop，windows.go:391-428）。
- **心跳发送**：fireOneBeat → sendBeat（observe→NoopSender，enforce→注入 sender，watcher.go:853-889）→ HttpBeatSender 重放渡口主快照（internal/beat/httpsender.go:88-144；快照缺失＝snapshot_missing 错误类，httpsender.go:92-97）。

### 2.2 等待窗/停车语义：dsh 不复用（结论与论证）

**停车不复用。** CC 停车回答的问题 Narrow 而明确：「子代理计数已归零，但 async 真身还在跑，等待没结束」——判据是文案（§1.4）。dsh 的事件面给了结构化运行态（§1.5），这个问题在 dsh 侧不成立为独立机制：运行态即等待态。若未来钉死 dsh 存在「主会话 idle＋后台任务在跑」形态（〔dsh 克隆〕packages/schedule、packages/jobs 面，本次未读未钉），届时按 CC 停车同构补——触发条件记入 §4 DC-3，今日不预铺。

**等待窗心跳泳道对 dsh 无标的。** 泳道保温的标的＝「主会话闲置、子代理在跑」期间的缓存空转。dsh 推荐组合里 running 事件 Touch 活动钟（§1.6 第 2 条）：会话在跑 ⇒ 闲置钟不虚走 ⇒ 没有空转期 ⇒ 无保温需求。会话真闲置（不在跑）时窗口不存在，泳道自然不排。结论：**maybeWaitBeats 不扩 dsh**（watcher.go:999 的 cc-only 检查维持）。

**既有闭窗道对 dsh 天然无害**，无需防守：NoteGatePrompt 在 Gate 步 0 对全 agent 调用（gate.go:95），但 dsh 键在窗口表无窗（winKey{dsh,sid} 从未开窗），closeWindowLocked 按键查空即返（windows.go:189-196）；票 05 边界注「不喂 NoteUsage/ReqClock」（watcher_dsh.go:34-35）维持不变——没有停车窗，NoteUsage 无窗可闭。

### 2.3 问询窗：骨架全复用，命中判定是缺口

机制骨架（开窗排计划、心跳节律、死线强制入队、一键停、熔断降级、与停车窗互斥）全部 agent 无关或已参数化，**零改动复用**。逐条件对照四道开窗判据：

| 条件 | CC 形 | dsh 形 | 缺口 |
|---|---|---|---|
| ①提问潮 | CC jsonl 文本检测（detect，watcher.go:644） | **有材料无接线**：dsh 事件流有 `user/message`（〔dsh 克隆 packages/core/session/src/types.ts:309〕），插件今日不转发（events.ts:250-252 白名单外）；文件面解析与票 05 单源化冲突（fed 会话文件面让位，dsh_dedup.go） | 需插件扩转发＋daemon 侧 dsh 形判定算法（未设计，实施票钉） |
| ②悬空集 ⊆ {AskUserQuestion} | CC 工具名 | dsh 等价「向用户提问」工具名清单**未钉**（〔dsh 克隆〕tools 面本次未读） | 实施票钉源码清单 |
| ③子代理在飞＝0 | 台账计数 | **直接映射**：运行态不在效（§1.5） | 无（随 DC-1 落地即得） |
| ④前缀 ≥ min | 台账 peak | **已有**：事件面/文件面均回写 peak（dsh_receive.go:148-154；watcher_dsh.go:299-308） | 无 |

**死线语义原样复用**：`block_s − lead` 公式与「赶在拦截前留出交接生成余量」的理由（watcher.go:595-599）对 dsh 同构；dsh 阈值走同一 `ThresholdFor`（现全 agent 共用全局，config.go:221-223），lead 默认 480s 不变（config.go:192）。

**一期建议：dsh 问询窗 off**（§4 DC-4）。理由：①dsh 流量规模小，保温经济性未证；②①②两道检测缺口未钉，开了也只能开半套判据；③问询窗是增强不是止血（闸门/摆渡主链不依赖它）。

### 2.4 心跳排程与 beat 面（与 §3 的接缝）

心跳排程入口今日全 cc-only：maybeFireBeats 只被 pollCC 调用（watcher.go:341），maybeQwatch/maybeWaitBeats 自带 agent 检查（watcher.go:625-627、999）。**dsh 要排任何心跳，先决问题是排程扩大**（pollDsh 侧接 maybeFireBeats 或等价入口）——这是独立于头分发的决策（§4 DC-4/DC-5 的分工：DC-4 拍板「dsh 排不排心跳」，DC-5 拍板「排的话头怎么发」）。此外心跳发送的语义前提是**渡口里有该会话的主快照**——dsh 快照的可得性见 §3.4 前置条件①，这是整个「dsh 心跳」话题的总闸。

---

## 3. beat 发送侧非 UUID36 键改发 dsh 头（决策点——本票不改代码）

### 3.1 现状与问题

beat 的两个发送点（快照重放 Send、追加重放 SendAppendReplay）**无条件**发 CC 会话归因头：

- `req.Header.Set(dock.HeaderClaudeCodeSessionID, plan.SessionID)`（internal/beat/httpsender.go:110-112，注记「渡口记账行据此按会话归集」）；
- `req.Header.Set(dock.HeaderClaudeCodeSessionID, p.SessionID)`（internal/beat/appendreplay.go:100-104，同款注记）。

`HeaderClaudeCodeSessionID = "x-claude-code-session-id"`（snapshot.go:238）。渡口侧归因提取 `ExtractSessionID` 的头回落对 CC 头做 **isUUID36 校验**（snapshot.go:300-310），对 dsh 头做 `session-` 前缀形校验（`HeaderDeepSeekHarnessSessionID = "x-deepseek-harness-session-id"`，snapshot.go:244-261、312-337）。

**问题**：若未来对 dsh 会话（键 `session-<uuid>`，非 UUID36）排心跳，把 dsh 键塞进 CC 头会被渡口 isUUID36 校验拒收（视同缺失，snapshot.go:306-308）→ beat 记账行归因空。且头是 dsh 唯一归因面——dsh 请求体没有 `metadata.session_id`（P2-2 四源钉死，snapshot.go:249-256），「头体并存以体为准」的体道恒缺。

### 3.2 改动点定位（两处，票面要求）

| # | 位置 | 现行代码 | 改法 |
|---|---|---|---|
| 1 | internal/beat/httpsender.go:112（Send） | 恒发 CC 头 | 按 `plan.SessionID` 形状分流：UUID36 → CC 头（现状）；`session-` 前缀形 → `dock.HeaderDeepSeekHarnessSessionID` |
| 2 | internal/beat/appendreplay.go:104（SendAppendReplay） | 恒发 CC 头 | 同上（对 `p.SessionID`） |

形状校验应**单源**：`isUUID36`/`isDshSessionIDShape` 今日均未导出（调用点 snapshot.go:306、328）——实施票从 dock 导出一个「会话键形状分类器」（或导出两谓词），beat 包消费；禁止在 beat 包复刻第二份形状规则（漂移风险，见 §3.3）。校验规则本体已钉死：`session-` 前缀、总长 9..128、字符集 `[A-Za-z0-9._-]`（snapshot.go:324-337）。

### 3.3 风险

1. **形状分类两处实现漂移**——缓解：dock 单源导出＋两侧测试互钉（§3.2）。
2. **CC 侧回归面**：接近零。CC 会话键恒 UUID36（转录文件名实证，snapshot.go:234-236），两形状互斥，分流不会改变任何现有 CC 行为；测试仍需钉「UUID36 键走 CC 头」不回退。
3. **beatHeaders 白名单对 dsh 快照的适配未实测**：头部重建白名单＝content-type/anthropic-version/anthropic-beta/user-agent/accept 五头「有则带」（httpsender.go:166-181）——llm-deepseek 路由快照的头集是否落在白名单内未验证（风险低：缺头只影响重放保真度，不影响归因；归因头是显式另设的）。实施票真机验证时一并核对。
4. **误发面**：对 CC 会话误发 dsh 头或反之＝归因错桶。分流谓词互斥＋测试钉双向即闭。

### 3.4 前置条件与阻塞关系

1. **快照可得性（总闸）**：dsh beat 有意义的前提是渡口有该会话主快照；快照捕获键＝`ExtractSessionID`，而 dsh 唯一带键上线的通道＝llm-deepseek 适配器路由（snapshot.go:257-260）。今日接管路由 llm-pi-ai 不发任何键 → dsh 捕获恒 skipped → dsh beat 必然 `snapshot_missing`（httpsender.go:92-97；watcher_dsh.go:25-26 同结论）。⇒ **本决策点受接法乙路由决策阻塞**（spec.md:49 已把路由切换列晨间人操作；接法乙代价评估见 `.scratch/dsh-phase2-finish/spike/jiefayi-report.md`）。
2. **排程扩大**：心跳排程入口 cc-only（§2.4）——头分发改动若先行落地，在 dsh 排程存在前是零调用者的死代码（行为零变化，但需测试钉住分流本身）；与「dsh 心跳排程」票合票落或先行落，二选一（§4 DC-5 给推荐）。

---

## 4. 决策点清单（供晨会/后续票拍板）

| # | 议题 | 推荐 | 关键论据 | 阻塞关系与落点 |
|---|---|---|---|---|
| DC-1 | `agent/status`/`agent/disposed` 白名单收编＋台账运行/终结态 | **做**（按 §1.6 语义） | 通道已通（events.ts:294-302 预留）；插件零改动；堵「长 turn 被闲置钟误拦」 | 实施票 A（daemon：dsh_receive.go:108 白名单＋SessionState 字段＋gate.go:316-327 加 dsh 道） |
| DC-2 | dsh 机器等机器豁免道加不加 | **加**（随 DC-1 一并） | 不加的后果：enforce 档长跑 dsh 会话被真拦、observe 档警告噪音；加了失效也只回正常路径（I4，gate.go:313-315 哲学） | 同票 A |
| DC-3 | 停车语义不移植 dsh | **确认不移植** | §2.2：dsh 无 async 停车标的，运行态即等待态 | 重议触发条件＝钉死 dsh「主 idle＋后台任务在跑」形态（schedule/jobs 面未读）；无需落票 |
| DC-4 | 问询窗 dsh 一期开关 | **off** | §2.3：①②检测缺口未钉、经济性未证、问询窗是增强非止血 | 若开：实施票 B（插件 user/message 转发＋daemon dsh 形提问潮判定＋dsh 提问工具清单钉源码）＋排程扩大 |
| DC-5 | beat 头形状分流（非 UUID36 键改发 dsh 头） | **做**；与「dsh 心跳排程」票合票落（推荐）或先行落（零调用者、零 CC 回归） | §3：两改动点 httpsender.go:112 / appendreplay.go:104；头是 dsh 唯一归因面（体无键，snapshot.go:249-256） | **实施另立票，受本设计结论与本表阻塞链约束**；且受接法乙路由决策阻塞（快照总闸，§3.4①）；dock 形状分类器导出随票 |
| DC-6 | 悬空道对 dsh 的白读开销 | **顺手做**：gate 对 agent="dsh" 跳过 `HasDanglingToolUse` | §1.5 末段：形不匹配恒 False（dshtrans.go:132 封套 vs cctrans.go:216 顶层 message），每次 dsh 问闸白读 256KB 尾窗（cctrans.go:23） | 可随票 A 顺手；不阻塞任何件 |

拍板最小集：DC-1/DC-2（一个实施票即可兑现判活定案）＋ DC-5（与接法乙路由决策串行）。DC-3/DC-4/DC-6 可勾选确认，不产生紧迫工作量。

## 5. 本票交付物声明

- 交付物＝本文档一个文件（`.scratch/dsh-phase2-finish/p25-design.md`）；**零代码改动、零测试变更**（符合 decision_refs D2、proposal rev1 第 4 件口径、票面「本票不改代码」）。
- 验收对照：四节齐全（§1 判活重选／§2 等待窗口与问询窗适配／§3 beat 头决策点含两处改动点定位／§4 决策点清单）；判活推荐组合含不变量表述（§1.5 判定表＋I1-I4）；每条主张带 file:line；自包含（§0 背景速览＋全文不依赖会话记忆）。
