# 票 07 · P2-5 设计文档：判活信号重选＋等待窗口＋beat 头决策点

## What to build
设计文档 `.scratch/dsh-phase2-finish/p25-design.md`：
①dsh 判活信号重选——候选（`agent/status`→idle、`agent/disposed`、子女目录写入）逐一评估可靠性/时延/盲区，对照 CC 现行判活（含 async 文案判据——明确不移植及原因），给出推荐组合与不变量；
②等待窗口/问询守望对 dsh 的适配设计——与既有等待窗口机制（票05 上报的活动信号）如何对接，停车/硬死线语义是否复用；
③beat 发送侧对非 UUID36 键改发 `x-deepseek-harness-session-id` 头（httpsender/appendreplay 两处）——**列为决策点**（改动点/风险/前置条件），实施另立受本设计结论阻塞的票，本票不改代码；
④决策点清单（供晨间/后续票拍板）。
依据：票 05 已落地的事件面、dsh 源码事实（调研克隆）、既有等待窗口实现。

## 验收标准
- [ ] 四节齐全且每条主张带依据出处（源码 file:line 或本仓实现位置）
- [ ] 判活推荐组合给出不变量表述（什么信号组合判定"活着/死了"）
- [ ] beat 头改动作为决策点呈现（含两处改动点定位），无代码改动
- [ ] 文档自包含（晨间读它即可拍板，不依赖会话记忆）

## Blocked by
票 05

## 涉及路径
.scratch/dsh-phase2-finish/p25-design.md

## 副作用声明
纯文档；无测试/构建。

## decision_refs
D2（P2-5＝设计文档）、proposal rev1 第 4 件口径（决策点不实施）

## review_blocks
无
