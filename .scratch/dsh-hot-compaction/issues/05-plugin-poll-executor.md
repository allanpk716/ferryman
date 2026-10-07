# 票05 · 宿主插件轮询执行臂（取指令→复查→compactNow→上报）

## What to build
plugin/ferryman-dsh 宿主半面新增：apply() 起 setInterval 轮询循环（默认 30s，daemon 应答 poll_hint_s 可调，下限 10s），POST {daemon}/dsh/poll 携带宿主会话清单；收到 compact 指令后双重复查（agent 空闲 ∧ 闲置 < TTL 热窗）不过则上报 expired-or-busy；过则懒注入取 compaction 服务面、带 receiver 调 compactNow(agent)，catch busy 上报；完成后尽力读新前缀上报。宿主 dispose 清 interval。daemonURL/token 复用既有 config 面。

## 验收标准
- [ ] node --test 全绿（mockdaemon 先例）：轮询循环起停；双重复查两分支；busy catch 上报；成功路径上报 {ok, prefix_tokens?}；interval 随 dispose 清理
- [ ] 不新增任何宿主服务依赖声明（inject 数组不变）
- [ ] 既有 109 测零回归

## Blocked by
无，可立即开始（契约以 spec 为准，不依赖票02 实现完成）

## 涉及路径
- plugin/ferryman-dsh/src/（新增 compact.ts 或并入 index.ts/events.ts + 会话注册表）
- plugin/ferryman-dsh/test/（新增 compact.test.ts）

## 副作用声明
默认只跑 node --test（plugin/ferryman-dsh/test/）；禁联网（mock daemon 本地）

decision_refs: D1 D8（N1 复查）
review_blocks: F1 F4 N1
