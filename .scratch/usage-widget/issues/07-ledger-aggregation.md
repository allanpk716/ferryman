# 票 07 · 台账窗聚合与花费估算

> **状态（2026-09-25 夜）**：部分完成（query_widget.go widgetLedgerAggregate）——
> usage 四列本自然月按上游聚合（model_map 值域归属+单上游兜底口径，见头注）、
> handoff 月/周计数（新键 handoffs_month/handoffs_week）。**未做**：5h 滚动窗聚合、
> 自然周 usage 聚合、DeepSeek 今/周花费计价（本机无 DS 上游/无流水）、handoff
> spend_* 计价（摆渡 provider=local 无价格表，price_ver=null，不造数）——待有
> paygo 上游或价格表补齐后续做。与 /report month 口径一致性：同源（usage 科目
> 四列纯加总 + 同月界推导），交叉对账数字见 2026-09-25 晨报。

## What to build

台账/账本侧新增聚合维度（现只有 project/session/month）：

- **5h 滚动窗聚合**与**自然周聚合**（按上游过滤 usage 科目四列）——供应月度已有（/report month）直接复用口径。
- **DeepSeek 今/周花费**：台账流水 × 价格表计价（价格表缺 P_cache 时如实按无缓存经济处理，花费标注口径；与成效账同一版本化公式纪律——公式单源）。
- **handoff 科目**月/周花费（摆渡行数据）。
- 全部产物打 `source: estimated` 语义（进契约时标注）。

## 验收标准

- [ ] 5h 滚动窗边界单测（跨窗滚动、整点重置邻近、空窗）
- [ ] 月聚合与既有 /report scope=month **交叉对账一致**（同输入同数）
- [ ] 花费计价复用唯一价格表实现，出现第二份公式即缺陷（公式单源纪律）
- [ ] 大账本性能：全量月流水聚合 P95 在主程序可接受阈值内（实测记录）
- [ ] 单测不依赖真实账本文件（合成夹具）

## Blocked by

与票 06 同款阻塞条件（渡口多上游合并 + observe 周满）

## 涉及路径

- internal/report/ 或新聚合包；对应 *_test.go

## 副作用声明

- 独占验证命令：`go test ./internal/report/...`（或所在包）
- 只读账本，不改写任何流水（report 只读纪律）

decision_refs: CONTEXT.md"账本/四列口径/价格表"；ADR-0008 子代理入账
review_blocks: 有（动主程序）
