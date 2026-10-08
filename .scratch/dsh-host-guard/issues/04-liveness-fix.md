# 04 · doctor daemon_liveness 误报修复

## What to build

修 internal/installer/doctor.go 的 daemon_liveness 判定:2026-10-08 16:0x 实测 daemon 双口监听(PID 13004,15700/15722 LISTENING)且台账数据持续更新,doctor 却报 `fail(daemon 未运行)`。先读码钉死现有判据(CheckDaemon:d.Probe 探测+daemon.pid 文件),定位「监听中但被判死」的具体成因(候选:探测超时过短/并发探针失效/pid 文件陈旧误配——以读码结论为准,不预设),修正后加回归断言。

spec 依据:`.scratch/dsh-host-guard/spec.md` G 节。

## 验收标准

- [ ] 读码结论落注释:现行判据是什么、误报形态的成因是什么(实测/推理分清)
- [ ] 回归测试:daemon 端口监听中 → liveness 必 pass;daemon 真死(端口无监听)→ 必 fail
- [ ] 既有 doctor_test.go 相关用例(1236/1343/1374 行一带)语义保留且通过
- [ ] 不引入新的误报面(修完的判定对「pid 文件在但进程死」「进程在但端口未就绪」等形态各归其位,测试覆盖至少这两种)

## Blocked by

票 03(internal/installer/doctor.go 同文件路径互斥)。

## 涉及路径

- internal/installer/doctor.go
- internal/installer/doctor_test.go

## 副作用声明

- 允许运行:`go test ./internal/installer/ -run 'Doctor|Liveness' -count=1`(落 .scratch/dsh-host-guard/logs/t04-*.log)
- 测试不依赖生产 daemon 状态(用 httptest/假探针,不探真 15700)

## decision_refs

D1(修复三授权);D2

## review_blocks

无
