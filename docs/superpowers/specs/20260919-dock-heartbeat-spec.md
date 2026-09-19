# 渡口与心跳保活 Spec

- 日期：2026-09-19
- 来源：评审链 `.xcheck/20260919-195118`（round 0）→ `.xcheck/20260919-202930`（round 1 复审，终态夜间收工）；对象 = 后者 proposal.md（rev1）；决策 D1-D10 与 FINDINGS F1-F13 同环绑定。
- 术语：遵守 CONTEXT.md 词汇表（渡口/改写五件/出站头卫生/形态漂移告警/等待窗口/心跳/捕获重放/无效保温/问询守望）。

## Problem Statement

主会话等待子代理期间闲置，GLM 缓存（TTL≈600s）过期后恢复要全价重付；现状对"机器等机器"没有任何保温手段。同时 CC 链路的中转改写依赖第三方 cc-switch，心跳若绕过它改写路径不一致就会 miss。本版要：daemon 进入 CC 流量路径成为中转层（渡口），在其上实现等待窗心跳（前缀源=内存捕获重放），并配齐常驻保障、等价验证工具与通知文案。

## Solution（用户视角）

- **渡口（透传）**：配置里加了 `[dock]` 节后，daemon 在本机 15722 端口接收 Claude Code 的 API 流量并原样转发给上游（默认仍是 cc-switch，行为不变）；把用户 CC 的 base_url 指到 15722 是人工操作。**没配 `[dock]` 时 daemon 与之前完全一样，不多绑任何端口**（F11 裁定）。
- **等待窗心跳**：主会话等子代理超时闲置后，daemon 按策略计算器定出的节律，把该会话最后一份真实请求（内存快照）原样重放一遍（只把 max_tokens 降到 1）保温缓存；子代理回来时缓存还热，续跑不再全价重付。默认关，三态开关（off/observe/enforce）人工拨。
- **渡口（改写，默认停用）**：五件改写（模型档位映射/剥 [1M]/换真钥/text-only 图片降级/出站头卫生）落地在渡口内，`rewrite_enabled=true` 才启用；启用前必须先过 L1-L3 验证门。上游可直连智谱 GLM，从此 Claude 链路不再依赖 cc-switch（它继续服务 Codex）。
- **常驻保障**：注册表自启＋每 5 分钟 HTTP 探活看门，重启机器不断服务。
- **验证工具**：tap/diff/replay/场景矩阵，把"等价"从推断变成测量。
- **通知**：pushover 标题带项目名与会话标题，一眼知道是哪个会话。

## User Stories

1. 作为 CC 用户，我在主会话里等子代理干活几十分钟，回来续跑时缓存仍命中，不为整段上下文再付全价。
2. 作为 CC 用户，我可以一键看到心跳花了多少积分、保温是否命中（账本逐跳入账），绝不出现我看不见的烧钱。
3. 作为 CC 用户，重启电脑后 ferryman 自动起来并在挂掉 5 分钟内被看门拉起，不用手动启动。
4. 作为本机运维者，我不配置 `[dock]` 时 daemon 行为与旧版完全一致；配置后也能一行回退（CC base_url 指回 15721）。
5. 作为本机运维者，渡口对每个请求记一行纯元数据流水（改写前后模型名、四列 token、延迟、状态码），出问题能对账。
6. 作为本机运维者，CC 版本升级带来新的 beta 头或请求参数时，渡口第一时间告警而不是静默 miss。
7. 作为本机运维者，我能在面板/报告里看到智谱余额（顶替 cc-switch 的余额面板）。
8. 作为本机运维者，我把 rewrite_enabled 打开但上游还指着本地 cc-switch 时，渡口拒绝进入改写模式并告警（防双重改写）。
9. 作为被通知的用户，pushover 标题告诉我哪个项目的哪个会话被摆渡了（ai-title→首问→项目名降级链）。
10. 作为验证执行者，我有 tap/diff/replay 工具和场景矩阵，能拍基线、做差分、验等价，最后按门禁清单人工放行切换。
11. 作为 CC 用户，会话里的小请求（如标题生成）不会顶掉心跳保的主上下文前缀。

## Implementation Decisions

### 渡口（internal/dock 新包）

