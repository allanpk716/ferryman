# billion-context 持续压缩（ACP）源码级调研：省不省钱、能不能接

- 日期：2026-09-18
- 对象：github.com/ranxianglei/billion-context（代理，v0.1.118，commit 7bf7cde）+ github.com/ranxianglei/acp-kernel（压缩内核，v0.0.77，commit e8900f6）。两仓 2026-09-17/18 仍在推送。
- 方法：两仓克隆到临时目录逐文件读源码（只读），关键字符串用脚本实测字符数；外部口碑查 GitHub API / 搜索。全文源码路径相对仓库根。
- 背景对齐：我们链路 CC → cc-switch(127.0.0.1:15721) → GLM；价格 输入 6.9 / 缓存读 1.7 / 输出 24（积分/万 tok）；实测 GLM 前缀缓存 TTL≈10 分钟；我们账本 82% 成本是 cache_read（均值每请求重读 219k）。

---

## 0. 一句话结论

**方向对口：我们 82% 的成本花在"每请求重读自己的历史"上，ACP 的折叠正是直接砍这一块的机制；净省钱在数学上成立（一次折叠约 2~3 轮回本，之后每轮省 30~50%），但代价是每请求固定多付 1.5~2.5%、压缩质量风险由我们承担、且它默认的触发带按"声明窗口"算——接 1M 窗口时均值 219k 的会话根本不会触发压缩，必须显式把窗口配小才真正开始省。** 串联 cc-switch 在协议层可行（/bili/ 前缀支持 http、模型名原样透传），两处需实测确认（CC 是否发会话头、cc-switch 是否剥 tools 成员）。

---

## 1. 注入开销实测（工具 schema + 系统提示 + nudge + 标签）

实测字符数（脚本从源码提取字符串常量统计；token 按 3.5~3.8 字符/tok 估）：

**系统提示（Anthropic 线，wire 模式）**，出线总量 ≈ 9,400~9,500 字符 ≈ **2,450~2,700 tok**：

| 组成 | 字符 | 出处 |
|---|---|---|
| COMPRESS_PHILOSOPHY | 534 | acp-kernel/src/compression-rules.ts:12 |
| HOW_TO_COMPRESS_RULES | 5,306 | compression-rules.ts:18 |
| acpTags 段 | 510 | acp-kernel/src/compress-tools.ts FUNCTION_PROMPT_SECTIONS |
| tools 段（四工具说明） | 1,293 | 同上 |
| summariesInContext 段 | 813 | 同上 |
| MARKER_INTEGRITY_NOTE（防模型伪造压缩确认行，#717） | 563 | billion-context/src/compress-tool.ts:165 |
| 会话 id 注入行（模板 365 + UUID） | ~400 | compress-tool.ts:181 |

官方 fixture 可交叉验证：acp-kernel/tests/fixtures/prompt-function-default.txt = **8,510 字节**（未含 marker/conversation-id 两段，这两段是 billion-context 代理层加的）。

**工具 schema**：线上注 4 个（acp_cache 未上线，被 UNADOPTED_KERNEL_TOOLS 过滤，billion-context/src/compress-tool.ts:74；search_context 被加了 conversation_id 参数，#841）：

| 工具 | 序列化后约 | 出处 |
|---|---|---|
| compress | ~1,700 字符 | acp-kernel/src/compress-tools.ts:39-86 |
| decompress | ~700 | compress-tools.ts:376-401 |
| search_context（含 conversation_id） | ~570 | compress-tools.ts:403-418 + billion-context/src/compress-tool.ts:82-97 |
| acp_status | ~240 | compress-tools.ts:420-431 |

合计 ≈ 3,200 字符 ≈ **800~850 tok**。工具追加在 CC 自己的 tools 数组**尾部**（billion-context/src/server.ts:2598-2606，只补缺失项），顺序稳定。

**nudge**（触发时以临时 user 消息追加在消息列表最末，下一轮即消失，不进 CC 历史，server.ts:1926-1941 注释明说 "Ephemeral user message…prefix-cache-anchor safe"）：
- gentle 档 ≈ 效率说明 180 + breakdown ~150 + HOW_TO_COMPRESS 全文 5,306 + 可压缩范围表（随会话 300~1,500）+ 块图 ≈ 6,800~8,700 字符 ≈ **1,800~2,300 tok**（acp-kernel/src/nudge-text.ts:170-243）
- T2/T3 档再加 tier 规则（T2=2,953 / T3=2,055 字符，compression-rules.ts:55/87）+ 目标块清单 ≈ **2,500~3,100 tok**
- 触发频率受 growthFloor=50,000 tok 节流（acp-kernel/src/config.ts:16），即上下文每净涨 ~5 万 tok 至多触发一次/层

