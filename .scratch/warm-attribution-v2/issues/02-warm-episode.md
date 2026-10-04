# 02 · 保温回合切分与双轴四象限（收入腿）

## What to build

internal/report 新增保温回合模块（建议 warm_episode.go）：从账本科目行（map[string]any 形态，与 SavingsV1 同入口）切出保温回合并按双轴判类，供 SavingsV2 组装。端到端行为：给定一组已解析账本行，能产出每个保温回合的 {lineage, 回合起点ts, 终型(回归/拦截/过期), 终点ts, 回合前缀, need, hit, 判类(兑现/白保温无害/亏损), 兑现节省额}。

规则（照规格记帐合同，逐条实现）：
- 回合起点 = 该 lineage 末次真实 `usage` 行 ts（真实写入）；保温动作行（handoff provider=智谱 的 lane=same_model、beat）落在 [起点, 终点) 内即属该回合。
- 终点三型：回归 = 起点后该 lineage 下一条 `usage` 行；拦截 = block 行；过期 = 最后一个保温动作后自然 TTL（1800s，可配常量）内无新事件。
- 回合前缀 = 回合内首个保温动作行 prefix（handoff.prompt_tokens / beat.prefix_tokens）；不可得回落末次真实 usage 行四列之和并标 fallback=true。
- need = (回归ts − 起点) > TTL；hit = 回归首发 usage 的 cache_read_tokens ≥ 前缀×80%（80% 可配常量）。
- 判类：need∧hit→兑现，节省 = 前缀 × (p_in−p_cache)/per（价书 [prices.glm]，按行时刻取版本，SavingsV1 同款 econBook 传递）；¬need∧hit→白保温无害（不计省）；¬hit→亏损。多跳回合收益一次结、不逐跳重复。
- 跨月：回合收益计入回归所在月（输出带归属月键）；成本归发生方（本票只产出回合清单，成本由 03/04 接）。

## 验收标准

- [ ] 表驱动单测（warm_episode_test.go）：三终型、多跳一次结、TTL 两侧 need 翻转、80% 阈值两侧 hit 翻转、前缀 fallback、dsh 行缺 ts 回落盖章（传入行已带 ts 字段则用之）
- [ ] 纯函数、不读盘、不改既有文件（ SavingsV1 零触碰）
- [ ] go test ./internal/report/ 全绿（输出落日志）

## Blocked by

无，可立即开始

## 涉及路径

internal/report/warm_episode.go（新建）
internal/report/warm_episode_test.go（新建）

## 副作用声明

允许并要求运行：`go test ./internal/report/ > <repo>/.scratch/warm-attribution-v2/logs/t02-test.log 2>&1`（先建 logs 目录；禁 -race）

decision_refs: D2, D3
review_blocks: 无
