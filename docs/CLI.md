# Ferryman CLI 参考

面向 AI agent 与脚本的命令面契约：全部子命令 × 旗标 × 退出码 × `--json` 字段表 × 危险面。
事实源 = 盘面源码里的 usage 常量（`cmd/ferryman/`）；本文与行为不一致时以 `ferryman <命令> -h` 输出为准。
调用前先读 [AGENTS.md](../AGENTS.md)（何时用哪条）；术语见 [CONTEXT.md](../CONTEXT.md)。

## 通用契约

**退出码三态**（全部子命令统一）：

| 退出码 | 含义 |
|---|---|
| 0 | 成功（`backtest` 数据空态亦为 0——引擎未产出时报告照落、输出明示空态原因） |
| 1 | 失败（真错误：config/账本读不到、报告写盘失败、体检有 fail 项等） |
| 2 | 用法错（未知子命令 / 未知旗标 / 多余位置参数 / 旗标缺值——改参数重试，不是环境问题） |

**help 安全**：`-h` / `--help` 绝不触发真实动作。多数命令 `-h` 打印用法退 0；唯二例外是
`doctor` 与 `install-ccswitch`——零参数契约，任何参数（含 `-h`）都退 2 且绝不执行
（真跑会触真机配置，误触发代价高）。顶层用法：`ferryman help` 或 `ferryman -h`。

**`--json` 契约**：

- 单行 JSON 到 stdout；文本面照常走 stdout；诊断与空态标注走 stderr。
- `doctor --json`、`upstream list --json`、`provider list --json` 输出禁明文密钥——
  `api_key` 一律尾 4 位掩码（如 `****ab12`；长度 ≤4 的钥全遮为 `****`），空钥输出空串、
  状态看 `key_status` 字段。输出可直接进日志、贴给用户。
- `backtest --json` 特例：数据空态的标注走 stderr，stdout 恒为纯 JSON。

**配置解析优先级**（凡带 `--config` 的命令共用）：显式 `--config 路径` > 环境变量
`FERRYMAN_CONFIG` > `~/ferryman/config.toml`。配置字段全集与语义的事实源 =
[config.example.toml](../config.example.toml)。

**密钥纪律**：任何命令输出永不回显完整密钥；密钥只落本机 config。

**缺省端口**：守护管理口 127.0.0.1:15700、渡口 127.0.0.1:15722、面板 15900。

## 危险面（⚠ 动手前先读）

| 命令 | 影响 |
|---|---|
| `ferryman update`（无 `--check`） | ⚠ 停守护 → 换 exe → 重启；升级期间渡口/摆渡不可用，在途请求随停机中断 |
| `ferryman stop` | ⚠ 优雅停守护（渡口/闸门/摆渡全部停止；绝不硬杀，超时如实报告） |
| `ferryman upstream use` | ⚠ 中断在途请求（SSE/长请求随守护重启断开）；⚠ 已弃用——改用 `provider switch` |
| `ferryman provider apply` | ⚠ 改四份宿主配置（CC / codex / orca / pi 共五个文件；写前逐份备份） |
| `ferryman install-cc` / `install-ccswitch` / `install-codex` | ⚠ 写宿主配置（CC settings.json 与 cc-switch 快照 / codex config） |
| `ferryman install-mcp` | ⚠ 写 CC 用户级 MCP 配置（`~/.claude.json`） |
| `ferryman autostart install/uninstall`、`ferryman watchdog install/uninstall` | ⚠ 写系统注册（登录自启 Run 键 / 看门计划任务） |

只读、对 agent 零风险：`doctor`、`status`、`version`、`provider list`、`upstream list`、
`account report`、`backtest`、`tuning status`、`provider switch`（热切换：不重启不断流，
配置写盘由守护端点完成）、`mcp`、`help`。

---

## serve 与面板

### `ferryman`（无参）与 `ferryman serve`

```bash
ferryman                # 无参 = serve：守护(15700) + 面板(15900) + 托盘
ferryman serve [--port N] [--no-tray] [--no-browser] [--smoke]
```

