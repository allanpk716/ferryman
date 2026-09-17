# T23 · Codex 钩子验收（2026-09-17 自主验收定案）

> 我（Claude）自己启动 codex exec 做了全链路验收。结论：**代码侧全部就绪并实机验证**，
> 剩余唯一一步（TUI 信任 + 发一条消息）只能你来——原因见下。

## 验收中发现并已修复的三个真实缺陷

| # | 缺陷 | 修复 |
|---|---|---|
| 1 | **`[features] hooks = true` 旗标缺失**——Codex 钩子默认关闭，不开旗标则 hooks.json 被静默忽略（此前钩子"从没生效"的头号根因） | config.toml 已开；`install-codex` 自动确保 |
| 2 | **控制字符事故**——手工写入把路径 `\a`（agent）/`\f`（ferryman）写成 BEL(0x07)/FF(0x0C)：hooks.json 指向不存在路径、两个 codex 脚本 token 路径非法 → 全部 fail-open 静默死亡 | 已清洗；全部经 `json.dumps`/heredoc 重写；install-codex 测试含控制字符回归 |
| 3 | **additionalContext 用了 CC 的 hookSpecificOutput 包装**——Codex 契约是顶层字段 | gate-codex.ps1 已改顶层 additionalContext |

## 实测定案的事实

- **stdin schema**（官方文档校准，防御式映射全部命中）：`session_id`、`transcript_path`
  （= rollout 路径，可为 null）、`cwd`、`prompt`、`hook_event_name`、`model`；SessionStart
  另有 `source` ∈ startup|resume|clear|compact（脚本内过滤，等价于 matcher）。
- **响应契约**：`decision:"block"` + `reason` 拦截；顶层 `additionalContext` 注入警告。
- **`codex exec` 不派发钩子是上游已知 bug**（openai/codex#26452 → #26383，0.137–0.153
  未修，含 `--dangerously-bypass-hook-trust` 也不派发）——自动化验收到此为止，
  **TUI 主线路径不受影响**（Orca 即经此路径工作）。
- Codex 净化钩子环境变量 → 抓包改**标记文件开关**：`~/ferryman/hook-debug/ON` 存在时
  两个 codex 钨子把 payload 写到同目录 `ferryman-*-codex.jsonl`（**当前已开着**，校准
  完可删 ON）。
- Codex 0.153 有 SubagentStart/SubagentStop 事件（Orca 全套即证）——T32 子代理计数
  将来接 Codex 只需一个上报脚本，daemon `/subagent` 端点已就绪。
- Orca 的 Codex 全套钩子已按你 CC 侧"一并注入"的决策合并回 hooks.json（orca 8 +
  ferryman 2 共存，实机验证）。

## 已实机验证（我做的部分）

- 手动调用 gate-codex.ps1：payload 抓包 ✓、POST /gate ✓、daemon /stats 出现
  `codex: 1` gate 调用 ✓（FF 修复后全链路通）
- install-codex 在真机落盘：orca 8 + ferryman 2、无控制字符、旗标恰好 1 次（幂等）
- `codex exec -s read-only` ×4：确认钩子生命周期打印与 exec 不派发行为

## 剩余唯一一步（你）——已完成 ✅（2026-09-17 10:26）

TUI 信任 + 发送 "hi" 完成。真实 payload 比对结果：

- **schema 全命中**：`session_id` / `transcript_path`（真实字段名，回落链第二级）/
  `cwd` / `prompt` / `hook_event_name` / `source:"startup"`；另有 `turn_id`、`model`、
  `permission_mode` 扩展字段（不影响映射）。
- daemon `/stats` 出现 `codex: 2` 真实 gate 调用——**TUI 全链路正式通车**。
- **新发现并已修**：经 Orca 启动的 codex 把 CODEX_HOME 重定向到
  `%APPDATA%\orca\codex-runtime-home\home\sessions`（真实 transcript_path 即在此）——
  守望新增多目录支持（`codex_extra_dirs` 配置 + Orca 目录自动发现），否则这些会话
  gate 能收到但永远不被摆渡。已重启生效。
- 抓包标记与含 prompt 内容的抓包文件已清理（隐私）。

## 验收（TEST_PLAN T23 原标准，TUI 信任后）

`~/ferryman/config.toml` 的 `codex_mode` 临时切 `"observe"` → 跑一个 Codex 会话闲置
→ 发消息 → 比对被拦 turn 前后 rollout 零新增、观察警告注入（顶层 additionalContext）。
完成后按 E0b 数据决定 observe/enforce。

## 运维命令

```bash
uv run ferryman install-codex   # 注入/修复 hooks.json + 确保旗标（幂等、备份）
```
