# 票08 spike 四验结论（B1 对话区选择框）

- 日期/分支：2026-10-06，xcheck-night-20261006-083952。
- 验源（只读，未联网、未改宿主、daemon 零改动）：
  - dsh 调研克隆 `C:/Users/allan716/AppData/Local/Temp/dsh-research/`（master@639ed015，下文 dsh 侧 file:line 均相对克隆根）；
  - 本机实装 `~/.dsh/profiles/desktop/`（profile bundles：dsh-base＋dsh-web-app＋ferryman-dsh＋dsh-hypatia；ferryman-dsh 以 file:link 安装）；
  - 本仓 `internal/daemon/`（只读）与 `plugin/ferryman-dsh/src/*.ts` 既有协议注释。

## 总判定：验①不过 → 本票停靠（D6），未写任何实现代码

②③④三验全过，结论留档——将来若扩票启用插件浏览器半面（见 fallback F1），三验结论可直接复用，无需重查。

---

## 验①：插件能在对话区渲染可交互元素（两按钮）——不过

**一句话：dsh 宿主插件 API 面上，「纯宿主侧插件」（ferryman-dsh 现形态）没有任何对话区渲染能力；平台确实有该能力，但它在浏览器侧插件面（dsh.client 双面包），启用要动 package.json＋新增 client 产物＋宿主↔浏览器数据通道——三样全在本票涉及路径之外。**

### 证据（宿主面没有）

- 票03 钉点复核成立：`plugin/ferryman-dsh/src/events.ts:12-14`（协议不携带用户可见理由字段，理由呈现走 logger——宿主插件可见通道唯一）；`plugin/ferryman-dsh/src/index.ts:7-10`（没有插件级 toast API）。
- dsh 宿主侧包面逐包核过：`packages/host/` 下只有 directory-picker／webserver／telemetry 等基础设施包，无任何通知／横幅／UI 服务可注入。
- 同类先例佐证：官方桥 `@deepseek-ai/dsh-hooks-claude-code`（实装于 `~/.dsh/profiles/desktop/node_modules/@deepseek-ai/dsh-hooks-claude-code/package.json`）——纯宿主插件，无 `dsh.client` 声明、无 `./client` 导出、零 UI。纯宿主插件＝无 UI 是生态现状。

### 证据（能力在浏览器侧，机制完整存在）

- 桌面端形态＝Electron 壳＋web 客户端（apps/desktop 为 Electron；profile bundles 含 dsh-web-app＝「browser surface over dsh-base」）。对话区＝React。
- 对话区节点渲染是前端 keyed slot：`packages/client/ui-chat/src/client/chat/register-node-renderers.ts:34-36`（`ctx.slots.register({ name:'conversation.chat.node', key:'user', … })`）——注册者是浏览器侧插件。对话区现役可交互元素（审批按钮 ui-approval、提问卡 ui-user-questions、队列坞编辑/移除/steer 按钮 ui-conversation/QueueDock.tsx:320-369）全是浏览器侧组件。
- 双面包机制：包在 package.json 声明 `dsh.client:{platform:'web'}`＋`exports["./client"]`，宿主扫描器（`packages/client/modules/src/index.ts:830-858` resolveMeta；:196-207 clientExportOf；`modules/src/client/manifest.ts` parseDshClient）即把它编进浏览器 roster（`window.__DSH_BOOT__`），无需新增 patch 行——本插件既有 insert 行即可作载体。
- 浏览器模块表共享 react／ui-slots／ui-primitives 等（`packages/client/web/src/platform.ts:10-19` PLATFORM_MODULES），故 client 半面理论上可零依赖手写（无 JSX、走 React.createElement），不必引 tsdown 构建链——分发形态可保持零依赖。

### 卡点：三件事都在本票涉及路径外，且不止是文件问题

1. `plugin/ferryman-dsh/package.json` 加 `dsh.client`＋exports（涉路径外）。
2. 新增 client 产物文件（如 `client.js`，涉路径外；本票涉及路径只列了 src/events.ts、src/index.ts、src/usermessage.ts、test/、本文件）。
3. 被拦事件如何到达浏览器：prompt RPC 只到 `agent.followup` 入队即返 `{accepted:true}`（`packages/api/session-controller/src/commands.ts:311-377`），pre-step reject 是事后异步发生——前端要么订阅 `turn/end{blocked}` 会话事件，要么宿主插件自供 Remote（typert，先例：plugin-manager 即插件供的 Remote）让前端拉取被拦原话。两条路都要求宿主侧＋浏览器侧各写一段新代码。

**判定：按 D6 停靠，不实现任何替代 UI。已定 UX（对话区选择框）在平台上有真实落地路径（F1），但需要用户拍板扩票。**

---

## 验②：强续重发＝插件代发「强续＋原话」复用 bypass（daemon 零改动）——过

