# 接法乙代价评估 —— dsh 接管路由从 pi-ai 换 llm-deepseek 适配器（只读 spike）

> 票 02（spec「接法乙 spike」节）· 2026-10-03 · 报告性质：只读评估，零业务代码改动。
> 源码依据：dsh 调研克隆 `C:/Users/allan716/AppData/Local/Temp/dsh-research/`（master@639ed015，只读；下文 dsh 侧 file:line 均相对克隆根）＋ 本仓 `internal/dock/`、`internal/provider/dsh.go` 现行实现。
> 读者＝晨间拍板人。四节：①方言清单（逐条 file:line）②渡口改写面工作量 ③与现 pi-ai 路行为差异 ④结论建议＋切换步骤。

## 0. 一句话结论

**代价＝天级偏低（核心工程 ≈1 人日，含验证与 ADR 合计 ≈1~1.5 人日），且方言面大头可用 dsh 侧配置抑制，渡口硬改写只剩两件小活**（剥 `dsh_*` 请求体字段、剥 `x-deepseek-harness-*` 出站头）。建议**条件切**：接法乙是 dsh 会话拿到「渡口捕获→心跳保温→摆渡→闸门」全套同权的**唯一路径**（P2-4 插件直报给不了请求体捕获，心跳重放必须有快照体），条件＝最小方言配置＋渡口两件剥除先行＋三项真机验证过（见 §4.3）。最大未知是缓存经济（§3.3）：llm-deepseek 永不发 `cache_control`，若账面 A/B 显示切换后缓存命中崩零，需追加「渡口注入 cache_control」件（另 ≈0.5~1 人日，有 codex_cache_inject.go 先例）。

---

## 1. DeepSeek 方言清单（llm-deepseek 适配器 vs 标准 anthropic-messages）

### 1.0 基线与一个结构性发现

- 传输层是标准 anthropic-messages 骨架：`POST {baseURL}/v1/messages`（`messages-api.ts:14-17` 自动补 `/v1`，与 pi-ai 同路径，`internal/provider/dsh.go:16-17` 注释钉死）、SSE 标准事件名、usage 四列字段名全标准（`translate.ts:36`）。
- **结构性发现：方言是能力驱动的。** 大半方言项只在 catalog 模型条目声明对应能力标志（`systemPromptUpdate`/`toolUpdate`/`inputModalities` 含 image）或思考开启时才上线——而能力标志**缺省即关**（`config.ts:74` inputModalities 缺省 `['text']`；`types.ts:31-43` 两标志缺省 omitted；`model-info.ts:78-79` 只有声明了才上报运行时）。即：**接法乙的方言面可以靠 patch 里的 models 配置压到最小集**（§2.2），渡口不必逐条兜。
- 例外（配置压不掉的硬核）：`thinking` 参数恒上线（启用/停用二值、无 budget_tokens）、思考开启时的 `output_config.effort`、`dsh_*` 两个插件贡献体字段、三个 `x-deepseek-harness-*` 归因头。

### 1.1 请求头方言

| # | 头 | 值 | 触发条件 | 出处（克隆内 file:line） |
|---|---|---|---|---|
| H1 | `x-deepseek-harness-user-id` | home 级匿名 UUID | 每个模型请求恒带 | `packages/llm/llm-deepseek/src/adapter.ts:128` |
| H2 | `x-deepseek-harness-session-id` | 请求 `sessionId` 原串（＝会话头行 id `session-<uuid>`） | 带 Session 的请求（dsh 会话流量即恒带） | `packages/llm/llm-deepseek/src/adapter.ts:129` |
| H3 | `x-deepseek-harness-compact: 1` | 字面量 1 | purpose=compaction 的辅助请求 | `packages/llm/llm-deepseek/src/adapter.ts:130` |
| H4 | `anthropic-beta: files-api-2025-04-14` | beta 标记 | 仅当请求带 file 引用图 | `packages/llm/llm-deepseek/src/messages-api.ts:4`、`adapter.ts:114-115` |
| H5 | `anthropic-beta: mid-conversation-tool-changes-2026-07-01` | beta 标记 | 仅当体含 `tool_addition`/`tool_removal` 块 | `packages/llm/llm-deepseek/src/messages-api.ts:7`、`adapter.ts:116-118` |
| H6 | `x-api-key`（鉴权） | 凭据值 | deepseek-official 路由恒带 | `packages/llm/llm-deepseek-api-key/src/index.ts:41`（另 llm-deepseek-account 路是 `x-dsh-auth-token`，`llm-deepseek-account/src/index.ts:25`，接管不用它） |
| — | `user-agent: deepseek-harness/<版本> (+url)` | 应用归因 | 两路同源恒带，**非差异** | `packages/llm/llm/src/attribution.ts:40-44,64-68`（pi-ai 路同样经 `attributionHeaders()`，`llm-pi-ai/src/adapter.ts:206-212`）——渡口 agent=dsh 归因（`internal/dock/server.go:613-615`）不受切换影响 |

