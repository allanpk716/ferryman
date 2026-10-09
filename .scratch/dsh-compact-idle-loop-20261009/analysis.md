# DSH 热压缩空转循环案（2026-10-09）

## 现象

用户在 DSH 界面看到会话「调研可刷自定义系统的手机及隐私AI助手方案」（`session-d5927238`，research_things 项目）出现很多次会话压缩记录。

## 根因（设计缺口，非显示错误）

热缓存压缩触发条件（compact_trigger.go 五/六条件）缺「还有东西可压」的终止判据：

1. 首次压缩有效：前缀 60k→~25k（地板=系统提示+工具 schema+摘要本体）。
2. compressed 标记只保 2×TTL（1h，置位钉死不续期），**纯时间过期**后再判五条件：
   闲置 24min+ ✓（闲置单调涨）、peak 25000 ≥ min_peak_tokens 20000 ✓（**压完的地板仍在门槛之上**）、
   无在途 ✓、无标记 ✓（刚过期）、槽空 ✓ → 再触发。
3. 宿主再压=压「上一份摘要+残渣」：新摘要难小于已很小的影子 → 80% 撞宿主不变量
   `summary is not smaller than the shadowed content (1920 >= 1920)` 报 error；
   偶尔成功也只降 10-40 token。失败不立标记 → 24min（触发线 0.8×TTL）后再来；
   成功立标记 → 1h 后再来。**循环无终止**，直到用户真回来发消息。

放大器：每次压缩（含失败）都往会话文件写 compaction/* 事件 → watcher TouchFull 推 LastWrite
→ （a）闲置被重置成从压缩时刻起算（拖慢节奏到 24min 一发）；（b）**no-agent 退避被立刻解除**
（退避解除条件「LastWrite 前进=用户回流」被机器写入冒充）→ error 阶段连 8 发拦不住。

## 生产实锚

- 账本（~/ferryman/accounts/202610.jsonl）：d5927238 共 131 条 compacted 流水（7 ok / 19 error /
  其余 no-agent），10-08 19:07 → 10-09 09:33，成功后 1h 一发、失败后 24min 一发。
- 宿主 session.v4.jsonl.zstd：27 次 compaction/start、26 end、仅 7 次 summary；
  7 条 `This is an automatically generated check` 机器消息被塞进对话历史。
- 前缀序列：25218→25030→24985→24979→24975→24953→24939——15 小时降 279 token（1.1%）。
- 全局 6 会话共 1983 条 compacted 流水；另 4 会话同模式（19814→19797、20562→20524、26106→25877）。
- `8ff6309e` 会话 668 次全失败（expired-or-busy 467 + no-agent 201，23h）＝双宿主 live 路由死锁
  另一形态（不落界面），**不在本票**，另立票。

## 伤害

界面 27 条记录污染；对话历史被塞 check 机器消息；宿主每 25-60min 真调模型生成 16-23k 字符摘要
（27 次模型调用换 279 token）；账本噪音。

## 修复（本分支 xcheck-night-20261009-dsh-compact-idlegate）

条件②红利门槛从**绝对值**改**增量**（还原 min_peak_tokens 本意「压缩红利盖过冷重付」）：

- 从未压过：peak ≥ min_peak_tokens（不变）。
- 压过（标记在册，含过期——条目不随过期清除）：`peak − PostPrefix ≥ min_peak_tokens`。
  - DshCompressMark 加 PostPrefix 字段（=压缩成功上报的 prefix_tokens）。
  - PeakCtx 压缩时 ←prefix（=PostPrefix）、其后 enrich 只增不减 → 增量即压缩后净增长，
    天然免疫压缩自身落盘写的时钟噪音。
  - PostPrefix=0（历史标记/缺前缀形）回落绝对门槛。

- 改动：ledger.go（字段）、compact.go（存值+头注）、compact_trigger.go（条件②+头注+mark 抄取）、
  dsh_compact.go（config 注释）、compact_trigger_test.go（表驱动 5 例+全链回归）。

## 换装注意

标记是内存态（daemon 重启归零）：换装后受害闲置会话无标记 → 绝对门槛 → **最多再吃一次压缩**，
成功后 PostPrefix 立位从此永静。可接受。

## 遗留

- 8ff6309e 双宿主 live 路由死锁（668 发全失败形态）另立票。
- 退避被「压缩自身落盘写推 LastWrite」解除的问题随本案消失（不再入槽），但该解除条件的
  机器写入冒充语义仍在（对 no-agent 形态仍是放大器），留待路由票一并考虑。
