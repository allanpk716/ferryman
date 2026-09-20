# Agent 面 v1：MCP 只读查询面 — 规格

- 日期：2026-09-20
- 状态：夜链 20260920-110838 固化（评审环 20260920-112150，终态夜间收工）
- 对象：`.xcheck/20260920-112150/proposal.md`（rev1）＋同环 decisions.md（D1-D13）＋FINDINGS.md（F1-F8）
- 决策依据：仓库 ADR-0009（agent 面：MCP 只读起步、四格禁区、降闸禁手、一切读经 daemon——操作者工作区未提交稿）；术语遵 CONTEXT.md 词汇表（台账、凉会话、拦截阈值、有效交接、等待窗、问询窗、心跳、账本、成效账、渡口等）

## Problem Statement

Ferryman 已有三张面：HTTP API（钩子用，/gate /subagent /qwatch_stop /stats /restore）、CLI（人用）、config.toml（人手编辑，含上游真钥）。没有一张是 AI agent 能自描述发现的入口——被守望的会话无法查询"本场成本、缓存活性、闸门判定、交接覆盖"，运维型 agent 无法查询台账、账本报表与心跳遥测。用户评估确认：v1 做只读 MCP 查询面；一切写动作后置（v2 三件套）。

## Solution

新增 `ferryman mcp` 子命令：stdio JSON-RPC（initialize / tools/list / tools/call）的 MCP server，暴露六件只读工具，内部转发 daemon 新增的只读 GET 端点；doctor 工具进程内复用既有检查函数。配套 `ferryman install-mcp`（用户级注册，幂等，冲突语义见下）与 doctor"MCP 注册在位"检查项。三条面各归其位：CLI 留人、HTTP 留钩子、agent 只认 MCP。

## User Stories

1. 作为被守望的 CC 会话，我想查询本会话的四列 token 总账与金额（主转录＋各子代理转录加总），以便决定是否压缩或收尾。
2. 作为被守望的 CC 会话，我想查询闸门此刻对我的判定与离拦截阈值的剩余分钟，以便规划下一次提交。
3. 作为被守望的 CC 会话，我想查询自己最近一次有效交接的覆盖截止与状态（fresh/skeleton），以便判断续接信息是否充分。
4. 作为运维型 agent，我想列出全部会话的闲置状态与凉热判定，以便找出需要摆渡的凉会话。
5. 作为运维型 agent，我想跑一次体检并拿到结构化结果（钩子在位、脚本、快照、daemon 活性、MCP 注册在位），以便发现静默失效。
6. 作为运维型 agent，我想查询账本报表（按项目/会话/月：四列汇总、金额、成效账、bypass 与无效保温单列），以便汇报花费与节省。
7. 作为运维型 agent，我想查询在飞等待窗/问询窗与逐窗心跳遥测（跳数、hit/miss、TTL 观测、累计花费），以便排查保温有效性。
8. 作为用户，我想用一条幂等命令把 agent 面注册进 CC 用户级 MCP 配置（并安全处理同名冲突），以便本机全部会话可用。
9. 作为用户，我想让 doctor 能查出"MCP 注册不在位"，以便 CC 配置被外部工具重写后能及时发现。

## Implementation Decisions

### 架构与读取路径
- MCP server 是唯一 agent 入口；一切读经 daemon HTTP 端点（Bearer＋127.0.0.1 原样复用，不新开监听、不复制鉴权）；**MCP 进程永不直读台账/账本/交接库文件**（公式单源：internal/report、internal/policy 只有 daemon 内一份消费面）。唯一例外 doctor：进程内复用同一段检查函数，不经 HTTP。
- daemon token：MCP server 启动时经既有配置解析取得（显式参数 > FERRYMAN_CONFIG 环境变量 > 默认路径，复用 internal/config 的 Load 优先级）；token 只活进程内存，任何工具响应、错误信息、日志不得回显。
- 不做调用者身份识别（自报不可信，账本不记不可信数据）；"人把 MCP server 注册进 agent 配置"即授权动作（安装即授权，同机同信任域）。

### daemon 只读端点（internal/daemon，新文件旁挂；/stats 现有字段名逐字契约不动）
- `GET /sessions`：query agent、cwd（前缀过滤）、limit（默认 50）；台账内存态按最后写入倒序；返回 session_id、Agent、项目路径、最后写入距今秒、闲置判定（凉/未凉）、上下文规模、族系。
- `GET /session?id=<session_id>`：台账条目＋四列会话总账（主转录＋各子代理转录加总）＋有效交接覆盖（covers_until、fresh/skeleton）＋窗口状态（等待窗/问询窗，含停车标志）。
- `GET /gate_check`：带 session_id＝单会话判定（此刻 allow/block/warn、离拦截阈值剩余分钟、判定依据＝有效交接路径或缺失原因）；**无 session_id＝汇总模式**：逐会话一行，limit 同构默认 50，排序键＝预计拦截时刻升序（等价闲置时长降序），并列按 session_id 字典序稳定排序，无有效交接者排在有交接者之前。只读，不改任何状态。
- `GET /report?scope=<project|session|month>&key=...`：复用 internal/report 公式单源；四列汇总与金额、成效账（价格表缺缓存价时如实标注"节省额不可算"）、bypass 与无效保温单列。
- `GET /beats`：在飞等待窗/问询窗清单（含停车状态、预估 close_reason）＋逐窗心跳遥测（跳数、hit/miss/error/observe 计数、TTL 观测、累计实收花费）。