头字段契约总表：`docs/deepseek-llm-api-wire-extensions.md:22-31`（克隆内，英文版；规定「网关经 baseURL 收到与官方端点同样的值」）。

### 1.2 请求体方言

| # | 字段/形状 | 私有点 | 触发条件 | 出处 |
|---|---|---|---|---|
| B1 | `thinking: {type:'enabled'\|'disabled'}` | **恒上线**；enabled 形**不带 `budget_tokens`**（标准 anthropic 的 enabled 必带） | 所有请求 | `packages/llm/llm-deepseek/src/serialize.ts:154`、`wire-types.ts:30` |
| B2 | `output_config: {effort:'low'\|'high'\|'max'}` | 顶层私有参数（标准无此键） | effort≠off 时（缺省 effort=high） | `serialize.ts:146-148,155`、`wire-types.ts:31` |
| B3 | `dsh_plugin_packages` | 顶层插件清单字段（`dsh_` 前缀保留域） | 贡献者插件启用（缺省 true）即每请求 | 合并点 `serialize.ts` 之外的 `request-extensions.ts:36-44`＋`adapter.ts:106-112`；贡献者 `packages/llm/plugin-package-inventory-deepseek/src/index.ts:27,39,196` |
| B4 | `dsh_session_log` | 顶层**全会话日志后缀**（可达 8 MiB/请求；含 cwd、系统提示快照、用户/助手全文、工具参数结果） | 贡献者插件启用（缺省 true）＋有活会话 | 贡献者 `packages/session/session-log-deepseek/src/index.ts:34,52-53,185`；线语义 `docs/deepseek-llm-api-wire-extensions.md:74-156`；暴露面声明同文档 `:158-162` |
| B5 | `tools[].defer_loading: true` | 工具级私有字段 | 路由声明 toolUpdate 能力＋运行时投影 | `serialize.ts:164`、`wire-types.ts:38-39` |
| B6 | `tool_addition`/`tool_removal` 内容块 | 私有块类型（`tool:{type:'tool_reference',name}`） | 同上（对话中工具增删） | `serialize.ts:94-95`、`wire-types.ts:12`；beta 头 H5 联动 `adapter.ts:116-118` |
| B7 | `messages[]` 内 `role:'system'` | 标准 anthropic 不允许消息数组内 system 角色 | 路由声明 `systemPromptUpdate:'in-history'`＋对话中有中途系统更新（developer 消息投影） | `serialize.ts:99,111`、`wire-types.ts:19`；能力开关上报 `model-info.ts:78` |
| B8 | `image.source:{type:'file',file_id}` | 私有图片源变体 | 路由声明 image 模态＋Files API 可用 | `wire-types.ts:7`、`serialize.ts:76`；beta 头 H4 联动 |
| B9 | `system` 为拼接单串 | 形状差异（标准兼容：串或块数组皆合法），非拒收点 | 恒为串 | `serialize.ts:150,156` |
| B10 | `max_tokens` 缺省 256,000 | 缺省值差异（pi-ai 侧由 profile 推导） | 未显式给 maxTokens 时 | `packages/llm/llm-deepseek/src/defaults.ts:8`、`serialize.ts:153` |
| B11 | `stream: true` 恒流式 | 无非流式分支 | 恒 | `serialize.ts:152` |
| B12 | **无 `cache_control`** | llm-deepseek 序列化器**从不发任何缓存断点**（wire 类型里无此字段） | 恒 | `wire-types.ts:24-41` 全类型无 cache_control；`serialize.ts` 全文无写入 |
| B13 | 无 `metadata` | 与 pi-ai 同（P2-2 已钉死 pi-ai 也不写 metadata.session_id） | 恒 | `serialize.ts` 全文；P2-2 结论见 `internal/dock/snapshot.go:249-254` 注释 |

### 1.3 响应面（客户端期待，非渡口改写面，但决定上游应答兼容度）

