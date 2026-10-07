# 票03 · 注册表全量播种（三钟分工终版，F2 已解除）

## What to build

插件会话注册表从"事件喂养"补成"快照播种"，静置会话进入轮询名单、守护压缩指令可送达。三钟分工（spec 终版，round2 双家 AGREE）：

- **播种**：启动后首轮轮询前 + 每 5 分钟重播，经宿主 `sessions` 服务 `getListSnapshot()`（主路）或 `workspaceRegistry.list()`（Plan B，sessions 被隔离时）取会话清单，`agents.get(sessionId)` 解析活引用；容错降级（服务缺面/抛错→静默回事件喂养，不炸插件）；`origin=subagent` 照登
- **钟②（核心新增）**：注册表新字段 `lastEventAt`——只由宿主事件面五事件位推进（events.ts 的 touch/setStatus 调用点），播种/快照 updatedAt/重播**永不可触碰**（条目创建时初始化不算触碰）；**busyLive 衰减输入改为钟②**：busyLive := busy ∧ (now − lastEventAt) < 300s
- **钟③**：lastActivityAt 维持现状语义+播种可推进（仅快照 updatedAt 较上次播种记录实际推进时，单调不回退）——纯信息性 idle_s 上报，无执行语义依赖
- **busy 合并**：播种以快照 running 覆盖 busy（校正陈旧）；护栏=事件面 busy=true 且 lastEventAt<300s 时不被快照 false 覆盖
- **Plan B**：首次播种钟②/③=播种时刻；重播不推进任何已播种条目的两钟；不做 busy 校真
- **D7 探针**（实现内置，结果只注记）：sessions 可注入性/静置会话 running 可信度/updatedAt 噪声——日志一行即可

## 验收标准（全自动化可判）

- [ ] 播种合并纪律逐字段表驱动：断言①"噪声推 updatedAt → 钟②不受影响 → busyLive 照常成熟"（两路通用）②"stale 事件 busy 被快照校真"（主路）③"事件新鲜 busy 不被快照 false 覆盖"（主路护栏）④"重播推进钟③仅当 updatedAt 实际推进且不影响钟②"（仅主路）⑤"Plan B 重播不推进已播种条目钟②/③"（仅 Plan B）⑥播种新建时钟来源（主路=快照 updatedAt/Plan B=播种时刻）+ agent 只补 null 永不覆盖 + subagent 照登
- [ ] busyLive 输入切换到钟②后，既有 N1/busy 衰减用例改走新输入零回归
- [ ] 服务缺面/抛错降级：播种失败静默回事件喂养，插件加载与轮询不受影响
- [ ] 播种调度：启动首轮 poll 前一次 + 5min 周期重播（时钟可注入，不真等）
- [ ] 全插件套件绿（票02 后基线 160+新增）
- [ ] 沙箱 E2E 既有 15 断言回归绿（跑 tools/e2e_dsh/suite/run.sh）

## Blocked by

无（F2 已解除：round2 双家 AGREE + 用户拍板 D8）

## 涉及路径

plugin/ferryman-dsh/src/seed.ts（新）
plugin/ferryman-dsh/src/registry.ts
plugin/ferryman-dsh/src/compact.ts
plugin/ferryman-dsh/src/events.ts
plugin/ferryman-dsh/src/index.ts
plugin/ferryman-dsh/test/seed.test.ts（新）
plugin/ferryman-dsh/test/compact.test.ts

## 副作用声明

跑 `node --test --experimental-strip-types "test/*.test.ts"`（plugin/ferryman-dsh 目录）；沙箱 E2E 由协调者统一跑（占沙箱 25xxx 段，不碰生产口）。

decision_refs: D1、D2、D4、D7、D8
review_blocks: 无（F2 已解除）
