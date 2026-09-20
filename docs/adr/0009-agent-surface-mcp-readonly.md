# agent 面：MCP 只读起步，禁区清单与降闸禁手

Ferryman 已有三张面——HTTP API（钩子用）、CLI（人用）、config.toml（人手编辑，含真钥）——但没有一张是 AI agent 能自描述发现的入口。评估"支持 AI agent 的控制、查询、配置"（2026-09-20 grill 定稿）查明：真正的难点不是加 API，而是被守望的 CC 会话自己就是最可能的 agent 客户端，天然有动机把闸门拆掉；config 里躺着上游真钥（T39 规矩：只活本机）。故先划禁区，再谈能力。

决定（2026-09-20 grill 定稿）：

- **MCP stdio 是唯一 agent 入口**：`ferryman mcp` 子命令，内部转发既有 HTTP daemon（Bearer/127.0.0.1 原样复用，不新开监听、不复制鉴权）。CLI 留人、HTTP 留钩子、agent 只认 MCP。
- **首期画像**＝(a) 自省查询全量（被守望会话问成本/缓存/闸门判定）+ (b) 运维只读（doctor 结果、账本报表）；(b) 的执行动作与 (c) 编排型后置。
- **v1 严格只读**：连现成的 `/qwatch_stop` 都不放。v2 开控层时三件同票落地——`agent_ctl` 审计科目 schema + 白名单动作（qwatch_stop、手动摆渡、会话级心跳开关）+ 降闸禁手 daemon 侧强制。顺序不许反：先让读面跑稳看清 agent 实际用什么，再设计写面审计。
- **四格禁区**：①永不让 agent 碰（读凭据、直写账本、全局无条件心跳——ADR-0004 红线）；②只读开放（台账、账本流水+成效账、窗口状态、心跳遥测、交接库索引、时间线数据）；③可控但 daemon 侧强制不变量；④配置只开无凭据键。**不变量必须在 daemon 代码里强制，不写在 agent 提示词里**——提示词会被注入攻破，代码不会；出现"agent 能做而人不能做"的动作即为缺陷。
- **降闸禁手**：enforce→observe/off 对 agent 永不可执行；agent 对闸门只有查询与升级建议上报（它建议、人执行）。合法单次绕过只有逃生门两条（环境变量豁免、魔法前缀），agent 面不得成为第二条绕过通道。
- **MCP server 永不直读台账/账本/交接库文件**——一切读经 daemon 端点（公式单源：直读＝第二份公式实现＝缺陷；daemon 是唯一事实源）。唯一例外 doctor：进程内复用同一段检查函数，不经 HTTP。
- **v1 工具六件**：sessions / session_detail / gate_check / cost_report / heartbeat_status / doctor；响应永不包含消息内容（与时间线同一条红线）；演示模式的合成数据永不经 MCP 吐出（demo mode 只属于 viewer）。
- **鉴权**：token 从 config 读、只活 MCP server 进程内存、任何响应/错误/日志不回显；不做调用者身份识别——agent 自报不可信，账本不记不可信数据；"人把 MCP server 注册进 agent 配置"这个动作本身就是授权。
- **错误面**：daemon 不可达/超时→工具明确报错，不缓存、不伪造、不顺手自举（fail-open 是闸门的放行语义，不是数据语义；查询无"放行"可言，空/旧数据会被 agent 当事实用）。
- **工程面**：注册走独立 `install-mcp` 幂等命令（与钩子安装职责分离）；doctor 增"agent 面注册在位"检查项（CC 配置被重写后 MCP 悄悄消失正是静默失效类事故）；工具名英文、描述中文直接引用 CONTEXT.md 词条原文，不造第二套翻译。

## Considered Options

- **HTTP 端点直接暴露给 agent**：agent 只能靠文档发现能力、每个 agent 集成都要手写——否决。
- **CLI 包装（agent 跑 ferryman.exe 子命令）**：权限提示噪音、输出是人格式非结构化、`cutover rollback` 类命令裸奔无确认门——否决。
- **v1 即开控层（qwatch_stop 端点现成）**：审计科目未设计先开写面，等于为想象中的需求设计审计——否决，读面先行。

## 后果

daemon 新增资源化只读 GET 端点（/sessions 等），/stats 契约不动；工具面一旦给出即难收回，后续加能力只在 MCP 层加工具名、不动守卫；drift_alerts（存储位置未查实）、handoffs 单列、timeline 数据留 v1.x；"agent 面""降闸禁手"词条入 CONTEXT.md。v2 顺序＝控层三件套（agent_ctl 科目+白名单动作+降闸禁手 daemon 强制）→ 运维执行层 → 编排型——控层是后两者的共同前置（没有审计科目，执行/编排的写动作没地方记账）；Codex 注册随 v2 一并考虑。
