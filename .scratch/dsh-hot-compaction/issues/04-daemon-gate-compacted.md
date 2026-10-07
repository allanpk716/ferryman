# 票04 · daemon gate 联动：已压缩短前缀不拦

## What to build
gate.go 拦截判定处新增 compressed 标记检查：命中拦截条件时，若会话带有效 compressed 标记（未过期、未被流量作废）∧ 当前 prefix_tokens < min_peak_tokens → 放行（账本 allow，reason="compacted-short-prefix"）；否则照旧拦。不放宽任何其他判定。标记刷新/作废规则按 spec（其后流量刷新 prefix、重新变热即作废、自然过期不续期）。

## 验收标准
- [ ] 表驱动测试绿四态：有效标记+短前缀→放行；标记过期→照拦；标记被流量作废→照拦；无标记→照拦（现有用例零回归）
- [ ] 账本 allow 事件带 reason 字段可 grep
- [ ] gate_test.go 既有表全绿

## Blocked by
票02（compressed 标记态先落）

## 涉及路径
- internal/daemon/gate.go + gate_test.go

## 副作用声明
默认只跑 go test internal/daemon

decision_refs: D6
review_blocks: F2