- **opt-in 启动（F11 裁定，seam）**：`[dock]` 配置节缺失＝渡口完全不启动（不绑 15722，零行为变化）；存在＝监听 `127.0.0.1:15722`（绑本机、入站不鉴权、占位令牌照抄不校验）。
- **透传实现**：`httputil.ReverseProxy`＋Rewrite API（forwarder.go 同款：`SetURL`＋`Out.Host=target.Host`＋`FlushInterval:-1`）——实验已证不加 XFF、Host 可控、体逐字节同。超时对齐 `API_TIMEOUT_MS`=50min，SSE 不断流。
- **内存快照（F2/F3/F4）**：仅 `/v1/messages` POST（排除 count_tokens 子路径）：
  - 内容＝请求体＋最小必要头集（content-type、anthropic-version、anthropic-beta、user-agent、accept、accept-encoding）；**auth 类头不入快照**（渡口出站本就替换认证；beats 也走渡口）。全内存不落盘。
  - 选取＝每会话保留**最大请求体**为主快照（主循环单调增长⇒最大=最新主轮，辅助小请求永不覆盖），另存最后一份仅作漂移诊断。已知边界（F12/S2）：compaction/上下文收缩后最大体可能陈旧→该窗首跳 miss→停窗告警，一次有界浪费，注释与 runbook 显式记录。
  - Pinned 不变式：任一等待窗/问询窗开着的会话不可淘汰；LRU 只淘汰非 pinned（上限最近 16 个非 pinned）；清理晚于窗口收尾与最后一跳结算。daemon 重启后内存快照必然丢失：窗口仍调度则该跳记 error（snapshot_missing），不阻塞调度。
- **改写五件（rewrite_enabled=true 才生效，默认 false）**：
  1. 模型映射六键：claude-opus-5/claude-fable-5-1/claude-sonnet-5/claude-haiku-4-5 别名→GLM 档（映射表可配）；default 兜底；子代理真名精确保留（剥 [1M] 后精确匹配映射表已知真名则原样透传）。
  2. 剥模型名 `[1M]` 后缀，大小写变体兼容。
  3. 换上游真钥：出站 `Authorization: Bearer <key>`，删 x-api-key 与入站 Authorization。真钥只活本机 config.toml（T39 永不入库）。
  4. text-only 图片降级：目标模型在 text_only 列表且请求含 image block→文本占位（GLM-5.3 在名单）。
  5. 出站头卫生：host 换上游；追踪/CDN 头删；anthropic-beta 重建（保留客户端全部 beta＋确保含 `claude-code-20250219`）；anthropic-version 透传；UA 保留。
- **双改写守卫（F10/S3）**：启动校验＋doctor——`rewrite_enabled=true` 且 upstream 主机归一化后（localhost/::1/127.0.0.0/8 全归一）指向已知本地中转端口（15721/15722/15723）→ 拒绝进入改写模式（退回纯透传）并告警。
- **dock 科目（F13）**：透传与改写两种模式都入账——每请求一行：改写前后模型名（透传时同值）、四列 token、延迟、状态码、session、ts。纯元数据。
- **形态漂移告警**：登记见过的 anthropic-beta 标记与顶层参数键集合，新出现即日志＋pushover 告警。
- **余额查询**：`GET /user/balance`（bigmodel API，[dock].api_key），只读，report/面板展示。
- **配置（config 重启生效，热加载后置）**：`[dock]` upstream_base_url / api_key / model_map / text_only / rewrite_enabled。

### 心跳（internal/beat ＋ internal/daemon/watcher.go）

- **HttpBeatSender 实现 beat.Sender**：按 plan 的 session 取渡口内存快照（主快照=最大体），原样重放、唯一改写 max_tokens=1，带快照头集（auth 类按占位令牌字面量重建）发往渡口入站口 15722（与真流量同路径同改写）；秒级超时。
- **错误语义（F1，唯一语义）**：传输错误（429/5xx/网络错/超时）→ 本跳作废跳过、绝不重试，按 ERROR 语义入账与连击；**miss 绝不重试**。`internal/beat/beat.go:64-67` 旧注释（"重试 1 次"）随实现更正。
- **SSE 解析**：以 `message_delta` 的 usage 为准（覆盖 `message_start` 的 0）；发送侧固定可解析的 accept-encoding（或 identity），解析前处理解压，单测覆盖压缩响应（S4）。
- **头集等值断言（S1）**：HttpBeatSender 测试夹具以 Q14 捕获件的实际头集为参照，断言快照头集 ⊇ 实验重放集（减 auth）。
- **排跳判据（F8）**：排跳 ⇔ 等待窗开着（含停车未过期窗＝异步子代理仍在跑；停车满 1h 懒过期即停）；主会话恢复写入立即停。纯工具等待（无子代理）本版不保温（扩范围须用户决策）。
- **熔断作用域（F9）**：问询守望保持现行全局 Breaker（2 MISS→降级 enforce→observe、3 ERROR→暂停窗口剩余跳）；等待窗按窗计（1 MISS→停本窗剩余跳＋告警＋建议复测 TTL；连续 3 transport-ERROR→停本窗）；全局单在途跨泳道共用（多窗排队，绝不并行）。
- **策略接线**：间隔与等待上限只从 internal/policy 计算器出（公式单源；手动只能调低）；逐跳入既有 beat 科目（带泳道标记），无效保温单列入账。
- **双泳道**：`serve.go` 注入 HttpBeatSender（现为 nil）——问询守望 enforce 即刻受益；observe 演练语义不变。
- **配置**：`[wait_window]` 独立三态 off/observe/enforce（默认 off），与 `[question_watch]` 平行；校验：enforce＋渡口关 → 拒绝真发并告警（依赖快照源）。

