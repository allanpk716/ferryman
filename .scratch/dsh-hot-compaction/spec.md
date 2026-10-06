# spec：DSH 会话热缓存压缩适配（"拦你之前先救你"）

> 夜链 20261006-233527。评审两环收敛（round1 codex AGREE / kimi 两条技术细化已裁定吸收：指令有效期 N1、在途并发查证 N2）。决策 D1-D9 见 .xcheck/20261006-233527/decisions.md。

## Problem Statement

DSH 会话闲置后缓存过期，继续使用要全量重付整个上下文。现有方案是被拦后弹选择卡做选择题。根治：趁缓存还热（闲置 ~0.8×TTL）对同一会话做一次原生压缩——前缀变短后冷重付也便宜，会话不开新、用户零选择。CC 泳道做不到（压缩不可外部触发）不代表 DSH 做不到（原生压缩子系统可编程触发）。

## Solution（用户视角）

长会话闲置到接近缓存过期时，系统自动：①生成 Ferryman 交接文档落盘（跨 agent 档案）；②对会话执行 DSH 原生压缩（会话内摘要基底，缓存友好）。用户回来：同窗口一条轻横幅"本会话已压缩归档，直接继续"，发消息不再被拦（前缀已短，重付便宜）。压缩做不了/失败时：照旧弹选择卡兜底。

## User Stories

1. 作为 DSH 用户，我希望长会话闲置后回来能直接继续而不被拦，以便不打断工作流。
2. 作为 DSH 用户，我希望闲置期间系统自动压缩会话使继续成本降到很低，以便不用手动管理上下文。
3. 作为多泳道用户（CC/codex/DSH），我希望每次压缩都同步产出 Ferryman 交接文档落盘，以便任何工作哪天换工具都能接上。
4. 作为 DSH 用户，我希望压缩失败时仍有选择卡兜底，以便极端情况不无助。
5. 作为系统维护者，我希望整条链有 Playwright E2E 全自动验收，以便改动后不用人工回归。

## Implementation Decisions

### 架构与契约（跨票钉死，改契约=全部受影响票返工）

**daemon（Go, internal/daemon）**：
- `[dsh_compact]` 配置节：`enabled`(默认 true)、`trigger_ratio`(默认 0.8)、`min_peak_tokens`(默认 20000)、`command_ttl_ratio`(默认 0.2，即指令有效期=0.2×TTL)、`poll_hint_s`(默认 30，下发给插件的建议轮询间隔)、`compressed_flag_ttl_ratio`(默认 2.0，compressed 标记有效期=2×TTL)。
- **指令槽**：daemon 内存态 per-session {action:"compact", session_id, cwd, enqueued_at, expires_at}；同槽新指令覆盖旧指令；**派发时（poll 应答时）expires_at 已过即丢弃并清槽**（N1 语义：过期不执行）。触发条件（watcher 扫描）：闲置 ≥ trigger_ratio×TTL ∧ peak_ctx ≥ min_peak_tokens ∧ 无在途请求 ∧ 无有效 compressed 标记 ∧ 无未过期在槽指令。触发同时按既有 L1 管线异步生成交接（与指令独立，互不阻塞）。
- **POST /dsh/poll**（鉴权同 /dsh/gate，同 token）：请求 `{agent:"dsh", sessions:[{sid, idle_s}]}`（插件报其宿持有会话）；应答 `{commands:[{action:"compact", session_id, cwd}]}`（只回未过期；过期即丢弃）。多宿主同 sid：先到先得（poll 应答即清槽），天然去重（kimi 非阻断参考①的吸收）。
- **POST /dsh/compacted**（同鉴权）：请求 `{session_id, ok, reason?, prefix_tokens?, source?}`；daemon 落账本事件 **kind=compacted**（含全部字段）；ok=true 且 prefix_tokens 非空时更新 gate 会话状态：compressed 标记（带 expires=now+compressed_flag_ttl_ratio×TTL）+ prefix_tokens 覆盖。**刷新规则（codex 建议吸收）**：其后任何该会话新流量按既有 enrich 逻辑刷新 prefix_tokens；compressed 标记自然过期（不再续期）；标记有效期内会话重新变热（有流量）则标记立即作废（压缩红利只领一次）。
- **gate 联动（gate.go）**：既有拦截判定处，命中拦截条件（闲置过线等）时**先查 compressed 标记**：标记有效 ∧ 当前 prefix_tokens < min_peak_tokens（阈值同配）→ 放行（reason="compacted-short-prefix"，账本照记 allow）；否则照旧拦。**不放宽任何其他判定**（未压缩会话照拦）。
- 生产 daemon 代码改动仅进夜链分支；换装生产需用户点头（D5 红线）。

