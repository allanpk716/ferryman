# 票08 · 对话区选择框(spike 四验 → 实现)

## What to build
dsh 用户消息被闸门拦下时,对话区直接出现一个选择框:「强续重发」(把被拦原话以「强续 」前缀代发,走既有 bypass 放行)和「新会话」(一键建新会话并自动收到交接+原话)。手机远控也能走完两条路。**先做 spike 四验,全过才实现;任一验不过,票停靠并把结论与 fallback 选项写清楚,不擅自改已定 UX(对话区选择框;Toast 已被否决)。**

## spike 四验(实现前必须全过)
1. 插件能在对话区渲染可交互元素(两按钮);能力事实从 DSH 宿主插件 API 面查证——线索:`~/.dsh/profiles/desktop/node_modules`(宿主插件 SDK/类型)与本仓 plugin/ferryman-dsh/src/*.ts 既有协议注释(runtime-types 等);只读取证,不改宿主。
2. 强续重发:插件代发「强续+原话」复用 bypass 语义(daemon 零改动)。原话主径=拦截现场本地缓存(payload.messages,拦截时插件本就持有);宿主重启丢缓存→降级为用户重打(已知取舍);daemon 只读 pending 端点=可选验项。
3. create({seed}) API 存在性 + followup 队列(开场交接+原话归还链路已在生产验证过)。
4. followup 自动派发是否经过 agent/pre-step 用户步(决定票05 的晚到重问在一键新会话场景是否天然生效);若不经,验"注入前能否主动查询一次交接"。

## 验收标准
- [ ] spike 四验结论落 .scratch/dsh-post-accept-fixes/spike-b1.md(每验:结论/证据出处/过或不过)
- [ ] 四验全过:实现选择框——被拦时对话区出现两按钮;点强续重发=原话以强续前缀发出并被放行(不重拦);点新会话=新会话开场收交接+原话
- [ ] 任一验不过:本票停靠,spike-b1.md 写明 fallback 选项待用户,不实现任何替代 UI
- [ ] 插件测试基线不破,新增用例覆盖拦截→呈现→两条按钮路径(mock 宿主面)
- [ ] daemon 侧零改动

## Blocked by
票05(同文件先后落地)。

## 涉及路径
plugin/ferryman-dsh/src/events.ts
plugin/ferryman-dsh/src/index.ts
plugin/ferryman-dsh/src/usermessage.ts
plugin/ferryman-dsh/test/(或既有测试目录)
.scratch/dsh-post-accept-fixes/spike-b1.md(spike 结论,新文件)

## 副作用声明
插件测试命令(按仓库既有插件测试跑法);spike 只读取证不改宿主、不联网。

## decision_refs: D5、D6、F4、F8
## review_blocks: 无(spike 不过→按 D6 停靠,不算阻断)
