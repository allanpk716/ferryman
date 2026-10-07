# 票01 · 压缩指令"领取不执行"空转环：告警归位 + 在飞节流 + 插件不白领

## What to build

静置 DSH 会话的热缓存压缩指令不再陷入"领取→不执行→30 秒重发"的静默空转：领取后执行窗内没有成功上报就计"未送达"轮（3 轮触发既有 gateWarn compact-undelivered）；槽内指令在有效期内不被同会话新触发覆盖；插件对没有活 agent 引用的注册表条目不再领取指令（或领取即上报不可执行）。真开会话的秒领取秒执行（2026-10-07 16:09 首单形态）零回归。

背景机制（夜链预查证已钉死，勿重复排查）：
- daemon 侧 `compact_trigger.go` 头注"poll 应答即清槽"——插件一轮询领走指令槽即清空，下一轮守望（3s）条件⑤（无未过期在槽指令）不成立→重新入槽→30s 循环；"过期无人领取"告警只认槽内过期，领取即清槽=告警永不可达
- 插件侧 `compact.ts:434` 执行闸在 `entry.agent` 为空（注册表播种的静置会话无活 agent 引用）时跳过执行，但轮询已把指令领走
- 生产实证：bb5d5e37 194 次触发 0 执行；f9d6523c 160 次触发；0592c18d 播种前为 360s/次过期重发节奏

## 验收标准

- [ ] 模拟"领取后 180s+余量无 /dsh/compacted ok 上报"：计一轮未送达，连续 3 轮出 gate.log `compact-undelivered`（与既有"过期无人领取"同权同告警路径）
- [ ] 槽内指令有效期内同会话新触发不覆盖（触发行间隔 ≥ 指令有效期或领取后执行窗，表驱动用例含"领取后 30s 重触发被拒"）
- [ ] 插件 `entry.agent` 为空时不领取或领取即上报 `{ok:false, reason:"no-agent"}`，daemon 不计该轮为送达
- [ ] 真开会话（registry touch 且 agent 引用在）：指令领取→执行→上报 ok 全链零回归（既有 compact 用例全绿）
- [ ] 既有"指令过期无人领取"路径（无人领取）行为不变

## Blocked by

无，可立即开始

## 涉及路径

- internal/daemon/compact.go
- internal/daemon/compact_trigger.go
- internal/daemon/compact_test.go
- internal/daemon/compact_trigger_test.go
- plugin/ferryman-dsh/src/compact.ts
- plugin/ferryman-dsh/test/（compact 相关测试文件，实施时在既有文件内扩展）

## 副作用声明

- daemon 侧：`go test ./internal/daemon/ -run 'Compact'`（scoped，不跑全仓）
- 插件侧：`cd plugin/ferryman-dsh && npm test`（node --test 全量，为本票独占验证命令）
- 不安装依赖、不联网、不动 .xcheck/

## decision_refs

D1（三票范围）、D7（心跳保活维持关闭——不得引入任何保温/心跳路径）

## review_blocks

无