**<acp> 历史标签**：`<acp tokens="1.2K" type="user">m00005</acp>\n` ≈ 45 字符 ≈ **11~12 tok/条**，只打在 user/assistant 纯文本消息上（renderTags:"text-only"，server.ts:1897；tool-call 参数与 tool-result 内容不打，render-refs.ts:63-65）。

## 2. 前缀缓存友好的真实机制：一次压缩后什么变了

管线：`assign-refs → sync-blocks → prune → absorb-* → filter → hide-compress-calls → recommend → nudge-inject → emergency-truncate → render-refs`（acp-kernel/src/compress.ts:475-490）。

**折叠点在哪**：prune 把被覆盖消息从视图里删掉，换成一条 `role:"system"`、正文 `[Compressed conversation section] — topic` 的摘要消息，**插在压缩范围最早那条消息原来的位置**（prune.ts:106-118 锚点取 effectiveMessageIds 的最小索引；237-249 渲染）。所以：

- 折叠点之前（更早的摘要、更老的历史）：**字节不变**，缓存继续命中；
- 折叠点之后（新摘要 + 原范围之后的全部消息）：位移变了，首个请求**全部按新输入重付**。 billion-context 自己的观测日志也这么记：`[acp-compress-obs] … next-request cache ceiling ≥X%`（billion-context/src/stream.ts:106-123），并把分歧点定义为"折叠的第一个起点"（acp-kernel/src/cache-report.ts:12-16 "the prefix diverged at the fold's first start point"）。

**摘要是否字节级稳定**：是。block.summary 只在创建时赋值一次（compress.ts:988 是唯一赋值点），之后永不改写。T2 蒸馏/T3 凝缩是"消费旧块+创建新块"（旧块 active=false），新摘要锚定在被消费块的最早消息位置——**分歧点不会后移**，之前的前缀继续命中。ref 标签的 token 数也有冻结机制：tokenSnapshot 首次渲染时写入、此后复用（render-refs.ts:74-83，注释原文 "token count is fixed at first render (stable prefix cache)"）。

**为什么它对 GLM 这种"内容寻址前缀缓存"友好**：每轮 CC 全量重发历史 → bili 用持久化 state 确定性地重建同一折叠视图 + 新尾部 → 除尾部外字节一致。配套保险：nudge 只追加在尾部；系统提示只放静态文本（server.ts:2585-2588 注释）；BILI_MAX_SHRINK_PER_COMPRESS 开关还会引导模型做"小步、偏尾部"的折叠以保住前缀（compress-tool.ts:141-155，注释明说防 GLM 3007 风控）。

**代价**：压缩那一轮，compress 循环会用折叠后视图再发一次请求（core.ts:619 reRequest），折叠点之后的全部内容按新输入价重付一次；摘要本身是输出 token（24 积分/万，最贵的一种）。

## 3. 每请求固定改写：不开压缩也要多付多少

- **ref 标签**：每条 user/assistant 文本消息头部注入 `<acp …>mNNNNN</acp>`。ref 一次分配永不复用/不重排（refs.ts:69 已映射则跳过；错误提示也明说 "refs are per-session snapshots, assigned once"，compress.ts:318），tokenSnapshot 冻结 token 数 → **标签字节稳定，进前缀缓存**。
- **系统提示 + 工具**：每请求固定多 3.3k tok 左右（§1），会话内字节稳定（只有会话 id 那一行是会话级的，放在整段提示的末尾，跨会话共享在 CC 自己的系统提示之后才分叉，影响小）。
- 不做任何压缩时，**每请求新增成本 ≈ (3.3k + 11×文本消息数) tok**：首轮按输入价 6.9 计（≈2.3 积分），之后按缓存读价 1.7 计。500 条消息、其中 300 条文本的大会话 ≈ +0.56(提示工具) + 0.34(标签) ≈ **+0.9 积分/请求**，相对我们均值 45.4 积分/请求 ≈ **+2%**。injectTool=0 可关掉提示+工具（billion-context/src/config.ts:500），但标签仍会打（processTurn 无条件跑 render-refs）。

## 4. usage 透传：我们的账本数字还真实吗

**部分真实，压缩轮失真。** wire 模式下所有流式响应都过 runCompressLoop 的逐帧解析重组（server.ts:3885），不是字节直通：

