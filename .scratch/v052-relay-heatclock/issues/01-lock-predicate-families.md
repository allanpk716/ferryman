# 票 01 · 换装锁判据三族定案（.old-* 备份族 + supervisor-copy 过渡保留）

## What to build
换装锁的持有者存活判定（lock.go holderAlive）在"监督者自身映像被两步换装第
一步改名后"仍能识别它（并发第二个 update 应得到"升级进行中"而不是误判锁
陈旧接管）；同时 v0.5.1 在途 supervisor-copy 活副本持锁也被正确识别（过渡
保护，一版后随分支删除）。这是删除副本机制（票02）的前置安全件。

## 验收标准
- [ ] holderAlive 映像集合 = {换装目标, supervisor-copy（过渡）, 换装目标同目录 `ferryman.exe.old-*` 备份族}；PID 活性为主判据、映像查不出保守按活（现行语义不变）
- [ ] 新钉子（红绿）：持有者 PID 活且其映像=目标同目录 ferryman.exe.old-vX.Y.Z → 仍判持有（当前代码此场景红）
- [ ] 回归钉：持有者 PID 活且映像=目标+".supervisor-copy" → 仍判进行中（当前绿，改后必须保持绿）
- [ ] 匹配语义代码注释钉死：同目录 + 文件名前缀 `ferryman.exe.old-`，并注明 supervisor-copy 分支的生命周期=次版删除
- [ ] `go test ./internal/update/...` 绿

## Blocked by
无，可立即开始

## 涉及路径
- internal/update/lock.go
- internal/update/lock_test.go

## 副作用声明
无独占验证命令；包级测试即可

decision_refs: D2
review_blocks: F1, F2