- **代发通道存在**：`agent.followup(message)`——「Queue an ordinary follow-up turn and wake the driver. The item becomes the sole ordinary message of its own turn」（`packages/core/agent/src/runtime-types.ts:218-222`；实现 `packages/core/agent-loop/src/agent.ts:166-168`，followup＝send('next-turn', wakeup=true)）。唤醒→新 turn→首步过 agent/pre-step→闸门问询→「强续」前缀命中 daemon bypass（`internal/daemon/gate.go:106` `strings.HasPrefix(prompt,"强续")`→放行＋解除本轮拦截，后续消息不需再带前缀＝不重拦）。**daemon 零改动成立。**
- **原话主径＝拦截现场本地缓存**：pre-step `payload.messages` 插件本就持有（`plugin/ferryman-dsh/src/events.ts:169-184` 已在用 blocksToText 拍平）。被拒消息本身从 durable inbox 消失、不进会话日志（`runtime-types.ts:291`「the claimed message ends here: it is neither discarded nor re-emitted as user/message, and the turn closes without a step」；`agent-loop/src/agent.ts:318` reject→turnEnds=blocked 后 turn 关闭无 step）——除拦截时缓存外无别处可取。已知取舍成立：宿主重启丢缓存→降级为用户手打「强续 \<原话\>」（原文在拦截时的 logger.warn 文案里）。
- **按钮触发时的执行体**：拦截现场可直接捕获 `payload.agent` 引用（AgentRef 鸭子面加 `followup?` 可选方法即可），无需注入 agents 服务；备选 `ctx.agents.get(sid)`（`packages/core/agent/src/index.ts:27-31`）。
- **可选验项「daemon 只读 pending 端点」**：现状不存在（HTTP 面只有 /dsh/gate|/dsh/event|/dsh/handoff，`internal/daemon/dsh_receive.go:58`），且本票验收要求 daemon 零改动——本地缓存主径已足，按「不存在、不需要」记。

---

## 验③：create({seed}) API 存在性＋followup 队列——过

- **宿主侧 create 在**：`ctx.agents.create(CreateAgentOptions)`（`packages/core/agent/src/index.ts:391`；服务声明 :27-31）。`CreateAgentOptions.seed?: readonly SessionEvent[]`（:91-95）。
- **followup 队列在**（见验②）。
- **前端侧同款也在**（若按钮走前端路径）：Session Controller Remote `sessions.create({workspaceId|cwd|sessionId|agentPreset})`（`packages/api/session-controller/src/types.ts:284-296`；`commands.ts:105`）；UI 新会话流 `ctx.uiWorkspace.startSession()`（`packages/client/ui-workspace/src/client/navigation.ts:223`，侧栏「新建会话」按钮同款，含导航与空白会话复用）。
- **「开场交接＋原话归还链路已在生产验证过」复核成立**：新会话 created→插件 askHandoff→daemon /dsh/handoff（`internal/daemon/dsh_receive.go:313-315`）→Restore 锚定归还（`internal/daemon/restore.go:35-95`：有被拦锚→返回锚会话交接 MD＋被拦原话拼接，`PopPendingPrompt` 交付即消费；锚会话无交接→只带原话＋中性缺交接文案）。**即一键新会话不必自带原话，开场 MD 里就有。**
- **实现口径澄清（非障碍）**：create({seed}) 的 seed 语义是 fork 继承前缀事件（SessionEvent[]），不是「交接文本」。D5 原文「create({seed})+followup」落地时应理解为：普通 create（同 cwd）＋既有 onCreated 播种链（交接经 askHandoff 注入），原话由 daemon 归还链带回；followup 用于需要主动推送原话/首条消息的场景。

---

## 验④：followup 自动派发是否经 agent/pre-step 用户步——过（经）

- 证据链：followup＝send('next-turn', wakeup=true)（`agent-loop/src/agent.ts:166-168`）→wakeDriver→turn()：turn/start 落账后首步 `step = phase.step + 1 = 1`（:315）→preStep 以 `{turn, step:1}` 发 'agent/pre-step' waterfall（:271-283，claimed＝inbox.claim）。
- **因此**：一键新会话场景无论自动 followup 原话、还是用户自然首条消息，票05 的晚到交接重问（handoffPending，每用户步 step===1 补问一次，`plugin/ferryman-dsh/src/events.ts:163-168` 注释／:193-204 实现）**天然生效**——首个 pre-step 就会再问一次交接，拿到即清账注入同批。
- 备验项「注入前能否主动查询一次交接」：成立且现成——askHandoff 是任意时刻可调的 HTTP 口（onCreated／onPreStep 两处现役调用），不依赖 followup 派发路径。

---

## 停靠后的 fallback 选项（只列不拍板，待用户）

- **F1（忠实已定 UX，建议优先评估）**：扩票启用双面包。package.json 加 `dsh.client{platform:'web'}`＋`exports["./client"]`；手写零依赖 client.js（React.createElement，无构建链）；前端注册对话区/输入坞 slot（如 `conversation.input.dock`，QueueDock 同位）呈现两按钮；宿主插件拦截时缓存原话并经 Remote（typert，先例 plugin-manager）供前端拉取＋触发 followup；「新会话」按钮走 `uiWorkspace.startSession()`（开场交接＋原话由 daemon 归还链带回，零新增链路）。牵动：涉路径外文件（package.json／client 产物）、插件分发形态复核（现 file:link 直装）、宿主重启才会加载新半面。
- **F2（纯宿主侧，零 UI）**：只强化拦截文案——logger.warn 带上被拦原话＋两行指引（「手打：强续 \<原话\> 重发／新建会话自动拿回原话」）。手机端从零反馈变文字反馈（日志面板可见），**不满足**已定 UX（对话区选择框），属降级项。
- **F3（宿主侧＋会话日志，不推荐，仅对照）**：拦截时宿主插件向会话日志 append 自定义事件（SessionEventMap 可 declare-merge 扩展，非 surface 类型运行时校验放行，`packages/core/session/src/surface.ts:172-177`）——前端以 unknown JSON 块渲染（`ui-chat/.../MessageItem.tsx:381-391`），非交互、观感差、会污染转录。
- **F4**：维持现状，等 dsh 宿主未来提供宿主侧通知面再战。

## 附：基线

- 插件测试基线：79/79 绿（`node --test --experimental-strip-types "test/*.test.ts"`，2026-10-06 本分支复跑；票05 后已从 73 增至 79）。本票零代码改动，基线未动。