| 旗标 | 作用 |
|---|---|
| `--port N` | 面板端口（0 = `FERRYMAN_PANEL_PORT` 或 15900；守护口不随此旗标变） |
| `--no-tray` | 不建托盘图标，前台运行，Ctrl+C 优雅停守护 |
| `--no-browser` | 面板口被占且探到活面板时不自动开浏览器 |
| `--smoke` | 放宽阈值差≥120s 校验（秒级阈值的冒烟环境用） |

面板口被占时：探得到活面板 → 打开浏览器完事；探不到 → 守护照跑、面板缺位只告警。
退出码：0 = 守护正常收班；非 0 = 守护启动失败/异常退出；2 = 旗标错。

### 面板族（只起面板，不起守护）

```bash
ferryman --demo|--data 目录 [--port N] [--no-tray] [--no-browser]
ferryman --install-shortcuts    # 建桌面+开始菜单快捷方式后退出
```

| 旗标 | 作用 |
|---|---|
| `--demo` | 演示模式：确定性合成账本写一次性临时目录（忽略 `--data`，绝不碰真实账本） |
| `--data 目录` | 账本目录或数据根（缺省 `$FERRYMAN_DATA` 或 `~/ferryman`；根下无 `*.jsonl` 而有 `accounts/` 时自动下钻） |
| `--port N` | 面板端口（0 = 随机口；只读时间线查看器） |
| `--no-tray` / `--no-browser` | 同 serve |

退出码：0 = 正常；2 = 旗标错/未知参数；服务异常非 0。

## 体检

### `ferryman doctor [--json]`

一键体检：钩子在位 / 脚本健康（BOM/控制字符）/ cc-switch 快照覆盖 / daemon 活性 /
渡口上游生效链 / pi 生效链等。除恰好一个可选 `--json` 外**不收任何参数**（`-h` 亦然，
给了就退 2，绝不执行体检）。

退出码：0 = 全过（`not_checked` 不计失败）；1 = 有 fail 项；2 = 用法错。

`--json` 字段表（与 agent 面 MCP `doctor` 工具同源序列化）：

| 字段 | 含义 |
|---|---|
| `version` | 版本号 |
| `ok` | 总判定（= 零 fail） |
| `checks[]` | 每项 `{name, status, detail}` |
| `checks[].status` | `pass` / `fail` / `not_checked` |
| `summary` | `{total, pass, fail, not_checked}` 计数 |

脱敏示例（截取；数值随实况）：

```json
{"version":"v0.5.2","ok":true,"checks":[{"name":"hooks_installed","status":"pass","detail":"…"},{"name":"provider_pi_dock","status":"pass","detail":"…"}],"summary":{"total":27,"pass":27,"fail":0,"not_checked":0}}
```

### `ferryman version`

打印版本号；`dev` = 非 release 构建（附提示）。退出码恒 0。

## 守护

### `ferryman status`

探活三面如实报告（只读）：守护（在线/不在线/鉴权失败 + 版本）、渡口（在线/不在线/未启用）、
台账（在册会话数、其中闲置(凉)数）。守护不在线也如实报告——状态查询不是失败。

退出码：恒 0。参数：不接受（`-h` 除外，退 0；其余参数退 2）。

### `ferryman stop [--wait 秒]`

⚠ 停守护。POST `/shutdown` + 等端口释放与进程退场（排水窗语义）；`--wait` = 预算秒数
（缺省 240；0 = 不等）。全程无 kill 路径——绝不硬杀，超时如实报告。

退出码：0 = 已停，或本就不在线（无需停止）；1 = 超时（端口/进程未让位，未硬杀）或被拒
（401 鉴权失败）；2 = 用法错（负数 `--wait`、多余参数）。

## 安装（⚠ 写宿主配置 / 系统注册）

### `ferryman install-cc [--events 事件1,事件2,…]`

CC 钩子注入 `~/.claude/settings.json`；检测到 `~/.cc-switch/cc-switch.db` 时把钩子同步
注入全部 claude 供应商快照（幂等、保留既有条目、改库前备份）。`--events` 缺省 = 全集
（UserPromptSubmit,SessionStart,SubagentStart,SubagentStop）；切换日三类子集 =
`SessionStart,SubagentStart,SubagentStop`（闸门 UserPromptSubmit 暂不装）。
退出码：0 / 1；`-h` 退 0。

### `ferryman install-ccswitch`

