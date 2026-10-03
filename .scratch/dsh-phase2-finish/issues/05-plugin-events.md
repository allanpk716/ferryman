# 票 05 · 插件事件对接：闸门／播种／上报／判活

## What to build
在票 03 骨架与票 04 三口之上实现插件五事件位的业务对接：
①`agent/pre-step`——调 daemon 闸门问询口（键＝头行 id），reject 时以用户可见理由拒绝该步；
②`agent/created`（awaited）——同 Agent＋同 cwd 调交接查询口，取到交接 MD 经 `agent.inject()` 播种（赶首请求）；
③`session/event`——turn/start、assistant/message（含 usage 四列）、compaction/* 上报 daemon 事件接收口（含去重/重试的朴素策略：失败不阻塞会话、静默重试一次）；
④`agent/disposed` 与 `agent/status`——判活信号挂接（本票只上报事件，消费方是 P2-5 设计）；
⑤挂载自检扩展——接线三口连通性检查。
对 dsh 源码钉夹具：五事件位的挂接语义、awaited 语义、agent.inject 行为（调研克隆，标注 file:line）；三口交互用 mock daemon（本地测试面，不要求活实例、不碰 ~/.dsh）。

## 验收标准
- [ ] 五事件位全部接通，`node --test --experimental-strip-types` 全绿（落日志）
- [ ] mock daemon 测试面覆盖：闸门 reject 理由可见、created 播种赶首请求时序、事件上报字段与票 04 口形状咬合、上报失败不阻塞
- [ ] 源码钉夹具注释带 file:line 出处
- [ ] 零依赖保持（无 node_modules）

## Blocked by
票 03、票 04

## 涉及路径
plugin/ferryman-dsh/

## 副作用声明
`node --test --experimental-strip-types`；输出落 `.scratch/dsh-phase2-finish/logs/t05-*.log`；mock daemon 走 localhost 随机端口（测试自起自灭）；不联网外呼、不装依赖。

## decision_refs
D12（不碰活实例）、spec「插件 ferryman-dsh」节

## review_blocks
无
