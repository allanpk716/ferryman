# 02 · poller 身份心跳+持久化基线+生命周期状态机+跃迁日志

## What to build

宿主插件在 poll 体顶层带 `poller` 身份名(模块路径推导 profile 名,推导不出=`unknown-<短随机后缀>`);插件 dispose 时尽力上报下线一次(best-effort 不重试);daemon 维护 `~/ferryman` 数据目录下 `dsh-pollers.json` 基线(first_seen/last_seen/offline,惰性落盘,损坏即弃重建,启动加载);daemon 内实现 poller 生命周期状态机(online/stale/offline/retired,判定=24h 心跳∧无下线∧静默>90s → stale);状态跃迁「活→静默」时打显著日志行 `[poller] <名> 静默>90s(最近心跳 <时间>)`(跃迁制不刷屏)。

spec 依据:`.scratch/dsh-host-guard/spec.md` A(poller 字段)/E/F(跃迁行)节。

## 验收标准

- [ ] poller 名推导:profile 路径形态取 `<name>`;非标准路径取 `unknown-<短随机后缀>`(进程内稳定)
- [ ] 基线 roundtrip:心跳更新→落盘;daemon 重启→基线恢复(「插件先死、daemon 后重启」可检出)
- [ ] 基线损坏(写坏 JSON)→ 弃置重建,不崩
- [ ] 生命周期矩阵测试:online→stale(24h 内心跳+静默>90s);offline 标记→不算 stale;静默>24h→retired;首建前(无基线)→全部按未知处理不判 stale
- [ ] dispose 上报:插件卸载路径触发一次下线上报(daemon 收到置 offline)
- [ ] 跃迁日志行只在 online→stale 跃迁时打一次,持续静默不重复打

## Blocked by

票 01(共用 plugin/ferryman-dsh/src/compact.ts、daemon.ts 与 internal/daemon/dsh_receive.go,路径互斥)。

## 涉及路径

- plugin/ferryman-dsh/src/index.ts
- plugin/ferryman-dsh/src/compact.ts
- plugin/ferryman-dsh/src/daemon.ts
- plugin/ferryman-dsh/test/
- internal/daemon/poller_baseline.go(新增)
- internal/daemon/poller_baseline_test.go(新增)
- internal/daemon/dsh_receive.go
- internal/daemon/dsh_receive_test.go

## 副作用声明

- 允许运行:`go test ./internal/daemon/ -run 'Poller' -count=1`(落 .scratch/dsh-host-guard/logs/t02-*.log)
- 允许运行:插件测试同票 01 命令
- 测试内使用临时目录,不写生产 ~/ferryman(除只读)

## decision_refs

D2;F8(24h 视界)/F9(下线 best-effort 残余)/F10(unknown 后缀)已按 spec 显式取舍

## review_blocks

无(F2/F3 的解除依赖已由票内实现承接;spec E 节即其解除条件的实现面)