cc-switch 供应商快照注入（幂等自动备份）。**不接受任何参数**——含 `-h`，任何参数退 2
且绝不执行。注：退役路径中——供应商切换的正路是 `provider switch` / `provider apply`，
本命令保留用于 cc-switch 共存期的钩子快照修复。退出码：0 / 1 / 2。

### `ferryman install-codex [--events 事件1,事件2,…]`

Codex 钩子注入 + 开 `[features] hooks = true`（改后需在 TUI `/hooks` 信任）。
`--events` 语义同 install-cc。退出码：0 / 1；`-h` 退 0。

### `ferryman install-mcp [--force]`

把 agent 面 MCP server 注册进 CC 用户级 MCP 配置（`~/.claude.json` 的 `mcpServers.ferryman`，
`claude mcp add --scope user` 等效）。幂等：自有旧条目直接刷新；外部/不兼容同名条目默认
拒绝并说明原因，仅 `--force` 覆盖。退出码：0 / 1 / 2。

### `ferryman autostart [install|uninstall|status]`

登录自启 Run 键三操作，缺省 `status`（只读最安全）。status 输出 installed / mismatch /
missing（mismatch = exe 挪窝或手改，重跑 install 修复）。退出码：查询/操作成功 0，
失败 1；未知子命令 2；`-h` 退 0。⚠ install/uninstall 写注册表 Run 键。

### `ferryman watchdog [install|uninstall|status]`

看门。**无参 = 单次探活**：无监听拉起 daemon（计划任务每 5 分钟调的就是这个形态；
占用只告警不双拉）。install/uninstall/status = 看门计划任务三操作。
退出码：0 = 在跑 / 已拉起 / 端口被非守护占用（只告警）；1 = 拉起失败或查询失败；
2 = 未知子命令；`-h` 退 0。⚠ install/uninstall 写系统计划任务。

## 账本

### `ferryman account report [--since 日] [--until 日] [--project 名] [--session id] [--kind 类型] [--provider 键] [--json]`

按项目/会话/月汇总账本报表：四列 token（input/output/缓存写/缓存读）、成效账
（节省额；价格表缺缓存价时如实标注"节省额不可算"）、bypass 与无效保温单列。只读。

| 旗标 | 取值 |
|---|---|
| `--since` / `--until` | `YYYY-MM-DD`（本地时区） |
| `--kind` | `handoff` / `beat` / `block` / `inject` / `bypass` / `window` |
| `--provider` | block 侧经济价格表键（缺省 = ferry provider） |

退出码：0 / 1（config 或账本读不到）/ 2。

### `ferryman backtest [--config 路径] [--projects glob]… [--exclude glob]… [--json] [--out 路径]`

等待窗扫参（离线只读，反事实重放）：读自己的账本与价格表，全程唯一写目标 = markdown
实验报告（`--out`，缺省 `docs/<当天日期>_等待窗扫参_实验报告.md`）。`--projects` fnmatch
语义可多次；`--exclude` 恒压过正集。

**空态语义（票02 契约）**：引擎未产出（`ttl_s` 未配置、价格不可算）与零窗都是数据状态
非失败——报告照落、空态原因明示、退出码 0；真错误（config/账本读不到、写盘失败）退 1。
`--json` 时 stdout 恒为纯 JSON，空态标注走 stderr。

`--json` 顶层字段（SweepView）：`loaded_at_iso`、`loaded_at`、`counts`（窗计数：全部/
过滤后/可重放/unknown/不可算/未决/多匹配）、`gap`（闭式对照 vs 网格最优 + ttl_s 校准建议）、
`tiers[]`（分档明细）、`diagnostic[]`、`useless_warm`（无效保温）、`double_count`、
`holdout`（留出集）、`blind_spots[]`、`evidence_level`。

退出码：0（含空态）/ 1 / 2。

## 渡口与供应商

> 供应商表（`[dock.upstreams]`）一条目 = 一家模型 API 服务：`base_url` / `api_key` /
> `model_map` / `dialect`（`anthropic` \| `openai_responses`）/ `codex`、`pi` 否决位 /
> `balance_url`。`active` 单选。字段定义见 [config.example.toml](../config.example.toml)。

### `ferryman upstream list [--config 路径] [--json]`

渡口上游表：active 标注 / base_url / model_map 概要 / 密钥脱敏（尾 4 位）。

