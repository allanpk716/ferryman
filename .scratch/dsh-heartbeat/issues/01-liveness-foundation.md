# 01 · dsh 判活地基（事件收编＋运行/终结态＋闸门豁免）

## What to build

守护进程收下 dsh 插件已在发送的两个结构化判活信号（今日被白名单收窄丢弃）：`agent/status`（running/idle）与 `agent/disposed`，在台账维护会话"在跑/已终结"状态（含活动事件刷新、族系判定、1 小时失效上界），并给闸门的"机器等机器"豁免加 dsh 道。完成即可独立验收：喂事件→台账态正确；问闸→豁免生效。这是 dsh 心跳的前置（问询窗条件③）也是闸门升 enforce 的前置（长任务中段不被闲置钟误判）。

规格依据：`.scratch/dsh-heartbeat/spec.md`「判活」节。

## 验收标准

- [ ] `agent/status`：主会话（parent_session_id 空）running → Touch("dsh") 活动登记＋运行态置位；idle → 清运行态；status 非法值按 unknown-event 收窄（200＋skipped）
- [ ] `agent/disposed`：不 Touch、置终结态；同键后续 resume 载荷或新 running 清除终结态
- [ ] 子会话直报（parent 非空）不 Touch 父；运行态记子键；族系在跑＝父 OR 任一已知子键（子键来源＝台账 usage 行 subagent 列＋fed 表回种；mechanics 本票钉死并写注释）
- [ ] 运行态刷新：同键 `turn/start`/`assistant/message` 事件推进运行态时间戳（fed 走事件口、未接管会话走文件面检测态推进——文件面若本票尚未有检测态，先落事件口路径并在注释声明文件面随票 03 补）；距置位/上次刷新 >3600s 判失效（回正常闸门路径）；idle 显式复位优先于刷新
- [ ] `machineWaiting` 对 agent=dsh 命中在效运行态（含族系）返回 true；过期/无态回 false；agent=dsh 不再调用 HasDanglingToolUse（悬空白读跳过）；cc/codex 路径行为零变化
- [ ] 事件行不入账（无 usage），账本零新键
- [ ] scoped 测试绿；既有测试零回归

## Blocked by

无，可立即开始

## 涉及路径

- internal/daemon/dsh_receive.go
- internal/daemon/dsh_dedup.go
- internal/daemon/gate.go
- internal/daemon/gate_test.go
- internal/daemon/dsh_receive_test.go
- internal/ledger/ledger.go
- internal/ledger/ledger_test.go

## 副作用声明

- 独占验证命令：`go test ./internal/daemon/... ./internal/ledger/...`（输出落 `.xcheck/20261003-183301/t01-test.log`，跑一次不重跑）

## decision_refs

D1（对象）、D5（CC 零回归）、D6（接法乙前置已开）

## review_blocks

无
