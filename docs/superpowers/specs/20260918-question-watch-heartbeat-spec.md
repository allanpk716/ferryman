# Spec · T51 问询守望心跳（question-watch heartbeat）

- **日期**：2026-09-18（夜链内联 to-spec 产出）
- **上游**：`docs/superpowers/specs/20260918-question-watch-heartbeat-consensus.rev1.md`（含文末两轮评审附录，10 条 round-1 补漏的裁定已并入本 spec 决策）
- **词汇表**：CONTEXT.md（本 spec 新词：心跳、问询潮、等答复窗口——随文档票补入）

## Problem Statement

拷问式讨论技能（一条 assistant 消息抛 5~10 个问题）迫使用户组织 10~40 分钟的长答案，远超服务商缓存 TTL（GLM 实测约 10 分钟）：答案提交时缓存已死、全量重付输入费；且闲置钟照走，第 25 分钟摆渡白做交接、第 35 分钟闸门直接拦截提交。守护进程目前无法区分"真没人了"与"人在憋长答案"。

## Solution（用户视角）

AI 回复被识别为提问潮（≥5 个独立问题单元）且会话在等用户作答时，该会话进入"等答复窗口"：缓存由提前排定的 1~2 次心跳保温（默认把可靠命中窗从 ~10 分钟延到 ~24 分钟）；摆渡推迟但有死线（最迟拦截阈值前 8 分钟强制执行，失败降级骨架交接）；用户一旦提交，剩余心跳立即取消、一切恢复正常调度。功能默认关闭，先以 observe 模式（只记录不真发请求）跑数据，人工确认后才升 enforce。闸门语义一字不动。

## User Stories

1. 作为拷问式讨论的用户，我想要提问潮后我的会话缓存被自动保温，以便慢慢作答、提交时仍按缓存读价计费。
2. 作为成本管理者，我想要每次心跳（含 observe 演练）都落台账事件与费用、`/stats` 可见计数与累计花费、并有一键停，以便一眼看到状态与花费、随时停。
3. 作为成本管理者，我想要 beat 结果分"命中 / MISS / ERROR"三道计数、连续 MISS 自动熔断降级、连续 ERROR 自动暂停，以便代理故障或服务端缓存策略漂移时不烧钱。
4. 作为守护进程运维者，我想要检测/窗口/调度/记账的一切异常都被吞掉且不影响守望与闸门主路径，以便服务持续可用。
5. 作为 CC 用户，我想要提问潮落盘后摆渡推迟但赶在闸门前必有交接（或骨架），以便超时被拦时交接已就绪、引导开新会话。
6. 作为需求方，我想要 observe 命中清单（含证据形态）与漏检对照数据攒够后再人工拨 enforce，以便放行基于证据而非假设。

## Implementation Decisions

**模块边界**（四个新件＋三处既有件接线，全部挂在守望轮询循环内，无新线程模型）：