### 常驻保障（internal/installer）

- 注册表 HKCU Run 键登录自启（无窗口后台启动），安装/卸载幂等可回退。
- 计划任务每 5 分钟看门（F5）：HTTP 健康端点探活（短超时，非探进程）；**无监听才拉起**；端口被占/进程在但无响应＝只告警不双拉（单实例）；钩子自举继续兜底。doctor 报 Run 键＋看门两状态；runbook 有停用流程。
- 形态：`ferryman watchdog` 子命令（cmd/ferryman），schtasks 注册调用。

### 验证套件（experiments/router-fidelity/，实验代码不进 daemon 二进制）

- `tap.go`：上游捕获转发器（forwarder.go 变体，监听 15723，捕获含头与体；产物本地文件权限收紧、验完即删、永不入库）。
- `diff.py`：两份捕获逐字段 diff（白名单忽略 ts/session_id 类）。
- `replay.py`：差分重放器（q14s3 发送器改造：同一入站请求喂两条路径）。
- `scenarios.py`＋README：场景矩阵生成骨架（四档别名/子代理真名/[1M]/多轮/图片/长前缀/ai-title 小请求含 session_id 归属实测）；L1 形状差分→L2 行为对照（cacheRead 探针＋套餐 4 格）→L3 全功能清单与四条决策门。

### 通知（internal/notify）

- pushover 标题 `Ferryman｜<项目名>：<会话标题>`；降级链 ai-title→首问截断→仅项目名；sid 等标识移正文尾部小字；notify 管道加宽＋测试钉死。

### 红线（全部继承，实施不得违反）

ADR-0004 无条件全局保活禁止（opt-in 默认关，三态永不自动升级）；隐私不变量（台账/账本/dock 流水永不落消息内容，快照全内存）；真钥只活本机 config.toml 永不入库（T39）；miss/传输错误绝不重试；不动项（强续/grill/停车窗 1h 语义/闸门钩子）；摆渡路由与渡口零共用；公式单源；SSE 立即冲刷；出站头必含 claude-code-20250219；激活顺序不可跳步（基线→透传→L1-L3→改写）。

### 实施合同（F6）

全部实施在本夜链分支（xcheck-night-20260919-195118）；原分支（main）零 commit/push/pull/rebase；操作者未提交内容（NIGHT 附表）原样保留，票路径与之零相交；合并/部署/真实切换（改 CC base_url、开 rewrite_enabled、跑真流量验证）一律是人的早晨决定。

## Testing Decisions

- 全部单测走既有 go test 惯例（各包 `*_test.go`）；只测外部行为：
  - dock：快照生命周期（largest-wins/pinned-LRU/清理时序/snapshot_missing 分支）、透传保真（体逐字节＋头集合除 Host/逐跳外逐项相同、零新增头；夹具带全套头，F7 实验同款断言）、无 [dock] 零行为、改写五件逐件（真名保留/[1M] 大小写/beta 重建含标记/图片降级/头卫生）、双改写守卫（含回环别名归一化）、dock 科目行形状。
  - beat：HttpBeatSender（mock 上游：max_tokens 改写、传输错误跳过不重试＋ERROR 计账、message_delta usage 覆盖、压缩响应、头集等值断言）；beat.go Classify/Breaker 既有测试零回归。
  - daemon：等待窗排程（窗开含停车未过期即排、1h 过期即停、主会话恢复即停、1 MISS 停窗、3 ERROR 停窗、policy 接线、enforce＋渡口关拒绝真发）、问询守望既有测试零回归（sender 注入后 observe 语义不变）。
  - installer：Run 键注册/卸载幂等性（命令构造层单测；真实注册表操作走 runbook 人工冒烟清单）、看门判定（无监听拉起/占用告警不双拉——HTTP mock）。
  - notify：标题降级链钉死。
- go test ./... 全绿为票验收底线；-race 在本机不可用走既有人工补偿条款。
- L1/L2/L3 真流量验证为人工步骤（工具交付即止，夜间不烧积分）。

## Out of Scope

多供应商热切换、Codex 轨迁移、config 热加载、渡口入站鉴权、Windows Service、闸门钩子安装、cc-switch 退役、纯工具等待窗保温、jsonl 重构（已废弃路线）。

## Further Notes

- 开放点（验证阶段）：claude-code-20250219 对套餐的真实作用（L2 4 格定严格度）；count_tokens 存在性（场景矩阵实测，非 messages 路径一律透传、含 model 则同映射）；多轮 wire 样本补齐；辅助请求 session_id 归属（F3 的 L1 项，主快照策略不依赖它）。
- Q14 工程雷继承：GLM SSE 真 usage 在 message_delta；重放头部跳过转发器脱敏残留值（U+2026）并按占位令牌字面量重建；SSE 缓冲＝重试风暴。
- 相邻既有测试若因 sender 注入等接线失败，修复接线不改测试语义。
