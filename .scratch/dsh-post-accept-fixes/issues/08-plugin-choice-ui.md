# 票08 · 对话区选择框·F1 双面包实施(D11 定案,mock 已过目 2026-10-06)

> 状态更新:spike 四验已完成(①不过→路线拍板后本票即 F1 扩票);D11 两步走的 F2 由票10 承担(已落地);本票=F1 正餐。mock 已过目(.scratch/dsh-post-accept-fixes/mock-choice-box.html,视觉参照只读)。

## What to build
dsh 消息被闸门拦下时,对话区出现选择框卡片(mock 形态):标题"这条消息被 Ferryman 闸门拦下"+一句原因+被拦原话引用+两按钮「强续重发」(留在本会话)「新会话继续」(一键新建,开场自动收交接+原话)+底部小字;点过按钮后卡片塌缩为"✓ 已按…——原话:…"一行记录。实现走浏览器侧插件面(双面包,ADR-0023):宿主插件拦截时缓存原话并经 Remote 供浏览器侧拉取;浏览器侧注册对话区卡片渲染器;按钮动作回落宿主面执行。

## 验收标准
- [ ] package.json 声明 `dsh.client:{platform:'web'}`+`exports["./client"]`;分发形态保持零依赖(client 半面手写 React.createElement,不引构建链;spike-b1 验①证据链的 roster 机制复用)
- [ ] 宿主侧:拦截现场缓存原话+会话键(与票05/票10 同源 payload.messages);经 Remote(typert,先例 plugin-manager)暴露被拦事件拉取面;浏览器卡片数据来自该面
- [ ] 浏览器侧:对话区卡片渲染按 mock 信息结构(待处理态+已处理塌缩态);多条被拦各自成卡
- [ ] 「强续重发」→宿主侧 agent.followup("强续 "+原话);「新会话继续」→宿主侧 create 同 cwd 新会话(开场交接+原话由 daemon 归还链带回,不自带);动作后卡片转塌缩态
- [ ] 手机 web UI 真机可用——**用户晨间真机验收项,自动化只验逻辑**
- [ ] daemon 零改动;插件既有测试基线 82 不破;新增用例(mock 宿主替身):拦截→缓存→Remote 暴露→卡片数据→两按钮动作→塌缩态
- [ ] spike 结论与实现期事实冲突时以事实为准,回写 spike-b1.md 留档一行

## Blocked by
无(spike 已完成、mock 已过目、票05/票10 同文件前序已落地)。

## 涉及路径
plugin/ferryman-dsh/package.json
plugin/ferryman-dsh/client.js(新)
plugin/ferryman-dsh/src/events.ts
plugin/ferryman-dsh/src/index.ts
plugin/ferryman-dsh/src/usermessage.ts
plugin/ferryman-dsh/test/(既有布局)
.scratch/dsh-post-accept-fixes/spike-b1.md(仅冲突回写)

## 副作用声明
插件测试命令(plugin/ferryman-dsh/ 既有跑法);spike 事实源只读;不联网、不改宿主、不装依赖。

## decision_refs: D5、D11、F4、F8、ADR-0023
## review_blocks: 无