- 事件/字段全标准：`message_start/content_block_*/message_delta/message_stop`、usage 四列（`translate.ts:36,104-166`）、thinking 块含 `signature`/`signature_delta`（`translate.ts:51-54,78-81`）。
- **客户端严格性强于 pi-ai**：响应块只认 `text/thinking/tool_use`（其余抛 UNSUPPORTED_CONTENT，`translate.ts:60`）；stop_reason 只认 `end_turn/stop_sequence/tool_use/max_tokens`（其余 MALFORMED，`translate.ts:90-97`）；未见 `message_stop` 即 STREAM_CLOSED（`translate.ts:165`）；容忍未知**事件类型**（`translate.ts:116-118`）。
- 错误面期待标准 `{error:{type,code,message}}` 信封＋`retry-after`；请求 id 探测 `request-id`/`x-request-id`/`x-deepseek-request-id`（`transport.ts:36-38`，私有头仅此一处、缺失无害）。
- thinking 回放：助手历史 thinking 块带 signature 上行（`serialize.ts:31-34`）；跨模型即静默降级为无 signature（`replay.ts:63`），不炸。

### 1.4 关联面（仅当 catalog 声明 image 模态才出现）

- **Files API**：图片先 `POST {baseURL}/v1/files` 上传换 file_id（`packages/llm/llm-deepseek/src/files-api.ts` 的 `DeepSeekFilesClient`）；上传失败→`FileResolutionFailure`→**当次请求回退内联 base64**（`request-files.ts:15-20`、`adapter.ts:93-100`）——即渡口**不实现** `/v1/files` 也不会断请求，只是每带图请求先白付一次「渡口→上游」的 404 往返。
- 文本-only catalog（缺省）下整个图片/Files 面不上线（能力驱动，§1.0）。

---

## 2. 渡口改写面工作量评估（internal/dock/ 逐条）

现状锚点：改写模式只动顶层 `model` 与 `messages`（图片降级）两键，**其余顶层键原样透传**（`internal/dock/rewrite.go:11-13,64-100`）；出站头卫生剥追踪头＋换真钥＋重建 beta（`internal/dock/headers.go:29-51,74-91`）；会话归因三回落已含 dsh 头（`internal/dock/snapshot.go:269-282`）。

### 2.1 逐方言项处理方式与量级

| 方言项 | 渡口处理 | 模块/落点 | 量级估算 |
|---|---|---|---|
| B3/B4 `dsh_plugin_packages`/`dsh_session_log` | **剥**（顶层键删除后重编码）。隐私刚需：`dsh_session_log` 含全会话明文（§1.2 B4），绝不可送 GLM/Kimi；CC 流量永不带这两键，键名无歧义，**无需按 UA 分岔** | `internal/dock/rewrite.go` `Rewrite()` 解析后 delete 两键（防御纵深——即使 patch 侧已关贡献者） | 实现 ~8 行；表驱动测试 ~40 行（含「CC 体带同名键也不误伤语义」与重放同路径断言） |
| H1/H2/H3 `x-deepseek-harness-*` 三头 | **剥**（出站前缀剥除）。归因在入站侧已完成（`server.go:449-452` 先于 sanitize；快照头白名单 `snapshot.go:31-38` 本就不含它们），出站剥除零代价 | `internal/dock/headers.go` `stripPrefixes` 加 `"x-deepseek-harness-"` 一项 | 实现 1 行；测试 ~10 行 |
| B1 `thinking:{type:'disabled'}` | **不动**。disabled 形状是标准 anthropic 合法值；GLM 侧 ferry 重放今天就在用 disabled（ADR-0016 线） | 无 | 0（验证项 V2 覆盖） |
| B1' `thinking:{type:'enabled'}`（无 budget_tokens）＋B2 `output_config` | **最小路线不出现**（patch 配 `thinking: disabled`，§2.2）；若将来开思考档：`output_config` 须按 UA 门控剥除（CC 原生有此键不能全局剥）＋enabled 无 budget_tokens 的上游接受度须先验（GLM 可能 400） | `rewrite.go`＋`server.go` UA 分岔（参照 `server.go:613-615` 判据） | 思考档追加 ~30-50 行＋测试（**本票不计入**，列为条件项） |
| B5/B6/H5 defer_loading＋tool 增删块＋beta | **配置抑制**：catalog 不声明 `toolUpdate` → 运行时不投影、键不上线（§1.0 能力驱动） | 零代码；patch models 条目写法约束 | 0（验证项 V1 覆盖残险） |
| B7 消息内 system 角色 | 同上：不声明 `systemPromptUpdate` → 前导 system 参数化 | 零代码 | 0（同上） |
| B8/H4 file 图片源＋files beta | 同上：catalog 不声明 image 模态 → 图片面整体不上线（与今天 pi-ai 文本-only 路对称：pi-ai 对无 image 能力模型同样拒图，`llm-pi-ai/src/adapter.ts:358-364`） | 零代码 | 0 |
| B9 system 单串 / B11 恒 stream / B13 无 metadata | 不动（标准兼容；渡口 `bodyIsStream`/记账/快照全兼容，`server.go:506,532-541`） | 无 | 0 |
| B10 `max_tokens` 缺省 256k | **配置钉值**：patch 显式 `maxTokens`（可配，`config.ts:86`），防超上游上限 400 | patch 生成器 | 0（配置项） |
| B12 无 cache_control | **不能配置补**——llm-deepseek 序列化器无此概念。若上游靠显式断点缓存（V3 验证），需渡口新件「dsh 车道 cache_control 注入」（先例 `internal/dock/codex_cache_inject.go`） | 潜在新件 | 条件项：~0.5-1 人日（**仅在 V3 验出缓存崩零时**） |
| 响应面严格性（§1.3） | 不动（上游今天服务 CC 同事件面；dsh 客户端严格性是其自家风险） | 无 | 0 |

