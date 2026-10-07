# 票04 · ADR-0025 + 子代理硬禁（依 T5 结论条件实施）

## What to build

1. **ADR-0025《自动注入挂显式意图信号》**：原则——自动路径永不激进注入；注入必须挂显式意图信号（被拦待领锚 / 用户显式命令 / 明确开关）。边界记录：锚定归还按线（agent+目录）不按人/主题归属，锚窗内一次性交付（每笔被拦原话至多进一个后续新会话），多真人共用同目录即共享一条线——为已验收现状，多真人形态出现时需显式信号重设计。ADR 目录 README 索引同步。
2. **子代理硬禁（R3，条件实施）**：读票03 的 T5 探针结论——
   - 若子代理创建**触发** agent/created 且识别形态可靠（payload 明确字段或稳定结构）→ 插件 onCreated 对子代理形态跳过 askHandoff，配契约测试；
   - 若**不触发**（通道 D 不存在）→ 本半票记"不适用"完成（事实即结论，晨报说明）；
   - 若触发但**识别不可靠** → 记"延后"，禁止硬凑识别规则（误伤正常新会话的风险大于窄口收益——R1 已灭无锚注入面，R3 只剩锚窗内子代理领锚窄口的防御价值）。

## 验收标准

- [ ] docs/adr/0025-auto-inject-explicit-intent.md 落盘，含原则句+锚定边界记录+与 0023/0024 的关系不混淆
- [ ] docs/adr/README.md 索引追加
- [ ] R3 三分支之一落定并在票产出注明（实施+测试 / 不适用+事实 / 延后+理由）
- [ ] 若实施 R3：新增契约测试（子代理形态跳过问询、正常会话不受影响）；插件用例零回归
- [ ] `node --test`（若动插件）或纯文档零验证负担（若不动）

## Blocked by

票03（T5 探针结论）

## 涉及路径

- docs/adr/0025-auto-inject-explicit-intent.md
- docs/adr/README.md
- plugin/ferryman-dsh/src/events.ts（仅 R3 实施分支才动）
- plugin/ferryman-dsh/test/events.test.ts（仅 R3 实施分支才动）

## 副作用声明

纯文档分支无验证命令；R3 实施分支只跑 `node --test --experimental-strip-types test/events.test.ts`。

decision_refs: D3、D6
review_blocks: F1（锚定边界 ADR 记录落地）、F5（R3 降级顺序落地）
