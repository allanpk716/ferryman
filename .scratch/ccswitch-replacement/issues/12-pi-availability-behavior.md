# 票12 · pi 可用性位接进 provider switch / apply(行为语义)

## What to build
把票09 的 pi 可用性位接进行为面(采纳仓内 codex="unsupported" 既有先例,cmd/ferryman/provider.go:178,200 同款):①provider switch:目标上游对 pi 不可用(显式否决/缺 pi 主模型键/dialect 非 anthropic)→默认拒绝并报因;--cc-only 显式放行并明示"pi 暂断供,仅 CC/codex"(文案与 codex 分开列明各自断供面);②provider apply:active 上游对 pi 不可用→pi 目标拒写并在回显如实报告"pi 目标跳过:<原因>",其余目标照常,不半写不整单失败;③退出码:switch 拒绝=1(失败语义不变);apply 部分=0 成功但回显明示跳过面;④usage 与 providerUsage 文案同步;⑤单测:不可用默认拒/--cc-only 放行文案/apply 跳过回显三形态。

## 验收标准
- [ ] switch 到 pi 不可用上游:默认 exit 1 报因;--cc-only 放行且文案明示 pi 断供
- [ ] apply 时 pi 不可用:pi 目标跳过+回显,其余目标照常,exit 0
- [ ] 文案与 codex 断供面分开列明(两 agent 同时断供时都列)
- [ ] go test ./cmd/ferryman/ 绿

## Blocked by
票09(可用性位)、票10(apply 目标面)

## 涉及路径
cmd/ferryman/provider.go
cmd/ferryman/provider_test.go
internal/provider/writer.go(apply 前置校验挂接)

## 副作用声明
无独占验证命令;go test ./cmd/ferryman/ ./internal/provider/

## decision_refs
D16;F2(行为侧)

## review_blocks
无
