# 票03 · 新会话播种新鲜度校验(过期交接不供)

## What to build
dsh 新会话来取交接时,只供"覆盖截止不早于基准会话最新 last_write"的交接(60 秒容差,与闸门覆盖判据同口径);过期的一律不供,落空走既有降级(带回被拦原话+中性"交接缺失"文案,restore.go 无交接分支已在)。基准会话:有被拦锚时=锚会话(LatestPendingFor),无锚=候选源会话(Entry.SessionID 对应台账会话)。cc/codex 供出行为逐字不变。

## 验收标准
- [ ] 仅 agent=="dsh" 生效:dsh 供出前按基准会话 last_write 校验 covers(含 60s 容差);过期不供
- [ ] 落空时走既有降级路径(原话+中性文案),不报错不静默
- [ ] 单测:构造 covers 落后 last_write 的交接→新会话收原话+缺交接文案而非旧文档;有锚/无锚两种基准形态各有用例;cc 同数据行为与改动前逐字一致
- [ ] restore_test 既有断言不破

## Blocked by
票01(消耗过滤同链路,先后落地避免同文件冲突由票02 承担,本票只动 restore 面)。

## 涉及路径
internal/daemon/restore.go
internal/daemon/restore_test.go

## 副作用声明
只跑 internal/daemon 包内 restore 相关单测。

## decision_refs: D3、D4、F5
## review_blocks: 无