`--json` 字段表：

| 字段 | 含义 |
|---|---|
| `config` | 解析后的配置文件路径 |
| `active` | 当前活跃条目名 |
| `note` | 可选；空态/旧单值形态的人话说明 |
| `upstreams[]` | 条目数组（恒非 null） |
| `upstreams[].name` / `.active` | 条目名；是否 active |
| `upstreams[].base_url` / `.model_map` | 端点；模型位映射 |
| `upstreams[].api_key` | 尾 4 位掩码；空钥 = 空串 |
| `upstreams[].key_status` | `configured` / `not_configured` / `local_relay_exempt`（本地中转豁免） |
| `upstreams[].balance_url` | 可选；不配不出 |

脱敏示例：

```json
{"config":"C:/Users/<你>/ferryman/config.toml","active":"智谱","upstreams":[{"name":"智谱","active":true,"base_url":"https://open.bigmodel.cn/api/anthropic","model_map":{"default":"glm-5.3","haiku":"glm-5.3-flash"},"api_key":"****ab12","key_status":"configured"}]}
```

退出码：0（含"配置无 [dock] 节"空态）；1 = 配置加载失败（`--json` 时 stdout 出
`{"config":…,"error":…}` 一行）；2 = 用法错。

### `ferryman upstream use <名> [--config 路径]`

⚠ **已弃用——改用 `ferryman provider switch`**（热切换：守护不重启、在跑会话不断流、
CLI 零写盘）。本命令是重启式切换：写 config 的 `[dock].active` → POST `/shutdown` 停旧
→ detached 隐藏拉起新守护 → 轮询 `/stats`。仅当需要走重启语义（如把 active 切回
`cc-switch` 回退条目）时使用。

⚠ 中断在途请求（SSE/长请求随守护重启断开）；重启即全局作废内存缓存快照，旧会话按冷启动
全量重付。条目不存在 / 非本地条目缺 api_key / 写后校验失败 → 拒绝且配置不动，退 1；
自定义 `--config` 路径不自动重启（新守护读默认配置），切换成功退 0 并指引手动重启；
健康检查失败退 1——不自动回滚、不自动重试（回退：`ferryman upstream use cc-switch`）。
退出码：0 / 1 / 2。

### `ferryman provider list [--config 路径] [--json]`

供应商操作面的列表出口：上游表同一份条目 + `dialect` + codex/pi 可用性 + 模型位 +
密钥脱敏。三态取值：`dialect` = `anthropic` / `openai_responses`；`codex` =
`native`（原生透传）/ `translation`（需翻译）/ `unsupported`；`pi` = `available` /
`unavailable`（方言无入站车道）/ `unsupported`（否决位）。

`--json` 字段表（顶层 `config` / `active` / `note`? / `providers[]`；`providers` 恒非 null）：

| 字段 | 含义 |
|---|---|
| `providers[].name` / `.active` | 条目名；是否 active |
| `providers[].base_url` / `.dialect` | 端点；线协议方言 |
| `providers[].codex` / `.codex_model` | codex 可用性三态；codex 主模型（未配为空串） |
| `providers[].pi` / `.pi_model` | pi 可用性三态；pi 主模型（未配为空串） |
| `providers[].model_map` | 模型位映射（`default` 必看；`codex`/`pi` 键 = 对应车道主模型） |
| `providers[].api_key` / `.key_status` | 尾 4 位掩码；`configured` / `not_configured` / `local_relay_exempt` |
| `providers[].balance_url` | 可选 |

脱敏示例：

```json
{"config":"C:/Users/<你>/ferryman/config.toml","active":"智谱","providers":[{"name":"智谱","active":true,"base_url":"https://open.bigmodel.cn/api/anthropic","dialect":"anthropic","codex":"translation","codex_model":"glm-5.3","pi":"available","pi_model":"glm-5.3","model_map":{"default":"glm-5.3","codex":"glm-5.3","pi":"glm-5.3"},"api_key":"****ab12","key_status":"configured"}]}
```

退出码：0 / 1 / 2（语义同 upstream list）。

### `ferryman provider switch <名> [--cc-only] [--config 路径]`

热切换活跃供应商：走守护管理口 POST `/provider_switch`——守护不重启、无端口空窗、
在跑会话不断流；持久化由端点侧落盘，CLI 零写配置。成功回显新 active、base_url 与
codex 车道模式。

