# 票02 · 执行臂超时+作废纪元（含注册表预重构）

## What to build

压缩执行臂加 180 秒超时与作废纪元：超时清 inFlight 并上报 `{ok:false, reason:"timeout"}`（账本单行）；迟到的完成回调因纪元不匹配被静默丢弃（不再上报/不亮横幅/不写任何账本行）；超时后新指令可正常执行。预重构：SessionRegistry 从 compact.ts 机械抽到独立 registry.ts（后续播种票依赖的稳定落点），既有行为零变化。用户价值：执行臂偶发挂死不再吞后续指令、不再账本失明。

## 验收标准（全自动化可判）

- [ ] 预重构纯机械：SessionRegistry 类与既有用例行为零变化（149 插件测试全绿）
- [ ] 超时路径：慢执行（测试注入慢 promise）→ 180s（测试缩秒）到点上报 `{ok:false, reason:"timeout", source:同源}` 且 inFlight 清除
- [ ] 迟到丢弃：超时上报后慢 promise 终于 resolve → 无第二条上报（纪元不匹配静默丢弃），横幅不亮
- [ ] 超时后恢复：同会话下一条指令正常执行并上报 ok:true
- [ ] 作废纪元机制对正常快路径零影响（现有成功路径用例零回归）
- [ ] 单测用可控时钟/注入 executor，不真等 180 秒

## Blocked by

无，可立即开始。

## 涉及路径

plugin/ferryman-dsh/src/compact.ts
plugin/ferryman-dsh/src/registry.ts（新，预重构落点）
plugin/ferryman-dsh/test/compact.test.ts
plugin/ferryman-dsh/test/registry.test.ts（新）

## 副作用声明

跑 `node --test --experimental-strip-types "test/compact.test.ts" "test/registry.test.ts"` 与全套件一次；不联网、不碰生产 profile。

decision_refs: D3、D6；F1 解除证据（epoch 机制）
review_blocks: 无
