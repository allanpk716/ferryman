# 票 02 · 删除换装自中继副本机制（supervisor-copy 整套退场）

## What to build
换装监督者不再复制自身副本：自身映像==换装目标时直接跑两步换装（自己的
镜像被改名成 .old-<版本> 备份即设计保留件），Run() 的自中继步、副本路径、
延迟自删、派生旗标全部退场。换装后盘面不再产生 supervisor-copy 文件。
依赖票01 的锁判据先定案（删除副本后锁的唯一保护就是票01 的三族判据）。

## 验收标准
- [ ] supervisor.go 删 selfRelayIfNeeded / relayCopyPath / relaySelfDelete / relayArgs 与 Run() 0.5 自中继步
- [ ] proc_windows.go 删 selfDeleteImpl 与 spawnRelayImpl；proc_other.go 非 Windows 面同步（若有对应物）
- [ ] Result.Relayed 字段与 cmd/ferryman/main.go 的 Relayed 分支收口（`ferryman update` 同步跑完，行为变化按 spec 声明）
- [ ] --self-relay 旗标保留"解析但忽略"，注释注明次版删除
- [ ] cleanSwapResidues 的 supervisor-copy* 模式保留不动；doctor 不动
- [ ] 钉子（红绿）：自身映像==换装目标 → 两步换装直接成功、盘面零 supervisor-copy、备份=.old-<版本>（当前代码此场景红：走副本分支）
- [ ] 票01 的两颗钉子保持绿（回归）
- [ ] `go test ./internal/update/... ./cmd/ferryman/...` 绿

## Blocked by
票 01

## 涉及路径
- internal/update/supervisor.go
- internal/update/proc_windows.go
- internal/update/proc_other.go
- internal/update/standin_test.go
- internal/update/supervisor_test.go
- internal/update/swap_locked_windows_test.go
- cmd/ferryman/main.go
- cmd/ferryman/main_test.go

## 副作用声明
无独占验证命令；包级测试即可（全仓 go test 留给协调者终局跑）

decision_refs: D2, D1
review_blocks: F1（零副本钉）, F7（验收口径见 spec：首跳 23/24 预期）