codex = `unsupported` 或 pi 不可用（否决位 / dialect 非 anthropic / 缺 pi 主模型键）→
**默认拒绝**并逐因报明（两 agent 同时断供都列），`--cc-only` 显式放行并分开列明各自
断供面。守护不在线如实报错并给拉起指引（`ferryman serve` 或等看门），不静默失败。
退出码：0 / 1（不存在、不可用拒绝、端点拒绝、不在线）/ 2。

### `ferryman provider add <名> --base-url <端点> [--key <钥>|--key-env <环境变量>] [--dialect anthropic|openai_responses] [--codex unsupported] [--pi unsupported] [--model-map k=v,k=v] [--balance-url <端点>] [--config 路径]`

新增条目到本机 config（文本手术 + 前置校验 + 原子写）。密钥只落本机 config，输出永不
回显全钥（`--key-env` 从环境变量读，密钥不进 shell 历史）。`--codex` / `--pi` 只收
`unsupported` 否决位（可用性缺省按 dialect 推导）；`--model-map` 非本地端点必含
`default`，`codex` / `pi` 键 = 对应车道主模型。重名拒绝不覆盖。退出码：0 / 1 / 2。

### `ferryman provider remove <名> [--config 路径]`

删除条目。active 条目拒删（先 switch 到别的再删——删了 active 会让守护拒启）。
退出码：0 / 1 / 2。

### `ferryman provider import-ccswitch [--db 路径] [--config 路径]`

从 cc-switch 库（sqlite，只收 claude/codex 两类）导入供应商条目；dialect 按端点线协议
推断；重名跳过不覆盖。库路径缺省 `~/.cc-switch/cc-switch.db`（`--db` 可指）。保留为
搬家工具。退出码：0（含"无可导入条目"空态）/ 1 / 2。

### `ferryman provider apply [--restore] [--config 路径]`

⚠ 接管：把 CC / codex / orca / pi 四份宿主配置（`~/.claude/settings.json`、
`~/.codex/config.toml`、orca 的 codex config、`~/.pi/agent/` 下 models.json 与
settings.json 两文件）外科式改写为指向渡口。F7 认证前置校验；各目标同戳备份成组；
幂等（重跑只补缺）；逐份回显动作/路径/备份。pi 不可用时该目标跳过并如实回显、
其余目标照常。`--restore` 按最近一组接管前备份还原（回 interim 拓扑）。
退出码：0 / 1（含部分完成）/ 2。

### `ferryman eval-ferry --provider <名> --n <样本数> --out <目录> [--handoffs 目录] [--config 路径] [--timeout 秒]`

盲评生成：取最近 N 份交接的骨架素材，经指定摆渡供应商生成模型叙事，一样本一文件对
（骨架/叙事并排）落 `--out`，供人工盲评。`--handoffs` 缺省 `~/ferryman/handoffs`；
`--timeout` 缺省 600 秒/样本。退出码：0 = 全成；1 = 硬错误或存在失败样本；2 = 用法错
（`--out` 必填）。

## 调参

> 三态档位（`[tuning].mode`）：manual 只出报告 / recommend 自动扫参+提醒、人工应用（默认）
> / auto 护栏内自动应用。写操作唯一路径在 CLI；永不自动升档。

| 命令 | 作用 | 退出码 |
|---|---|---|
| `ferryman tuning status [--config 路径] [--json]` | 只读面板：建议与生效值（建议值/配置值/生效值三列，拒绝拒算不造数） | 0 / 1 / 2 |
| `ferryman tuning sweep [--config 路径] [--projects glob]… [--exclude glob]… [--ttl-min 分,分,…]` | 同模型阈值扫参，按档位三态分流；写目标仅报告/调参流水/校准投影（数据目录内） | 0 / 1（任一上游失败）/ 2 |
| `ferryman tuning apply <建议id> [--config 路径]` | 接受建议（唯一人工写路径；id 见 status） | 0 / 1 / 2 |
| `ferryman tuning reject <建议id> [--reason 文本] [--config 路径]` | 拒绝建议 | 0 / 1 / 2 |
| `ferryman tuning rollback <上游名> [--config 路径]` | 一键回滚该上游最近一次生效 | 0 / 1 / 2 |

