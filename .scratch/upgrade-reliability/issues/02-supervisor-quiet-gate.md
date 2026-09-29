# 02 · 监督者静默门（三分支+判据+交互/旗标）

## What to build
监督者停旧之前加静默门：
1. 门前置三分支：①已验证旧守护不存在（cfg.Port 端口探测空且 daemon.pid 无活进程）→跳过门直接进既有停旧/拉新流程；②守护在但 /stats 不可达→直接进入非静默兜底路径（不干等静默窗）；③可达→执行判据。
2. 判据：GET /stats 的 dock_inflight==0 且 now-last_request_ts>=10s（last_request_ts==0 视为静默成立）。
3. 判据不满足：默认轮询等待 60 秒（每 10 秒记一行日志）。到期仍不满足：stdin 为交互终端则提示三选（继续等 60s/现在切/放弃）读一行；非交互/无法判定→notify 告警一条（"静默门未达成，硬切兜底"）后按既有语义直接走停旧。
4. 旗标：`--wait-quiet=<秒>`（覆盖默认等待预算，0=不等）；`--force`（跳过门直接停旧）。装配进 cmd/ferryman update 子命令与 update.Config。
5. 门通过后立即 POST /shutdown（既有 postShutdown；门与 shutdown 之间不插入其他等待）。
6. 硬切路径在 update.log 记一行："硬切兜底：门未达成（等待 Ns，末次在途 M/距最后请求 Ts）"。

## 验收标准
- [ ] 三分支行为可测（桩注入 /stats 应答与端口状态）
- [ ] 判据满足即放行不额外等待
- [ ] --wait-quiet=0 与 --force 均跳过等待直接停旧
- [ ] 非交互告警走 notify（失败不阻塞事务）
- [ ] update.log 有硬切兜底记录行
- [ ] 既有升级事务测试全绿（可达且静默的测试环境不改变行为）

## Blocked by
01、03

## 涉及路径
internal/update/supervisor.go
internal/update/supervisor_test.go
cmd/ferryman/main.go
cmd/ferryman/main_test.go

## 副作用声明
go test ./internal/update/ ./cmd/ferryman/ 允许

## decision_refs
D1、D10

## review_blocks
无
