# 子代理用量四列随父会话入账：harvest 从"整段排除"改为"只跳摆渡、不跳采数"

（编号取 0008：0006/0007 已被心跳侧决策引用占位、文件未落。）

usage 采集（设计 §3.7，规格 ferryman/harvest.py → internal/harvest）v1 的范围声明是"仅 CC 主会话（subagents 转录由守望 glob 层排除）"；守望层 `hasPathPart(p, "subagents")` 一跳（watcher.go pollCC）把台账 Touch、摆渡入队与 usage 采集一并挡在门外。排除的动机是**摆渡语义**——T32 注释："子代理转录不是独立会话，不摆渡"；usage 采集被同一跳过顺带排除是实施捷径，不是独立决策。事实面：CC 子代理转录位于 `~/.claude/projects/<munged>/<父sid>/subagents/agent-<agentId>.jsonl`，记录与主转录同构（assistant.message.usage 官方四列；行内自带 `sessionId`＝父会话、`agentId`、`isSidechain:true`、`cwd`，父会话关联不依赖路径推导）；e0c 实验实测子代理吃掉会话 output tokens 的 47%（均值；重度会话更高）。CONTEXT 既有概念「会话总账」＝主转录四列加总＋各子代理转录四列加总——实现缺口使 webui 列表/时序页与 report 对重度子代理会话的 tokens 系统性低估，列表页 tokens 长期虚低近半。

决定：把"不摆渡"与"不采数"拆开——子代理转录纳入 usage 采集，随**父会话**入账（session_id＝父 sid、lineage_id＝父转录路径的归一键、usage 白名单追加最小标记字段携带 agentId；主会话行该字段写空串，白名单"必填"语义不变，参照 beat.lane 的 Go 版追加先例）；守望对 subagents 路径的其余各面（台账 Touch、摆渡入队、闲置判定、问询/心跳）维持跳过不变；仅 CC 轨，Codex 无此转录结构。隐私铁律一字不动：只记数字与 agentId，永不落消息内容（白名单拒内容字段的既有防线沿用）。harvest 状态键由 (agent, stem) 扩为父域复合键——子代理文件名 `agent-<hex>` 实测全局唯一，父域键是防御性投资且使断点恢复语义自明。后果（如实声明）：账本 append-only，切换日起 usage 行含子代理——成本口径跨切换日有台阶跳，report/面板对比旧数据须知晓此断点；「会话文件 30 天清理后 usage 流水仍是审计地基」的设计意图由此才真正覆盖子代理（此前子代理用量随转录清理永久丢失，想补账也无地基——这也是否决"报表层事后聚合"的根本理由）。

## Considered Options

- **维持 v1 排除**：47% output 持续不入账，「会话总账」概念空转，webui 永远看不到子代理——否决（本次面板改进的直接动因）。
- **子代理按独立 lineage 入账**：违背「子代理转录不是独立会话」的既有语义，且列表页会多出一批无主行——正是本次要修的显示痛点——否决。
- **报表层事后聚合、不动逐行账**：会话文件 30 天清理，账本是唯一长存事实源；事后聚合在清理后无地基——否决。
- **只改 UI 不入账**（窗口 close_reason 已有 subagents_done 痕迹）：无数据可标，空转——否决。
