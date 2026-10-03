# AGENTS.md — AI agent 调用 Ferryman 的操作契约

面向 AI agent（Claude Code / Codex / pi 等）的自包含使用契约：不读源码即可正确调用。
逐命令的旗标、退出码明细与 `--json` 字段表见 [docs/CLI.md](docs/CLI.md)；术语见
[CONTEXT.md](CONTEXT.md)。

## Ferryman 是什么

本机常驻守护：盯 Claude Code / Codex 的会话文件，会话闲置缓存将失效时自动生成"交接 MD"、
闸门拦截凉会话、新会话自动注入交接——避免为死缓存全量重付。四个动作：
守望（watch）→ 摆渡（ferry）→ 闸门（gate）→ 归还（restore）。

## 何时用哪条命令

| 需求 | 命令 |
|---|---|
| 全面体检（钩子在位/脚本健康/快照覆盖/守护活性/供应商生效链） | `ferryman doctor`（机器读加 `--json`） |
| 快速探活（守护/版本/渡口/台账摘要） | `ferryman status` |
| 停守护（优雅停，绝不硬杀） | `ferryman stop` |
| 看供应商表（含 codex/pi 可用性，密钥脱敏） | `ferryman provider list`（机器读加 `--json`） |
| 切换活跃供应商 | `ferryman provider switch <名>`（热切换：不重启、在跑会话不断流） |
| 增删/导入供应商条目 | `ferryman provider add` / `remove` / `import-ccswitch` |
| 编辑器配置指向/还原渡口 | `ferryman provider apply`（还原加 `--restore`） |
| 账单报表（token 四列/成效账） | `ferryman account report`（机器读加 `--json`） |
| 等待窗扫参（离线反事实重放） | `ferryman backtest` |
| 调参建议面板（只读） | `ferryman tuning status` |
| 查版本 | `ferryman version` |
| 升级检查（只读，不动手） | `ferryman update --check` |

## 输出怎么读

- 文本面 = 人话逐行；`--json` 面 = 单行 JSON 到 stdout，诊断与空态标注走 stderr。
- `doctor --json`：`ok` = 总判定（零 fail）；`checks[].status` ∈ `pass`/`fail`/`not_checked`。
- `upstream list --json` / `provider list --json`：`api_key` 恒为尾 4 位掩码、空钥 = 空串
  （状态看 `key_status`）——输出无明文密钥，可直接进日志或贴给用户。
- `backtest --json`：stdout 恒纯 JSON；数据空态（引擎未产出/零窗）的标注走 stderr。

## 错误处理（退出码）

- 0 = 成功；1 = 失败（真错误：config/账本读不到、写盘失败、doctor 有 fail 项等）；
  2 = 用法错（未知子命令/未知旗标/多余参数——改参数重试，不是环境问题）。
- `status` 恒 0：不在线也如实报告，0 不代表"守护在线"，看输出文本。
- `backtest` / `tuning sweep` 数据空态退 0，输出明示空态原因——空态不是失败。
- `stop` 对"本就不在线"也退 0（无需停止）。
- `doctor` 与 `install-ccswitch` 零参数契约：任何参数（含 `-h`）退 2 且绝不执行。

## 硬边界

- `ferryman update`（无 `--check`）会停守护换 exe：仅当用户明确要求升级时执行；
  日常只跑 `update --check`（只读）。升级由人触发，agent 面永不自行升级。
- `ferryman stop` 仅当用户明确要求停止守护时执行。
- 编辑器宿主配置（`~/.claude/settings.json`、`~/.codex/config.toml` 等）由
  `provider apply` 统一接管写入（带备份）——不手改这些文件来指渡口。
- 守护 HTTP 端点不裸敲：探活走 `status`、停止走 `stop`，端口与 token 是实现细节。
- 闸门状态查询走 MCP `gate_check`；档位调整（enforce→observe/off）由人手改配置，
  不经 agent。
- 密钥只落本机 config：`provider add` 优先用 `--key-env`（不进 shell 历史）；
  任何命令输出都不含明文密钥。

## MCP 工具面（六件只读）

`ferryman install-mcp` 注册进 CC 用户级配置后，agent 经 MCP 使用以下六件工具（全部
只读：一切读经 daemon 端点，MCP 进程永不直读台账/账本/交接库文件）：

| 工具 | 用途 |
|---|---|
| `sessions` | 台账清单（闲置状态与凉热判定） |
| `session_detail` | 单会话总账 + 最近有效交接覆盖 |
| `gate_check` | 闸门判定只读推演（allow/warn/block 与剩余分钟） |
| `heartbeat_status` | 等待窗/问询窗清单 + 心跳遥测 |
| `doctor` | 体检（与 `ferryman doctor --json` 同源） |
| `cost_report` | 账本报表（token 四列 + 成效账） |

## 配置事实源

配置 = `~/ferryman/config.toml`（或 `FERRYMAN_CONFIG` / `--config` 指路）。字段全集与
语义以 [config.example.toml](config.example.toml) 为准；改配置重启守护生效。供应商条目
日常增删改走 `provider` 族命令，不手编 config。
