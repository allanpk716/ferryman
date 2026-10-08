# 票 01 · daemon 侧 L1 挂载记账＋宿主进程旁证＋/dsh/health 端点

## What to build

daemon 收到 `/dsh/poll`（internal/daemon/dsh_receive.go 既有入口）时顺带记账：全局最近 poll 时刻＋按 sid 的最近被 poll 见到时刻（内存即可，daemon 重启冷启动可接受）。新增只读查询面 `GET /dsh/health`（管理口，与 /stats 同域）返回：全局最近 poll 年龄秒数、各 sid last-seen、生效 poll 间隔依据（daemon 经 poll 应答建议的 hint 与实测节律）与推导出的超龄阈值（3×生效间隔；无任何已知间隔时 90s 兜底并在返回中注明 assumed=true）。

新增宿主进程旁证助手：枚举 DeepSeek Harness 进程与 3080 监听（进程枚举用任务管理器 API 或 `tasklist` 解析均可——**由 Go 直接调 Win32/生成进程须加 SysProcAttr{HideWindow:true}（Windows 分支），零闪窗铁律**），返回布尔＋证据摘要；接入 /dsh/health 返回。灯色判定规则由票 04 消费，本票只供数据：`poll_overdue`（bool）＋`host_processes_present`（bool）。

## 验收标准
- [ ] /dsh/poll 序列（伪造请求）后 /dsh/health 返回正确的全局年龄与 sid last-seen
- [ ] 阈值推导：有 hint 时=3×hint；无 hint 时=90s 且 assumed=true
- [ ] 宿主旁证：宿主进程在/不在两态返回正确布尔（测试可注入枚举器假实现）
- [ ] 表驱动单测覆盖上述矩阵；`go test ./internal/daemon/ -run DshLiveness` 绿
- [ ] 新起进程全部 HideWindow（Windows 分支）

## Blocked by
无，可立即开始

## 涉及路径
- internal/daemon/dsh_liveness.go（新）
- internal/daemon/dsh_liveness_test.go（新）
- internal/daemon/dsh_receive.go（poll 挂钩一处）
- internal/daemon/daemon.go（/dsh/health 路由注册，如路由在他文件则就地）

## 副作用声明
仅单测；不跑全仓测试；不弹任何窗口

decision_refs: D1（无常驻求值——只记账不判定不告警）、D10
review_blocks: 无
