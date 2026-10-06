# 票01 · N2 前置查证：compactNow 在途与新用户消息的并发语义

## What to build
在沙箱实测+源码双证"DSH compactNow 压缩进行中（摘要调用在途）时用户发新消息"的实际行为：排队等待？消息报错？会话状态异常？结论写成 research 文档，供下游票吸收为降级语义。这是评审裁定 N2 的落地（material=trusted 要求查证带 file:line 或实测记录）。

## 验收标准
- [ ] .scratch/dsh-hot-compaction/research/n2-concurrency.md 存在，含：源码证据（克隆 file:line，指向 agent-loop/compaction 的并发处理代码）+ 沙箱实测步骤与观测记录
- [ ] 结论三选一明确：排队/干净报错/状态错乱
- [ ] 若为前两者：写明降级链吸收语义（一行）；若为状态错乱：写明受影响断言清单（票08 对应断言标记跳过+waiting）
- [ ] 全程只读生产（实测用沙箱口，不得碰 3080/15700）

## Blocked by
无，可立即开始

## 涉及路径
- .scratch/dsh-hot-compaction/research/（新建）
- 只读引用：C:/Users/allan716/AppData/Local/Temp/dsh-research/（克隆，只读）

## 副作用声明
沙箱实测若需起进程：端口限 25xxx 段；禁碰生产端口 3080/15700/15722/3081

decision_refs: D8
review_blocks: 无
