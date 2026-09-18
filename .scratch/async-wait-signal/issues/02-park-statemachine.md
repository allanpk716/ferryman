# 票 02 · 等待窗口停表停车状态机

## What to build
窗口表（内存）从"计数归零即闭"升级为停车状态机：窗口结构含 opened_ts/stop_ts/saw_async，跨线程加锁。行为：同步派发 stop 即闭（旧语义，subagents_done）；异步派发（票 01 判定）或本窗曾异步停车（saw_async 锁存）→ 停车挂起；start 事件处理时若尾判 async 亦置 latch（覆盖"sync start 先于 async stop"重叠序）；停车窗遇新 start 续窗。闭窗三道：prompt（仅未停车窗）/ 主会话恢复调用（新 usage 行 ts > stop_ts + 90s ack 宽限 → main_resumed）/ 停车超 1h（PARK_EXPIRE_S 独立命名常量）→ expired。重锚旧停车窗按"距 stop_ts 超 PARK_EXPIRE_S"判过期，closed_ts=min(stop_ts+PARK_EXPIRE_S, now)，不得出现未来时刻。window_wait 谓词与 note_usage 均保证不抛（记账异常吞掉；note_usage 不得先丢窗后记账失败）。缺口 A 判定加第三道：停车未过期 → 机器等待豁免。
设计参照：rev1 Task 2（3b–3h）+ 附录 #1/#3/#5/#7/#10/#12/#13。

## 验收标准
- [ ] 单测全过：async stop 停车不闭→恢复行(+200s)闭 main_resumed；宽限内(+5s)的 usage 行不闭；同步 stop 即闭 subagents_done；交错用例分两阶段写文件（先 async 块→stop→追加 sync 块→再 start/stop）验证 latch；重叠事件序（async start→sync start→async stop→sync stop）最终仍停车；停车窗期间 gate 走 machine-waiting 放行且窗口不因 prompt 闭；过期（回拨 stop_ts 超界）闭 expired；window_wait 在记账路径打爆时返回 False 不外抛（patch 面收窄到窗口记账内部，不得影响 gate 主路径断言）；未停车窗遇 prompt 照旧闭
- [ ] 测试 import server 模块真常量 PARK_EXPIRE_S（不复制 3600 字面量）
- [ ] python -m pytest tests/ -q 全量绿后才 commit；既有窗口/闸门用例零改动通过

## Blocked by
票 01
