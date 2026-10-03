# 03 · dsh 等答复窗全套（检测/开窗/关窗/排程/分档/熔断/Pin）

## What to build

dsh 心跳保温的扳机与全套调度机制：读会话文件判"最后说话人是 dsh"（等答复态），四条件开窗排心跳；关窗只认用户侧事件或 block_s 到期（机器侧写入不误关）；心跳调度入口放宽到 dsh；独立分档开关（默认关、生产先演练）＋独立熔断＋快照 Pin 对账。完成即可独立验收：伪造 dsh 会话文件与事件 → 开窗/关窗/心跳/记账/Pin 全链行为正确，CC 侧行为零变化。

规格依据：`.scratch/dsh-heartbeat/spec.md`「dsh 等答复窗」节（含 F7 消歧后的取消跳两验、F6 参数断言）。

## 验收标准

- [ ] dshtrans 检测态：`turn/start`/`user/message`＝用户侧（合成注入回合也算用户侧——协议语义，无害）；`assistant/message`＝dsh 侧（不要求带 usage）；"dsh 后说"＝最后一条用户侧事件早于最后一条 assistant/message；跨 chunk 累积（同 title 携带形）；zstd 与明文两形态都过
- [ ] pollDsh 在 Touch＋观察窗之后、fed 早退之前跑检测（fed 会话必须也检测）；fed 会话检测偏移独立于 harvest 偏移（harvest 不推进时检测照常前进）
- [ ] 开窗四条件：dsh 后说 ∧ 运行态不在效（票 01）∧ 未终结 ∧ peak ≥ MinCtxTokens；版本章缓存；开窗临界区与 CC 同款（QWatch 字段族＋beatPlan 计划 420s×2＋开窗基线快照）
- [ ] 用户侧事件到达 → 关窗（close_reason=user-write）＋整计划作废；机器侧写入（session/title、request/header、compaction、turn/end、subagent/catalog）不清窗、不取消跳——实现载体自选（touch 清窗旁路 or 独立 dsh 窗状态），语义以规格为准
- [ ] block_s 到期自动关窗（close_reason=deadline）；关窗（用户侧/到期）→ 快照 Unpin
- [ ] 心跳取消判据＝并列两验：①窗口仍开着 ②检测态未翻转用户侧；不复用 mtime/size 复验；CC 两道验零变化
- [ ] 心跳调度入口 agent 检查放宽 cc→cc+dsh；`qwatchModeFor(agent)` 单源取模式；全局单在途照旧
- [ ] `[question_watch] dsh_mode`（off|observe|enforce，默认 off）＋解析校验＋`/stats` 回显；dsh enforce＋渡口关 → 启动告警按 observe 对待
- [ ] dsh 独立断路器：连续 MISS 只降 dsh_mode、连续 ERROR 停本窗剩余跳；CC 断路器与模式零影响
- [ ] reconcilePins 扩 dsh：窗开 → Pin(sid)、关 → Unpin（与关窗路径一致）
- [ ] 参数断言：`BeatIntervalS × MaxBeats < BlockS`（config 校验或测试断言；生产 420×2=840<2100 成立）
- [ ] observe 档心跳走 Noop、记账零费、beat/qwatch 行 agent=dsh；close_reason 取值 user-write|deadline（dsh 不落 CC 的 write）
- [ ] scoped 测试绿；CC 侧既有测试零回归

## Blocked by

01（运行态判定）、02（心跳头发对）

## 涉及路径

- internal/dshtrans/dshtrans.go
- internal/dshtrans/dshtrans_test.go
- internal/daemon/watcher.go
- internal/daemon/watcher_dsh.go
- internal/daemon/dsh_qwatch_test.go（新建）
- internal/daemon/watcher_dsh_real_test.go
- internal/config/config.go
- internal/config/config_test.go

## 副作用声明

- 独占验证命令：`go test ./internal/dshtrans/... ./internal/daemon/... ./internal/config/...`（输出落 `.xcheck/20261003-183301/t03-test.log`，跑一次不重跑；daemon 全包含票 01 改动，彼时已 complete）

## decision_refs

D2（一次性四票）、D3（三取舍按方案执行——observe 先行/文件面检测/熔断隔离）、D5（CC 零回归）

## review_blocks

无
