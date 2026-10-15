# 作答闸门借 PostToolUse `continue:false`

2026-10-15 立则：闸门新增第二入口——CC 的 AskUserQuestion 作答。挂 PostToolUse（matcher=`AskUserQuestion`），与打字闸门同一份台账判定（拦截阈值、有效交接、待交接标记、判热、三态、fail-open 全复用，不新增分支、不另设开关）；拦截＝输出 `continue:false`＋`stopReason`（原因与交接路径），答案（钩子输入 `tool_response.answers`，“问题→所选答案”）作为待续 prompt 保管、归还时随交接注入。放行分两形：daemon 带 `additional_context`（observe 档警告、待交接标记警告——闸门三态与待交接标记语义对作答入口同样生效）→ 经 PostToolUse 的 `additionalContext` 回写（CC 包成系统提醒带给模型，与打字闸门 warn 同机制）；纯放行与任何故障零输出（fail-open）。同一脚本 `ferryman-gate.ps1` 按 `hook_event_name` 分支。

起因（2026-10-15 本机日报系统案）：会话停在 AskUserQuestion 选择框过夜，睡醒作答即对凉缓存全量重付，全程无摆渡无拦截。两个洞：①悬空道把等人工具的悬空当"仍有工具在跑"，摆渡与闸门豁免永久推迟（问询守望默认 off、且只认问询潮，死线出口够不着）；②作答以 tool_result 回送模型，不经 UserPromptSubmit，打字闸门从未被问询。①由"等人工具"（AskUserQuestion、ExitPlanMode）排出悬空道修复；本篇管②。

## 依据：未文档化行为的实测（`.scratch/auq-hook-spike/`）

官方文档只写明 PostToolUse `continue:false` "stops processing entirely"，未写明挡住下一次模型请求。本机实测（CC，2026-10-15）：

- 作答不触发 UserPromptSubmit；序列为 PreToolUse → PermissionRequest（选择框弹出）→ 作答 → PostToolUse。
- PostToolUse 触发时 tool_result 尚未落盘（约 100ms 后写入），答案已在钩子输入 `tool_response.answers`。
- `continue:false` 后转录无 assistant 行直到用户再次输入——模型零调用。
- 其后原会话打字「强续」经 UserPromptSubmit 放行，模型同时看到答案 tool_result 与拦截提示——强续不需为作答另开通道。

**CC 改版即重测**：本篇成立依赖以上四条未文档化行为；CC 升级后作答闸门出现漏拦或卡死，先重跑该实测再查代码。

## 拦截回写不算会话内容

被拦后 CC 仍向转录追加带 timestamp 的一段：答案 tool_result、`attachment.type=hook_success`、`attachment.type=hook_stopped_continuation`（`hookName` 以 `PostToolUse:` 起头）、prompt_snapshot、`system.subtype=turn_duration`。若计入内容时钟，交接被判过期白重摆渡、闸门闲置钟归零——被拦后在原会话直接打普通话即冷缓存静默放行（与 2026-10-11 拦截回写案同类）。处置：以带 Ferryman 标记的 `hook_stopped_continuation` 为锚、按其 `toolUseID` 认出整段，内容时钟与覆盖截止一并跳过（`IsHookBlockNotice` 同族）。事实源只有转录，daemon 重启不丢。

## 考虑过的备选

- **只修摆渡、不拦作答**：交接按时就绪，但作答照样发往凉会话全额重付，用户撞上的恰是这一步。否。
- **daemon 内存挂"已拦"标记代替转录识别**：实现轻，但重启即丢，丢一次漏一次。否。
- **PermissionRequest 入口**：它在选择框弹出时触发（作答之前），拦不到作答这一刻。否。

## 边界

- 一期仅 CC 轨、仅 AskUserQuestion。ExitPlanMode 的作答（批准计划）只享受排出悬空道，作答闸门待补测 PostToolUse 时序、`continue:false` 效力与待续内容形状（计划正文可能很长）后再议。
- 工具权限确认框不归等人工具：转录上与工具运行中不可区分，维持悬空道原语义。
- codex/dsh 无对应的作答路径，不涉。