**宿主插件（plugin/ferryman-dsh, TS）**：
- apply() 增设轮询循环：`setInterval(poll_interval_ms)`（默认 30000，daemon 应答可带 poll_hint_s 调整，取 max(daemon 提示, 10s)）；每轮 POST {daemon}/dsh/poll，body 携带本宿主持有的活跃会话清单（从五事件位维护的会话注册表取：sid+idle_s）。
- 收到 compact 指令：**执行前双重复查**（N1 插件侧）：①agent 仍空闲；②会话闲置时钟仍 < TTL（热窗内）。任一不满足→POST /dsh/compacted {ok:false, reason:"expired-or-busy"}，不执行。满足→`ctx.compaction.compactNow(agent)`（懒注入先例取服务面，方法带 receiver 调用），catch ManualCompactionError('busy') → 上报 {ok:false, reason:"busy"}。完成后尽力读新前缀（会话投影 contextPressure/tokenUsage 可得则得，不可得省略）→ 上报 {ok:true, prefix_tokens?}。
- 轮询失败静默（下轮再试）；宿主 dispose 时清 interval。
- **横幅（浏览器半面 client.js）**：宿主半面在 session/event 流上观察会话压缩完成事件（DSH compaction 落 surface 后有事件可观察；具体事件名实施时从克隆查证钉入）→ 经既有 Remote/卡片数据通道给浏览器 → 横幅组件（复用 dock 机制+窄容器浮层，无按钮）："本会话已压缩归档（交接已存档），直接继续"。展示一次后随用户下次发消息消失。

**E2E 验收栈（tools/e2e_dsh/，可复用套件）**：
- 沙箱 daemon：以测试 config 起第二个 daemon 实例（端口 25xxx 段；TTL 类参数压到秒级，如 ttl_s=90/trigger_ratio=0.5/command_ttl_ratio=0.2）；独立数据目录（不碰 ~/ferryman 生产数据）。
- 隔离 web 实例：脚本复制 profiles/web 为测试 profile 副本（不动生产 profile），插件 config daemonURL 指向沙箱 daemon；起 `dsh web` 独立端口（25xxx 段）。
- Playwright 剧本：token 登录→建会话→灌上下文（多轮对话至 peak 超阈值）→闲置等触发→断言：账本 kind=compacted 出现、poll/compacted HTTP 往返、横幅 DOM 出现→继续发消息**不被拦**（无选择卡、账本 allow reason=compacted-short-prefix）→交接文件落盘存在。含降级链回归断言（禁用沙箱 daemon 后被拦→选择卡在）。
- 生产 3080/生产 daemon/生产 profile 零接触（D5）。
- **N2 前置查证**（独立票）：读 compaction/agent-loop 源码+沙箱实测"compactNow 在途时新用户消息"行为，结论写入 research 文档；若为排队/干净报错→吸收为降级语义；若中断/状态错乱→该竞态场景记 waiting 停相关断言（晨报列明）。
- **经济性软验证（F5）**：不在 E2E（TTL 压短验不了经济性）；晨报观察项=合并后首单生产 TTL 自然压缩会话的账本前后对比。

### Testing Decisions

- daemon：表驱动 Go 测试（gate_test.go 先例）——指令槽过期丢弃、poll 应答清槽、compacted 上报落账本、gate 联动放行/照拦/标记过期/流量作废四态。
- 插件：node --test + mock daemon（既有 mockdaemon.ts 先例）——轮询循环、双重复查、busy 上报、receiver 绑定；横幅组件渲染测试（fake React 先例）。
- E2E：Playwright 对沙箱栈全链断言（上列清单）。
- 全仓复核：go test ./... 经隐身运行器落日志（零闪窗铁律）；插件全测 node --test。

## Out of Scope

替换 DSH 压缩摘要后端；CC/codex 泳道同类改造；选择卡退役；保温策略改造；生产 daemon 换装（用户点头后另行）。

## Further Notes

- 宿主服务方法永远带 receiver 整体调用（今晚事故教训）。
- 插件部署面：三 profile node_modules 是拷贝要同步；E2E 用独立 profile 副本自含。
- 账本新事件 kind=compacted 的 wire 字段与既有事件族（block/bypass/inject）同风格。
- 夜链期间票路径文件由夜链独占。
