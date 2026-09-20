# 01 · 子代理用量入账：守望分流＋harvest 父域复合键＋usage 白名单标记字段

## What to build

1. **守望分流**（internal/daemon/watcher.go pollCC）：现 `hasPathPart(p, "subagents")` 整段跳过 → 改为分流——subagents 路径的文件**继续跳过** Ledger.Touch / maybeEnqueue / 问询守望 / 心跳排程（摆渡与闸门语义零变动，T32/T48 既有语义只消费不改变），**新增**只喂 usage 采集一条路。
2. **采集**（internal/harvest）：子代理转录与主转录同构（assistant.message.usage 官方四列；行内自带 `sessionId`＝父会话、`agentId`、`isSidechain:true`、`cwd`——实测样例 `~/.claude/projects/C--Users-allan716-ferryman-probe-t32/<父sid>/subagents/agent-<agentId>.jsonl`）。扩展采集：**父 sid 从行内 sessionId 取，勿只靠路径推导**；出行沿既有 title/project/cwd 语义。状态键由 `(agent, stem)` 扩为父域复合键 `(agent, 父sid, stem)`——子代理文件名 `agent-<hex>` 实测全局唯一，父域键是防御性投资且使恢复语义自明（原键假设"监视树下文件名唯一"，子代理文件名不满足该假设的表述要同步更正注释）。
3. **入账形状**（internal/accounts 白名单 + watcher 记账点）：子代理 usage 行 `session_id`=父 sid、`lineage_id`=NormPath(父转录路径)；白名单追加最小标记字段，**值=文件 stem（`agent-<agentId>`，完整文件名去扩展名）**——账本行自身即携带恢复所需的全部键成分；**主会话行该字段写空串**（白名单"必填"语义不变，参照 beat.lane 的 Go 版追加先例）。**父转录路径推导（rev1·F4）**：从子代理文件自身路径剥离尾段 `/<父sid>/subagents/<stem>` 得同目录父转录文件 `projects/<munged>/<父sid>.jsonl`；与台账 TranscriptPath 交叉校验——**台账有值时以台账值为准**，不一致记一行可诊断日志（不阻断入账），台账无值用剥离结果。
4. **断点恢复**（internal/harvest NewHarvestState）：**首见语义=从 offset 0 全量回填，与主转录既有语义一致**（不跳尾、不设截断点）；**恢复键零推导**：复合键三成分全部取自账本行内字段（agent 公共字段、session_id=父sid、标记字段=stem）；旧账本无标记字段的行按主会话键恢复（向后兼容）。
5. Codex 轨不动（无子代理转录结构）。

## 验收标准

- [ ] 两个会话各带同名形态的 agent 文件 → 采集状态不串扰（复合键测试）
- [ ] 子代理 usage 行形状：session_id/lineage_id 落父会话、标记字段=文件 stem、四列取自该子代理自己的 assistant 记录；主会话行标记字段为空串
- [ ] 父会话不在台账用例：从子代理路径剥离构造的 lineage 键 == 父转录后续 Touch 赋值的 lineage 键（台账有值时以台账值为准）
- [ ] 历史回填用例：已存在未采集的子代理文件首见即全量采集（offset 0），不跳尾
- [ ] daemon 重启后子代理采集偏移正确恢复（键成分全部取自行内字段，含"旧账本无标记字段"兼容路径）
- [ ] 守望其余面零变动：subagents 文件不 Touch、不入摆渡队、不参与闲置判定（既有 T32/T48 测试零回归）
- [ ] 隐私不变量：子代理行只含数字与标识字段，消息内容被白名单拒绝（沿用既有拒绝测试形态）
- [ ] `go test ./...` 全绿

## Blocked by

无。

## 涉及路径

- internal/daemon/watcher.go（pollCC 分流＋记账点）
- internal/harvest/（采集扩展＋复合键＋恢复）
- internal/accounts/accounts.go（usage 白名单追加标记字段）

## 副作用声明

- 成本口径断点：切换日起 usage 行含子代理（ADR-0008）——report/面板对比跨断点数据须知晓台阶跳。
- 独占验证命令：`go test ./internal/daemon/... ./internal/harvest/... ./internal/accounts/...`。

decision_refs: 2026-09-20 grill 会话 Q3(a)/Q6、ADR-0008（docs/adr/0008-subagent-usage-into-ledger.md）
review_blocks: 无
原型（呈现目标形态，非本票范围）: .scratch/webui-subagent-timeline/prototype.html 变体 D