- **普通轮**：round-1 的 `message_start`（含 input_tokens/cache_read/cache_creation）**原文转发**（loop/adapter-anthropic.ts:257-262，注释 "reaches the host verbatim — no rewriting"）；ping 原文（:263-264）；内容块帧原文转发（index 重映射，正常流数值相同）。**但结尾的 `message_delta`+`message_stop` 不转发原文**——由 buildTerminal 合成（adapter-anthropic.ts:177-198；core.ts:661 emitCompletion），usage 只含 {input_tokens, output_tokens, cache_read_input_tokens}，**丢掉 cache_creation_input_tokens**。CC 从 message_start 读 input（原文保真），所以普通轮的 input/cache_read 数字仍真实；GLM 无缓存写价，丢 cache_creation 影响小（不确定：CC 是否也读 terminal 里的 input 字段，若是则普通轮 input 也会被 terminal 覆写）。
- **压缩轮**（模型真调了 compress，循环最多 10 轮，core.ts:29）：CC 收到 round-1 的 message_start + **末轮**的合成 terminal。中间各轮的真实 usage 只进 bili 自己的日志（`[acp-usage] round N input=… cached=…`，core.ts:190-192）。即：**凡发生压缩循环的那一轮，CC jsonl 账本会低估真实花费**（看不到中间轮的输入大头和 round-1 的输出）。我们的对账回路接入后必须把 bili 日志纳入，否则账本对不平。

## 5. 与 cc-switch 串联可行性

链路：CC → bili（/bili/ 前缀）→ cc-switch(127.0.0.1:15721) → GLM。

- **http 上游支持**：/bili/ 前缀后可以直接嵌 `http://`（server.ts:134 `rest.startsWith("http://")`），也支持显式协议段 `/bili/anthropic/http://127.0.0.1:15721/v1/messages`（:122-133）。不写协议段时按路径嗅探，`/v1/messages` → anthropic（:824-834）——cc-switch 的端点正是 /v1/messages，能识别。
- **目标准入（#409）**：回环客户端允许访问回环/内网目标（tunnel-guard.ts 头注释原文："loopback/private destinations → allowed for loopback clients (the self-hosted-upstream case: sglang on 127.0.0.1:8199, ollama on 11434)"）。我们 CC 在本机 → 127.0.0.1:15721 放行；只有"转发到 bili 自己端口"的自环被拒。
- **模型名透传**：prepareAnthropic 重建体只动 messages/system/tools，`model` 原样（server.ts:1953 `{ ...parsed, messages, system, tools }`）→ CC 发的 claude-* 别名原样到 cc-switch，映射逻辑不受影响。
- **tools 字段会不会被 cc-switch 剥**：不确定，需实测。bili 把 4 个工具追加在 CC tools 数组尾部；cc-switch 的白名单按我们此前调研是请求级参数白名单（模型名、thinking、[1M] 等），而 CC 自己的 tools 必须透传给 GLM 才能编码，推断"成员级剥除"不太可能——但 ACP 工具名（compress 等）与 GLM 端的函数调用兼容性、以及 cc-switch 是否做工具名改写，需真机验证。降级路径：即使 tools 被剥，bili 还有 text 协议（`<acp_compress>` 文本触发器）但那只配了 Responses/Codex 线，Anthropic 线未配，等于压缩失能、只剩固定开销。
- **防双压**：bili→bili 有 x-bili-hop 标记自动直通（server.ts:865-878）；cc-switch 不是 bili，无冲突。
- **cache_control 保留**：CC 打的 cache_control 断点按消息 id 记录并在重建时回填（acp-kernel/src/wire/anthropic.ts:62-142、154-157；buildSystem 保 system 的断点 :52-58）。
- **注意**：重建请求体**不是字节保真**（anthropicToCore→coreToAnthropic 往返，块结构/键序/合并同角色消息都会变，wire/anthropic.ts:145-203）。bili 自身逐轮确定（缓存照常命中），但**切换当天 GLM 现存缓存池全量失效一次**——所有活跃会话首请求全款重付，选低峰切换。

## 6. 会话 ID：CC 会落到首条消息哈希回退吗

**题面过时了——首条消息哈希回退已被移除。** 现行机制（billion-context/src/session-id.ts + server.ts:1060-1230）：

