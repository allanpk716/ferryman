# 04 · 守护层锁让路 + 锁中继盲区修

## What to build
1. serve 启动早期（绑端口之前）查 update.lock：持有者活（PID 活且映像路径 ∈ {换装目标 exe, 换装目标+".supervisor-copy"}）→打印一行"[ferryman] 升级事务进行中（持有者 PID N）——本实例静默让路"，退出码 0。锁不存在/不可读/持有者死/映像不匹配→照常启动。
2. 锁判定单源：update 包导出只读助手，daemon 调用，不复制第二份。
3. 修 lock.go holderAlive 盲区：映像==换装目标 或 ==自中继副本路径都算活（当前副本路径恒判陈旧→并发第二次 update 误接管双监督者）。
4. 既有"复停重拉"防线保留。

## 验收标准
- [ ] 锁被活监督者持有时 serve 静默退出码 0、有让路日志行
- [ ] 锁不存在/陈旧/映像无关时正常启动
- [ ] 并发第二次 update 不再误接管（副本路径持有者判活的锁测试）
- [ ] daemon 不含复制的锁判定逻辑
- [ ] go vet/build 净；既有 daemon/update 测试绿

## Blocked by
无，可立即开始

## 涉及路径
internal/update/lock.go
internal/update/lock_test.go
internal/daemon/serve.go
internal/daemon/serve_lock_yield_test.go（新增）

## 副作用声明
go test ./internal/update/ ./internal/daemon/ 允许

## decision_refs
D8

## review_blocks
无