### MCP 工具六件（名称/参数英文，描述中文并直接引用 CONTEXT.md 词条原文）
`sessions`、`session_detail`、`gate_check`、`cost_report`、`heartbeat_status` 对应上列端点转发；`doctor` 进程内复用。工具面不存在任何写操作工具。

### 错误面
- 五个走 daemon HTTP 的工具：daemon 不可达/超时 → 返回明确错误（MCP isError 语义），文案含"任意钩子触发或 ferryman serve 会拉起"；**不缓存、不伪造、不顺势自举 daemon**。
- doctor 例外：daemon 不可达/超时 → 照常返回结构化体检结果，daemon 活性项记失败/异常；同样不缓存、不伪造、不自举。

### install-mcp（含 F6 冲突语义——seam 裁定）
- 注册范围 v1 固定：CC 用户级 MCP 配置（`claude mcp add --scope user` 等效）。理由：agent 面服务本机全部被守望会话，项目级需逐项目安装，与"安装即授权"单机语义不符。
- 同名冲突语义：①能识别为 Ferryman 自建且形态兼容的旧条目 → 幂等覆盖（重复执行结果一致）；②外部或不兼容条目 → 默认拒绝并说明原因，仅显式 `--force` 才覆盖；③回显仅限非敏感摘要与白名单字段（command/args 形状），env/header/token 类值一律掩码或不输出；--force 覆盖前输出被替换条目的脱敏摘要。
- 与 install-cc（钩子）职责分离；doctor"MCP 注册在位"检查项查同一 scope（用户级）。

### 红线（工具与端点响应＋CLI 输出面共同遵守）
1. 永不包含消息内容（台账标题、路径、计数、金额可以；对话原文不行）。
2. 永不暴露凭据（api_key、token、上游真钥、provider 凭据字段）——范围含 install-mcp 的回显输出。
3. daemon token 永不回显（响应/错误/日志）。
4. 无任何写路径（全部只读 GET；MCP 工具面无写工具）。
5. 合成数据（演示模式）永不经 MCP 吐出——演示模式只属于 viewer。
6. 降闸禁手（v2 预埋铁律）：enforce→observe/off 对 agent 面永不可执行。

### 协议实现
stdio JSON-RPC 2.0，实现载体（官方/社区 Go SDK 或最小手写）由实施票面决定；行为约束＝initialize/tools/list/tools/call 三方法可被标准 MCP 客户端发现与调用。

## Testing Decisions

只测外部行为，不测内部结构；沿用 internal/daemon 既有 E2E 风格（临时目录、临时端口、构造态断言）。

- **六工具 E2E**：起临时 daemon（临时端口/临时 token/临时目录），经 `FERRYMAN_CONFIG` 指向该临时 config——MCP server 与 doctor 检查目标均从该 config 解析；测试显式断言目标端口非生产端口（15722/15724 等）；doctor 的用户目录检查在临时环境下面向临时 HOME 或该项显式标注"未检查"。
- **反向断言**：响应不含消息内容样本、不含凭据字段、不含 token 串；MCP 工具面不存在写操作工具。
- **错误面**：五 HTTP 工具在 daemon 不可达时断言——返回明确错误、无新 daemon 进程/监听（不自举）、第二次调用仍为错误（不缓存）；doctor 在 daemon 不可达时返回结构化结果且活性项＝异常。
- **安装面**：install-mcp 重复执行幂等（两次执行后配置一致）；doctor 注册检查装/卸两态各断言一次；**F6 两断言**——外部同名条目默认不破坏；含假凭据的旧条目值不回显（脱敏验证）。
- **/stats 契约回归**：既有 /stats 字段测试保持绿（或字段名集合快照对比不变）。

## Out of Scope

- 控层任何动作（qwatch_stop、手动摆渡、会话级心跳开关）与 agent_ctl 审计科目——v2 三件套同票。
- 配置修改（即使无凭据键）。
- drift_alerts 工具（形态漂移告警存储位置未查实，v1.x 再定）；handoffs 独立工具（并入 session_detail）；timeline 工具（viewer 已存在）。
- Codex 侧 MCP 注册（v2 一并考虑）。
- v2+ 顺序：控层三件套 → 运维执行层（渡口切换 runbook 的 agent 化）→ 编排型。

## Further Notes

- F1-F5（round0 五阻断）已双家复核解除并烘焙入本规格；F6 以 seam 裁定烘焙（见 install-mcp 节），受影响票记 review_blocks: F6；F7（汇总排序键）已烘焙；F8：本规格以机制/函数语义引用，不钉代码行号。
- 开放事实（不阻塞）：渡口形态漂移告警存储位置未查实；Codex CLI 对 MCP 的支持面未查实。
- ADR-0009 与 CONTEXT.md"agent 面/降闸禁手"词条在操作者工作区未提交，不在夜链分支提交范围；是否随合并一并提交由人决定。
- 生产 daemon 正在本机服务真实 CC 流量（渡口）：实施与测试一律临时端口，不得触碰生产端口或杀生产进程。