### 2.2 两档配置路线（dsh 侧 patch 决定方言面大小）

- **最小方言档（推荐，本报告工作量口径）**：patch 配 `thinking: disabled`（此时仅容 effort=off，`config.ts:209-212`）＋ models 条目只写 `id/contextWindow`（能力标志全缺省关）＋显式 `maxTokens` 钉值＋关两个贡献者插件（§2.3）。上线方言＝H1/H2/H3 三头＋`thinking:{type:'disabled'}`＋标准骨架——**渡口只需 §2.1 前两件剥除**。
- 全方言档（开思考/开图片/开 in-history）：方言全表上线，渡口逐条兜（UA 门控剥 output_config、tool 块翻译或剥除、files 路由应答……），量级升到 2~3 人日＋上游兼容风险面扩大。**不建议**；缺省 catalog（deepseek-flash/deepseek-v4-pro，`models.ts:6-21`）就是全方言形状，patch 必须覆写 models。

### 2.3 provider apply（patch 生成器）改造

`internal/provider/dsh.go:132-158` `dshHomePatchYAML` 是接管形态唯一源（「改这里＝改接管形态」）。切接法乙＝字面量 v2：

```yaml
# 形状示意（生成器产物，非手改文件）
- id: llm-deepseek-api-key          # 插件名（llm-deepseek-api-key/src/index.ts:12）
  name: '@deepseek-ai/dsh-llm-deepseek-api-key'
  config:
    baseURL: http://127.0.0.1:15722   # messagesApiRoot 自动补 /v1（§1.0）
    apiKeyEnv: FERRYMAN_DOCK_TOKEN    # 缺省 DEEPSEEK_API_KEY，须覆写（config.ts:16）
    thinking: disabled
    maxTokens: <上游安全值>            # 防 256k 缺省超限（B10）
    models:
      - id: claude-opus-5
        contextWindow: 200000
      - id: claude-sonnet-5
        contextWindow: 200000
- id: plugin-package-inventory-deepseek   # 关贡献者（enabled 缺省 true）
  config: { enabled: false }
- id: session-log-deepseek
  config: { enabled: false }
- id: agent-default-model
  config: { provider: deepseek-official, model: claude-opus-5 }   # 路由 id 硬编码（index.ts:15）
```

量级：字面量 ~25 行＋既有钉字面量测试更新 ~30 行；幂等/带标记重铸/还原纪律全部现成（`dsh.go:79-102,190-212`），回退＝重铸回 pi-ai 形态。doctor 现无 dsh 探针（grep 证实），无期望要改。

### 2.4 拿来即用、零改动的面

会话归因三回落（`snapshot.go:269-282`，P2-2 已上）直接接住 H2 → 快照捕获＋dock 科目归因即刻有键；usage 四列扫描（`server.go:748-837`）与标准 usage 字段兼容；首包闸门（`server.go:506`）对恒流式请求天然适用；错误形状（`errorshape.go:47-66` 的 anthropic 信封＋Retry-After）正是 llm-deepseek `transport.ts:36-37` 认的形状；排水/漂移/守卫零涉及（漂移对新键 `dsh_*` 会在基线后告警一次——贡献者已关则不出现；若走「只靠渡口剥、patch 不关贡献者」路线，则首日各告警一次，可当切换确认信号看，`drift.go:79-96`）。

