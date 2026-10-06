# 票01 · store 层交接消耗标记(三处口径地基)

## What to build
强续(bypass)一经使用,被强续会话自己的旧交接立即"作废":供出链、闸门覆盖判定、去重/boot 补记三处都不再把它当有效交接。同一目录其他会话的交接不受影响。仅 dsh 会话会被标记(cc/codex 的交接永不消耗,零行为变化)——本票只做 store 层能力与过滤,agent 限定由票02 的接线面执行。

## 验收标准
- [ ] Entry 增消耗标记字段,持久化于 store 索引(与 pending 同载体,flush 落盘,重启后仍在)
- [ ] 新增按会话消耗的方法:给定 (agent, sessionID),把该会话名下 status∈{fresh,skeleton} 的交接标记为已消耗(Entry.SessionID 匹配,不动同 (agent,cwd) 其他会话)
- [ ] RestoreCandidates 过滤已消耗交接
- [ ] ValidHandoff 不认已消耗交接(闸门覆盖判定/去重/boot 补记共用此函数,一处改三处生效)
- [ ] 单测:消耗→供出链拿不到、ValidHandoff 判无覆盖、同目录其他会话交接不受影响、重启(重开 store)后标记仍在
- [ ] cc/codex 既有 store 测试全部不变绿转

## Blocked by
无,可立即开始。

## 涉及路径
internal/store/store.go
internal/store/store_test.go

## 副作用声明
只跑 internal/store 包单测。

## decision_refs: D2、D4(保守面)、F1、F5、F11
## review_blocks: 无