1. 优先取客户端会话头，列表（session-id.ts:50）：`x-bili-plugin-conversation`、**`x-claude-code-session-id`**、`x-session-affinity`、`x-acp-session`、`x-session-id`、`x-opencode-session`、`session-id/_id`。取到 → 会话 id = 头值原样，clientProvided=true。
2. 源码两处断言 **CC 每个请求都带 `x-claude-code-session-id`**（session-id.ts:45-46 "the CLI's true per-session UUID"；server.ts:1227-1229 launcher 绑定依赖它保证 race-free）。本机 cc-switch 日志/DB 不记请求头，无法直接复核 → **待实测**（跑一次 bili 看会话日志即可确认）。
3. 无头时不再是 content-fingerprint 会话：#286 把指纹会话判定为有碰撞面，**空/纯系统请求直接 400**（server.ts:1173-1179）；#309 引入**前缀亲和**：按消息列表最长前缀匹配老会话，被截断的重放走 tail-window 重连，分叉即拆分（fork 语义，server.ts:1160-1192）。
4. **并发碰撞后果**：CC 多窗口各带唯一 UUID → 无碰撞。若头缺失：两个共享长前缀的会话在分叉前会并进同一压缩会话（状态交叉），分叉后下一请求拆开。**子代理风险**：CC 的 Task 子代理请求若复用主会话头而历史不同，会共用压缩状态——上游同样形态的 zcode 子代理问题有现成 issue #862（40 评论，open：子代理"stream truncated…90789 reasoning chars"后停摆）。CC 子代理是否带头、带什么头，接入前必须实测。

## 7. GLM 上下文窗口识别

**models.dev 有 glm-5.3**：billion-context 内置注册表快照（src/registry-snapshot.json，fetchedAt 2026-09-16，400 模型）含 `zhipuai/glm-5.3 → context 1,000,000`、`glm-5.3-flash → 1,000,000`。

但我们的链上 bili 看到的模型名是 CC 别名（如 `claude-sonnet-5[1M]`），registry 的 modelVariants 只剥 `-thinking/-fast/…` 推理后缀，**不剥 `[1M]`**（registry.ts:277-306），别名查不到 glm 的窗口时会落到内置表或 anthropic-beta 头。**显式配置路径**（CONFIGURATION.md L136-168，解析顺序：anthropic-beta 头 → providers 内 per-model 声明 → models.dev 注册表 → 内置表）：

```jsonc
// billion-context.json
{ "providers": {
    "http://127.0.0.1:15721": {        // key 必须带 scheme（L168 明说）
      "models": { "claude-sonnet-5[1M]": { "context": 1000000 } }
    }
} }
```

或全局 `compress.modelContextLimit` 一锤定音。**关键推论**：这个窗口是 nudge 的分母（usage = tokenCount / window），默认触发带 45%/75%/95%（acp-kernel/src/config.ts:10-20）。配 1M → 45 万 tok 才开始提示压缩；我们均值重读 219k 的会话**永远不会触发**。想省钱必须把声明窗口**故意配小**（如 300k：13.5 万 tok 进 gentle 带、22.5 万强制带），这是把"窗口"当"目标占用"用的调参思路。

## 8. 成熟度

- **测试**：billion-context 有 **200 个测试文件**，CI 矩阵 ubuntu+windows × node 22/24，typecheck+test+build（.github/workflows/ci.yml）。全部是 mock/进程内假上游（tests/e2e/chat-relay.ts、fake-completion.ts，本地 http server 断言转发体），**不测真模型**。真模型 e2e 单独一条 workflow_dispatch 手动触发（ci-e2e.yml：装 codex 0.147.0，用 secrets 的 E2E_UPSTREAM_URL），issue #329 显示跑过 glm-5.3 真实链路（warmup/load/compress/purity/forge）。对 GLM+Anthropic 协议+我们的链路组合，**仍是我们自己先小流量灰度**。
- **自动更新**：npm registry tarball + integrity/shasum 校验（update.ts:479-511），通道 latest/dev，原地替换+重启提醒（:119-143）。风险面：**挂着不管时链上组件行为随上游发版自动变**——建议关闭自动更新/锁版本，升级当变更管理做。
- **SSE 重写边界情况**：覆盖面比预期好——上游 error 事件走 in-band 错误路径不合成假成功（core.ts:331-348）；ping 原文转发；message_delta usage 的完整/残缺对象有不同采纳规则防 0 值覆盖（adapter-anthropic.ts:353-379，#299）；200 早断流零副作用重试一次（#413）；思考-only 空轮补一次 continuation 重试（#732/#821）；截断无完成事件会记弱溢出信号（#498/#887）。
- **节奏与状态**：8 周 118 个 minor 版本，40 open issues（billion-context）/63（acp-kernel）。迭代极快是双刃剑：bug 修复快，行为漂移也快。

