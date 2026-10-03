# 票 05 · 插件事件对接：闸门／播种／上报／判活＋跨源去重

> 链内修订（票04 评审发现，协调者落）：跨源 usage 去重自票06 改钉本票——事件口通流量那天起，pollDsh 文件守望与本票事件直报会对同一 assistant/message 各记一行（账面双计，report 按 lineage/session 聚合同组翻倍）。**通流量前必须消除**，验收新增第 5 条。

## What to build
在票 03 骨架与票 04 三口之上实现插件五事件位的业务对接：
①`agent/pre-step`——调 daemon 闸门问询口（键＝头行 id），reject 时以用户可见理由拒绝该步；
②`agent/created`（awaited）——同 Agent＋同 cwd 调交接查询口，取到交接 MD 经 `agent.inject()` 播种（赶首请求）；
③`session/event`——turn/start、assistant/message（含 usage 四列）、compaction/* 上报 daemon 事件接收口（含上报失败的朴素策略：失败不阻塞会话、静默重试一次）；
③b **跨源 usage 去重（链内修订新增，通流量前必须落地）**——事件直报与 pollDsh 文件守望对同一 assistant/message 会双记账。消除策略二选一（实施侧裁定并在代码注释留痕）：(a) 事件接管单源化——daemon 侧被插件上报接管过的会话（同一 session_id 有事件流量）从 pollDsh 文件守望面剔除，文件守望继续兜底未接管会话；(b) 记账侧去重键——两源行按 (agent, session_id, 事件 seq 或时间窗＋四列签名) 只记首发。加测试：双源同事件只出一行、未接管会话文件守望照常。
④`agent/disposed` 与 `agent/status`——判活信号挂接（本票只上报事件，消费方是 P2-5 设计）；
⑤挂载自检扩展——接线三口连通性检查。
对 dsh 源码钉夹具：五事件位的挂接语义、awaited 语义、agent.inject 行为（调研克隆，标注 file:line）；三口交互用 mock daemon（本地测试面，不要求活实例、不碰 ~/.dsh）。

## 验收标准
- [ ] 五事件位全部接通，`node --test --experimental-strip-types` 全绿（落日志）
- [ ] mock daemon 测试面覆盖：闸门 reject 理由可见、created 播种赶首请求时序、事件上报字段与票 04 口形状咬合、上报失败不阻塞
- [ ] **跨源去重落地（策略 a 或 b）＋双源同事件只出一行的测试证据；未接管会话文件守望不受影响**
- [ ] 源码钉夹具注释带 file:line 出处
- [ ] 零依赖保持（无 node_modules）

## Blocked by
票 03、票 04

## 涉及路径
plugin/ferryman-dsh/、internal/daemon/（跨源去重的 daemon 侧落点；票04 已 complete 无在跑冲突，票06 依赖本票天然串行）

## 副作用声明
`node --test --experimental-strip-types`；输出落 `.scratch/dsh-phase2-finish/logs/t05-*.log`；mock daemon 走 localhost 随机端口（测试自起自灭）；不联网外呼、不装依赖。

## decision_refs
D12（不碰活实例）、spec「插件 ferryman-dsh」节

## review_blocks
无
