# 票09 · dsh 文件面闲置锚根治(被拦回合写入不再顶新)

> 来源:票02 票级评审 FAIL 的根因闭环——A5①(idle 真实性)的真根因在文件面,本票根治后票02 的票面承诺即兑现。

## What to build
dsh 会话被拦的回合也会往会话文件落盘(turn/start、inbox splice、turn/end 等事件),pollDsh 文件面现在按文件 mtime 无条件 TouchFull 顶新台账 last_write——而 dsh 没有内容钟可挡(事件面 v0.8.10 已修"闲置锚只认机器产出",文件面漏修)。结果:闸门判定的 idle 被冲成"刚活跃"(晨间实测 8.8 小时被显示成 16.5 秒),分支5 错过、账本假数。修法=dsh 文件面与事件面对齐:文件增量里只有机器产出(assistant/message、compaction/*)才算会话活动;用户侧/被拦回合的文件写入不顶新闲置锚。

## 验收标准
- [ ] dsh 文件面增量处理不再以 mtime 无条件 TouchFull:只有增量中含机器产出事件(assistant/message、compaction/*)才推进闲置锚(last_write);纯用户侧/被拦回合写入不顶新
- [ ] fed 会话(事件面已接管)与文件面行为一致,无双计或漏计
- [ ] 机器产出后闲置锚正常推进(正常回复照旧刷新 idle——不能把会话钉死在"永不活跃")
- [ ] 单测:①被拦回合写入(turn/start+splice+turn/end blocked,无 assistant/message)后 idle 不被顶新;②含 assistant/message 的正常回合后 idle 正常推进;③compaction 事件同机器产出;④cc/codex 文件面路径零变化
- [ ] 既有 watcher_dsh/dsh_wiring/dsh_qwatch 测试不破;票02 的回归钉测试在文件面顶新场景下断言成立

## Blocked by
无,可立即开始(票06 的 boot 回放已落地,本票在其上继续)。

## 涉及路径
internal/daemon/watcher_dsh.go
internal/daemon/watcher_boot_test.go
(如需:internal/daemon/dsh_boot_replay.go 微调联动;测试可加 internal/daemon/ 下新测试文件)

## 副作用声明
只跑 internal/daemon 包单测(-run 选择器含 Dsh|Watcher|Gate 相关)。

## decision_refs: D9(A5① 根因闭环)、票02 评审发现
## review_blocks: 无
