# 票04 · dsh 强续后重铸触发(daemon)

> **本票 paused(F9/F10),今夜不派发**。spec 已按复审解除条件写好规则;待用户重敲 /xcheck --night 复审确认两句措辞(或明示同意)后自动进入就绪集。

## What to build
dsh 会话强续(bypass)并完成那次交换(模型回复落账)之后,该会话的下一次渡口请求(dock 记账)时自动入队重铸一份新交接——材料=快照 Main(sid)(此时已含完整强续交换),覆盖截止=入队时 last_write。触发条件从持久面推导,守护重启后按同一推导重算,零新增内存态。

## 验收标准(实施票时逐条)
- [ ] 仅 agent=="dsh" 生效(F10:cc/codex 零行为变化——推导谓词对 cc 恒真问题由作用域限定阻断)
- [ ] 触发=强续交换完成(assistant 回复落账)后,该会话下一次渡口请求时入队;**强续那条请求本身不是触发点**(F9:其请求体尚不含模型回复)
- [ ] 推导谓词:该会话存在晚于其最新未消耗交接 covers 的 bypass 账痕(账本持久,重启重算同结果)
- [ ] 空集边界:会话无任何未消耗交接时谓词不成立、不重铸(F12)
- [ ] 验收断言:重铸材料含强续交换的 assistant 回复(F9)
- [ ] 兜底回归:强续后无后续渡口请求的长闲置,既有 pollDsh→maybeEnqueue 摆渡线行为不变
- [ ] 单测覆盖以上每条

## Blocked by
票01、票02、票03。且 review_blocks 未解除前不派发。

## 涉及路径
internal/daemon/dsh_receive.go
internal/daemon/watcher_dsh.go
internal/daemon/dsh_enqueue_test.go
(如需新文件:internal/daemon/dsh_regen.go)

## 副作用声明
只跑 internal/daemon 包 dsh 相关单测。

## decision_refs: D2、D3、D7、D8④
## review_blocks: F9、F10
