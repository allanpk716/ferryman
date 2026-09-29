# 03 · 事务拉起直连 cmd + 拉起验证探针

## What to build
1. 事务拉起（launch 缝，覆盖 launchAndVerify/rollback/restoreServiceQuiet）固定为 `cmd.exe /c <StartCmd> + CREATE_NO_WINDOW + CREATE_NEW_PROCESS_GROUP`（proc_windows.go launchCmdImpl 的回落分支形态）——不再探测/优先 wscript/VBS。蜂群/看门/Run 键不经此路径。
2. 拉起验证：launch 后 2 秒起做活性探针——成功谓词＝本事务配置的管理端点（cfg.Port）HTTP 应答（任意状态码含 401/404 都算，沿用 probeDaemonAny 语义）；重试至 10 秒截止；进程退出或端点始终无应答→报"拉起验证失败（相位错误）"+notify 告警，不改变事务走向（回滚仍由既有 90 秒版本校验裁决）。
3. 探针端点用 cfg.Port 拼接，不硬编码 15700。

## 验收标准
- [ ] 事务拉起不再调用 wscript/VBS
- [ ] 探针成功=端点应答；进程活但端点 10 秒无应答=验证失败（有告警）
- [ ] 验证失败不触发回滚（事务继续走 90s 版本校验）
- [ ] 端点取 cfg.Port（改 Config.Port 的测试里探针目标随之变）
- [ ] notify 失败不阻塞事务
- [ ] 既有 supervisor 测试全绿

## Blocked by
无，可立即开始

## 涉及路径
internal/update/proc_windows.go
internal/update/supervisor.go
internal/update/supervisor_test.go

## 副作用声明
go test ./internal/update/ 允许

## decision_refs
D7

## review_blocks
无
