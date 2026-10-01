# Spec · v0.5.2：换装自中继副本机制删除 + 判热时钟持久化

> 来源：xcheck 链 20261001-222426（根）→ 20261001-235012（收敛 1 轮修订）。
> 对象快照 `.xcheck/20261001-235012/proposal.md`；决策 D1/D2/D4/D5/D6（D3 已替代）；
> 问题账 F1-F9（F1/F2/F5/F6/F7 已解除，F8/F9 为带走注记）。

## Problem Statement
1. 换装监督者在"自身映像==换装目标"时复制自身为 supervisor-copy 副本接手，
   副本退场前的延迟自删两次真机失败（3 秒窗口撞进程退场+通知耗时），残留
   文件使 doctor update_residues 报红、需手动清理。副本机制存在的前提（单步
   替换被运行映像锁死）已被两步改名换装消灭——它是应整体删除的过时补丁。
2. 同模型摆渡的判热时钟为进程内存态，每次守护重启清零；重启观察窗立即重判
   近 24h 会话，全部判冷跳过（当日 116 笔跳过中 74 笔爆发于两次重启点后数秒），
   丢失重启时刻缓存仍活（闲置 20-24 分钟）的最佳价差候选。

## Solution（用户视角）
- 换装后不再产生 supervisor-copy 文件：doctor 的 update_residues 长期干净，
  不再需要手动清理；`ferryman update` 变为同步跑完全程（行为变化，托盘入口无感）。
- 守护重启后判热钟不归零：闲置会话在缓存仍活时仍能被同模型摆渡接住，交接
  质量与缓存价差不再因发版/重启而整批丢失。

## User Stories
1. 作为换装后的用户，我想要不再残留 supervisor-copy，以便 doctor 全绿、无需手动清理。
2. 作为升级瞬间误触发第二次 update 的用户，我想要锁判定在目标被改名后仍识别
   活监督者，以便不发生两个换装事务交错。
3. 作为 v0.5.1→v0.5.2 首跳升级的用户，我想要旧茬残留有明确预期与自愈路径，
   以便首验不被误判为新代码回归。
4. 作为闲置会话的用户，我想要守护重启后判热钟保留，以便缓存仍活时仍能拿到
   同模型摆渡。
5. 作为发版运维者，我想要生产观察指标有干净的起算口径，以便读数可归因。

## Implementation Decisions
- 部件A（删副本）：
  - supervisor.go 删 selfRelayIfNeeded/relayCopyPath/relaySelfDelete/relayArgs
    与 Run() 0.5 步；Result.Relayed 与 cmd 侧分支收口。
  - proc_windows.go 删 selfDeleteImpl/spawnRelayImpl；proc_other.go 同步。
  - lock.go holderAlive 映像集合定案={换装目标, supervisor-copy（过渡一版，
    次版删除）, 换装目标同目录 ferryman.exe.old-* 备份族}；PID 活性为主判据、
    映像查不出保守按活（现行语义不动）；匹配语义=同目录+文件名前缀，代码注释
    钉死（F9）。
  - --self-relay 旗标留一版"解析但忽略"，注明次版删除。
  - cleanSwapResidues 的 supervisor-copy* 模式保留；doctor 不动。
- 部件B（判热钟持久化，D6）：
  - beat 侧：快照持久化到 dataDir（reqclock.json）；Note 推进时落盘（临时文件
    +替换写，防半写——F9）；写失败静默（丢快照=下次重启保守判冷，方向安全）；
    sid 键按 ts 淘汰或容量上限（F9，避免无界增长）。
  - daemon 侧：装配 watcher 时读快照回种（缺/坏文件=空钟起步 fail-safe）；
    回种值与后续 Note 取 max（单调语义不变）。
  - 不做渡口记账喂钟（D3 已替代：harvest 喂钟已覆盖真实流量，第二时钟源
    不同源无增益）。
- 发布边界（F7，验收口径）：
  - 首跳（v0.5.1 旧监督者执行 v0.5.1→v0.5.2）预期 doctor 23/24：update_residues
    一过性红，由 v0.5.2 的下次 update 启动清扫收走，或手动清一次。
  - v0.5.2 及其后发起的换装不再产生新副本（钉子保证）——这是验收本体。
  - 可选增强（不在本车，晨报供拍板）：daemon 启动顺带清扫 supervisor-copy*。
- 观察指标口径（F8）："-1 占比回落"自 v0.5.2 产生首次快照后的重启起算。

## Testing Decisions
- 仓库既有先例：internal/update 的锁/换装表驱动测试 + swap_locked_windows_test
  的真锁场景；internal/beat 的单元钉子。只测外部行为：
  - 票01：钉子"target 已改名 .old-<版本>、监督者仍活→仍判持有"（旧码红）；
    回归钉"supervisor-copy 活持有者→仍判进行中"（新旧皆须绿，注明次版随过渡
    分支删除——F9）。
  - 票02：钉子"自身映像==换装目标→两步换装直接成功、盘面零 supervisor-copy、
    备份=.old-<版本>"（旧码红）。
  - 票03：钉子"快照回种后 Last 返回快照值"（旧码红：恒无观测）；"快照钟=20
    分钟前+台账闲置 20 分钟→判热通过"（旧码红）。
- 全仓 `go test ./...` 绿为准；-race 本机工具链不可用（既往实证），不作为票内
  局部验证要求。

## Out of Scope
- daemon 启动清扫 supervisor-copy（F7 可选增强，晨报拍板）。
- 渡口记账喂钟（D3 已替代）。
- supervisor-copy 锁身份过渡分支的删除（次版）。
- 同模型泳道其他调优（门序/smSeen/重启观察窗语义均不动）。

## Further Notes
- 术语见 CONTEXT.md（换装/监督者/让位/判热/追加重放/摆渡）。
- 实施分支 xcheck-night-20261001-222426；发布走既有发版流程（tag 推送→CI），
  本夜链不自动发版、不自动开 PR。
