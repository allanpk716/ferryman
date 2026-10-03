# Spec · dsh phase 2 收官（P2-3 桥 / 接法乙 spike / P2-4 原生插件 / P2-5 判活设计）

> 出处：`.xcheck/20261003-124409/proposal.md`（评审收敛版 rev1）＋同环 decisions.md（D1~D12）＋FINDINGS.md（F1~F6）。
> 术语遵守 `CONTEXT.md`（守望/摆渡/闸门/归还/台账/凉会话/族系）。

## Problem Statement

dsh（DeepSeek Harness）的流量已被 Ferryman 渡口接管路由（phase 1），会话已被守望与台账看见（P2-1），会话键已定案对齐（P2-2：统一键＝会话头行 id `session-<uuid>`）。但 dsh 会话仍是"半残户"：闲置无闸门拦截（缓存凉后继续使用＝全价重付）、无摆渡（精华没人渡成交接文档）、新会话无归还。本役把 dsh 补齐到与 CC/codex 同权。

## Solution

四件套交付：①桥 MVP——官方 CC 钩子桥插件挂闸门（快速止血）；②接法乙代价评估（只读，为路由决策供数）；③原生插件 ferryman-dsh——闸门问询/归还播种/活动上报/判活信号/挂载自检（长期正确形态），含役内摆渡闭环接线（条件式规则）；④判活与等待窗口设计文档。全部止于仓库代码＋测试＋文档；活实例安装、路由切换留晨间人操作。

## User Stories

1. 作为 dsh 用户，我想要闲置的 dsh 会话在提交前被闸门拦下并提示开新会话，以便不再全价重付凉缓存。
2. 作为 dsh 用户，我想要新会话启动时自动收到上一会话的交接精华（或按桥的过渡方式首句引导），以便无缝续作。
3. 作为 Ferryman 管理者，我想要 dsh 会话的每轮活动与用量实时进台账（渡口捕获之外的事件直报补充），以便闲置判定与成本账完整。
4. 作为 Ferryman 管理者，我想要闲置 dsh 会话的精华被摆渡成交接文档，以便闸门拦截时有交接可还。
5. 作为 Ferryman 管理者，我想要接法乙（路由换 llm-deepseek 适配器）的真实代价评估，以便决定 dsh 流量键上线的长期形态。
6. 作为 Ferryman 管理者，我想要 dsh 判活信号的设计定案，以便等待窗口与心跳面知道 dsh 会话死活。

## Implementation Decisions