## 9. 质量证据：祖源项目的真实用户反馈

- **DCP**（Opencode-DCP/opencode-dynamic-context-pruning，acp-kernel 压缩思路的直系祖先）：**4,228 星**、62 open issues，真实采用度最高。README 明确承认缓存代价："When DCP prunes content, it changes messages, which invalidates cached prefixes… You lose some cache reads but gain token savings… In most cases, especially in long sessions, the savings outweigh the cache miss cost."（和我们第 10 节的公式同构）
- **DCP #372**（+3）"Models become obsessed with pruning"：提示的紧迫语气让部分模型把压缩当主业，陷入 read→check-build→prune 循环、坚信自己的编辑没保存。ACP 的 nudge 文案已迭代（efficiencyNote 特意声明"这是效率提示不是溢出警告"，nudge-text.ts:20-22），但**风险同类，GLM-5.3 的服从度未验证**。
- **DCP #387**（+4）：社区要求压缩操作用独立廉价模型执行——说明"压缩本身耗好模型"是公认痛点。ACP 目前用主模型写摘要（输出价 24 计）。
- **fork 佐证**：keakon/opencode-dynamic-context-pruning（独立 fork）README 明说 "DCP is disabled for subagents… pruning could interfere with this summarization behavior"——子代理场景禁用压缩是社区实践。
- **opencode-acp**（265 星，同作者的 OpenCode 插件形态）：#396 压缩质量门失败时错误信息过长反而耗上下文（已修）；#183 加 memory tool 让关键事实幸存压缩；#324 用户求"强制全量压缩模式"（保护机制挡事）——说明保护/质量门的调参是长期摩擦点。
- **billion-context #862**（40 评论，open）：zcode 子代理经 bili 后概率性停摆（§6）。
- 中文圈有真实曝光：B 站有"20w token 足矣！主动上下文压缩插件 opencode-acp 和 billion-context-pi"等视频；DeepSeek Harness 商店有移植版 billion-context-dsh（97 星）。

## 10. 费用模型：接入后每请求新增成本与净节省

记号：S=折叠掉的 tok，σ=摘要 tok，T=折叠点之后重付的 tok（≈折叠后视图），r=1.7/6.9=0.246，q=24/6.9=3.48；GLM 无缓存写价（w=1；不确定：若智谱计缓存写另算）。

**每请求固定新增**（挂链即付，压缩与否无关）：
```
+ [3.3k(sys+tools) + 11×文本消息数] tok × 1.7/10⁴     （进缓存后；首轮 ×6.9）
+ nudge 触发时一次性 nudge_tok(~2k) × 6.9/10⁴          （频率 ≈ 每 50k 增长一层一次）
```
**折叠的账**（内核自带同构公式：acp-kernel/src/cache-report.ts:241-267，breakeven = [(w−r)·T + q·σ] / [r·(S−σ)]，我们的价格代进去）：
```
一次性成本 = T×6.9/10⁴ + σ×24/10⁴ + nudge ~2
每轮净省   = S×1.7/10⁴
回本轮数   ≈ 4.06 × T / S
闲置冷重启（TTL 已死场景）每次额外省 = S×6.9/10⁴（重付基数变小，等比于我们 4% 的复活成本）
```

**三个数值点**（基线：我们均值 45.4 积分/请求 ≈ 219k 缓存读 + 8k 新输入 + 1.1k 输出）：

1. **只挂链不压缩**：+0.6~1.1 积分/请求 ≈ **+1.3%~2.4%**（纯税）。
2. **一次典型折叠** S=150k、T=70k、σ=2k：一次性 ≈ 48.3+4.8+2 ≈ 55 积分；此后**每轮省 25.5 积分**（=均值单请求成本的 56%）；**≈2.2 轮回本**；再活 20 轮净省 ≈ 455 积分（该区间基线 908 积分的一半）。
3. **稳态全局估算**：若折叠把稳态重读从 219k 压到 110k，省 ≈18.5 积分/请求 ≈ **41%（理论上限）**；考虑折叠追不上增长、固定开销、压缩轮的多付与质量维护，现实预期 **20%~35%**；若窗口配置错误（1M 直配）则触发不了，只剩 −2% 的纯税。

**结论公式化**：净省 ≈ Σ折叠[S×(0.246~1)×6.9/10⁴×轮数] − 固定税 − 一次性偿付。我们这种"重历史、长会话、82% 缓存读"的账本位于该机制收益区间的最好位置；短会话/轻历史用户反而是净亏（这正是它 README 之外的隐藏适用边界）。

