# 01 · 压缩指令领取路由:live 三值协议+触发聚合+派发路由+no-agent 退避

## What to build

打通端到端:宿主插件在每次 `/dsh/poll` 时为每个会话上报 `live`(有无活 agent 引用);daemon 触发面按「近窗任一 true 即可执行/全部显式 false 才压制/无显式上报照旧」聚合判定;派发面只把槽内指令交给本次报 `live=true` 的宿主(缺键旧体兼容可领);同一会话连续 no-agent 后进入固定 30 分钟退避。播种宿主从此领不到也压不住压缩指令。

spec 依据:`.scratch/dsh-host-guard/spec.md` A/B/C/D 节(语义已钉死,勿改语义只做实现)。

## 验收标准

- [ ] daemon 聚合语义三分支各有测试:近窗任一 live=true → 照常触发;全部显式 false → 压制;无显式上报(全缺键)→ 与现行行为一致
- [ ] 双宿主混合用例:同一 sid,宿主 A 报 true、宿主 B 报 false,交错轮询 → 触发不被压制,且指令只派给 A
- [ ] 派发路由用例:live=true 可领取;live=false 列了该 sid 也不派;缺键(旧体)可领取
- [ ] 退避用例:连续 no-agent 后 30 分钟内不再入槽/重发;再报 live=true 或 last_write 前进即解除
- [ ] 插件侧:poll 体带 live(RegistryEntry.agent 非空=true);`plugin/ferryman-dsh/test` 套件过
- [ ] 既有 `internal/daemon/compact_trigger_test.go`/`compact_test.go` 全部语义保留(除有意收口的派发行为)且通过

## Blocked by

无,可立即开始。

## 涉及路径

- plugin/ferryman-dsh/src/compact.ts
- plugin/ferryman-dsh/src/registry.ts
- plugin/ferryman-dsh/src/daemon.ts
- plugin/ferryman-dsh/test/compact.test.ts
- plugin/ferryman-dsh/test/registry.test.ts
- internal/daemon/compact.go
- internal/daemon/compact_test.go
- internal/daemon/compact_trigger.go
- internal/daemon/compact_trigger_test.go
- internal/daemon/dsh_receive.go
- internal/daemon/dsh_receive_test.go

(新增测试文件落在上述同目录既有文件内扩展;确需新文件时只落在 plugin/ferryman-dsh/test/ 或 internal/daemon/ 下并在回报清单注明)

## 副作用声明

- 允许运行:`go test ./internal/daemon/ -run 'DshCompact|DshPoll|CompactTrigger' -count=1`(落 .scratch/dsh-host-guard/logs/t01-*.log)
- 允许运行:`node --test --experimental-strip-types plugin/ferryman-dsh/test/compact.test.ts plugin/ferryman-dsh/test/registry.test.ts`(同目录落日志)
- 不跑全仓 go test(终局统一);不碰生产 daemon/宿主

## decision_refs

D1(三修复方向授权)、D2(设计细节授权)

## review_blocks

F5,F6(语义已由 spec B/C 节钉死=解除条件满足,实施须逐字遵循;实现偏离 spec 语义即回归该阻断)
