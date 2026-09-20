# 票 05 · 升级监督者主体:锁+journal+停旧+原子换+拉起+校验+回滚+恢复

**What to build**:
`internal/update` 监督者状态机,CLI `ferryman update`(执行)与内部旗标 `--supervise`(托盘派生用,行为同无参)收敛到同一序列:

1. 锁 `~/ferryman/update.lock`:O_CREATE|O_EXCL 原子建,内容 PID+进程映像路径+generation;存活判定 = PID 活且映像路径==换装目标(seam B);持有者活 → 「升级进行中」退出;陈旧 → 原子接管(generation+1)
2. journal `~/ferryman/update-journal.json` 阶段 staging/swap/verify,先写后动
3. staging:票03 的下载+SHA256 到旁路 `ferryman.exe.new`
4. 换装目标解析(seam E):解析 `~/ferryman/start-daemon.cmd` 引号内 exe 路径;失败回落 os.Executable()
5. 停旧:POST 127.0.0.1:7311/shutdown(票04)→ 等端口释放(≤30s);端点不可达且守护在跑 → 兜底 kill **前必须验证 PID 映像路径==换装目标,不匹配/stale 拒杀并报错**(seam B)
6. swap(seam A):copy 当前 exe → `ferryman.exe.old-<旧版本>` → 单次 `MoveFileEx(new→exe, MOVEFILE_REPLACE_EXISTING)` 原子替换(无缺位窗口)→ 旧备份只留 2 份 → 一切退出路径清理 .new/.swap-tmp
7. 拉起+校验:detached 隐藏拉起 start-daemon.cmd → 轮询 /stats ≤90s 且 version==目标;失败判定前查 7311 持有者,若旧版本(看门抢跑)→ 复停→重拉→重校验一轮(seam C);仍败才回滚
8. 回滚:杀新进程(同过身份校验)→ **copy** 恢复备份为正式 exe → 重拉旧版并校验 → 报错退出
9. 崩溃恢复:任何 update 启动先读 journal:staging→清残留续跑;swap/verify→核对健康,健康清账/不健康按备份回滚
10. 结果通知(seam F):升级结果经既有 internal/notify 通道推送;CLI 同时 stdout

**验收标准**:
- [x] 全流程 httptest+替身 exe 演练:成功路径换文件+备份+新版校验通过
- [x] SHA256 失败 → 拒绝替换,现场原样
- [x] 新版探活/版本校验失败 → 自动回滚,旧版重新服务
- [x] 并发取锁:双 update 仅一胜,败者收「升级进行中」;陈旧锁接管演练
- [x] kill 身份校验:假 PID 指向无关映像 → 拒杀(测试桩)
- [x] 看门抢跑演练:verify 期模拟旧版被重拉 → 复停重试路径命中
- [x] journal 崩溃恢复:staging 中断/swap 后中断/verify 中断三态各有断言
- [x] 备份保留 2 份自动清理;.new/.swap-tmp 无残留
- [x] `go test ./internal/update/ ./internal/daemon/ ./internal/notify/` 绿;`internal/mcp` 零触碰
- [x] 测试不碰生产 7311 口、不碰仓库根 ferryman.exe(替身 exe 用 `$env:TEMP`)

**Blocked by**: 03, 04
**涉及路径**: internal/update/, internal/notify/(若需最小接线), cmd/ferryman/main.go
**副作用声明**: 独占验证命令 `go test ./internal/update/ -count=1` 与 `go test ./internal/daemon/ -count=1`(与票04错峰);测试编译替身 exe 到 TEMP
**decision_refs**: D6, D8, D9, D10
**review_blocks**: F4, F8, F9