- **会话键**：全链路统一用会话头行 id `session-<uuid>`（P2-2 定案＝守望/台账现行键）。桥插件传给钩子的 `session_id` 即此键（源码 `hooks-claude-code/src/index.ts:329` `session_id: agent?.session.header.id`，零换算；空串回退仅在 agent 不可用的边界）。不得另造键。
- **桥（P2-3）**：Ferryman 为 dsh 单发 hooks.json——落在 Ferryman 自家目录（如 `~/ferryman/dsh-hooks/hooks.json`，**绝不写 `~/.dsh/` 任何文件**，D12 红线），生成机制并入 provider apply 的 dsh 配置家族（幂等、备份、还原纪律同 CC/codex/dsh 既有件）；内容复用现有 CC 钩子脚本（UserPromptSubmit 问闸门、block→deny 语义）。晨间人工执行 `dsh plugin --profile web add @deepseek-ai/dsh-hooks-claude-code` 并把 configPath 指向该文件——本役只交付文件生成＋文档说明。
- **桥行为钉死**（约束 6 纪律，源码在调研克隆 `packages/hooks/`）：对桥插件源码钉三件事——事件映射（哪些 CC 钩子事件被桥接、payload 形状）、deny 语义（block 如何传导）、configPath 生效方式；连同"桥键＝头行 id 零换算（含空串回退边界）"逐条钉夹具测试。
- **接法乙 spike（只读）**：对 dsh 源码（llm-deepseek 适配器、pi-ai 路由）与渡口改写面做只读评估——DeepSeek 方言（私有头/参数）清单、渡口剥除改写的工作量与风险、与现 pi-ai 路的行为差异。产出评估报告（结论：代价量级＋建议），不改任何业务代码、不碰路由。
- **插件 ferryman-dsh**：TypeScript，仓内新子目录 `plugin/ferryman-dsh/`（分发先 `file:./`，github 远程分发后续票）；零运行时依赖。事件面五件：
  - `agent/pre-step`：向 daemon 管理口问闸门——reject（带用户可见理由）或 enter；
  - `agent/created`（awaited）：同 Agent＋同 cwd 的最新交接 MD 经 `agent.inject()` 播种（赶首请求，补桥 detached 硬伤）；
  - `session/event`：turn/start、assistant/message（带 usage）、compaction/* 上报 daemon（渡口捕获的替代/补充）；
  - `agent/disposed` 与 `agent/status`：判活信号（P2-5 消费）；
  - 挂载自检：渡口路由健康检查＋版本对账（社区先例 ZhijiangTang/dsh-handoff 同款思路）。
- **daemon 接收面**：管理口扩三口——闸门问询（键＝头行 id，判定走既有闸门语义）、事件接收（Touch("dsh")＋usage 记账走既有白名单科目，字段与 P2-1 pollDsh 同构）、交接查询（同 Agent＋cwd 最新交接 MD，归还播种用）。与 CC 面同构复用，不另起炉灶。
- **摆渡闭环接线（F1 已确认规则，条件式）**：接线票首步先判定"事件上报口可提供会话键＋快照所需材料"（判据：session/event 携带会话键，且材料足以按捕获-重放前缀语义（ADR-0007 同源）重建快照输入），判定留痕。可得 ⇒ 接线 `maybeEnqueue` 并同步补 enrich 的 dsh 分支（P2-1 遗留注记③），票内完成＋测试钉验；不可得 ⇒ 该票转为产出降级说明（验收勾"降级"），「精华被渡」依赖晨间接法乙/直报决策，晨报声明。二选一显式定案，不允许静默缺位。
- **P2-5 设计文档**：判活信号重选（`agent/status`→idle、`agent/disposed`、子女目录写入；CC 特有 async 文案判据不移植）；beat 发送侧对非 UUID36 键改发 dsh 头（httpsender/appendreplay 两处）＝设计文档内决策点，实施另立受设计结论阻塞的票，本役不改。
- **红线（D3/D12）**：不切 `cordis.patch.yml` 路由；不装活实例插件；不改 `~/.dsh/` 任何文件；真机验证只读 `~/.dsh/sessions` 语料。

## Testing Decisions

- 每票 TDD：先写失败测试跑红，最小实现跑绿；scoped 测试落日志（沿用 `.scratch/dsh-phase2/` 惯例，日志入 `.scratch/dsh-phase2-finish/logs/`），不裸跑全仓 `go test ./...`（D11）。
- Go 侧：表驱动单测＋既有 daemon/dock 测试同族（mock 注入面）；真机夹具 env 门控（`FERRYMAN_DSH_REAL_*`）保 hermetic。
- 插件（TS）：纯逻辑模块化，`node --test --experimental-strip-types`（本机 Node v22.14 已验）零依赖跑；桥/插件协议事实对调研克隆源码钉静态夹具（只读克隆，不联网）。
- 验收证据路径记入票台账（tests= 字段）。

## Out of Scope

- v0.5.3 发版与合线次序；工作台 W0；doctor dsh 探针计数；P2-1/P2-2 返工；observe→enforce 升档；活实例安装与路由切换（晨间人操作）；github 远程插件分发。

## Further Notes

- 桥的 SessionStart 注入 detached（赶不上首请求）＝官方已知硬伤，接受；归还过渡＝新会话首句手打 /restore 或待续 prompt 引导（P2-4 `agent/created` 才是正解）。
- 晨报决策项预告：接法乙（带 spike 数据）、目标三级陈述确认（F5/D10）、活实例安装授权。
- F6（材料可得判定时点）已按复审建议落为接线票验收首项。
