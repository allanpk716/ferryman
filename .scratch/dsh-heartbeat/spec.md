# dsh 自动保温（心跳保温全套）· 实施规格

> 来源：xcheck 夜链 20261003-174658（评审收敛 1 轮修订；object=20261003-183301/proposal.md rev1）。
> 术语遵守仓库 CONTEXT.md；相关 ADR-0007（字节级重放）、ADR-0008（并发帽）、ADR-0013（内容时钟）、ADR-0015/0016（同模型摆渡，本案只读背景）。

## Problem Statement

dsh（DeepSeek Harness，手机/桌面双端）会话每轮都要重发全部历史。用户用完走开后，上游（智谱 GLM）的提示缓存几十分钟内凉透，回来续聊时整个前缀按全价重付。渡口接法乙（v0.7.1）已让 dsh 流量带会话键经渡口、快照原料在积累，但守护进程没有任何 dsh 心跳机制：不知道会话"在跑还是等答复"，即使跳了保温也无法归因记账。同时闸门距 enforce 只差拍板，而长任务中段无落盘的 dsh 会话会被闲置钟误判——判活地基是 enforce 的前置。

## Solution

给 dsh 补齐与 CC 问询守望同构的心跳保温全套，四件：①判活地基（收编 dsh 的"在跑/已终结"结构化信号，闸门豁免）；②心跳头分流（dsh 会话的心跳发 dsh 归因头，渡口才能记账）；③dsh 等答复窗（读会话文件判"最后说话人是 dsh"，开窗保温；关窗只认用户侧事件或到期，机器侧写入不误关；独立分档开关，生产先零成本演练）；④上线与观察（发版/换装/闪测/指标看判据——全部人的动作，不属夜链实施）。

## User Stories

1. 作为 dsh 用户，我在手机上问完走开、稍后回来续聊时，会话前缀大部分按缓存价（约一折）计费，以便小会话也值得保温。
2. 作为 dsh 用户，我的会话在跑长任务（中段长时间无落盘）时，不会被闲置告警误伤、enforce 后不会被误拦。
3. 作为运维者，我能用一条配置（`[question_watch] dsh_mode`）控制 dsh 心跳三态（关/演练/真发），真发钱之前先看零成本演练数据，升档由我拍板。
4. 作为运维者，dsh 心跳任何故障（快照坏、上游错）只停 dsh 侧，不连累 CC 的任何行为（CC 全链零回归）。
5. 作为维护者，账本里 dsh 心跳行（beat/qwatch_open/qwatch_close）按会话键归因、四列齐全，我能据此算"用户回访关占比"等升档指标。

## Implementation Decisions

### 判活（票 A）

- 事件白名单收编 `agent/status`、`agent/disposed`（插件已送达，今日被收窄 unknown-event；插件零改动）。
- `agent/status`：`status` 只认 `running`/`idle` 闭集；主会话（parent 空）running → Touch 活动登记＋置运行态；idle → 清运行态；不入账。子会话直报（parent 非空）不 Touch 父，运行态记子键。
- 运行态时间戳形（SessionState 新字段，内存态）：**刷新规则**＝同键任意已白名单活动事件（turn/start、assistant/message、agent/status=running）推进时间戳——fed 会话走事件口、未接管会话走文件面检测态推进；idle 显式复位优先于刷新；失效上界 3600s（有界失效，回正常闸门路径；与 CC 悬空道同哲学）。
- 族系在跑＝父运行态 OR 任一已知子键运行态；已知子键来源＝台账 usage 行 subagent 列＋fed 表回种（mechanics 实施票钉）。
- `agent/disposed`：不 Touch；置终结态；后续 agent/created(resume) 或新 running 清除。
- 闸门豁免：machineWaiting 加 dsh 道（读台账运行态＋族系＋失效上界）；对 agent=dsh 跳过悬空 tool_use 检测（对 dsh 事件封套恒 False 的白读）。

### 心跳头分流（票 B）

- dock 单源导出会话键形状分类器（UUID36 ↔ `session-` 前缀 dsh 形）；禁止 beat 包复刻第二份形状规则。
- 心跳两发送点（快照重放 Send、追加重放 SendAppendReplay）：dsh 形键 → 发 `x-deepseek-harness-session-id`；**其余（含一切非 dsh 形）一律维持现状发 CC 头**。两形互斥 ⇒ CC 零回归。

### dsh 等答复窗（票 C）

