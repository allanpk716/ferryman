# 票05 · 插件晚到交接注入(持续重试)

## What to build
dsh 新会话创建时若交接还没铸好(askHandoff 返回空),插件记住"欠一份交接";此后每个用户步(第一个 step)都再问一次,拿到就把交接注入当前对话并清账——用户在窗口里发的第一条、第二条…消息期间,交接一到就自动补进,不再"问一次没拿到就永久缺"。

## 验收标准
- [ ] askHandoff 空 → 置 handoffPending(含会话键,多会话互不串)
- [ ] onPreStep step===1 且 pending 时重问;拿到→以 injectedMessage 注入当前步并清 pending;没拿到→保持 pending 继续
- [ ] 会话结束/dispose 清 pending,不跨会话泄漏
- [ ] daemon 故障 fail-open 不变(重问失败静默放行,不阻塞用户消息)
- [ ] 插件测试基线(73)不破,新增用例:首问空→第二步重问拿到→注入;连续空→持续 pending
- [ ] 不改 CC/宿主任何其他行为

## Blocked by
无,可立即开始。

## 涉及路径
plugin/ferryman-dsh/src/events.ts
plugin/ferryman-dsh/src/daemon.ts
plugin/ferryman-dsh/test/(或既有测试目录,保持既有布局)

## 副作用声明
插件测试命令(按仓库既有插件测试跑法,落在 plugin/ferryman-dsh/ 内)。

## decision_refs: D3、D7、F3
## review_blocks: 无
