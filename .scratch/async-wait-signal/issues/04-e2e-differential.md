# 票 04 · 端到端集成测试（差异断言）

## What to build
Harness 全链路用例：async 派发 → stop → 窗口停车 → 主会话恢复调用 → 守望采集闭窗 main_resumed。断言用差异法：另设一个无停车窗、闲置超线的对照会话，等它被摆渡（证明守望确实在跑且会摆）之后，断言停车会话不在摆渡名单。时序要求：靠"写完文件后自然闲置"触发（Harness summarize 阈值 1 秒），不手改 last_write 也不把对照会话 mtime 拨到过去；ack 确认行在 stop 事件之后追加落盘（验证宽限不误闭）；恢复行时间戳写成 now+120s（越过 90s 宽限）。
设计参照：rev1 Task 4 + 附录 #2/#4/#8。

## 验收标准
- [ ] e2e 用例稳定通过：停车成立（差异断言负样本非空过）、ack 行落盘后窗口不闭、恢复行喂入后 close_reason=main_resumed
- [ ] python -m pytest tests/ -q 全量绿后才 commit

## Blocked by
票 03