---

## 11. 接入我们链路的前置条件清单

1. **显式配窗口并故意调小**：`providers."http://127.0.0.1:15721".models."claude-sonnet-5[1M]".context`（建议先 300k 实验），key 必须带 scheme；GLM-5.3 的 1M 真窗口不能直接当分母用（§7）。
2. **实测 CC 会话头**：跑一次 bili 代理确认 CC 请求带 `x-claude-code-session-id`；不带则评估前缀亲和对我们多会话并发的误并概率（§6）。
3. **实测子代理头**：CC Task 子代理请求的会话头形态，确认不会与主会话共用压缩状态（上游 #862 是现成反例）。
4. **实测 cc-switch 兼容**：4 个 ACP 工具 + 改写后的 system 穿过 cc-switch 后是否原样到达 GLM（参数白名单、[1M] 剥除、thinking 整流都不应碰 tools 成员）；GLM 端对 compress 函数调用的支持度。
5. **确定链序与端口**：CC 的 ANTHROPIC_BASE_URL 改指 bili 端口，bili 上游写死 `/bili/http://127.0.0.1:15721/...` 或启动参数 upstream；确保只有一个 bili 实例（x-bili-hop 防双压，cc-switch 侧无需改）。
6. **对账回路先行**：接入前先约定——CC jsonl 账本 + bili `[acp-usage]`/`[acp-compress-obs]` 日志两边对账（压缩轮 CC 侧必低估，§4）；acp-kernel 的 cache-report 三分桶（new/compRepay/ttlRepay）可直接抄作我们的报表口径。
7. **锁版本**：关自动更新（或 CI 钉死版本），升级走变更管理；当前 8 周 118 版的漂移速度不适合"挂着自动追"。
8. **切换时机**：重建体非字节保真 → 切换当天 GLM 缓存池全量失效，选低峰；切换后第一天的账本要剔除这一次性重付。
9. **与 Ferryman 闸门的关系想清楚**：bili 的 nudge+折叠是"会话内持续压缩"，我们现有 35 分钟闸门+交接文档是"极端压缩+重启"——两者不冲突但触发条件要互斥设计（bili 声明窗口调小后，闸门线是否还按 35 分钟/原窗口算需重推）；心跳体外重放请求也要确认 bili 会话归属（别把心跳记成新会话/污染压缩状态）。

## 12. 风险清单

| 风险 | 依据 | 缓解 |
|---|---|---|
| 压缩质量丢关键信息（路径/签名/决策/用户原话）→ 隐性返工成本 | 祖源 DCP 的 KEEP/DROP 规则已极尽详尽仍有人要求改；GLM-5.3 服从度未知（§9） | 先在非关键会话灰度；抽检摘要；保留 decompress 兜底 |
| 模型"压缩上瘾"/行为扰动 | DCP #372（§9） | nudge 带调高（45%→60%+）、growthFloor 调大 |
| 压缩循环轮的账本低估 | §4 源码证据 | 对账回路含 bili 日志（前置 6） |
| 子代理/并发会话状态污染 | #862、keakon fork 禁用实践（§6/§9） | 前置 3 实测；必要时 protectedTools 保子代理相关工具 |
| 大比例折叠触发 GLM 风控（3007） | 他们自己写了 staged-compress 引导且注释点名 GLM 3007（compress-tool.ts:141-148、stream.ts:127-134） | 开 BILI_MAX_SHRINK_PER_COMPRESS 平滑档 |
| 触发带错配 → 永不压缩（纯交税） | §7 窗口=分母推论 | 前置 1 显式小窗口；用 [acp-usage] 日志验证触发 |
| SSE terminal 重写丢 cache_creation / CC 聚合方式不确定 | §4 | GLM 无写价影响小；接入后用账本数字复核 |
| 上游迭代漂移、Windows 宿主边角 | 118 版/8 周；fix-679-windows-launch 等专属修复的存在本身说明 Windows 有过坑 | 锁版本 + 灰度 |
| tokenCount 依赖上游 usage，GLM 异常 0 usage 时 nudge 失明 | #793 有防护但依赖真实 usage 到达 | 它有 #553 匿名会话估兜底；纳入对账监控 |

---
*源码证据基于克隆快照：billion-context@7bf7cde（v0.1.118）、acp-kernel@e8900f6（v0.0.77），均为 2026-09-17/18 的 master。*
