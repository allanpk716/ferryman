# 票 05 · 文档收口

## What to build
词汇表（CONTEXT.md"等待窗口"条目）补停车语义全套：saw_async 锁存（防交错丢窗）、ack 宽限 90s、PARK_EXPIRE_S 1h 过期及其后果（豁免失效+摆渡可恢复入队）、close_reason 全集（subagents_done/prompt/main_resumed/expired）、"ack 落宽限内且会话随后静默 → 该窗记 expired@1h、dur 虚高约 1h"变体声明。A6 人工验收演练单追加 async 用例（派后台子代理 ≥3 分钟→中途输消息看豁免→完成后看续跑与真实 dur）。
设计参照：rev1 Task 5 + 附录 #12。

## 验收标准
- [ ] CONTEXT.md 条目含上述全部语义与后果声明
- [ ] A6 演练单含 async 用例且步骤可执行
- [ ] python -m pytest tests/ -q 全量绿后才 commit（文档票同样前置）

## Blocked by
票 04
