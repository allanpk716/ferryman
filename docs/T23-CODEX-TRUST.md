# T23 · Codex 钩子信任指引（晨间操作手册）

> 夜间（2026-09-16）已备好一切代码侧工作：脚本、hooks.json、daemon cwd 提取修复、88 用例全绿。
> 剩下只有需要你亲手做的两步（TUI 信任 + 真机校准）。

## 已就绪

| 项 | 位置 |
|---|---|
| gate 钩子脚本 | `hooks/ferryman-gate-codex.ps1`（防御式字段映射 + fail-open + DEBUG 抓包） |
| restore 钩子脚本 | `hooks/ferryman-restore-codex.ps1`（source=clear/startup 才注入） |
| Codex 钩子注册 | `~/.codex/hooks.json`（仅 Ferryman 两条；写前备份 `hooks.json.pre-ferryman`） |
| daemon 侧修复 | codex `_enrich` 现从 rollout `session_meta` 首行提取 cwd——此前恒空导致 codex gate 永远无法 block（生产 bug，已修） |
| 测试 | `tests/test_hooks.py` T23 段 4 例真跑（block JSON 契约 / fail-open / source 过滤） |

## 你的两步

### ① TUI 信任（必须最先做）

Codex 哈希信任 = **配置静态**：信任后不要再改 `hooks.json`，改了就要重新信任。

1. 打开 Codex TUI，输入 `/hooks`
2. 信任 `ferryman-gate-codex.ps1` 与 `ferryman-restore-codex.ps1` 两条

### ② 真机校准 stdin schema（一次性）

脚本目前是防御式多字段回落（`rollout_path`/`transcript_path`/`path`、session_id 缺席时从文件名推导）。
信任后跑一次真实会话并抓包确认：

```powershell
# PowerShell 会话里：
$env:FERRYMAN_HOOK_DEBUG = "$HOME\ferryman\codex-hook-debug.log"
# 然后在 Codex 里随便发一条消息，再看：
Get-Content "$HOME\ferryman\codex-hook-debug.log"
```

若真实字段名与回落不符（例如叫 `rollout` 或 `thread_path`），把实际名字加进
`ferryman-gate-codex.ps1` 的回落链即可（改脚本不用重新信任——信任锚是 hooks.json 里的命令行）。

### ③ 验收（TEST_PLAN T23 原标准）

把 `~/ferryman/config.toml` 的 `codex_mode` 临时切 `"observe"` → 跑一个 Codex 会话闲置 5 分钟 →
发消息触发 → 比对被拦 turn 前后 rollout 文件**零新增**、被拦 turn 零 token。
完成后切回 `"off"`（E0b 数据积累后再上 enforce）。

## 顺带决策：Orca 的 Codex 钩子

`~/.codex/hooks.json.bak` 里有 Orca 全套（SessionStart/UserPromptSubmit/PreToolUse/
PermissionRequest/PostToolUse/Stop/SubagentStart/SubagentStop → codex-hook.cmd）——
当前文件在我动手前就已被清空（`{"hooks":{}}`），**不知是你主动清的还是工具误伤**，故未擅自恢复。
若要恢复：把 .bak 里的各事件条目合并进 hooks.json（Ferryman 条目保留），再重新走一遍 /hooks 信任。

另：这证明 **Codex 0.153 也有 SubagentStart/Stop 事件**——T32 的子代理计数将来接 Codex 时，
加一个 codex 版上报脚本即可（daemon 侧 `/subagent` 端点已就绪，agent 字段已支持）。
