# 票03 · 注册表全量播种（受 F2 约束暂停）

## What to build

插件会话注册表从"事件喂养"补成"快照播种"：启动后首轮轮询前 + 每 5 分钟重播，经宿主 `sessions.getListSnapshot()` + `agents.get()` 拉式全量播种（Plan B：workspaceRegistry.list + agents.get）；静置会话进入轮询名单，守护压缩指令可送达执行。**本票因 F2（噪声时钟矛盾）暂停**——busyLive 衰减输入与播种的解耦设计（三钟分工候选）需先过一轮修订+复审，见 spec 活动约束节与 FINDINGS F2 解除条件。解除后本票按终版合并规则实施并补全验收（含沙箱 E2E 静置送达场景与 mock 形状断言、真机脚本全链）。

## 验收标准（F2 解除后细化，方向如下）

- [ ] 播种合并纪律逐字段表驱动（最终以解除版规则为准）
- [ ] 沙箱 E2E：宿主重载后无事件流的会话，指令仍送达执行（拉式播种模拟）
- [ ] mock 形状断言（D7 探针回写真实字段清单）
- [ ] 真机脚本全链 PASS（静置→触发→送达→执行→压缩落账→归来无卡）

## Blocked by

F2 解除（三钟分工或等效设计过复审）——paused(F2, 解除条件见 .xcheck/20261007-123925/FINDINGS.md)

## 涉及路径

plugin/ferryman-dsh/src/seed.ts（新）
plugin/ferryman-dsh/src/index.ts
plugin/ferryman-dsh/test/seed.test.ts（新）

（注：注册表接收面扩展落在票02 建立的 registry.ts——F2 解除版设计定形后若需扩展，届时以 follow-up 票协调，本票不直接列该路径）

## 副作用声明

暂停中不执行。

decision_refs: D1、D2、D4、D7
review_blocks: F2