---

## 3. 与现 pi-ai 路的行为差异

| 维度 | 现状（pi-ai 路接管） | 接法乙后（llm-deepseek） | 差异评级 |
|---|---|---|---|
| **鉴权** | apiKeyEnv→pi-ai api_key 凭据→anthropic 线 `x-api-key` | `x-api-key`（`llm-deepseek-api-key/src/index.ts:41`）；渡口两路都出站替换真钥（`headers.go:48-49`） | **无差异**（.env 占位令牌同键复用） |
| **会话键（切点本尊）** | 无任何键上线：体无 metadata.session_id、affinity 头被 catalog 标 withhold 不可开（P2-2 四源钉死，`snapshot.go:249-254` 注释＋调研 §3.4 结案） | 每请求 `x-deepseek-harness-session-id`（值＝台账现行键，零换算）→ 渡口第三回落现成接住：捕获、dock 科目、后续 Pin/心跳全链有键 | **核心增益**：dsh 流量从「半残户」变可捕获可保温 |
| **流式** | SSE 标准事件 | 同；但 dsh 客户端校验更严（§1.3：未知块类型/stop_reason 硬错） | 低风险（上游今天服务 CC 同事件面；列为 V2 观察项） |
| **缓存语义** | pi-ai 是否发 `cache_control` **未钉**（SDK 未随克隆分发，源码级不可证；10-02 验收仅证「可通」） | **从不发 cache_control**（B12）→ 完全依赖上游隐式前缀缓存 | **最大未知**：若 pi-ai 今天在发断点而 GLM 靠断点缓存，切换后缓存命中可能崩零 → V3 账面 A/B 定夺，崩零则补注入件（§2.1 末行） |
| **错误面** | pi-ai SDK 自行归类 | 认标准信封＋Retry-After＋request-id 三探（`transport.ts:22-44`）——渡口自产错误形状（ADR-0017）直接可读 | **兼容**；渡口 5xx 细类会被归 SERVER 重试，与 CC 车道同待遇 |
| **请求形状** | pi-ai 序列化（SDK 内部） | §1.2 全表；最小方言档下与 CC 车道差异仅 `thinking:disabled` 形＋被剥的 dsh 头 | 最小档≈无感；B10 max_tokens 须钉值 |
| **功能面** | `GenerateOptions.stop` 直接抛不支持（`llm-pi-ai/src/adapter.ts:334-336`） | 支持 `stop_sequences`（`serialize.ts:158`） | 微增益 |
| **心跳重放归因（依赖项）** | 无键，N/A | beat 重放请求头由发送侧重建，快照头白名单不含 dsh 头（`snapshot.go:31-38`）——dsh 会话的重放行归因要 **P2-5** 落「非 UUID36 键改发 dsh 头」（httpsender/appendreplay 两处）才齐 | 不阻塞切换；切换后 P2-5 从「设计点」变「必做跟进」（保温字节流本身不受影响，丢的只是重放行的会话归因） |

---

## 4. 结论

### 4.1 代价量级

**天级偏低：核心渡口改写＋patch 生成器 ≈1 人日**（§2.1 前两件 ~60 行实现＋~50 行测试＝半天；`dsh.go` v2＋测试＝半天），加 ADR＋晨报半天，加切换当日真机验证 1-2 小时与账面观察数日——**合计 ≈1~1.5 人日**。条件追加项不计入：思考档（+0.5 人日级）、cache_control 注入（仅 V3 验出崩零时 +0.5~1 人日）、P2-5 重放头（另票）。

### 4.2 建议：**条件切**

- **切的论据**：接法乙是 dsh 会话进「捕获→心跳保温→同模型摆渡→闸门」全套渡口机械的**唯一入口**（P2-4 插件直报补台账活动，但心跳重放必须有请求体快照，插件面拿不到）；顺带 H3 compact 头免费给出压缩轮信号。成本侧：方言大头配置抑制＋两件小剥除，量级一天级，回退现成（provider apply 重铸）。
- **不切/暂缓的论据**：若晨间判定 dsh 只需要「台账活动＋闸门」（P2-3 桥＋P2-4 插件路径已覆盖），渡口捕获与保温暂不为 dsh 启用，则接法乙唯一独占增益（会话键→快照）用不上，pi-ai 路零方言零维护更省。
- **条件（切的前置）**：①渡口两件剥除先行合入；②patch 走最小方言档（§2.2）；③三项真机验证（§4.3）过；④P2-5 重放头排期确认。