`tuning status --json` 顶层字段：`mode`（档位）、`dir`（调参流水目录）、`events`（事件数）、
`calibrations[]`（公式输入校准）、`suggestions[]`（建议及状态）、`effective[]`（每上游
现算生效值；拒算带 `err_kind`/`err_text` 与回落种子）。

`--ttl-min` 缺省时收割 beat 遥测的 TTL 实测观测；收割失败降级告警、回落种子，不阻塞扫参。

## 换装

### `ferryman update [--check] [vX.Y.Z] [--prerelease] [--wait-quiet=<秒>] [--force]`

⚠ 无 `--check` = **执行升级**（监督者：静默门等流量空闲 → 下载 → 校验 → 停守护 →
原子换装 → 重启 → 失败回滚）。agent 只在用户明确要求升级时执行；平时用 `--check`（只读）。

| 旗标/参数 | 作用 |
|---|---|
| `--check` | 只检查并报告（已是最新/发现新版/降级注明），不下载不换文件 |
| `[vX.Y.Z]` | 显式目标版本（支持降级） |
| `--prerelease` | 检查纳入预发布版（rc/beta；缺省只看稳定版） |
| `--wait-quiet=<秒>` | 静默门等待预算（0 = 不等；缺省 60s） |
| `--force` | 跳过静默门直接停旧（脚本态） |

旗标与版本号先后顺序任意。退出码：执行 0 = 升级完成；1 = 失败（已回滚恢复服务，或
回滚未成——如实报告转人工）；`--check` 0 = 报告已出；1 = 检查失败；2 = 用法错。
结果经通知通道（Pushover/Toast）推送（config 可载时）。

## 工具

### `ferryman mcp [--config 路径]`

stdio MCP server（JSON-RPC 2.0）。六件工具**全部只读**：

| 工具 | 作用 |
|---|---|
| `sessions` | 台账清单：闲置状态与凉热判定，按最后写入倒序 |
| `session_detail` | 单会话：台账条目 + 四列总账 + 最近有效交接覆盖 + 窗口状态 |
| `gate_check` | 闸门判定只读推演：此刻 allow/warn/block 与剩余分钟（不改状态） |
| `heartbeat_status` | 在飞等待窗/问询窗清单 + 逐窗心跳遥测 |
| `doctor` | 一键体检（与 `ferryman doctor --json` 同源结构） |
| `cost_report` | 账本报表：token 四列 + 成效账（scope=project/session/month） |

stdout 专留给 JSON-RPC，诊断只走 stderr。退出码：0 = stdin EOF 正常收线；1 = 配置
加载失败或服务错。注册进 CC：`ferryman install-mcp`。

### `ferryman help`

打印顶层用法（`-h` / `--help` 同）退 0。各命令细账：手工分发族（account / tuning /
autostart / watchdog / upstream / provider / cutover）`-h` 打印专属 usage 页退 0；
status / stop / backtest 手工（或 ErrHelp）识别 `-h` 打印专属用法退 0；flag 包缺省面族
（serve / install-cc / install-codex / install-mcp / update / version / eval-ferry）`-h`
退 0；`doctor` 与 `install-ccswitch` 零参数契约，`-h` 退 2。

## 切换工具（cutover 族）

`ferryman cutover backup [--data 目录] [--dest 目录]`（数据全量备份，只读复制）、
`cutover rollback-write [--repo 目录] [--data 目录]`（生成 rollback-to-python.cmd）、
`cutover rollback-drill [--repo 目录] [--dir 临时目录]`（隔离演练回退机制）、
`cutover smoke [--config 沙箱配置]`（沙箱冒烟四链路）。族定位：切换工具，不执行生产切换。
退出码：0 / 1（动作失败）/ 2（无子命令或未知子命令；`-h` 退 0）。

## 关联文档

- [AGENTS.md](../AGENTS.md) — agent 使用契约（何时用哪条、输出与错误怎么读）
- [cmd/ferryman/README.md](../cmd/ferryman/README.md) — 构建与面板/托盘细节
- [config.example.toml](../config.example.toml) — 配置字段事实源
- [CONTEXT.md](../CONTEXT.md) — 术语表
- [README.md](../README.md) — 项目总览
