# 票03 · 追加重放执行档与失败链

## What to build
同模型档的核心执行路径,打通端到端:心跳捕获重放发送器(internal/beat + internal/dock 快照)新增"追加重放"形态——前缀字节一个不动,末尾追加一条摆渡指令消息,放开 max_tokens(封顶防输出失控)。摆渡执行器(internal/ferry)接入:指令明令"只输出交接 MD 结构、禁止调用工具";响应解析出模型叙事,与既有确定性骨架合成(交接产物结构不变);stop_reason=tool_use 或输出不合交接 MD 结构 → 判该档失败 → 第三方摆渡 → 骨架(既有链不动),同模型档**不重试**。同模型的模型调用入 handoff 科目,结果码带 lane 标注(same_model/third_party/skeleton)。

## 验收标准
- [ ] 追加重放构造单测:与捕获快照 diff 仅末尾追加段(字节保真)
- [ ] 指令模板含禁工具与结构要求;max_tokens 封顶可配
- [ ] 失败链单测:tool_use/格式不符→落第三方→再败落骨架;同模型零重试
- [ ] handoff 记账带 lane 标注,账本测试覆盖三档
- [ ] 既有摆渡/心跳测试照绿(不回归)

## Blocked by
票02

## 涉及路径
internal/beat/
internal/dock/
internal/ferry/
internal/accounts/
internal/daemon/watcher.go(热路径接线;协调者增补:票02 的判热门在此,执行器需挂接)

## 副作用声明
go test ./internal/beat/... ./internal/dock/... ./internal/ferry/... ./internal/accounts/...;go build ./...

## decision_refs
D1 D4 D8

## review_blocks
无(F2 的命中验证属票04 实跳臂,本票只交付机制)