- **检测态**：dshtrans 尾读加"最后说话人"累积态（同 title 跨 chunk 携带）；`turn/start`/`user/message`＝用户侧（协议钉死 turn/start＝认领排队输入；user/message 含合成注入——注入回合也算用户侧，无害：注入即活动、缓存刚被使用）；`assistant/message`＝dsh 侧（不要求带 usage）。**dsh 后说**＝候选。
- **接入点**：pollDsh 在 Touch＋观察窗之后、fed 早退之前跑检测（fed 会话是常态；fed 会话维护检测专用偏移，与 harvest 偏移分离）。
- **开窗**四条件：dsh 后说 ∧ 运行态不在效（票 A）∧ 未终结 ∧ peak ≥ MinCtxTokens；版本章缓存防重复判定；开窗临界区与 CC 同款（计划＝beatPlan 全局参数 420s×2、窗口快照记开窗基线）。
- **关窗/取消跳（dsh 专用语义，CC 分道）**：只由**用户侧事件到达**（最后说话人翻转用户侧；close_reason=user-write）**或 block_s 到期**（close_reason=deadline，兜永不回话会话＋Pin 时限）触发；机器侧写入（session/title、request/header、compaction、turn/end、subagent/catalog 等）只更新检测态与台账，不清窗不取消跳。实现载体（touch 清窗旁路 or 独立 dsh 窗字段）实施票定，**语义以本条为准**。
- **取消跳判据（并列两验，任一不满足即取消）**：①窗口仍开着（关窗即作废整计划）②检测态未翻转用户侧（开窗前提仍成立）。不复用 mtime/size 复验（机器侧写入会误伤）。
- **排程**：心跳调度入口 agent 检查放宽 cc→cc+dsh；模式按 agent 单源取值（`qwatchModeFor`）；全局单在途/串行节奏照旧。
- **分档开关**：`[question_watch] dsh_mode`（off|observe|enforce，默认 off）＋解析校验＋`/stats` 回显；dsh enforce＋渡口关 → 启动告警按 observe 对待；生产首启 observe。
- **熔断按 agent 隔离**：dsh 独立断路器实例；连续 MISS 降级只降 dsh_mode；连续 ERROR 停本窗剩余跳。
- **Pin 对账扩 dsh**：窗开 → Pin(会话键)；关窗（用户侧/到期）→ Unpin——窗口到期上界同时是 Pin 时限，防泄漏。
- **记账**：沿既有科目（beat/qwatch_open/qwatch_close），agent=dsh 自动；close_reason 新增取值 user-write|deadline（dsh 专属；CC 的 write 语义不变）；dsh close 不落 write。
- **参数相容断言**：`BeatIntervalS × MaxBeats < BlockS`（生产 420×2=840 < 2100 成立）写成 config 校验或测试断言，违反即报错/测试红。

### 上线（票 D——人的动作，不在夜链）

发版纪律全套→换装→config 加 `dsh_mode = "observe"`→重启→三层验收（doctor/真会话开窗关窗/enforce 闪测翻回）→观察期三指标（用户回访关占比、窗口时长分布、到期关占比）→阈值化升档判据→升档拍板。

## Testing Decisions

- 只测外部行为不测实现细节；同族先例＝daemon 的 watcher/windows/dsh_receive 测试、beat 的 httpsender/appendreplay 测试、dock 的 snapshot/rewrite 测试。
- 关键语义钉死：关窗二分（用户侧关 vs 机器侧写入不清窗）、到期关窗、取消跳两验、运行态刷新（含 >1h 失效边界）、族系判定、闸门豁免分支＋悬空跳过、头分流双向＋非两形键保守、开窗四条件＋版本章、observe 零费记账、分档三态、熔断隔离（dsh 故障不停 CC）、Pin 对账含到期 Unpin、参数相容断言。
- Windows 会话内测试纪律：测试产物落日志、不重跑；评审 subagent 禁跑测试；禁裸全仓 `go test ./...`（发版级落日志除外）。

## Out of Scope

- 等待窗泳道（机器等机器保温）扩 dsh——设计定案无标的；
- 停车（parking）语义移植 dsh；
- 摆渡（maybeEnqueue）dsh 接线（另立票）；
- dsh 插件任何改动（含 agent/created 白名单收编——见 Further Notes）；
- 发版、生产换装、生产配置、重启、enforce 闪测与升档（票 D，人的动作）；
- CC 三条泳道（问询/等待/同模型）与闸门 CC 行为的任何变化。

## Further Notes

- 评审留档 F4（子键登记时序盲区）：agent/created 收编与否待实施票真机核验"父委派期间父是否保持 running"后定，本规格不预设。
- F5 证据（turn/start＝用户侧）入档：克隆 types.ts:283-288＋真机序列；合成注入注记见上。
- D3 三取舍（扳机选 C、observe 先行、熔断隔离）按"未推翻即按方案"执行——实施物与文档不得表述为用户逐项确认；enforce 真发与发版上线均为人的拍板。
- 并行观察事项（非本案）：V3 dsh 缓存 A/B（接法乙后 cache_read 比例）；M3（dsh 升级后 settings 层叠复验）。
- 机器账：`.xcheck/20261003-174658/`（round0）与 `.xcheck/20261003-183301/`（round1 复审+FINDINGS）。
