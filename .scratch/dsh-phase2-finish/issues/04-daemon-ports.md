# 票 04 · daemon 接收面：闸门问询／事件接收／交接查询三口

## What to build
daemon 管理口扩展三个接收端点（与 CC 面同构复用，不另起炉灶）：
①闸门问询口——入参含会话键（头行 id）与上下文，判定走既有闸门语义（台账/阈值/观察窗），返回 enter 或 reject＋用户可见理由；
②事件接收口——session/event 形状（turn/start、assistant/message 带 usage、compaction/*）落台账：Touch("dsh") 活动登记＋usage 四列记账（科目/字段与 P2-1 pollDsh 完全同构，白名单零新键）；
③交接查询口——按 Agent＋cwd 取最新交接 MD（归还播种用，复用既有交接读取语义）。
表驱动测试覆盖三口（正常/边界/坏形防御——dshtrans 同族防御纪律：坏输入静默收窄不炸）。

## 验收标准
- [ ] 三口各带表驱动测试跑绿（落日志）
- [ ] usage 记账字段与 pollDsh 现行四列逐字段一致（测试断言科目形状）
- [ ] 闸门判定复用既有语义（不复制阈值逻辑，测试引用同一判定入口）
- [ ] 坏形输入防御测试（空键/坏 JSON/未知事件类型静默跳过不 5xx）

## Blocked by
无，可立即开始（与票 01/02/03 路径不相交）

## 涉及路径
internal/daemon/

## 副作用声明
测试构建禁弹黑窗；`go test ./internal/daemon/` 结果落 `.scratch/dsh-phase2-finish/logs/t04-*.log`（文件名带票号不互斥）；不联网。

## decision_refs
D5（同构复用）、spec「daemon 接收面」节

## review_blocks
无
