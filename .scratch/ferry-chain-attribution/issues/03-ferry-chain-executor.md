# 票 03 · 摆渡多级顺位链执行器（失败语义+记账+告警）

## What to build

第三方摆渡按顺位链逐级执行：每级**单次尝试、零重试**（滑落即重试）；本地级拨号超时 5 秒（训练期死亡罚则有界），云端级沿用既有 context 总时限语义。失败判定（任一即滑落）：连接失败/超时、HTTP ≥400、响应解析失败、输出为空、输出未通过交接结构校验（与同模型档产物结构校验同款纪律）。成功=非空且结构校验过。每次尝试逐行记账（provider、lane 三档语义不变、outcome、wall_s、token 列、顺位序号）。降级告警：每次从某级滑落到下级推一条（既有 Pushover/Toast 通道，文案含"从 X 级滑落到 Y 级"）；滑入骨架按进程生命周期只告警一次。含 Anthropic 协议适配器（非流式 /v1/messages，x-api-key + anthropic-version，text 块拼接忽略 thinking 块，usage 映射 input/output，extra_body 逐键并入）。链尾骨架行为不变。

## 验收标准

- [ ] 杀掉第 N 级（httptest 假上游按协议回放）→ 第 N+1 级产出交接，顺序断言
- [ ] 五类失败（连接/HTTP≥400/解析失败/空输出/结构不合）各有一测：判失败→滑落
- [ ] 全链死→骨架交接（对闸门有效），且骨架告警单进程仅一次（多次全链降级仍一条）
- [ ] 每次尝试各一行记账：字段含 provider/lane/outcome/wall_s/token/顺位序号；降级轨迹可从账本重放
- [ ] Anthropic 适配器单测：正常回包/thinking 块混排/usage 映射/extra_body 并入
- [ ] 每级单次尝试零重试有断言（假上游计数=1）
- [ ] 本地级拨号超时 5s 有配置语义测试
- [ ] 全仓 go test 相关包全绿 + go vet 净

## Blocked by

02

## 涉及路径

- internal/ferry/ferry.go
- internal/ferry/chain.go（新建）
- internal/ferry/chain_test.go（新建）
- internal/ferry/anthropic.go（新建，协议适配器）
- internal/daemon/serve.go（装配处接线）
- internal/daemon/worker.go（或实际调用摆渡的 worker 文件，以代码为准）
- internal/notify/notify.go（若需降级事件入口；以代码为准）
- internal/accounts/accounts.go（若需顺位序号字段；以代码为准）

## 副作用声明

只跑 `go test ./internal/ferry/... ./internal/daemon/... ./internal/notify/... ./internal/accounts/...` 与 `go vet`；无网络（全部用 httptest 假上游）、无端口、无构建产物。

## decision_refs

D1、D2、D3

## review_blocks

无（F5 失败语义契约已随 rev1 解除，本票为其实现载体；F10 骨架告警去重口径=按进程生命周期一次）