### 4.3 若切——具体步骤清单（晨间人操作）

1. **渡口先行**：合入 §2.1 两件剥除（rewrite.go 剥 `dsh_*` 键＋headers.go 剥 `x-deepseek-harness-` 出站前缀）＋测试，随版发布（独立于切换，先行无害）。
2. **生成器 v2**：`dshHomePatchYAML` 换 §2.3 形状（含关两贡献者＋thinking disabled＋maxTokens 钉值），跑 `go test ./internal/provider/ -run DSH`（落日志惯例）。
3. **执行切换**：`ferryman provider apply`（带标记旧 patch 自动整体重铸；`.env` 令牌行不动）。
4. **验证三件**：
   - V1 抓线：mock 端点（P2-2 同款手法）收 dsh 一轮请求，断言＝最小方言（无 `dsh_*` 体键、无 H4/H5 beta、`thinking:{type:'disabled'}`、H2 头在、model 映射后正确）。**须写 dsh 配置，属晨间人操作。**
   - V2 上游接受度：对当前 active 上游（GLM）真发一条 `thinking:{type:'disabled'}`＋钉值 max_tokens＋无 cache_control 的 /v1/messages，确认 2xx＋SSE 四列 usage 正常。
   - V3 缓存 A/B：切换前后各取一天台账 dock 科目 dsh 行，比对 `cache_read_tokens/input_tokens` 占比；崩零 ⇒ 立项 cache_control 注入件（§2.1），期间可接受缓存退化或回退。
5. **观察**：台账 dsh 行 `session_id` 由恒空变 `session-<uuid>`；形态漂移面板应零新增（贡献者已关）；doctor 跑一遍无回归。
6. **回退预案**：`provider apply` 重铸回 pi-ai 形态即回（`dsh.go:98-100` 带标记重铸路径现成）；上游侧无任何残留状态。

### 4.4 风险与开放问题

| # | 风险/未知 | 评级 | 处置 |
|---|---|---|---|
| R1 | 缓存经济（§3）：pi-ai cache_control 发射未钉＋llm-deepseek 恒不发 | **高影响、可验证** | V3 A/B 定夺；崩零走注入件预案 |
| R2 | 上游对 `thinking:{type:'disabled'}`/钉值 max_tokens 的接受度 | 低（GLM ferry 线已在用 disabled；ADR-0016） | V2 一条 curl 定夺 |
| R3 | dsh 是 developer preview，插件 id/配置键漂移会让「patch 关贡献者」失效 | 中 | 渡口剥除是键基、版本耐受，兜底；V1 每次升级 dsh 后重跑 |
| R4 | 关 `session-log-deepseek` 是插件全局开关：若将来并行用 deepseek-account 直连路，其会话日志上传也一并关 | 低（当前全接管无并行直连） | 混合用法时重开并依赖渡口剥除兜底 |
| R5 | dsh 客户端响应严格性（§1.3）在上游发非标准块/stop_reason 时硬错 | 低 | 观察项；上游今天服务 CC 无此形状 |
| R6 | 运行时是否严格按能力标志投影 developer 消息（§1.0 推断链） | 低 | V1 抓线断言覆盖（若仍见 system-role 消息，属 dsh 侧意外，回退或补剥） |
| R7 | 心跳重放行归因缺 dsh 头（§3 末行） | 中（账面缺口，非功能缺口） | P2-5 落地（本役设计票 07 产出） |

---

### 附：证据与读法

- dsh 侧 file:line 均出自只读克隆 `C:/Users/allan716/AppData/Local/Temp/dsh-research/`（master@639ed015）；线扩展官方契约另见克隆内 `docs/deepseek-llm-api-wire-extensions.md`（中英双语）。
- 渡口侧 file:line 均出自本仓夜链分支工作区（`internal/dock/`、`internal/provider/dsh.go`、`config.example.toml`）。
- pi-ai 路既有事实（无键上线、affinity withhold）直接引用 P2-2 结案（`internal/dock/snapshot.go:244-261` 注释＋`docs/research/20261002_dsh-服务商配置与摆渡可行性调研.md` §3.4），未重复取证。
- 本 spike 全程零业务代码改动（git status 除本报告文件外干净）。