1. **检测器（question detector）**：纯函数，输入转录文件路径（读尾部），输出 `Verdict{is_surge, unit_count, breakdown{marker_lines, qmark_lines, qualified_numbered_lines}, askuserquestion_dangling: bool}`。判据（rev1 D2 紧档，实验定型）：剥代码块后计数——标记行（`❓`、`**Qn**`）计入；问号行计入；编号/列表行**仅当行内含问号或疑问词**计入；`unit_count ≥ min_questions`（默认 5）为提问潮。**隐私不变量**：只出计数，永不返回或落盘消息内容。
2. **命中谓词（四条件，全真才开窗）**：①末条 assistant 消息提问潮；②悬空 tool_use 集 ⊆ {AskUserQuestion}（该工具的悬空＝正在等用户作答，恰是靶场景，不阻止开窗；共享的悬空判定函数不改动，摆渡推迟路径按"在跑"处理不变）；③子代理在飞计数 = 0；④前缀 ≥ `thresholds.min_ctx_tokens`（引用既有配置，不另设常数）。
3. **等答复窗口（ledger 新态）**：`SessionState` 增窗口字段（opened_ts、beats_fired、plan）；开窗＝命中谓词全真且**异步等待窗口（停车窗）未开启**——两窗口互斥、先开者赢；关窗＝该会话任何新写入（用户提交即触发）；关窗后末条又是提问潮 → 重开新窗（重新计时）。**摆渡推迟＋死线**：窗口开启期间摆渡入队推迟，最迟 `block_s − ferry_deadline_lead_s`（默认 480s，load 时夹取 `[60, block_s − summarize_s]` 且强制 `summarize_s + lead_s ≤ block_s`，违例拒绝配置并告警）强制入队；摆渡模型失败/超时走既有骨架交接降级——"被拦 ⇒ 交接必已存在"不变量与既有路径同水位。闸门语义不动。
4. **心跳调度器（scheduler）**：开窗即排计划（`max_beats` 跳、每跳间隔 `beat_interval_s`，默认 2×420s＝0.7×实测 TTL，3 分钟安全余量；口径统一 0.7×，禁写 0.8×）；**不立刻跳**（末条落盘本身已刷新缓存）。每跳发送前**两道验**：①计划开始后无新写入；②重新 stat 转录 mtime+size 与快照一致。调度器与台账读取共享 RLock（check-then-act 残余窗口压缩到毫秒级，命中记事件备查）。**全局同时最多 1 个 beat 在途**（跨会话串行队列）；429/5xx/超时指数退避重试 1 次，再失败放弃本跳记 ERROR。任何新写入 → 未发心跳全部作废（守望轮询 3s 兜底取消延迟）。
5. **beat 请求形状（enforce 才生效；observe 全程不发）**：直打本地代理 `127.0.0.1:15721`（cc-switch，不持真钥）；**发 CC 别名**（反查 settings.json `ANTHROPIC_DEFAULT_*_MODEL` 或 cc-switch DB，禁用 jsonl 回显上游名——必 miss）；重放前缀 `[system + tools + u1…uN]`（不含末轮 assistant 输出）；**等价性约束只作用于被缓存前缀字段**（system/tools/messages/cache_control），采样参数不在等价范围；**`max_tokens=1` 封顶输出成本**（前缀缓存命中与采样参数无关；代理参数白名单是否放行 max_tokens 列入 Q14 段二验证项）；**不与摆渡路由共用任何路径**。BeatBuilder（jsonl → 前缀重构）与逐字段 diff 属 Q14 段二范畴，本 spec 只落接口占位与回归测试挂点。
6. **beat 结果三态与熔断**：`HIT`（cacheRead ratio ≥ 阈值）/ `MISS`（请求成功但 cache_read≈0）/ `ERROR`（代理不通/429/5xx/超时，重试后仍失败）。**连续 2 次 MISS → 自动降级 enforce→observe ＋告警**（人工重新拨回）；**连续 3 次 ERROR → 暂停当前窗口剩余心跳＋告警**（不降级模式）。三道计数独立入账。
7. **记账与遥测**：每跳（含 observe）逐条记入既有费用账本（时间/会话/token/实收费用/三态）；cacheRead 即线上 TTL 遥测，突变记异常（沿用既有心跳设计文档 §3.5 口径）。账本数据不反推跳数（记归记、算归算——需求方决定）。
8. **事件与证据形态**：命中/开窗/每跳/关窗全部为台账事件（viewer 时间线可显）；**复核证据形态**（enforce 条件③的清单字段）：`session_id ＋ 命中时间 ＋ unit_count ＋ breakdown ＋ 转录文件绝对路径`，viewer 提供跳转/复制路径，不落消息内容。`/stats` 增问询守望计数器（命中数/跳数/三态/累计花费）＋一键停（改 `mode` 配置）。
9. **配置**（独立节，默认 off）：
   ```toml
   [question_watch]
   mode = "off"                  # off | observe | enforce
   min_questions = 5
   beat_interval_s = 420         # 0.7×GLM 实测 TTL 600s
   max_beats = 2
   ferry_deadline_lead_s = 480   # 夹取 [60, block_s − summarize_s]，且 summarize_s+lead ≤ block_s
   ```
