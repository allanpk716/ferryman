# 票02 · 插件：continuation 扩注契约测试（零行为改动）

## What to build

守护进程将把 `{"context":null,"continuation":true}` 的含义从"续用档零注入"扩为"续用档或新会话无料零注入"（票01）。插件 v0.9.7 已实装该键的清欠账语义，本票**不改任何运行时行为**，只做两件事：

1. 契约测试固化：events 测试新增——新会话 created 问询得 `md=null, continuation=true` → 清欠账、不置账、零注入；用户步欠账重问得同答 → 清欠账止问（不再每条消息重问）；有锚 `md` 非空 → 注入照旧。防止未来回归到欠账死循环形态。
2. 注释扩注：daemon.ts 的 `/dsh/handoff` 答话形状注释与 events.ts 欠账机制注释，把"续用档"表述扩为"续用档或新会话无料（daemon 侧零注入终态）"。

## 验收标准

- [ ] 新增契约用例：`md=null+continuation=true`（新会话）→ handoffPending 清除且不置账、无 inject 消息
- [ ] 新增契约用例：欠账会话用户步重问得 `continuation=true` → 清欠账，后续用户步不再问询
- [ ] 新增契约用例：`md` 非空 → 注入与现状一致（回归保护）
- [ ] 现有插件用例（184）零回归
- [ ] daemon.ts/events.ts 注释完成扩注；src 行为零 diff（仅注释与测试文件变化）

## Blocked by

无，可立即开始。

## 涉及路径

- plugin/ferryman-dsh/src/daemon.ts
- plugin/ferryman-dsh/src/events.ts
- plugin/ferryman-dsh/test/events.test.ts

## 副作用声明

只跑 `node --test --experimental-strip-types test/events.test.ts`（plugin/ferryman-dsh 目录内）；不跑插件全量（终局统一跑）。

decision_refs: D1
review_blocks: 无
