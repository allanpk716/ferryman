# 票 03 · 心跳调度器：observe 模式＋两道验＋串行＋三态熔断

## What to build

 窗口开启后"跳不跳、何时跳、怎么取消"的端到端调度：开窗排计划（默认 2 跳×420s，不立刻跳），每跳发送前两道验（无新写入＋mtime/size 二次新鲜度），全局同时最多 1 个 beat 在途（跨会话串行），任何新写入取消剩余跳；observe 模式只落事件不发网络请求；beat 结果三态（HIT/MISS/ERROR）与熔断（连续 2 MISS 降级 enforce→observe、连续 3 ERROR 暂停窗口）以可注入 sender 的方式实现并单测（不发真实网络）。调度器与台账读取共享 RLock。

## 验收标准

- [ ] 计划生成：N 跳按 `beat_interval_s` 排定；开窗瞬间不跳
- [ ] 两道验单测：预检通过但文件 mtime/size 变 → 不发并作废计划
- [ ] 新写入取消：写文件后剩余跳全部作废（守望轮询粒度内生效）
- [ ] 全局串行：两个窗口同刻到期，同一时刻只有 1 个 beat 在途（fake sender 观测）
- [ ] observe 模式：`mode=observe` 时零网络调用（fake sender 断言不被调），事件照落
- [ ] 三态分类：HIT/MISS/ERROR 判定单测（成功但 cache_read≈0 = MISS；代理不通/429/5xx/超时重试 1 次仍败 = ERROR）
- [ ] 熔断：连续 2 MISS 自动 enforce→observe＋告警事件；ERROR 不计入 MISS；连续 3 ERROR 暂停窗口剩余跳
- [ ] RLock 串行化：调度器与台账读共享锁（并发单测：check-then-act 窗口内台账更新可见）
- [ ] 每跳逐条记账入既有费用账本（含 observe 演练跳，标 observe）
- [ ] 全量 pytest 绿

## Blocked by

票 02（窗口与计划字段是调度输入）。