10. **漏检观测（observe 期指标）**：命中事件与用量账本对照，统计"末条疑似提问未触发＋随后全量重付"作为粗粒度漏检信号，供阈值回调（漏得多往 4 调、误报多往 6 调）与 enforce 条件③复核。

**放行纪律**：三态开关永不自动升级；升 enforce 三条件——observe 跑满一周或 ≥10 次命中、Q14 段二（BeatBuilder 逐字段 diff 全等，范围＝前缀字段）＋段三（3 臂实跳，首跳 ratio ≥0.85×3）通过且请求形状锁定回归测试、需求方按证据形态人工复核点头。连续 MISS 熔断是安全降级，不算升级。

**文档义务（随代码票落）**：ADR——推翻 DESIGN.md §9"不保活"红线（红线原意针对无条件全局 keepalive，本功能条件化/会话级/短窗）＋D6 闲置钟取舍；CONTEXT.md 补词：心跳（beat）、问询潮、等答复窗口、"机器等人"泳道；DESIGN.md §9 红线改写为"不做无条件全局保活；条件化会话级保活见 ADR"。

## Testing Decisions

- **检测器单测**：已知正例语料（真实提问潮消息 fixture，units=8/7 两例来自本会话）触发；反例语料（状态汇报/票台账/步骤清单——round-0 实验 153 条误报的形态缩影）不触发；代码块内问号剥离；一次一问不触发；AskUserQuestion 悬空场景豁免。
- **谓词单测**：四条件真值表；悬空集含非 AskUserQuestion 工具 → 不开窗。
- **窗口单测**：与停车窗互斥（先开者赢）；关窗重开；摆渡推迟与死线（自然闲置触发、死线强制入队、骨架降级）；死线夹取校验（非法配置拒绝）。
- **调度器单测**：无立刻跳；两道验（预检后文件 mtime 变 → 不发）；全局串行（双窗同刻只有 1 个在途）；observe 模式只出事件不出网（fake sender）；新写入取消剩余跳；429 重试 1 次后记 ERROR。
- **熔断单测**：连续 2 MISS 降级；ERROR 不计入 MISS；连续 3 ERROR 暂停窗口。
- **集成（Harness，沿用既有风格）**：写提问潮 jsonl → 自然闲置 → 开窗 → observe 心跳事件落台账 → 再写入 → 关窗＋取消；对照会话（非提问潮）不产生事件，证明守望在跑。
- **回归挂点**：beat 请求前缀字段形状锁定测试（enforce 门槛件，Q14 段二产物接入前以占位 skip 标记）。
- 全量测试绿是每个 commit 的前置条件（rebase 后基线 204 用例）。

## Out of Scope

- enforce 放行与真实心跳发送（Q14 段二/三执行＋人工拨开关，均另行批准）；
- Codex 轨（原话痛点之一"使用 GPT"暂缓——**部分覆盖，晨间向需求方确认**；信号质量探路列 backlog）；
- Q14 实验本身的执行；T50 心跳复盘；viewer 改版（仅事件接入）。

## Further Notes

**开发时要盯**（撞上即停下反馈，别默默绕过）：
1. CC 改版改掉拷问式技能输出格式或 AskUserQuestion 语义 → 判据失准、豁免名单失效——observe 数据异常要反馈；
2. 代理 15721 参数白名单若不放行 `max_tokens=1` → 输出封顶失效，Q14 段二会暴露，届时改用 stop_sequence 方案；
3. "跳必命中"是显式假设（TTL 方差未建模，单跳 MISS＝全前缀输入价），D10 遥测承担证伪——连续 miss 数据出来前不升 enforce；
4. 实施第一步必须是拉平 main（T41–T48：费用账本/用量落库/viewer/停车窗状态机都在 main，本 worktree 停在 T40 缺它们）；
5. `.scratch/` 票目录须在版本库（gitignore 守卫），worktree 才取得到。
