# 票06 · switch 端点纳管

## What to build
daemon 新增 `POST /settings/dock/switch` `{name}`：内部复用既有 provider_switch 逻辑（COW 热换绑+落盘 SetActiveUpstream），外面包票03单写者锁+写前自动快照（reason=switch）+审计行。保持既有语义不变：只能切到**启动时已加载**的条目（内存快照），不在此端点重载配置；不可用上游（codex 不支持）按既有规则拒绝。响应 `{switched, needs_restart:false}`；若目标条目为本会话新增（启动后经设置面写入），响应 needs_restart=true 并附原因文案。

## 验收标准
- [ ] 切到已加载条目：active 落盘+内存换绑（既有 provider_switch 测试语义不回归）
- [ ] 切到启动后新增条目：409/明确错误或 needs_restart=true 提示（按实现选择断言，二选一写死在测试里）
- [ ] 写前自动快照存在（reason=switch）、审计行落盘
- [ ] `go test ./internal/daemon/ -run 'TestSettingsSwitch'` 全绿（日志 06-test.log）

## Blocked by
票03、票05

## 涉及路径
- internal/daemon/settings_switch.go（新）
- internal/daemon/settings_switch_test.go（新）
- internal/daemon/httpapi.go（仅分派行）

## 副作用声明
- 独占验证：`go test ./internal/daemon/ -run 'TestSettingsSwitch'`；不得改动 internal/daemon/provider_switch.go 既有行为（只允许调用）

## decision_refs
D7（热缝仅两处）、ADR-0022 决策5

## review_blocks
无
