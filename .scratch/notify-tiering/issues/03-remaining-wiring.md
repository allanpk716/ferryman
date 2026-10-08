# 票03 · 剩余触发点接线收尾(breaker/upgrade/hard_cut/drift/tray_reply)+旧入口清理

## What to build
把剩余六类通知触发点全部接入票02 的事件分派入口,随后收编旧 NotifyAlert 入口——完成后九类事件全部受 notify.events 配置组控制,包内不再存在绕过分派的调用点。

## 验收标准
- [ ] watcher.go 四处 qwatchAlert 调用点(问询熔断降级/错误熔断/等待窗无策略/心跳熔断)与 watcher_dsh.go 两处走 breaker
- [ ] cmd/ferryman/main.go 升级结果、internal/update/supervisor.go 事务告警走 upgrade
- [ ] supervisor.go 静默门硬切兜底走 hard_cut
- [ ] internal/dock/drift.go 形态漂移走 drift
- [ ] cmd/ferryman/main.go trayCheckUpdate、cmd/ferryman/restart.go 安全重启走 tray_reply(缺省 toast)
- [ ] 旧 NotifyAlert 入口收编:内部调用点全部迁移后删除或私有化(不得残留可绕过分派的公开入口);内部/cutover/smoke.go 的 NotifyBlock 空操作 seam 若签名受影响同步修正(沙箱铁律:不出网不弹 toast 不变)
- [ ] 涉及处测试断言更新;go build ./... 编译绿

## Blocked by
票02(分派入口)

## 涉及路径
- internal/daemon/watcher.go
- internal/daemon/watcher_dsh.go
- cmd/ferryman/main.go
- cmd/ferryman/restart.go
- internal/update/supervisor.go
- internal/dock/drift.go
- internal/cutover/smoke.go

## 副作用声明
无独占验证命令;默认 go build ./... + go vet

## decision_refs
D2、D6
review_blocks: 无
