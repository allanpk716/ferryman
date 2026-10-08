# 03 · /stats pollers 段+doctor 哨兵检查项+看门消费

## What to build

`/stats` 应答增 `pollers` 段(每 poller 的 name/last_seen/age_s/state);doctor 新增检查项「宿主插件哨兵」——通过 daemon /stats 读取 pollers 段,stale(应在线而沉默)→ fail 并点名,其余 → pass 注记(无 poller 数据/端点不可达 → pass 注记不误报);`ferryman watchdog` 单次探活(internal/installer/watchdog.go RunWatchdogCLI)在 daemon 可达时顺带消费 /stats pollers 段,发现 stale 在看门日志记显著行(不改计划任务定义)。

spec 依据:`.scratch/dsh-host-guard/spec.md` E(doctor 判定)/F 节。

## 验收标准

- [ ] /stats 应答含 pollers 段,形状=name/last_seen/age_s/state(online/stale/offline/retired)
- [ ] doctor 检查项:模拟 stale 基线 → fail 且 detail 含 poller 名;offline/retired → pass;无任何 poller → pass 注记;daemon 不可达 → 沿用既有 daemon_liveness fail,哨兵项不叠加误报
- [ ] watchdog:daemon 可达+存在 stale → 看门日志记显著行;无 stale → 与现行行为一致(daemon 有响应即正常退出)
- [ ] 既有 /stats 消费方(看门探活、upstream 等)零回归

## Blocked by

票 02(消费其基线与状态机;internal/daemon 派发面与票 02 的 dsh_receive.go 同包路径互斥从宽处理——本票主改 httpapi/installer,票 02 先落地避免冲突)。

## 涉及路径

- internal/daemon/httpapi.go
- internal/daemon/httpapi 相关既有测试文件(httpapi_test.go 或相邻,按实际命名)
- internal/installer/doctor.go
- internal/installer/doctor_test.go
- internal/installer/watchdog.go
- internal/installer/watchdog_test.go(若无则新增)

## 副作用声明

- 允许运行:`go test ./internal/daemon/ ./internal/installer/ -run 'Stats|Doctor|Watchdog|Poller' -count=1`(落 .scratch/dsh-host-guard/logs/t03-*.log)
- 不跑全仓 go test(终局统一)

## decision_refs

D2;F4 解除条件的实现面(接现成看门节奏);F11(晨报口径)不属本票

## review_blocks

无
